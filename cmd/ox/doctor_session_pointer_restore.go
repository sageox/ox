package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/lfs/pointer"
)

// CheckSlugSessionPointerRestore finds session artifacts that unpushed Ledger
// commits hold as raw content and rewrites them back to LFS pointers (#1174).
const CheckSlugSessionPointerRestore = "session-pointer-restore"

const pointerRestoreCheckName = "session LFS pointers"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionPointerRestore,
		Name:     pointerRestoreCheckName,
		Category: "Sessions",
		// Suggested: the repair adds one commit. It only swaps raw bytes for the pointer
		// whose OID is the sha256 of those same bytes, so no confirmation is needed.
		FixLevel:    FixLevelSuggested,
		Description: "Restores LFS pointers where unpushed Ledger commits hold raw session content",
		Run:         checkSessionPointerRestore,
	})
}

type pointerRestoreFailure struct {
	Path   string
	Reason string
}

// pointerRestoreReport is the outcome of one scan (and repair) of @{u}..HEAD.
type pointerRestoreReport struct {
	Raw          []string                // artifacts that are raw content at HEAD
	Restored     []string                // rewritten to a pointer in the new commit
	Unrepairable []pointerRestoreFailure // raw content no pointer or manifest vouches for
	Committed    bool
	// Remaining is what the push validator still reports after the repair; it
	// names the first file that keeps blocking the push.
	Remaining error
}

func checkSessionPointerRestore(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(pointerRestoreCheckName, "no ledger found", "")
	}
	if !isGitRepo(ledgerPath) {
		return SkippedCheck(pointerRestoreCheckName, "ledger not a git repo", "")
	}

	return runSessionPointerRestore(ledgerPath, fix)
}

// runSessionPointerRestore is the check body for one resolved Ledger.
func runSessionPointerRestore(ledgerPath string, fix bool) checkResult {
	report, err := restoreUnpushedSessionPointers(context.Background(), ledgerPath, fix)
	switch {
	case errors.Is(err, errNoUpstream):
		return SkippedCheck(pointerRestoreCheckName, "ledger has no upstream branch", "")
	case gitutil.IsRepoLockBusy(err):
		return SkippedCheck(pointerRestoreCheckName, "ledger busy with another ox process", "Rerun `ox doctor --fix-slug="+CheckSlugSessionPointerRestore+"` in a moment")
	case err != nil:
		return FailedCheck(pointerRestoreCheckName, "could not inspect unpushed session commits", err.Error())
	}

	if len(report.Raw) == 0 {
		return PassedCheck(pointerRestoreCheckName, "no raw session content in unpushed commits")
	}
	fixCommand := "ox doctor --fix-slug=" + CheckSlugSessionPointerRestore
	if !fix {
		return FailedCheck(pointerRestoreCheckName,
			fmt.Sprintf("%d session artifact(s) in unpushed commits hold raw content where LFS pointers belong; pushes are refused", len(report.Raw)),
			fmt.Sprintf("Run `%s` to restore the pointers (adds one commit, local copies kept in the cache)", fixCommand))
	}
	if len(report.Unrepairable) > 0 {
		var detail strings.Builder
		fmt.Fprintf(&detail, "restored %d; %d cannot be restored automatically and still block the push:", len(report.Restored), len(report.Unrepairable))
		for _, failure := range report.Unrepairable {
			fmt.Fprintf(&detail, "\n       %s: %s", failure.Path, failure.Reason)
		}
		fmt.Fprintf(&detail, "\n       Re-upload the session from a machine that has its content, or restore the pointer by hand, then rerun `%s`.", fixCommand)
		return FailedCheck(pointerRestoreCheckName, "some session artifacts hold raw content no known LFS object matches", detail.String())
	}
	if report.Remaining != nil {
		return FailedCheck(pointerRestoreCheckName, "restored pointers but the push validator still refuses the tip", report.Remaining.Error())
	}
	return PassedCheck(pointerRestoreCheckName, fmt.Sprintf("restored LFS pointers for %d session artifact(s)", len(report.Restored)))
}

var errNoUpstream = errors.New("no upstream branch")

// rawSessionArtifact is one content artifact whose HEAD blob is not a pointer.
type rawSessionArtifact struct {
	path    string // ledger-relative, slash-separated
	content []byte // the HEAD blob
}

// restoreUnpushedSessionPointers scans @{u}..HEAD for session artifacts that are
// raw content at HEAD. With fix, each one whose sha256 matches the pointer
// upstream holds at that path, or the OID in its session's meta.json, is rewritten
// to that pointer and committed once through CommitLedgerSnapshot with explicit
// pathspecs. Existing commits are never amended or rewritten; anything nothing
// vouches for is reported and left exactly as it is.
func restoreUnpushedSessionPointers(ctx context.Context, ledgerPath string, fix bool) (pointerRestoreReport, error) {
	var report pointerRestoreReport
	upstream, err := ledgerUpstream(ctx, ledgerPath)
	if err != nil {
		return report, err
	}
	raws, err := rawSessionArtifactsAhead(ctx, ledgerPath, upstream)
	if err != nil {
		return report, err
	}
	for _, raw := range raws {
		report.Raw = append(report.Raw, raw.path)
	}
	if len(raws) == 0 || !fix {
		return report, nil
	}

	err = gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		var repairable []string
		for _, raw := range raws {
			if reason := restoreOne(ctx, ledgerPath, upstream, raw); reason != "" {
				report.Unrepairable = append(report.Unrepairable, pointerRestoreFailure{Path: raw.path, Reason: reason})
				continue
			}
			repairable = append(repairable, raw.path)
		}
		if len(repairable) == 0 {
			return nil
		}
		guard := newSessionStageGuard(ledgerPath, filepath.Join(ledgerPath, "sessions"))
		if err := guard.gitPathspec(ctx, repairable, "add", "--sparse"); err != nil {
			return fmt.Errorf("stage restored pointers: %w", err)
		}
		msg := fmt.Sprintf("doctor: restore LFS pointers for %d session artifacts", len(repairable))
		committed, err := gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, repairable...)
		if err != nil {
			return fmt.Errorf("commit restored pointers: %w", err)
		}
		report.Committed = committed
		report.Restored = repairable
		return nil
	})
	if err != nil {
		return report, err
	}
	// the same validation the push path runs, so a clean result proves the push is unblocked
	report.Remaining = lfs.ValidateUnpushedTip(ctx, ledgerPath, upstream)
	return report, nil
}

// restoreOne rewrites one working-tree artifact to the pointer that the evidence
// names, or returns the reason it cannot. It touches the file only after the
// evidence matches the committed bytes exactly.
func restoreOne(ctx context.Context, ledgerPath, upstream string, raw rawSessionArtifact) string {
	parts := strings.Split(raw.path, "/")
	sessionID, name := parts[1], strings.Join(parts[2:], "/")
	abs := filepath.Join(ledgerPath, filepath.FromSlash(raw.path))

	ref, _, ok := lfs.ResolveRestorableRef(raw.content, name, headSessionMeta(ctx, ledgerPath, sessionID), upstreamPointer(ctx, ledgerPath, upstream, raw.path))
	if !ok {
		return "content sha256 matches no pointer on " + upstream + " and no entry in the session's meta.json"
	}

	// the new commit is built from the working file, so it must be exactly what HEAD holds
	// (or already the matching pointer); never overwrite someone's newer local edit
	working, err := os.ReadFile(abs)
	switch {
	case err != nil:
		return fmt.Sprintf("working copy unreadable: %v", err)
	case bytes.Equal(working, raw.content):
		cachePath := filepath.Join(ledgerPath, ".sageox", "cache", filepath.FromSlash(raw.path))
		if err := lfs.RestorePointer(abs, cachePath, raw.content, ref); err != nil {
			return err.Error()
		}
	case lfs.IsPointerFile(abs):
		current, err := lfs.ReadPointerFile(abs)
		if err != nil || current.BareOID() != ref.BareOID() || current.Size != ref.Size {
			return "working copy is a different pointer than the committed content's OID"
		}
	default:
		return "working copy differs from the committed content (uncommitted local edit); commit or discard it first"
	}
	return ""
}

func ledgerUpstream(ctx context.Context, ledgerPath string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Output()
	if err != nil {
		return "", errNoUpstream
	}
	return strings.TrimSpace(string(out)), nil
}

// rawSessionArtifactsAhead lists content artifacts that HEAD changes relative to
// upstream and holds as raw content, using the tree delta the push validator reads.
func rawSessionArtifactsAhead(ctx context.Context, ledgerPath, upstream string) ([]rawSessionArtifact, error) {
	raw, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "diff-tree", "-r", "-z", "--no-renames", "--raw", upstream, "HEAD", "--", "sessions/").Output()
	if err != nil {
		return nil, fmt.Errorf("diff unpushed tree: %w", err)
	}
	tokens := strings.Split(string(raw), "\x00")
	var found []rawSessionArtifact
	for i := 0; i+1 < len(tokens); i += 2 {
		fields := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		path := tokens[i+1]
		parts := strings.Split(path, "/")
		if len(fields) < 5 || strings.HasPrefix(fields[4], "D") || fields[1] != "100644" && fields[1] != "100755" {
			continue
		}
		if len(parts) < 3 || parts[0] != "sessions" || !lfs.IsContentArtifact(strings.Join(parts[2:], "/")) {
			continue
		}
		blob, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "cat-file", "blob", fields[3]).Output()
		if err != nil {
			return nil, fmt.Errorf("read unpushed blob %s: %w", path, err)
		}
		if _, _, perr := pointer.Parse(string(blob)); perr == nil {
			continue
		}
		if meta := headSessionMeta(ctx, ledgerPath, parts[1]); meta.StoredInGit(strings.Join(parts[2:], "/")) {
			continue // Storage=git: raw content is the correct state
		}
		found = append(found, rawSessionArtifact{path: path, content: blob})
	}
	return found, nil
}

// headSessionMeta reads the session manifest from HEAD, not the working tree, so
// the evidence is exactly what the unpushed commits hold.
func headSessionMeta(ctx context.Context, ledgerPath, sessionID string) *lfs.SessionMeta {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", "HEAD:sessions/"+sessionID+"/meta.json").Output()
	if err != nil {
		return nil
	}
	var meta lfs.SessionMeta
	if json.Unmarshal(out, &meta) != nil {
		return nil
	}
	return &meta
}

// upstreamPointer returns the pointer the upstream branch holds at path, or nil.
func upstreamPointer(ctx context.Context, ledgerPath, upstream, path string) *lfs.FileRef {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", upstream+":"+path).Output()
	if err != nil {
		return nil
	}
	oid, size, err := lfs.ParsePointer(string(out))
	if err != nil {
		return nil
	}
	return &lfs.FileRef{Storage: lfs.StorageLFS, OID: oid, Size: size}
}
