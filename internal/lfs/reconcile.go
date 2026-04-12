package lfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// ReconcileResult describes what ReconcileUnpushedPointers found and fixed.
type ReconcileResult struct {
	ScannedPointers int      // total pointer files found in sessions/
	MissingOnRemote int      // pointers whose LFS OIDs are not in the remote store
	Replaced        int      // pointers replaced with empty stubs
	Squashed        bool     // whether unpushed history was squashed
	ReplacedFiles   []string // relative paths of replaced files
}

// ReconcileUnpushedPointers scans the working tree under sessions/ for LFS pointer
// files whose backing blobs are missing from the remote LFS store, replaces them
// with empty stubs, and squashes all unpushed commits into one so the poisoned
// pointer blobs no longer appear in the push pack.
//
// This is the repair mechanism for ledgers whose push is blocked by
// "GitLab: LFS objects are missing" — regardless of HOW the bad pointer got
// committed (daemon murmur, user manual commit, unscoped git add, etc.).
//
// Safe to call on clean repos — returns immediately if no pointer files exist
// or all pointers have valid backing objects.
//
// The squash is necessary because GitLab's pre-receive hook scans ALL commits
// in the push pack, not just HEAD. Replacing pointers in the working tree and
// committing on top is not sufficient — the old commits still reference the
// missing OIDs.
func ReconcileUnpushedPointers(ctx context.Context, ledgerPath, endpointURL string, logger *slog.Logger) (*ReconcileResult, error) {
	if logger == nil {
		logger = slog.Default()
	}

	result := &ReconcileResult{}

	// find all pointer files under sessions/
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return result, nil
	}

	type pointerEntry struct {
		relPath string // relative to ledgerPath
		ref     FileRef
	}
	var pointers []pointerEntry

	err := filepath.Walk(sessionsDir, func(absPath string, info os.FileInfo, err error) error {
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
		return result, fmt.Errorf("walk sessions/: %w", err)
	}

	result.ScannedPointers = len(pointers)
	if len(pointers) == 0 {
		return result, nil
	}

	// batch-check which OIDs exist in the remote LFS store
	client, err := NewClientFromLedger(ledgerPath, endpointURL)
	if err != nil {
		return result, fmt.Errorf("lfs client: %w", err)
	}

	// deduplicate OIDs — multiple pointer files can reference the same blob
	oidToIndices := make(map[string][]int, len(pointers))
	var uniqueObjects []BatchObject
	for i, p := range pointers {
		bareOID := p.ref.BareOID()
		if _, seen := oidToIndices[bareOID]; !seen {
			uniqueObjects = append(uniqueObjects, BatchObject{OID: bareOID, Size: p.ref.Size})
		}
		oidToIndices[bareOID] = append(oidToIndices[bareOID], i)
	}

	// batch-check in chunks — the LFS Batch API request body can exceed WAF
	// limits (8KB) when there are many pointers. Each pointer is ~100 bytes
	// of JSON, so 50 per chunk stays well under the limit.
	const batchChunkSize = 50
	missing := make(map[int]bool)
	for start := 0; start < len(uniqueObjects); start += batchChunkSize {
		end := start + batchChunkSize
		if end > len(uniqueObjects) {
			end = len(uniqueObjects)
		}
		chunk := uniqueObjects[start:end]

		resp, err := client.BatchDownload(chunk)
		if err != nil {
			return result, fmt.Errorf("lfs batch check: %w", err)
		}

		for _, obj := range resp.Objects {
			if obj.Error != nil {
				// mark ALL files that reference this OID, not just one
				for _, idx := range oidToIndices[obj.OID] {
					missing[idx] = true
				}
			}
		}
	}

	result.MissingOnRemote = len(missing)
	if len(missing) == 0 {
		logger.Debug("lfs reconcile: all pointer OIDs present in remote", "count", len(pointers))
		return result, nil
	}

	logger.Info("lfs reconcile: replacing orphaned pointers with empty stubs",
		"missing", len(missing), "total_pointers", len(pointers))

	// replace missing pointers with empty content and stage
	for idx := range missing {
		p := pointers[idx]
		absPath := filepath.Join(ledgerPath, p.relPath)
		if err := os.WriteFile(absPath, []byte{}, 0o644); err != nil {
			logger.Warn("lfs reconcile: write failed", "path", p.relPath, "error", err)
			continue
		}
		addCtx, addCancel := context.WithTimeout(ctx, 5*time.Second)
		_, addErr := gitutil.RunGit(addCtx, ledgerPath, "add", "--sparse", p.relPath)
		addCancel()
		if addErr != nil {
			logger.Warn("lfs reconcile: stage failed", "path", p.relPath, "error", addErr)
			continue
		}
		result.Replaced++
		result.ReplacedFiles = append(result.ReplacedFiles, p.relPath)
	}

	if result.Replaced == 0 {
		return result, nil
	}

	// commit the replacements
	msg := fmt.Sprintf("fix: replace %d orphaned LFS pointers with empty stubs", result.Replaced)
	commitCtx, commitCancel := context.WithTimeout(ctx, 10*time.Second)
	_, commitErr := gitutil.RunGit(commitCtx, ledgerPath, "commit", "-m", msg, "--no-verify")
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

	logger.Info("lfs reconcile complete", "replaced", result.Replaced)
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

	countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
	countOut, err := gitutil.RunGit(countCtx, repoPath, "rev-list", "--count", upstream+"..HEAD")
	countCancel()
	count := strings.TrimSpace(countOut)
	if err != nil || count == "0" || count == "1" {
		return nil
	}

	resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = gitutil.RunGit(resetCtx, repoPath, "reset", "--soft", upstream)
	resetCancel()
	if err != nil {
		return fmt.Errorf("reset --soft: %w", err)
	}

	squashCtx, squashCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = gitutil.RunGit(squashCtx, repoPath, "commit", "-m", commitMsg, "--no-verify")
	squashCancel()
	if err != nil {
		return fmt.Errorf("squash commit: %w", err)
	}

	return nil
}
