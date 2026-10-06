package lfs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/sacred"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/internal/useragent"
)

// ReconcileResult describes what ReconcileUnpushedPointers found and fixed.
type ReconcileResult struct {
	ScannedPointers  int      // distinct pointers examined (push-range scope) or pointer files found (whole-tree scope)
	MissingOnRemote  int      // pointers whose LFS OIDs are not in the remote store
	HistoryOnly      int      // missing OIDs referenced only by intermediate unpushed commits, not the tip
	RecoveredUploads int      // missing OIDs restored from exact local recovery-cache content
	Replaced         int      // unrecoverable pointer artifacts removed
	Squashed         bool     // whether unpushed history was squashed
	ReplacedFiles    []string // relative paths of removed pointer artifacts, sorted
}

// Changed reports whether the reconcile rewrote anything a push retry can
// benefit from: it restored a blob, removed pointer artifacts, or rewrote
// unpushed history.
func (r *ReconcileResult) Changed() bool {
	return r != nil && (r.RecoveredUploads > 0 || r.Replaced > 0 || r.Squashed)
}

// ReconcileUnpushedPointers repairs a ledger whose push is blocked by
// "GitLab: LFS objects are missing". It looks at exactly what GitLab's
// pre-receive hook looks at — the pointer blobs introduced by the commits in
// @{upstream}..HEAD — and nothing else.
//
// Pointers already on the remote are out of scope however broken they look: the
// server accepted them when they were pushed, and a coworker's session whose
// blob later went missing is not this push's problem. Walking the whole working
// tree (as this once did) meant one such session aborted the repair with a
// metadata mismatch, a different one on every run, so the repair never reached
// the squash that fixes the push.
//
// For each in-range pointer whose blob the remote does not have (a 404 from the
// LFS Batch API; any other answer aborts):
//   - at the tip, under sessions/ or data/plans/: the unrecoverable artifact is
//     removed and its meta.json reference cleared, then the replacement is
//     committed;
//   - only in an intermediate unpushed commit: nothing at the tip to repair, so
//     the squash alone drops it from the push pack.
//
// Unpushed commits are then squashed into one, because GitLab's pre-receive hook
// scans every commit in the pack, not just HEAD: replacing a pointer at the tip
// does not remove it from an earlier commit.
//
// data/plans/ is covered because a poisoned PLAN pointer wedges the push exactly
// like a session one (the pre-fix GH #810 bug left a ledger unpushable for 43
// commits). Post-fix the plan path never commits an un-uploaded pointer; this
// heals the ones that predate the fix (their bytes are gone, so removing the
// broken reference is the only recovery) and backstops any future regression.
//
// An exact session recovery-cache file is uploaded again without changing its
// pointer, metadata, or cache. Ambiguous cache content fails closed. A missing
// upstream or diverged branch also fails closed; callers needing a deliberate
// whole-tree repair must use ReconcileAllPointers.
func ReconcileUnpushedPointers(ctx context.Context, ledgerPath, endpointURL string, logger *slog.Logger) (*ReconcileResult, error) {
	return reconcileLocked(ctx, ledgerPath, endpointURL, logger, reconcileUnpushedPointers)
}

// ReconcileAllPointers is the whole-working-tree variant: it examines every
// pointer under sessions/ and data/plans/ whether or not it was ever pushed.
// `ox doctor` uses it to clear a missing blob it found by scanning the tree;
// the push path uses ReconcileUnpushedPointers, which cannot be derailed by
// pointers the remote already accepted.
func ReconcileAllPointers(ctx context.Context, ledgerPath, endpointURL string, logger *slog.Logger) (*ReconcileResult, error) {
	return reconcileLocked(ctx, ledgerPath, endpointURL, logger, reconcileAllPointers)
}

type reconcileFunc func(ctx context.Context, ledgerPath string, logger *slog.Logger, newClient func() (*Client, error)) (*ReconcileResult, error)

func reconcileLocked(ctx context.Context, ledgerPath, endpointURL string, logger *slog.Logger, run reconcileFunc) (*ReconcileResult, error) {
	var result *ReconcileResult
	err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		var err error
		result, err = run(ctx, ledgerPath, logger, func() (*Client, error) {
			return NewClientFromLedgerContext(ctx, ledgerPath, endpointURL)
		})
		return err
	})
	if result == nil {
		result = &ReconcileResult{}
	}
	return result, err
}

// reconcileUnpushedPointers is the client-injectable core of
// ReconcileUnpushedPointers. newClient is invoked ONLY when orphan candidates are
// found, preserving the "no pointers → no client, no error" behavior that clean
// ledgers with no configured remote rely on. Tests inject a fake-LFS-server
// client to exercise the missing-artifact removal and squash path.
func reconcileUnpushedPointers(ctx context.Context, ledgerPath string, logger *slog.Logger, newClient func() (*Client, error)) (*ReconcileResult, error) {
	return reconcilePointers(ctx, ledgerPath, logger, newClient, true)
}

// reconcileAllPointers is the client-injectable core of ReconcileAllPointers.
func reconcileAllPointers(ctx context.Context, ledgerPath string, logger *slog.Logger, newClient func() (*Client, error)) (*ReconcileResult, error) {
	return reconcilePointers(ctx, ledgerPath, logger, newClient, false)
}

// pointerEntry is one pointer file at the branch tip.
type pointerEntry struct {
	relPath string // relative to ledgerPath
	ref     FileRef
}

// pointerSet is everything a reconcile examines, split by what can be done
// about it if the blob is missing.
type pointerSet struct {
	upstream string
	// repairable are tip pointers under a tree the repair knows how to clean.
	repairable []pointerEntry
	// unrepairable are tip pointers anywhere else. The repair cannot clear their
	// metadata, so a missing blob here is reported, never guessed at.
	unrepairable []pointerEntry
	// historyOnly are pointers that appear in an unpushed commit but not at the
	// tip, keyed by bare OID. Squashing is the whole repair.
	historyOnly map[string]FileRef
}

func (p *pointerSet) total() int {
	return len(p.repairable) + len(p.unrepairable) + len(p.historyOnly)
}

func reconcilePointers(ctx context.Context, ledgerPath string, logger *slog.Logger, newClient func() (*Client, error), pushRangeOnly bool) (*ReconcileResult, error) {
	if logger == nil {
		logger = slog.Default()
	}

	result := &ReconcileResult{}

	var set *pointerSet
	if pushRangeOnly {
		if err := ensureReconcileGitSafe(ctx, ledgerPath); err != nil {
			return result, fmt.Errorf("preflight Ledger for LFS reconcile: %w", err)
		}
		var err error
		set, err = scanPushRange(ctx, ledgerPath, logger)
		if err != nil {
			return result, fmt.Errorf("scan push range for pointers: %w", err)
		}
	}
	if set == nil {
		// whole-tree mode: every pointer in the working tree
		pointers, err := walkWorkingTreePointers(ledgerPath)
		if err != nil {
			return result, err
		}
		set = &pointerSet{repairable: pointers}
	}

	result.ScannedPointers = set.total()
	if result.ScannedPointers == 0 {
		return result, nil
	}

	// batch-check which OIDs exist in the remote LFS store
	client, err := newClient()
	if err != nil {
		return result, fmt.Errorf("lfs client: %w", err)
	}

	missingOIDs, err := findMissingOIDs(ctx, client, set)
	if err != nil {
		return result, err
	}

	// every missing pointer file, in path order so removal, staging, and any
	// failure message are the same on every run
	var missingEntries []pointerEntry
	for _, p := range set.repairable {
		if missingOIDs[p.ref.BareOID()] {
			missingEntries = append(missingEntries, p)
		}
	}
	sort.Slice(missingEntries, func(i, j int) bool { return missingEntries[i].relPath < missingEntries[j].relPath })

	var unrepairableMissing []string
	for _, p := range set.unrepairable {
		if missingOIDs[p.ref.BareOID()] {
			unrepairableMissing = append(unrepairableMissing, p.relPath)
		}
	}
	for bareOID := range set.historyOnly {
		if missingOIDs[bareOID] {
			result.HistoryOnly++
		}
	}

	result.MissingOnRemote = len(missingEntries) + len(unrepairableMissing) + result.HistoryOnly
	if result.MissingOnRemote == 0 {
		logger.Debug("lfs reconcile: all pointer OIDs present in remote", "count", result.ScannedPointers)
		return result, nil
	}

	if len(unrepairableMissing) > 0 {
		sort.Strings(unrepairableMissing)
		return result, fmt.Errorf("LFS object for %s is missing from the remote but sits outside sessions/ and data/plans/, "+
			"which reconcile cannot repair (%d such path(s))", unrepairableMissing[0], len(unrepairableMissing))
	}

	// Classify every candidate before the first upload or local mutation. Exact
	// recovery-cache bytes are safe to re-upload; every ambiguous cache state
	// fails closed rather than being treated as permission to delete.
	var recoverable []recoveryUpload
	var replaceable []pointerEntry
	for _, p := range missingEntries {
		content, err := preflightMissingPointer(ledgerPath, p)
		if err != nil {
			return result, err
		}
		if content != nil {
			recoverable = append(recoverable, recoveryUpload{entry: p, content: content})
		} else {
			replaceable = append(replaceable, p)
		}
	}

	if !pushRangeOnly {
		if err := ensureReconcileGitSafe(ctx, ledgerPath); err != nil {
			return result, fmt.Errorf("preflight Ledger for LFS reconcile: %w", err)
		}
	}

	missingRefs := make(map[string]FileRef, len(replaceable))
	for _, p := range replaceable {
		missingRefs[p.relPath] = p.ref
	}
	metadata, err := prepareMissingPointerMetadata(ledgerPath, missingRefs)
	if err != nil {
		return result, fmt.Errorf("prepare missing artifact metadata: %w", err)
	}

	// Reconcile's commit is intentionally unscoped because the later soft-reset
	// squash republishes every unpushed change. Validate both the current index
	// and the final unpushed tip before uploading or changing local state.
	if err := gitutil.ValidateStagedLedgerCommit(ctx, ledgerPath); err != nil {
		return result, fmt.Errorf("validate Ledger before LFS reconcile: %w", err)
	}
	if pushRangeOnly {
		if err := validateUnpushedTip(ctx, ledgerPath, set.upstream); err != nil {
			return result, fmt.Errorf("validate unpushed Ledger before LFS reconcile: %w", err)
		}
		if err := preflightSacredDeletions(ctx, ledgerPath, set.upstream, replaceable); err != nil {
			return result, err
		}
	} else if len(replaceable) > sacred.MassDeleteThreshold {
		return result, fmt.Errorf("refusing LFS reconcile: would delete %d sacred files, exceeding threshold %d",
			len(replaceable), sacred.MassDeleteThreshold)
	}

	for _, upload := range recoverable {
		if err := uploadRecoveryBlob(ctx, client, upload); err != nil {
			return result, fmt.Errorf("restore LFS object for %s from recovery cache: %w", upload.entry.relPath, err)
		}
	}
	result.RecoveredUploads = len(recoverable)

	if len(replaceable) == 0 {
		if result.HistoryOnly == 0 {
			logger.Info("lfs reconcile complete", "recovered_uploads", result.RecoveredUploads)
			return result, nil
		}
		return squashHistoryOnly(ctx, ledgerPath, logger, result)
	}

	logger.Info("lfs reconcile: removing unrecoverable pointer artifacts",
		"missing", len(replaceable), "recovered_uploads", result.RecoveredUploads,
		"history_only", result.HistoryOnly, "total_pointers", result.ScannedPointers)

	// Remove missing artifacts instead of committing non-pointer bytes under
	// LFS-listed filenames. Metadata was validated before changing any file.
	for _, p := range replaceable {
		absPath := filepath.Join(ledgerPath, p.relPath)
		// absent is fine: a path outside the sparse cone has nothing on disk,
		// and the staged removal below is what matters
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("remove missing pointer %s: %w", p.relPath, err)
		}
		addCtx, addCancel := context.WithTimeout(ctx, 5*time.Second)
		_, addErr := gitutil.RunGit(addCtx, ledgerPath, "add", "--sparse", p.relPath)
		addCancel()
		if addErr != nil {
			return result, fmt.Errorf("stage missing pointer removal %s: %w", p.relPath, addErr)
		}
		result.Replaced++
		result.ReplacedFiles = append(result.ReplacedFiles, p.relPath)
	}

	metaPaths := make([]string, 0, len(metadata))
	for relPath := range metadata {
		metaPaths = append(metaPaths, relPath)
	}
	sort.Strings(metaPaths)
	for _, relPath := range metaPaths {
		if err := fileutil.AtomicWriteBytes(filepath.Join(ledgerPath, relPath), metadata[relPath], 0o644); err != nil {
			return result, fmt.Errorf("update missing artifact metadata %s: %w", relPath, err)
		}
		if _, err := gitutil.RunGit(ctx, ledgerPath, "add", "--sparse", relPath); err != nil {
			return result, fmt.Errorf("stage missing artifact metadata %s: %w", relPath, err)
		}
	}

	if result.Replaced == 0 {
		return result, nil
	}

	// commit the replacements
	msg := fmt.Sprintf("fix: remove %d unrecoverable LFS artifacts", result.Replaced)
	commitCtx, commitCancel := context.WithTimeout(ctx, 10*time.Second)
	_, commitErr := gitutil.CommitLedgerSnapshot(commitCtx, ledgerPath, msg)
	commitCancel()
	if commitErr != nil {
		return result, fmt.Errorf("commit replacements: %w", commitErr)
	}

	// squash all unpushed commits into one — GitLab's pre-receive hook checks
	// every commit in the push, not just HEAD. Without squashing, old commits
	// still reference the missing LFS OIDs and the push is still rejected.
	// a failed squash is a real error: the replacement commit exists but old
	// commits still poison the push pack
	if sqErr := squashUnpushed(ctx, ledgerPath, msg); sqErr != nil {
		return result, fmt.Errorf("pointer replacement committed but squash failed (push will still fail): %w", sqErr)
	}
	result.Squashed = true

	logger.Info("lfs reconcile complete", "replaced", result.Replaced, "recovered_uploads", result.RecoveredUploads)
	return result, nil
}

// squashHistoryOnly is the repair when the tip is clean and only intermediate
// unpushed commits reference missing blobs: the squash alone drops them from
// the push pack.
func squashHistoryOnly(ctx context.Context, ledgerPath string, logger *slog.Logger, result *ReconcileResult) (*ReconcileResult, error) {
	before, err := headCommit(ctx, ledgerPath)
	if err != nil {
		return result, err
	}
	logger.Info("lfs reconcile: squashing unpushed commits to drop missing LFS objects referenced only by history",
		"history_only", result.HistoryOnly)
	msg := fmt.Sprintf("fix: squash unpushed commits to drop %d unrecoverable LFS references", result.HistoryOnly)
	if err := squashUnpushed(ctx, ledgerPath, msg); err != nil {
		return result, fmt.Errorf("squash unpushed history: %w", err)
	}
	after, err := headCommit(ctx, ledgerPath)
	if err != nil {
		return result, err
	}
	result.Squashed = before != after
	logger.Info("lfs reconcile complete", "squashed", result.Squashed, "history_only", result.HistoryOnly)
	return result, nil
}

func headCommit(ctx context.Context, ledgerPath string) (string, error) {
	headCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := gitutil.RunGit(headCtx, ledgerPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

type recoveryUpload struct {
	entry   pointerEntry
	content []byte
}

// preflightMissingPointer verifies the working pointer and classifies its
// recovery cache without changing either. A nil content result means no cache
// exists and the pointer is eligible for deliberate removal. Any cache that is
// not provably the exact pointed-to object fails closed.
func preflightMissingPointer(ledgerPath string, p pointerEntry) ([]byte, error) {
	workingPath := filepath.Join(ledgerPath, p.relPath)
	if info, err := os.Lstat(workingPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to replace %s: working copy is not a regular file", p.relPath)
		}
		working, readErr := os.ReadFile(workingPath)
		if readErr != nil {
			return nil, fmt.Errorf("inspect working copy of %s: %w", p.relPath, readErr)
		}
		oid, size, parseErr := ParsePointer(string(working))
		if len(working) > maxPointerSize || parseErr != nil || (FileRef{OID: oid}).BareOID() != p.ref.BareOID() || size != p.ref.Size {
			return nil, fmt.Errorf("refusing to replace %s: working copy differs from the committed pointer", p.relPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect working copy of %s: %w", p.relPath, err)
	}

	if !strings.HasPrefix(p.relPath, "sessions"+string(filepath.Separator)) {
		return nil, nil
	}
	cachePath := filepath.Join(ledgerPath, ".sageox", "cache", p.relPath)
	info, err := os.Lstat(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect session recovery cache for %s: %w", p.relPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to replace %s: session recovery cache is not a regular non-symlink file", p.relPath)
	}
	content, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("read session recovery cache for %s: %w", p.relPath, err)
	}
	if pointerShaped(content) {
		return nil, fmt.Errorf("refusing to replace %s: session recovery cache contains an LFS pointer", p.relPath)
	}
	actual := NewFileRef(content)
	if actual.Size != p.ref.Size || actual.BareOID() != p.ref.BareOID() {
		return nil, fmt.Errorf("refusing to replace %s: session recovery cache does not match pointer OID and size", p.relPath)
	}
	return content, nil
}

// uploadRecoveryBlob restores one exact cache object through context-bound
// batch, upload, and verify requests. A nil error is proof the object is
// present; the pointer, metadata, and cache remain untouched.
func uploadRecoveryBlob(ctx context.Context, client *Client, upload recoveryUpload) error {
	ref := upload.entry.ref
	resp, err := client.BatchUploadContext(ctx, []BatchObject{{OID: ref.BareOID(), Size: ref.Size}})
	if err != nil {
		return fmt.Errorf("LFS batch upload: %w", err)
	}
	for _, obj := range resp.Objects {
		if obj.OID != ref.BareOID() {
			continue
		}
		if obj.Size != 0 && obj.Size != ref.Size {
			return fmt.Errorf("batch response size %d does not match expected %d", obj.Size, ref.Size)
		}
		if obj.Error != nil {
			return fmt.Errorf("server error %d: %s", obj.Error.Code, obj.Error.Message)
		}
		if obj.Actions == nil || obj.Actions.Upload == nil {
			return nil // object became present between the download and upload batches
		}
		if err := putRecoveryObject(ctx, obj.Actions.Upload, upload.content); err != nil {
			return err
		}
		if obj.Actions.Verify != nil {
			if err := verifyRecoveryObject(ctx, obj.Actions.Verify, ref); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("batch response omitted OID %s", ref.BareOID())
}

func putRecoveryObject(ctx context.Context, action *Action, content []byte) error {
	if err := validateActionHref(action); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, action.Href, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("User-Agent", useragent.String())
	if err := action.setRequestHeaders(req); err != nil {
		return err
	}
	req.ContentLength = int64(len(content))
	resp, err := lfsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("upload returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func verifyRecoveryObject(ctx context.Context, action *Action, ref FileRef) error {
	if err := validateActionHref(action); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	body := fmt.Sprintf(`{"oid":%q,"size":%d}`, ref.BareOID(), ref.Size)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action.Href, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("create verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	req.Header.Set("User-Agent", useragent.String())
	if err := action.setRequestHeaders(req); err != nil {
		return err
	}
	resp, err := lfsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("verify returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// findMissingOIDs batch-checks every distinct OID in set against the remote
// store and returns the bare OIDs the server reports as 404.
func findMissingOIDs(ctx context.Context, client *Client, set *pointerSet) (map[string]bool, error) {
	// deduplicate OIDs — multiple pointer files can reference the same blob
	seen := make(map[string]bool, set.total())
	var uniqueObjects []BatchObject
	add := func(ref FileRef) {
		if bareOID := ref.BareOID(); !seen[bareOID] {
			seen[bareOID] = true
			uniqueObjects = append(uniqueObjects, BatchObject{OID: bareOID, Size: ref.Size})
		}
	}
	for _, p := range set.repairable {
		add(p.ref)
	}
	for _, p := range set.unrepairable {
		add(p.ref)
	}
	for _, ref := range set.historyOnly {
		add(ref)
	}

	// batch-check in chunks — the LFS Batch API request body can exceed WAF
	// limits (8KB) when there are many pointers. Each pointer is ~100 bytes
	// of JSON, so 50 per chunk stays well under the limit.
	const batchChunkSize = 50
	missing := make(map[string]bool)
	for start := 0; start < len(uniqueObjects); start += batchChunkSize {
		end := min(start+batchChunkSize, len(uniqueObjects))

		resp, err := client.BatchDownloadContext(ctx, uniqueObjects[start:end])
		if err != nil {
			return nil, fmt.Errorf("lfs batch check: %w", err)
		}

		for _, obj := range resp.Objects {
			if obj.Error == nil {
				continue
			}
			// Only 404 permits missing-object recovery. Any other status —
			// 401 (token expired), 429 (rate limited), 5xx (server trouble) —
			// says nothing about whether the object exists, and treating it as
			// "gone" blanks a live recording to zero bytes, an operation with no
			// inverse. Abort the reconcile instead: a push that stays blocked is
			// recoverable, destroyed content is not.
			if obj.Error.Code != http.StatusNotFound {
				return nil, fmt.Errorf(
					"lfs batch check inconclusive for %s: HTTP %d: %s (refusing to treat "+
						"as missing — retry once the LFS endpoint is healthy)",
					obj.OID, obj.Error.Code, obj.Error.Message)
			}
			missing[obj.OID] = true
		}
	}
	return missing, nil
}

// walkWorkingTreePointers returns every pointer file under the pointer-bearing
// trees: session artifacts and captured plans. Either can poison the shared push.
func walkWorkingTreePointers(ledgerPath string) ([]pointerEntry, error) {
	roots := []string{
		filepath.Join(ledgerPath, "sessions"),
		filepath.Join(ledgerPath, "data", "plans"),
	}

	var pointers []pointerEntry
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(root, func(absPath string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > maxPointerSize {
				return nil
			}
			if !IsPointerFile(absPath) {
				return nil
			}
			ref, parseErr := ReadPointerFile(absPath)
			if parseErr != nil {
				return nil
			}
			relPath, _ := filepath.Rel(ledgerPath, absPath)
			pointers = append(pointers, pointerEntry{relPath: relPath, ref: ref})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	return pointers, nil
}

func ensureReconcileGitSafe(ctx context.Context, ledgerPath string) error {
	if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
		return err
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		pathOut, err := gitPlumbing(ctx, ledgerPath, nil, "rev-parse", "--git-path", marker)
		if err != nil {
			return fmt.Errorf("resolve git operation marker %s: %w", marker, err)
		}
		markerPath := strings.TrimSpace(string(pathOut))
		if !filepath.IsAbs(markerPath) {
			markerPath = filepath.Join(ledgerPath, markerPath)
		}
		if _, err := os.Lstat(markerPath); err == nil {
			return fmt.Errorf("git operation in progress (%s exists)", marker)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect git operation marker %s: %w", marker, err)
		}
	}
	return nil
}

// scanPushRange collects the pointers GitLab's pre-receive hook would check for
// a push of HEAD: the pointer-sized blobs introduced by the commits in
// @{upstream}..HEAD, whatever path or commit holds them. Missing or divergent
// upstream state is an error: push-triggered repair must never fall back to a
// whole-tree deletion pass.
func scanPushRange(ctx context.Context, ledgerPath string, logger *slog.Logger) (*pointerSet, error) {
	upstreamOut, err := gitPlumbing(ctx, ledgerPath, nil, "rev-parse", "--verify", "--quiet", "@{upstream}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("no upstream tracking ref: %w", err)
	}
	upstream := strings.TrimSpace(string(upstreamOut))
	if _, err := gitPlumbing(ctx, ledgerPath, nil, "merge-base", "--is-ancestor", upstream, "HEAD"); err != nil {
		return nil, fmt.Errorf("upstream is not an ancestor of HEAD (branch diverged; pull first): %w", err)
	}

	// the same object walk pack-objects does for the push, so the candidate set
	// is exactly what the server receives (and checks)
	objectsOut, err := gitPlumbing(ctx, ledgerPath, nil, "rev-list", "--objects", upstream+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("list objects in push range: %w", err)
	}
	var oids []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(objectsOut), "\n") {
		oid, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if oid != "" && !seen[oid] {
			seen[oid] = true
			oids = append(oids, oid)
		}
	}
	set := &pointerSet{upstream: upstream, historyOnly: make(map[string]FileRef)}
	if len(oids) == 0 {
		return set, nil
	}

	pointerBlobs, err := pointerSizedBlobs(ctx, ledgerPath, oids)
	if err != nil {
		return nil, err
	}
	refsByGitOID, err := readPointerBlobs(ctx, ledgerPath, pointerBlobs)
	if err != nil {
		return nil, err
	}
	if len(refsByGitOID) == 0 {
		return set, nil
	}

	// where the tip holds one of those blobs: the paths the repair can act on
	diffOut, err := gitPlumbing(ctx, ledgerPath, nil, "diff-tree", "-r", "-z", "--no-renames", "--raw", upstream, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("diff push range: %w", err)
	}
	atTip := make(map[string]bool)
	tokens := strings.Split(string(diffOut), "\x00")
	for i := 0; i+1 < len(tokens); i += 2 {
		// ":<src mode> <dst mode> <src oid> <dst oid> <status>"
		fields := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		if len(fields) < 5 || strings.HasPrefix(fields[4], "D") {
			continue
		}
		ref, ok := refsByGitOID[fields[3]]
		if !ok {
			continue
		}
		path := tokens[i+1]
		entry := pointerEntry{relPath: filepath.FromSlash(path), ref: ref}
		if strings.HasPrefix(path, "sessions/") || strings.HasPrefix(path, "data/plans/") {
			set.repairable = append(set.repairable, entry)
		} else {
			set.unrepairable = append(set.unrepairable, entry)
		}
		atTip[ref.BareOID()] = true
	}
	for _, ref := range refsByGitOID {
		if !atTip[ref.BareOID()] {
			set.historyOnly[ref.BareOID()] = ref
		}
	}

	logger.Debug("lfs reconcile: scanned push range",
		"objects", len(oids), "pointer_blobs", len(refsByGitOID),
		"at_tip", len(set.repairable)+len(set.unrepairable), "history_only", len(set.historyOnly))
	return set, nil
}

// validateUnpushedTip performs the immutable-tree validation that the later
// squash commit would perform, but against HEAD before any upload, removal, or
// reset. This closes the old failure mode where a corrupt unpushed blob was
// discovered only after pointer replacement had already changed local state.
func validateUnpushedTip(ctx context.Context, ledgerPath, upstream string) error {
	raw, err := gitPlumbing(ctx, ledgerPath, nil, "diff-tree", "-r", "-z", "--no-renames", "--raw", upstream, "HEAD")
	if err != nil {
		return fmt.Errorf("diff unpushed tree: %w", err)
	}
	tokens := strings.Split(string(raw), "\x00")
	for i := 0; i+1 < len(tokens); i += 2 {
		fields := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		if len(fields) < 5 || strings.HasPrefix(fields[4], "D") {
			continue
		}
		path := tokens[i+1]
		if err := gitutil.ValidateLedgerEntryMode(path, fields[1]); err != nil {
			return err
		}
		blob, err := gitPlumbing(ctx, ledgerPath, nil, "cat-file", "blob", fields[3])
		if err != nil {
			return fmt.Errorf("inspect unpushed Ledger blob %s: %w", path, err)
		}
		if sessionContentStorageGit(ctx, ledgerPath, path) {
			if gitutil.HasConflictMarkersBytes(blob) {
				return fmt.Errorf("%s contains an unresolved conflict", path)
			}
			continue
		}
		if err := gitutil.ValidateLedgerBlob(path, blob); err != nil {
			return err
		}
	}
	return nil
}

func sessionContentStorageGit(ctx context.Context, ledgerPath, path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if len(parts) < 3 || parts[0] != "sessions" || !isLedgerContentFile(filepath.Base(path)) {
		return false
	}
	metaPath := strings.Join(parts[:2], "/") + "/meta.json"
	data, err := gitPlumbing(ctx, ledgerPath, nil, "show", "HEAD:"+metaPath)
	if err != nil {
		return false
	}
	var meta struct {
		Files map[string]FileRef `json:"files"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	return meta.Files[strings.Join(parts[2:], "/")].Storage == "git"
}

func isLedgerContentFile(name string) bool {
	for _, candidate := range pipeline.LedgerContentFiles {
		if name == candidate {
			return true
		}
	}
	return false
}

func preflightSacredDeletions(ctx context.Context, ledgerPath, upstream string, replaceable []pointerEntry) error {
	deletedOut, err := gitPlumbing(ctx, ledgerPath, nil, "diff-tree", "-r", "--name-only", "--diff-filter=D", upstream, "HEAD")
	if err != nil {
		return fmt.Errorf("preflight sacred deletions: %w", err)
	}
	paths := strings.Fields(string(deletedOut))
	for _, entry := range replaceable {
		paths = append(paths, filepath.ToSlash(entry.relPath))
	}
	deleted := sacred.Filter(paths)
	seen := make(map[string]bool, len(deleted))
	for _, path := range deleted {
		seen[path] = true
	}
	if len(seen) <= sacred.MassDeleteThreshold {
		return nil
	}
	return fmt.Errorf("refusing LFS reconcile: would delete %d files under sacred paths, exceeding threshold %d",
		len(seen), sacred.MassDeleteThreshold)
}

// pointerSizedBlobs filters oids to blobs small enough to be an LFS pointer, so
// content files and trees are never read.
func pointerSizedBlobs(ctx context.Context, ledgerPath string, oids []string) ([]string, error) {
	out, err := gitPlumbing(ctx, ledgerPath, []byte(strings.Join(oids, "\n")+"\n"),
		"cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return nil, fmt.Errorf("size push range objects: %w", err)
	}
	var blobs []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		if size, convErr := strconv.Atoi(fields[2]); convErr == nil && size <= maxPointerSize {
			blobs = append(blobs, fields[0])
		}
	}
	return blobs, nil
}

// readPointerBlobs reads each blob and keeps those that parse as an LFS pointer,
// keyed by git object id.
func readPointerBlobs(ctx context.Context, ledgerPath string, blobs []string) (map[string]FileRef, error) {
	refs := make(map[string]FileRef)
	if len(blobs) == 0 {
		return refs, nil
	}
	out, err := gitPlumbing(ctx, ledgerPath, []byte(strings.Join(blobs, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, fmt.Errorf("read push range blobs: %w", err)
	}
	// each record is "<oid> <type> <size>\n<size bytes>\n"
	r := bufio.NewReader(bytes.NewReader(out))
	for {
		header, readErr := r.ReadString('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read push range blob header: %w", readErr)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue // "<oid> missing"
		}
		size, convErr := strconv.Atoi(fields[2])
		if convErr != nil || size < 0 {
			return nil, fmt.Errorf("unexpected blob header %q", strings.TrimSpace(header))
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			return nil, fmt.Errorf("read push range blob %s: %w", fields[0], err)
		}
		_, _ = r.ReadByte() // the LF git writes after each object
		if oid, pointerSize, parseErr := ParsePointer(string(content)); parseErr == nil {
			refs[fields[0]] = FileRef{Storage: StorageLFS, OID: oid, Size: pointerSize}
		}
	}
	return refs, nil
}

// gitPlumbing runs a read-only git plumbing command and returns its stdout.
// Unlike gitutil.RunGit it can feed stdin and returns the output unsanitized,
// because callers parse it. WaitDelay keeps a canceled context from hanging on
// a git child that outlives the killed process.
func gitPlumbing(ctx context.Context, repoPath string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// prepareMissingPointerMetadata removes only matching missing-object references,
// clearing trace metadata when either attachment is removed and preserving all
// unrelated fields (including fields from newer clients). Parse all
// manifests before touching pointers so corrupt or mismatched metadata fails safe.
func prepareMissingPointerMetadata(ledgerPath string, missing map[string]FileRef) (map[string][]byte, error) {
	manifests := make(map[string]map[string]json.RawMessage)
	filesByManifest := make(map[string]map[string]json.RawMessage)
	changed := make(map[string]bool)
	// sorted, so when more than one artifact is unusable the same one is
	// reported on every run instead of whichever the map yields first
	artifactPaths := make([]string, 0, len(missing))
	for artifactPath := range missing {
		artifactPaths = append(artifactPaths, artifactPath)
	}
	sort.Strings(artifactPaths)
	for _, artifactPath := range artifactPaths {
		ref := missing[artifactPath]
		parts := strings.Split(filepath.ToSlash(artifactPath), "/")
		depth := 2
		if parts[0] == "data" {
			depth = 3
		}
		if len(parts) <= depth {
			return nil, fmt.Errorf("invalid artifact path %s", artifactPath)
		}
		metaPath := filepath.Join(append(parts[:depth:depth], "meta.json")...)
		if _, seen := manifests[metaPath]; !seen {
			absPath := filepath.Join(ledgerPath, metaPath)
			info, err := os.Lstat(absPath)
			if os.IsNotExist(err) {
				manifests[metaPath] = nil
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("metadata %s is not a regular file", metaPath)
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil, err
			}
			var meta map[string]json.RawMessage
			if err := json.Unmarshal(data, &meta); err != nil || meta == nil {
				return nil, fmt.Errorf("metadata %s is not a valid JSON object", metaPath)
			}
			manifests[metaPath] = meta
			var files map[string]json.RawMessage
			if raw, ok := meta["files"]; ok {
				if err := json.Unmarshal(raw, &files); err != nil {
					return nil, fmt.Errorf("metadata files %s: %w", metaPath, err)
				}
			}
			filesByManifest[metaPath] = files
		}
		files := filesByManifest[metaPath]
		name := strings.Join(parts[depth:], "/")
		if raw, exists := files[name]; exists {
			var registered FileRef
			if err := json.Unmarshal(raw, &registered); err != nil {
				return nil, err
			}
			if registered.BareOID() != ref.BareOID() || !registered.IsLFS() {
				return nil, fmt.Errorf("metadata reference for %s disagrees with missing pointer", artifactPath)
			}
			delete(files, name)
			changed[metaPath] = true
		}
		if pipeline.IsTraceFile(name) {
			if _, exists := manifests[metaPath]["trace"]; exists {
				delete(manifests[metaPath], "trace")
				changed[metaPath] = true
			}
		}
	}
	result := make(map[string][]byte)
	for metaPath, files := range filesByManifest {
		if !changed[metaPath] {
			continue
		}
		encodedFiles, err := json.Marshal(files)
		if err != nil {
			return nil, err
		}
		meta := manifests[metaPath]
		if files != nil {
			meta["files"] = encodedFiles
		}
		content, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return nil, err
		}
		result[metaPath] = append(content, '\n')
	}
	return result, nil
}

// squashUnpushed collapses all unpushed local commits into a single commit.
func squashUnpushed(ctx context.Context, repoPath, commitMsg string) error {
	upCtx, upCancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := gitutil.RunGit(upCtx, repoPath, "rev-parse", "--verify", "@{upstream}")
	upCancel()
	if err != nil {
		return fmt.Errorf("no upstream tracking ref: %w", err)
	}
	upstream = strings.TrimSpace(upstream)

	// reset --soft to an upstream that HEAD does not contain would commit the
	// index back over the commits HEAD lacks, reverting a coworker's work. A
	// diverged branch needs a pull, never a squash.
	ancestorCtx, ancestorCancel := context.WithTimeout(ctx, 5*time.Second)
	_, ancestorErr := gitutil.RunGit(ancestorCtx, repoPath, "merge-base", "--is-ancestor", upstream, "HEAD")
	ancestorCancel()
	if ancestorErr != nil {
		return fmt.Errorf("upstream is not an ancestor of HEAD (branch diverged; pull first): %w", ancestorErr)
	}

	originalCtx, originalCancel := context.WithTimeout(ctx, 5*time.Second)
	original, originalErr := gitutil.RunGit(originalCtx, repoPath, "rev-parse", "--verify", "HEAD")
	originalCancel()
	if originalErr != nil {
		return fmt.Errorf("resolve original HEAD: %w", originalErr)
	}
	original = strings.TrimSpace(original)

	countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
	countOut, err := gitutil.RunGit(countCtx, repoPath, "rev-list", "--count", upstream+"..HEAD")
	countCancel()
	if err != nil {
		// not "nothing to squash": a caller that asked for a squash must learn
		// it did not happen, or the push stays wedged with no error
		return fmt.Errorf("count unpushed commits: %w", err)
	}
	if count := strings.TrimSpace(countOut); count == "0" || count == "1" {
		return nil
	}

	resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = gitutil.RunGit(resetCtx, repoPath, "reset", "--soft", upstream)
	resetCancel()
	if err != nil {
		return fmt.Errorf("reset --soft: %w", err)
	}

	squashCtx, squashCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = gitutil.CommitLedgerSnapshot(squashCtx, repoPath, commitMsg)
	squashCancel()
	if err != nil {
		return rollbackSoftReset(ctx, repoPath, original, fmt.Errorf("validate squash or commit: %w", err))
	}

	return nil
}

// rollbackSoftReset restores the pre-squash branch tip after a validation or
// commit failure. The soft reset's index already represents original's tree, so
// restoring only HEAD preserves both the replacement commit and working copy.
func rollbackSoftReset(ctx context.Context, repoPath, original string, cause error) error {
	rollbackCtx, rollbackCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rollbackCancel()
	if _, err := gitutil.RunGit(rollbackCtx, repoPath, "reset", "--soft", original); err != nil {
		return fmt.Errorf("%w; restore original HEAD: %w", cause, err)
	}
	return cause
}

// ValidateUnpushedTip reports whether HEAD's delta against upstream passes the
// validation the push path applies before it publishes anything (raw session
// content where a pointer belongs, conflict markers, invalid meta.json). The
// error names the first offending path. Exposed so `ox doctor` can prove a
// repair unblocks pushing with the very check push uses.
func ValidateUnpushedTip(ctx context.Context, ledgerPath, upstream string) error {
	return validateUnpushedTip(ctx, ledgerPath, upstream)
}
