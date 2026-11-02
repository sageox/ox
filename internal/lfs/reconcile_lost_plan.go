package lfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/sacred"
)

// A plan page (data/plans/<plan>/plan.html above 1 MiB) is committed as an LFS
// pointer once its upload is verified. The store can still lose the blob before
// the commit is pushed: GitLab housekeeping deletes every LFS object no pushed
// ref points at (every 200 pushes, no grace period), and a Ledger push can lag
// the save by days. Builds before the page was kept in the recovery cache left
// no local copy, so such a pointer can never be satisfied, and because GitLab
// checks every pointer in a push, it refuses every later push from the clone.
//
// When that is the whole problem, the pointer is replaced in unpushed history
// by a tombstone page that names the lost object. Only the pointer's content
// changes: plan.md, meta.json, events.jsonl and every other file stay as they
// are, pushed history is never rewritten, and the pre-squash tip stays
// reachable under refs/ox-backup/. Session pointers are never replaced.

// maxLostPlanRenders bounds how many lost pages one reconcile replaces. Real
// loss is a plan or two saved during a long push outage; 404s across many plans
// at once look like a store or credential fault, so above this the reconcile
// fails closed like any other unrecoverable pointer and a human looks.
const maxLostPlanRenders = sacred.MassDeleteThreshold

// renderLostMetaName marks a tombstone page and records the lost object, so a
// copy that turns up later can be matched to it.
const renderLostMetaName = "sageox:render-lost"

// isPlanRenderPath reports whether relPath is exactly data/plans/<plan>/plan.html.
func isPlanRenderPath(relPath string) bool {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	return len(parts) == 4 && parts[0] == "data" && parts[1] == "plans" &&
		parts[2] != "" && parts[2] != "." && parts[2] != ".." && parts[3] == "plan.html"
}

// onlyLostPlanRenders reports whether the unrecoverable pointers are all plan
// pages, at least one and at most maxLostPlanRenders.
func onlyLostPlanRenders(unrecoverable []pointerEntry) bool {
	if len(unrecoverable) == 0 || len(unrecoverable) > maxLostPlanRenders {
		return false
	}
	for _, p := range unrecoverable {
		if !isPlanRenderPath(p.relPath) {
			return false
		}
	}
	return true
}

// planRenderTombstone is the page committed in place of a lost plan.html. It is
// well over the pointer size limit and does not start like a pointer, so
// neither GitLab nor ox can take it for one.
func planRenderTombstone(ref FileRef) []byte {
	return []byte(fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="%s" content="%s size=%d">
<title>Plan page unavailable</title>
</head>
<body>
<h1>This plan's page was lost before it reached the team</h1>
<p>The rendered page (%d bytes) never made it to the team's content store, and no copy was left on the machine that saved it. The plan's text and history are intact: read plan.md next to this file.</p>
<p>If you still have the original page, save it again with <code>ox plan save</code>.</p>
</body>
</html>
`, renderLostMetaName, html.EscapeString(ref.OID), ref.Size, ref.Size))
}

// repairLostPlanRenders replaces each lost plan page pointer with its tombstone
// in one scoped commit, then squashes the unpushed commits so no commit in the
// push still carries the pointer. Both steps commit validated tree snapshots
// and never read other index entries.
func repairLostPlanRenders(ctx context.Context, ledgerPath string, logger *slog.Logger, result *ReconcileResult, upstream string, lost []pointerEntry) (*ReconcileResult, error) {
	// the squash republishes the whole unpushed tip: refuse before the first
	// local change rather than leave a tombstone commit the squash then rejects
	if err := validateUnpushedTip(ctx, ledgerPath, upstream); err != nil {
		return result, fmt.Errorf("validate unpushed Ledger before replacing lost plan pages: %w", err)
	}
	if err := tombstoneLostPlanRenders(ctx, ledgerPath, lost, logger); err != nil {
		return result, err
	}
	result.LostPlanRenders = len(lost)
	return squashHistoryOnly(ctx, ledgerPath, logger, result)
}

// tombstoneLostPlanRenders commits a tombstone at each lost page's path, then
// rewrites the working copy so it matches and no later `git add` re-commits the
// dead pointer. The caller holds the repo lock, and preflightMissingPointer has
// proven each working copy is the committed pointer or absent.
func tombstoneLostPlanRenders(ctx context.Context, ledgerPath string, lost []pointerEntry, logger *slog.Logger) error {
	pathspecs := make([]string, 0, len(lost))
	staged := make([]string, 0, len(lost))
	for _, p := range lost {
		rel := filepath.ToSlash(p.relPath)
		if _, err := gitutil.StageLedgerBlob(ctx, ledgerPath, rel, planRenderTombstone(p.ref)); err != nil {
			return errors.Join(fmt.Errorf("stage tombstone for %s: %w", rel, err), unstage(ctx, ledgerPath, staged))
		}
		staged = append(staged, rel)
		pathspecs = append(pathspecs, ":(top,literal)"+rel)
	}
	msg := fmt.Sprintf("ledger: replace %d lost plan page(s) with a tombstone", len(lost))
	if _, err := gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, pathspecs...); err != nil {
		return errors.Join(fmt.Errorf("commit tombstones for lost plan pages: %w", err), unstage(ctx, ledgerPath, staged))
	}

	for _, p := range lost {
		rel := filepath.ToSlash(p.relPath)
		planName := filepath.Base(filepath.Dir(p.relPath))
		logger.Warn("lfs reconcile: plan page lost before it reached the store; replaced its pointer with a tombstone in unpushed history",
			"plan", planName, "path", rel, "oid", p.ref.OID, "size", p.ref.Size)
		if err := rewriteWorkingPointer(filepath.Join(ledgerPath, p.relPath), p.ref, planRenderTombstone(p.ref)); err != nil {
			// the commit already landed and the push can proceed; a stale
			// working copy only shows up as a local modification
			logger.Warn("lfs reconcile: tombstone committed but the working copy was left as it is",
				"plan", planName, "path", rel, "error", err)
		}
	}
	return nil
}

// unstage restores the index entries of paths to HEAD after a failed commit, so
// a tombstone that was never committed cannot ride along with a later one.
func unstage(ctx context.Context, ledgerPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"reset", "-q", "--"}
	for _, path := range paths {
		args = append(args, ":(top,literal)"+path)
	}
	if out, err := gitutil.RunGit(context.WithoutCancel(ctx), ledgerPath, args...); err != nil {
		return fmt.Errorf("unstage tombstones: %s: %w", gitutil.SanitizeOutput(out), err)
	}
	return nil
}

// rewriteWorkingPointer replaces the file at path with content only while it
// still holds exactly the pointer for ref; an absent file (outside the sparse
// checkout) is left absent, and any other content is someone's edit.
func rewriteWorkingPointer(path string, ref FileRef, content []byte) error {
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, []byte(FormatPointer(ref.OID, ref.Size))) {
		return fmt.Errorf("working copy changed during reconcile")
	}
	return fileutil.AtomicWriteBytes(path, content, 0o644)
}

// MissingPlanRender is a plan page pointer in unpushed history whose blob the
// store does not have.
type MissingPlanRender struct {
	Path string // ledger-relative, slash-separated
	OID  string
	Size int64
	// Recoverable means the exact bytes are in the Ledger recovery cache, so
	// the push repair uploads them again; otherwise the page is lost.
	Recoverable bool
}

// FindMissingPlanRenders reports the plan page pointers at the unpushed tip
// whose blobs the store does not have. It changes nothing, and asks the store
// only when the push range holds a plan page pointer.
func FindMissingPlanRenders(ctx context.Context, ledgerPath, endpointURL string) ([]MissingPlanRender, error) {
	return findMissingPlanRenders(ctx, ledgerPath, func() (*Client, error) {
		return NewClientFromLedgerContext(ctx, ledgerPath, endpointURL)
	})
}

func findMissingPlanRenders(ctx context.Context, ledgerPath string, newClient func() (*Client, error)) ([]MissingPlanRender, error) {
	set, err := scanPushRange(ctx, ledgerPath, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("scan push range for pointers: %w", err)
	}
	renders := &pointerSet{}
	for _, p := range set.repairable {
		if isPlanRenderPath(p.relPath) {
			renders.repairable = append(renders.repairable, p)
		}
	}
	if len(renders.repairable) == 0 {
		return nil, nil
	}
	client, err := newClient()
	if err != nil {
		return nil, fmt.Errorf("lfs client: %w", err)
	}
	missingOIDs, err := findMissingOIDs(ctx, client, renders)
	if err != nil {
		return nil, err
	}
	var missing []MissingPlanRender
	for _, p := range renders.repairable {
		if !missingOIDs[p.ref.BareOID()] {
			continue
		}
		content, err := preflightMissingPointer(ledgerPath, p)
		if err != nil {
			return nil, err
		}
		missing = append(missing, MissingPlanRender{
			Path:        filepath.ToSlash(p.relPath),
			OID:         p.ref.OID,
			Size:        p.ref.Size,
			Recoverable: content != nil,
		})
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Path < missing[j].Path })
	return missing, nil
}
