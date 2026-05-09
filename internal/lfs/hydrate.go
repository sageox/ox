package lfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HydrateResult describes the outcome of a HydrateBubble run.
//
// Hydrated lists the relative paths (slash-separated, repo-root-relative)
// of files that were successfully turned from pointers into real content.
// Failed lists per-file errors so callers can surface them without aborting
// the whole walk. Skipped is informational — files that look pointer-shaped
// but aren't valid (forward-compat or corrupted), or directories the walker
// could not enter.
type HydrateResult struct {
	Hydrated []string         // repo-root-relative paths (forward-slash)
	Failed   []HydrateFailure // per-file failures
	Skipped  []HydrateFailure // diagnostic: pointers we skipped (e.g., parse errors)
	Scanned  int              // total candidate pointer files inspected
}

// HydrateFailure captures a single per-file failure for surface-level reporting.
type HydrateFailure struct {
	Path string // repo-root-relative (forward-slash)
	Err  error
}

// HydrationFilter, if non-nil, is consulted for every regular file the
// hydration walker encounters. Returning false skips the file entirely
// (no pointer detection, no batch lookup). Path is repo-root-relative
// using forward slashes.
type HydrationFilter func(path string) bool

// HydrateBubble walks dir for LFS pointer files and replaces them with the
// real content fetched via the LFS Batch API. Hydration is idempotent:
// non-pointer files (already hydrated, or regular content) are skipped and
// not counted as failures.
//
// The replacement is atomic per file (`<file>.lfs-tmp.<unix-nanos>` then
// rename) so a crash mid-write leaves either the old pointer or the new
// content — never a half-written file.
//
// `relPath`, if non-empty, restricts hydration to that single file
// (relative to dir). Empty means "the whole tree".
//
// Per-file errors (network, missing OID on server, write failure) are
// captured in HydrateResult.Failed and never abort the run, so partial
// progress survives a flaky server. The function only returns an error
// for setup-level problems: bad arguments, walker failures, or batch-API
// transport errors that prevent any work from being attempted.
//
// SAFETY: this function never writes `.gitattributes` and never invokes
// the git-lfs binary. See .claude/rules/lfs-no-git-lfs-binary.md.
func HydrateBubble(ctx context.Context, dir, relPath string, client *Client, filter HydrationFilter) (*HydrateResult, error) {
	if dir == "" {
		return nil, fmt.Errorf("hydrate: empty bubble directory")
	}
	if client == nil {
		return nil, fmt.Errorf("hydrate: nil LFS client")
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("hydrate: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("hydrate: %s is not a directory", dir)
	}

	result := &HydrateResult{}

	// Collect pointer entries first so we can batch the LFS API call.
	type pointerEntry struct {
		absPath string
		relPath string // forward-slash, relative to dir
		ref     FileRef
	}
	var pointers []pointerEntry

	if relPath != "" {
		// single-file mode — clean the input, reject absolute paths, and
		// confirm the resolved target stays inside `dir` before we touch
		// the filesystem. Lstat the path so a missing file surfaces as a
		// HydrateFailure rather than getting silently swallowed by
		// inspectPointer's open path.
		cleaned := filepath.Clean(filepath.FromSlash(relPath))
		if filepath.IsAbs(cleaned) {
			return result, fmt.Errorf("path %q must be repo-relative, not absolute", relPath)
		}
		abs := filepath.Join(dir, cleaned)
		absClean, absErr := filepath.Abs(abs)
		dirClean, dirErr := filepath.Abs(dir)
		if absErr != nil || dirErr != nil {
			return result, fmt.Errorf("resolve absolute path: %w", errors.Join(absErr, dirErr))
		}
		rel, relErr := filepath.Rel(dirClean, absClean)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return result, fmt.Errorf("path %q escapes bubble dir", relPath)
		}
		if _, lstatErr := os.Lstat(abs); lstatErr != nil {
			result.Skipped = append(result.Skipped, HydrateFailure{Path: relPath, Err: lstatErr})
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry, parseErr, scanned := inspectPointer(dir, abs)
		if scanned {
			result.Scanned++
		}
		if parseErr != nil {
			result.Skipped = append(result.Skipped, HydrateFailure{Path: relPath, Err: parseErr})
			return result, nil
		}
		if entry != nil {
			if filter == nil || filter(entry.relPath) {
				pointers = append(pointers, *entry)
			}
		}
	} else {
		walkErr := filepath.WalkDir(dir, func(absPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// skip the entry but record so callers can see it
				rel, _ := filepath.Rel(dir, absPath)
				result.Skipped = append(result.Skipped, HydrateFailure{
					Path: filepath.ToSlash(rel),
					Err:  walkErr,
				})
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				// skip the .git directory — nothing in there is ever a user-facing
				// LFS pointer and traversing it adds latency on large repos.
				if d.Name() == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			entry, parseErr, scanned := inspectPointer(dir, absPath)
			if scanned {
				result.Scanned++
			}
			if parseErr != nil {
				rel, _ := filepath.Rel(dir, absPath)
				result.Skipped = append(result.Skipped, HydrateFailure{
					Path: filepath.ToSlash(rel),
					Err:  parseErr,
				})
				return nil
			}
			if entry == nil {
				return nil
			}
			if filter != nil && !filter(entry.relPath) {
				return nil
			}
			pointers = append(pointers, *entry)
			return nil
		})
		if walkErr != nil {
			return result, fmt.Errorf("walk %s: %w", dir, walkErr)
		}
	}

	if len(pointers) == 0 {
		return result, nil
	}

	// Batch downloads in chunks to stay under WAF limits (mirrors
	// reconcile.go's batchChunkSize=50). Each pointer is ~100B of JSON,
	// so 50 per chunk stays well under typical 8KB body caps.
	const batchChunkSize = 50

	// Dedup OIDs — multiple pointer files can reference the same blob.
	oidToIndices := make(map[string][]int, len(pointers))
	var unique []BatchObject
	for i, p := range pointers {
		bareOID := p.ref.BareOID()
		if _, seen := oidToIndices[bareOID]; !seen {
			unique = append(unique, BatchObject{OID: bareOID, Size: p.ref.Size})
		}
		oidToIndices[bareOID] = append(oidToIndices[bareOID], i)
	}

	// Map OID -> Action so we can hand to the per-pointer download path.
	oidToAction := make(map[string]*Action, len(unique))
	oidToErr := make(map[string]error, len(unique))

	for start := 0; start < len(unique); start += batchChunkSize {
		end := start + batchChunkSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		if err := ctx.Err(); err != nil {
			return result, err
		}
		resp, err := client.BatchDownload(chunk)
		if err != nil {
			// Transport-level failure for this chunk: mark every OID in the
			// chunk as failed so the caller's per-file error report tells
			// the truth, and continue to the next chunk.
			for _, obj := range chunk {
				oidToErr[obj.OID] = fmt.Errorf("batch download: %w", err)
			}
			continue
		}
		for i := range resp.Objects {
			obj := resp.Objects[i]
			if obj.Error != nil {
				oidToErr[obj.OID] = fmt.Errorf("server error %d: %s", obj.Error.Code, obj.Error.Message)
				continue
			}
			if obj.Actions == nil || obj.Actions.Download == nil {
				oidToErr[obj.OID] = fmt.Errorf("no download action for OID %s", obj.OID)
				continue
			}
			oidToAction[obj.OID] = obj.Actions.Download
		}
	}

	// Per-file hydration. Each replacement is atomic (tmp + rename).
	for _, p := range pointers {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		bareOID := p.ref.BareOID()
		if action, ok := oidToAction[bareOID]; ok {
			start := time.Now()
			if err := hydratePointerInPlace(p.absPath, action, bareOID); err != nil {
				result.Failed = append(result.Failed, HydrateFailure{Path: p.relPath, Err: err})
				continue
			}
			result.Hydrated = append(result.Hydrated, p.relPath)
			slog.Info("kb_lfs_hydrate", "path", p.relPath, "size", p.ref.Size, "duration_ms", time.Since(start).Milliseconds())
			continue
		}
		// no action and no recorded error: assert one of them so we never silently drop a file
		if e, ok := oidToErr[bareOID]; ok {
			result.Failed = append(result.Failed, HydrateFailure{Path: p.relPath, Err: e})
		} else {
			result.Failed = append(result.Failed, HydrateFailure{Path: p.relPath, Err: fmt.Errorf("no batch response for OID %s", bareOID)})
		}
	}

	return result, nil
}

// inspectPointer reads a regular file and decides whether it is an LFS
// pointer worth hydrating. The three return values are:
//
//   - non-nil entry, nil err, true: caller should enqueue for hydration.
//   - nil entry, nil err, true: regular content, already hydrated, or a non-pointer
//     file we skipped after inspecting its size (counted toward Scanned only when scanned==true).
//   - nil entry, non-nil err, true: file looked pointer-shaped (size <= maxPointerSize)
//     but parsed invalid — caller should add to Skipped so the user sees it.
//   - nil entry, nil err, false: not even a pointer-shape candidate (too large), skip silently.
//
// Pre-pointer-detection size gate matches IsPointerFile so we never read
// the body of a multi-MB file just to discover it's not a pointer.
func inspectPointer(root, absPath string) (*struct {
	absPath string
	relPath string
	ref     FileRef
}, error, bool) {
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, nil, false
	}
	if !info.Mode().IsRegular() {
		return nil, nil, false
	}
	if info.Size() == 0 || info.Size() > maxPointerSize {
		return nil, nil, false
	}
	rel, _ := filepath.Rel(root, absPath)
	relSlash := filepath.ToSlash(rel)
	if !IsPointerFile(absPath) {
		// Pointer-shape candidate that didn't parse cleanly. Distinguish
		// "real small content" (no error reported) from "looks LFS but
		// invalid" (error reported) by trying ReadPointerFile.
		ref, parseErr := ReadPointerFile(absPath)
		if parseErr == nil {
			// shouldn't happen — IsPointerFile returned false, but we parsed.
			// Treat as hydrate candidate to be safe.
			entry := &struct {
				absPath string
				relPath string
				ref     FileRef
			}{absPath: absPath, relPath: relSlash, ref: ref}
			return entry, nil, true
		}
		// Not a valid pointer. We only flag it as "looks pointer-shaped
		// but invalid" if it starts with a version line — otherwise it's
		// just a normal small file and we should keep walking quietly.
		if looksPointerShaped(absPath) {
			return nil, parseErr, true
		}
		return nil, nil, false
	}
	ref, parseErr := ReadPointerFile(absPath)
	if parseErr != nil {
		return nil, parseErr, true
	}
	entry := &struct {
		absPath string
		relPath string
		ref     FileRef
	}{absPath: absPath, relPath: relSlash, ref: ref}
	return entry, nil, true
}

// looksPointerShaped reports whether the file appears to be an LFS pointer
// based on its first line, even if the full content fails to parse. Used so
// genuine small text files (a 50-byte README scrap) aren't reported in
// Skipped, but a corrupted pointer (`version https://git-lfs...` then
// garbage) is.
func looksPointerShaped(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(data)
	return strings.HasPrefix(s, "version ") && strings.Contains(s, "git-lfs")
}

// hydratePointerInPlace downloads the LFS object referenced by oid and
// atomically replaces the pointer file at absPath with the real content.
// The temp file lives next to the target so rename is a metadata-only
// operation (same device). Original file permissions are preserved
// across the swap so executable scripts stored as LFS pointers stay
// executable after hydration; if stat fails (rare) we fall back to
// 0644 so the file is at least readable.
func hydratePointerInPlace(absPath string, action *Action, bareOID string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(absPath); err == nil {
		mode = info.Mode().Perm()
	}
	tmpPath := fmt.Sprintf("%s.lfs-tmp.%d", absPath, time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if err := DownloadToFile(action, f, true, bareOID); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("download: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	// re-chmod after Close on platforms where umask narrowed the mode.
	_ = os.Chmod(tmpPath, mode)
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
