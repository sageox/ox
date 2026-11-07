package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// Redaction rewrites working files before it amends the holding commit. If the
// amendment fails (e.g. missing committer identity), the working files are
// clean while HEAD still holds the credential, and a later scan has nothing to
// rebuild the repair from (sageox/ox#1505). The repair is therefore recorded
// before staging and replayed on retry: the record binds the holding commit to
// the exact blobs the canonical writer produced, so a retry stages those bytes,
// never newer working-tree content, and never amends a different HEAD.

// pendingRedactionAmendRel is ledger-relative and gitignored with the rest of
// .sageox/cache/.
var pendingRedactionAmendRel = filepath.ToSlash(filepath.Join(".sageox", "cache", "redaction-pending", "amend.json"))

type pendingRedactionAmend struct {
	HoldingTip string                 `json:"holding_tip"`
	CreatedAt  time.Time              `json:"created_at"`
	Paths      []pendingRedactionPath `json:"paths"`
}

type pendingRedactionPath struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Blob string `json:"blob"`
	// HoldingBlob is the path's blob in HoldingTip; empty when absent there.
	HoldingBlob string `json:"holding_blob,omitempty"`
}

// stageAndAmendRedactedFiles stages and amends only repaired files and metadata.
// The caller holds the repo lock and checks Git state before staging. The
// repair is recorded first so a failed amendment can be completed on retry.
func stageAndAmendRedactedFiles(ctx context.Context, ledgerPath string, repairedPaths []string) error {
	if _, err := resumePendingRedactionAmend(ctx, ledgerPath); err != nil {
		return err
	}
	tip, err := redactGit(ctx, ledgerPath, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("resolve holding commit: %w", err)
	}
	rels := slicesSortedUnique(repairedPaths)
	rec := pendingRedactionAmend{HoldingTip: tip, CreatedAt: time.Now().UTC()}
	for _, rel := range rels {
		blob, err := redactGit(ctx, ledgerPath, nil, "hash-object", "-w", "--path="+rel, "--", filepath.Join(ledgerPath, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("record redacted %s: %w", rel, err)
		}
		mode, _, err := indexEntry(ctx, ledgerPath, rel)
		if err != nil {
			return err
		}
		if mode == "" {
			mode = "100644"
		}
		holding, err := treeBlob(ctx, ledgerPath, tip, rel)
		if err != nil {
			return err
		}
		rec.Paths = append(rec.Paths, pendingRedactionPath{Path: rel, Mode: mode, Blob: blob, HoldingBlob: holding})
	}
	if err := writePendingRedactionAmend(ledgerPath, &rec); err != nil {
		return fmt.Errorf("record pending redaction: %w", err)
	}
	return applyPendingRedactionAmend(ctx, ledgerPath, &rec)
}

// resumePendingRedactionAmend completes a recorded repair whose amendment
// failed. It amends only when HEAD is still the recorded holding commit. When
// newer commits sit on top, it holds publication rather than amend them. A
// record whose holding commit is no longer in HEAD's history is obsolete; the
// outgoing-object validator still checks whatever replaced it.
// The caller holds the repo lock.
func resumePendingRedactionAmend(ctx context.Context, ledgerPath string) (bool, error) {
	rec, err := readPendingRedactionAmend(ledgerPath)
	if err != nil || rec == nil {
		return false, err
	}
	if err := checkRedactionIndex(ctx, ledgerPath); err != nil {
		return false, err
	}
	head, err := redactGit(ctx, ledgerPath, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return false, fmt.Errorf("resolve Ledger HEAD: %w", err)
	}
	if head != rec.HoldingTip {
		ancestor, err := isAncestor(ctx, ledgerPath, rec.HoldingTip, head)
		if err != nil {
			return false, err
		}
		if ancestor {
			return false, fmt.Errorf("pending redaction for holding commit %s cannot be completed: newer Ledger commits were added on top of it, and ox amends only the holding commit. "+
				"Publication stays held; remove the unredacted content from unpushed history, then retry (record: %s)",
				shortOID(rec.HoldingTip), pendingRedactionAmendRel)
		}
		slog.Info("pre-push redaction: dropping obsolete pending amendment",
			"holding_tip", rec.HoldingTip, "head", head)
		return false, removePendingRedactionAmend(ledgerPath)
	}
	if err := applyPendingRedactionAmend(ctx, ledgerPath, rec); err != nil {
		return false, fmt.Errorf("complete pending redaction: %w", err)
	}
	slog.Info("pre-push redaction: completed pending amendment",
		"holding_tip", rec.HoldingTip, "files", len(rec.Paths))
	return true, nil
}

// applyPendingRedactionAmend stages the recorded blobs and amends HEAD with
// only those paths. An index entry holding anything other than the holding
// commit's blob or the recorded blob, or a different mode, belongs to another
// writer and is never replaced. The record is kept until the amendment succeeds.
func applyPendingRedactionAmend(ctx context.Context, ledgerPath string, rec *pendingRedactionAmend) error {
	var info bytes.Buffer
	pathspecs := make([]string, 0, len(rec.Paths))
	for _, p := range rec.Paths {
		if _, err := redactGit(ctx, ledgerPath, nil, "cat-file", "-e", p.Blob+"^{blob}"); err != nil {
			return fmt.Errorf("redacted content for %s is missing from the object store: %w", p.Path, err)
		}
		stagedMode, staged, err := indexEntry(ctx, ledgerPath, p.Path)
		if err != nil {
			return err
		}
		if (staged != p.Blob && staged != p.HoldingBlob) || (stagedMode != "" && stagedMode != p.Mode) {
			return fmt.Errorf("refusing to replace staged changes to %s; commit or unstage them, then retry", p.Path)
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", p.Mode, p.Blob, p.Path)
		pathspecs = append(pathspecs, ":(literal)"+p.Path)
	}
	if _, err := redactGit(ctx, ledgerPath, info.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return fmt.Errorf("stage redacted content: %w", err)
	}
	if _, err := gitutil.AmendLedgerSnapshot(ctx, ledgerPath, pathspecs...); err != nil {
		return err
	}
	return removePendingRedactionAmend(ledgerPath)
}

func readPendingRedactionAmend(ledgerPath string) (*pendingRedactionAmend, error) {
	buf, err := os.ReadFile(filepath.Join(ledgerPath, filepath.FromSlash(pendingRedactionAmendRel)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending redaction: %w", err)
	}
	var rec pendingRedactionAmend
	if err := json.Unmarshal(buf, &rec); err != nil || rec.HoldingTip == "" || len(rec.Paths) == 0 {
		// Unusable record: nothing can be replayed from it, and the
		// outgoing-object validator still refuses any unredacted blob.
		slog.Warn("pre-push redaction: discarding unreadable pending amendment", "path", pendingRedactionAmendRel, "error", err)
		return nil, removePendingRedactionAmend(ledgerPath)
	}
	for _, p := range rec.Paths {
		if p.Path == "" || p.Blob == "" || p.Mode == "" || strings.ContainsAny(p.Path+p.Mode+p.Blob, "\x00\t\n") {
			slog.Warn("pre-push redaction: discarding malformed pending amendment", "path", pendingRedactionAmendRel)
			return nil, removePendingRedactionAmend(ledgerPath)
		}
	}
	return &rec, nil
}

func writePendingRedactionAmend(ledgerPath string, rec *pendingRedactionAmend) error {
	abs := filepath.Join(ledgerPath, filepath.FromSlash(pendingRedactionAmendRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(abs, buf, 0o600)
}

func removePendingRedactionAmend(ledgerPath string) error {
	err := os.Remove(filepath.Join(ledgerPath, filepath.FromSlash(pendingRedactionAmendRel)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear pending redaction: %w", err)
	}
	return nil
}

// indexEntry returns the stage-0 mode and blob for rel, or empty strings when
// the path is not in the index.
func indexEntry(ctx context.Context, ledgerPath, rel string) (mode, blob string, err error) {
	out, err := redactGit(ctx, ledgerPath, nil, "ls-files", "--stage", "-z", "--", ":(literal)"+rel)
	if err != nil {
		return "", "", fmt.Errorf("read index entry %s: %w", rel, err)
	}
	entry, _, _ := strings.Cut(out, "\x00")
	if entry == "" {
		return "", "", nil
	}
	meta, _, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[2] != "0" {
		return "", "", fmt.Errorf("unexpected index entry for %s", rel)
	}
	return fields[0], fields[1], nil
}

// treeBlob returns rel's blob in commit, or "" when the commit lacks it.
func treeBlob(ctx context.Context, ledgerPath, commit, rel string) (string, error) {
	out, err := redactGit(ctx, ledgerPath, nil, "ls-tree", "-z", commit, "--", ":(literal)"+rel)
	if err != nil {
		return "", fmt.Errorf("read %s in holding commit: %w", rel, err)
	}
	entry, _, _ := strings.Cut(out, "\x00")
	if entry == "" {
		return "", nil
	}
	meta, _, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[1] != "blob" {
		return "", fmt.Errorf("unexpected tree entry for %s", rel)
	}
	return fields[2], nil
}

// isAncestor reports whether commit is in head's history. A commit that no
// longer exists is not.
func isAncestor(ctx context.Context, ledgerPath, commit, head string) (bool, error) {
	if _, err := redactGit(ctx, ledgerPath, nil, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var missing *exec.ExitError
		if errors.As(err, &missing) && missing.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check holding commit %s: %w", shortOID(commit), err)
	}
	_, err := redactGit(ctx, ledgerPath, nil, "merge-base", "--is-ancestor", commit, head)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// redactGit runs git in ledgerPath and returns trimmed stdout.
func redactGit(ctx context.Context, ledgerPath string, stdin []byte, args ...string) (string, error) {
	cmd := gitutil.NewNetworkCmd(ctx, append([]string{"-C", ledgerPath}, args...)...)
	cmd.Dir = ledgerPath
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", args[0], gitutil.SanitizeOutput(strings.TrimSpace(stderr.String())), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func shortOID(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

func slicesSortedUnique(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i == 0 || s != out[n-1] {
			out[n] = s
			n++
		}
	}
	return out[:n]
}
