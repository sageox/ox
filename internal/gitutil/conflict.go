package gitutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
)

// ConflictMarkerStart is git's textual conflict marker. We look for it as a
// line prefix (not merely a substring) so legitimate content that happens to
// contain the literal characters mid-line — a quoted diff in a session
// transcript, for instance — doesn't false-positive.
const ConflictMarkerStart = "<<<<<<<"

// HasConflictMarkers reports whether the file at path contains an unresolved
// git conflict marker.
//
// This exists because `git add` does not refuse a conflicted path — it takes
// the file's current working-tree content, markers included, and stages it
// as "resolved." A caller that adds a conflicted path alongside genuinely
// clean changes and then commits bakes the markers permanently into history.
// Callers that stage files for an unattended/automatic commit MUST check
// this first and exclude (or refuse on) any match; git will not do it for
// them.
func HasConflictMarkers(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return HasConflictMarkersBytes(data), nil
}

// HasConflictMarkersBytes is the scan behind HasConflictMarkers, taking raw
// content directly. Use this when the content under inspection isn't (or
// might not be) the working-tree file — e.g. a staged git blob read via
// `git show :<path>`, which can differ from what's currently on disk.
func HasConflictMarkersBytes(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, ConflictMarkerStart) {
			return true
		}
	}
	return false
}

// ResolveAutostashConflicts clears formatting-only session metadata conflicts
// left by pull --autostash, which can exit successfully with an unmerged index.
// It preserves every field from both sides and refuses differing values,
// deletions, other paths, active git operations, or edits made after the conflict.
// The caller must hold WithRepoLock. No commits are made or stashes removed.
func ResolveAutostashConflicts(ctx context.Context, repoPath string, safePrefixes, denyPrefixes []string) (bool, error) {
	entries, err := listUnmergedEntries(ctx, repoPath)
	if err != nil {
		return false, fmt.Errorf("inspect unmerged index: %w", err)
	}
	if len(entries) == 0 {
		return false, nil
	}
	for _, marker := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Stat(filepath.Join(repoPath, ".git", marker)); !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("cannot recover autostash while %s is present or unreadable", marker)
		}
	}
	tmp, err := os.MkdirTemp("", "ox-autostash-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	type repair struct {
		path     string
		original []byte
		merged   []byte
		mode     os.FileMode
	}
	var repairs []repair
	for path, stages := range groupByPath(entries) {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "sessions" || parts[2] != "meta.json" ||
			!matchesSafePrefix(path, safePrefixes, denyPrefixes) || !stages[1] || !stages[2] || !stages[3] {
			return false, fmt.Errorf("unresolved conflict in %s requires manual resolution", path)
		}
		var fields [4]map[string]any
		var files [4]string
		for stage := 1; stage <= 3; stage++ {
			// Read raw blobs: RunGit sanitizes output and must not rewrite metadata.
			data, err := exec.CommandContext(ctx, "git", "-C", repoPath, "show", fmt.Sprintf(":%d:%s", stage, path)).Output()
			if err != nil {
				return false, fmt.Errorf("read conflict stage for %s: %w", path, err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber() // preserve large IDs and sizes exactly
			if !json.Valid(data) || decoder.Decode(&fields[stage]) != nil || fields[stage] == nil {
				return false, fmt.Errorf("invalid JSON in conflict for %s", path)
			}
			files[stage] = filepath.Join(tmp, fmt.Sprint(stage))
			if err := os.WriteFile(files[stage], data, 0o600); err != nil {
				return false, err
			}
		}
		for key := range fields[1] {
			_, ours := fields[2][key]
			_, theirs := fields[3][key]
			if !ours || !theirs {
				return false, fmt.Errorf("field %s was deleted in %s; manual resolution required", key, path)
			}
		}
		for key, value := range fields[3] {
			if ours, exists := fields[2][key]; exists && !reflect.DeepEqual(ours, value) {
				return false, fmt.Errorf("field %s differs in %s; manual resolution required", key, path)
			}
			fields[2][key] = value
		}
		fullPath := filepath.Join(repoPath, path)
		info, err := os.Lstat(fullPath)
		if err != nil || !info.Mode().IsRegular() {
			return false, fmt.Errorf("conflicted metadata is missing or not a regular file: %s", path)
		}
		original, err := os.ReadFile(fullPath)
		if err != nil {
			return false, err
		}
		merged, err := json.MarshalIndent(fields[2], "", "  ")
		if err != nil {
			return false, err
		}
		merged = append(merged, '\n')
		// Reproduce git's stash conflict before replacing it. Otherwise a human's
		// edits outside the markers could be lost by rebuilding from index stages.
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-file", "-p",
			"-L", "Updated upstream", "-L", "Stash base", "-L", "Stashed changes", files[2], files[1], files[3])
		expected, mergeErr := cmd.Output()
		var exitErr *exec.ExitError
		// An earlier pass may have written the repair but failed to stage it.
		if !bytes.Equal(original, merged) && (!errors.As(mergeErr, &exitErr) || exitErr.ExitCode() < 1 || exitErr.ExitCode() > 127 || !bytes.Equal(original, expected)) {
			return false, fmt.Errorf("conflict in %s has additional edits or is not an autostash conflict; manual resolution required", path)
		}
		repairs = append(repairs, repair{path, original, merged, info.Mode().Perm()})
	}
	// Validate the whole conflict set before touching the index or worktree.
	for _, r := range repairs {
		fullPath := filepath.Join(repoPath, r.path)
		if err := fileutil.WithFileLock(ctx, fullPath, func() error {
			current, err := os.ReadFile(fullPath)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, r.original) {
				return fmt.Errorf("conflicted metadata changed during recovery: %s", r.path)
			}
			if err := fileutil.AtomicWriteBytes(fullPath, r.merged, r.mode); err != nil {
				return err
			}
			_, err = RunGit(ctx, repoPath, "add", "--sparse", "--", r.path)
			return err
		}); err != nil {
			return false, fmt.Errorf("resolve %s: %w", r.path, err)
		}
	}
	return true, nil
}
