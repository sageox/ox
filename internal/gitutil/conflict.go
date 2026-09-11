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

// ValidateLedgerBlob rejects bytes that an automatic Ledger writer must never
// publish. Conflict markers are invalid in every Ledger artifact; session
// metadata additionally has a structural JSON contract that Git cannot enforce.
//
// Keep this check content-only so both staged-index validators and immutable
// tree commits can enforce the same invariant without re-reading the worktree.
func ValidateLedgerBlob(path string, data []byte) error {
	if HasConflictMarkersBytes(data) {
		return fmt.Errorf("%s contains an unresolved conflict", path)
	}
	if isSessionMetaPath(path) {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return fmt.Errorf("%s contains invalid JSON object", path)
		}
	}
	return nil
}

// ValidateStagedLedgerCommit validates the exact blobs currently staged for an
// automatic Ledger commit. The caller MUST hold WithRepoLock from before its
// first git add through the subsequent git commit; otherwise another ox process
// could replace an already-validated index entry before commit.
//
// A live unmerged index fails at write-tree before git add can accidentally mark
// a conflicted path resolved. When pathspecs are supplied, only blobs that the
// path-scoped commit can publish are scanned; the unmerged-index check remains
// global because git refuses every commit while any index stage is unresolved.
func ValidateStagedLedgerCommit(ctx context.Context, repoPath string, pathspecs ...string) error {
	if err := IsSafeForGitOps(repoPath); err != nil {
		return fmt.Errorf("unsafe Ledger commit: %w", err)
	}

	if _, err := cleanGitOutput(ctx, repoPath, "write-tree"); err != nil {
		return fmt.Errorf("snapshot Ledger index (unresolved conflict in index?): %w", err)
	}

	// Exclude only deletions: every other status can introduce a blob. In
	// particular, a symlink-to-file type change is T rather than A/M and must not
	// bypass validation merely because the path already existed.
	args := []string{"diff", "--cached", "--name-only", "--diff-filter=d", "--no-renames", "-z", "HEAD", "--"}
	args = append(args, pathspecs...)
	paths, err := cleanGitOutput(ctx, repoPath, args...)
	if err != nil {
		// Managed Ledgers normally have a HEAD. Supporting an unborn clone keeps
		// the guard fail-closed without making initial bootstrap a special case.
		if _, headErr := cleanGitOutput(ctx, repoPath, "rev-parse", "--verify", "HEAD"); headErr == nil {
			return fmt.Errorf("list staged Ledger blobs: %w", err)
		}
		args = []string{"ls-files", "--cached", "-z", "--"}
		args = append(args, pathspecs...)
		paths, err = cleanGitOutput(ctx, repoPath, args...)
		if err != nil {
			return fmt.Errorf("list staged Ledger blobs on unborn branch: %w", err)
		}
	}

	for _, path := range splitNUL(paths) {
		// :./ disambiguates a pathname such as "1:file" from Git's stage
		// lookup syntax (:1:file). Read the index blob, never worktree bytes.
		blob, err := cleanGitOutput(ctx, repoPath, "show", ":./"+path)
		if err != nil {
			return fmt.Errorf("inspect staged Ledger blob %s: %w", path, err)
		}
		if err := ValidateLedgerBlob(path, blob); err != nil {
			return fmt.Errorf("refusing automatic Ledger commit: %w", err)
		}
	}
	return nil
}

func isSessionMetaPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	return len(parts) == 3 && parts[0] == "sessions" && parts[1] != "" && parts[2] == "meta.json"
}

func splitNUL(data []byte) []string {
	trimmed := bytes.TrimRight(data, "\x00")
	if len(trimmed) == 0 {
		return nil
	}
	parts := bytes.Split(trimmed, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		paths = append(paths, string(part))
	}
	return paths
}

// cleanGitOutput runs local Git plumbing without RunGit's output sanitization;
// staged JSON bytes must round-trip byte-for-byte for structural validation.
func cleanGitOutput(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	full := []string{"-C", repoPath, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
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
