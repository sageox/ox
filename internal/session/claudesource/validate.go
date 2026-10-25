// Package claudesource verifies that Claude's native JSONL belongs to one repo.
package claudesource

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// ErrUntrustedSource means a complete record proves the source cannot belong
// to this recording. It should be preserved locally, not retried or uploaded.
var ErrUntrustedSource = errors.New("claude source has untrusted ownership")

// ErrUncheckable means ownership cannot be decided right now: a directory the
// session visited cannot be resolved, or the source changed while it was read.
// It proves nothing about the recording, so callers retry or, for a capture
// already validated batch by batch, finalize it anyway.
var ErrUncheckable = errors.New("claude source ownership cannot be checked")

// ErrSourceGone means the native session file itself no longer exists. It is
// distinct from a vanished directory the session once visited, which is judged
// by where it stood.
var ErrSourceGone = errors.New("claude source file is gone")

// errNonCanonicalCwd marks a vanished cwd whose text cannot be trusted to name a
// place: Claude records canonical paths, so one with "..", "." or doubled
// separators was not written by it.
var errNonCanonicalCwd = errors.New("cwd is not in canonical form")

// Validate checks every complete native record before discovery or final upload.
// A partial trailing record is left for the next read, like the native reader.
func Validate(path, repoRoot, sessionID string) error {
	return validate(path, repoRoot, sessionID, 0, true)
}

// Snapshot records the native file identity before a separate adapter process
// reads it. Comparing after validation prevents validating a replacement file
// while importing entries read from the previous one.
func Snapshot(path string) (os.FileInfo, error) { return os.Stat(path) }

// ValidateRead checks ownership and that the path still names the read source.
// Growth on the same inode is normal while Claude appends; a rewrite or
// replacement is not, so callers retry rather than attribute unchecked bytes.
func ValidateRead(path, repoRoot, sessionID string, offset int64, full bool, before os.FileInfo) error {
	var err error
	if full {
		err = Validate(path, repoRoot, sessionID)
	} else {
		err = ValidateFrom(path, repoRoot, sessionID, offset)
	}
	if err != nil {
		return err
	}
	after, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	if err != nil {
		return err
	}
	if before == nil || !os.SameFile(before, after) || after.Size() < before.Size() ||
		(after.Size() == before.Size() && !after.ModTime().Equal(before.ModTime())) {
		return fmt.Errorf("%w: claude source changed while reading; retry without advancing cursor", ErrUncheckable)
	}
	return nil
}

// ValidateRecorded checks the part of a native file a recording can have
// captured: everything from the offset where the recording began. Turns before
// it were never imported, so a directory visited then says nothing about this
// recording. A recording that began at the top of the file also requires the
// file to carry repository metadata at all.
func ValidateRecorded(path, repoRoot, sessionID string, startOffset int64, before os.FileInfo) error {
	return ValidateRead(path, repoRoot, sessionID, startOffset, startOffset <= 0, before)
}

// ValidateFrom checks only records read since offset before they are written to
// raw.jsonl. Discovery verifies the prefix, and finalization rechecks the whole
// file; a cursor alone cannot prove that the native file was not rewritten.
func ValidateFrom(path, repoRoot, sessionID string, offset int64) error {
	return validate(path, repoRoot, sessionID, offset, false)
}

func validate(path, repoRoot, sessionID string, offset int64, requireInitialIdentity bool) error {
	if !filepath.IsAbs(repoRoot) {
		return fmt.Errorf("claude source repository root must be absolute")
	}
	// A workspace that has since been archived or deleted is placed where it
	// stood, like a deleted cwd: its absence is not a reason to stop checking.
	repoRoot, err := resolveVanished(filepath.Clean(repoRoot), false)
	if err != nil {
		// the cause is quoted as text on purpose: wrapping it would carry
		// fs.ErrNotExist (or a permission error) up as if the native file were gone
		return fmt.Errorf("%w: resolve Claude source repository root: %s", ErrUncheckable, err.Error())
	}
	fileID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if sessionID != "" && fileID != sessionID {
		return fmt.Errorf("%w: session ID does not match filename", ErrUntrustedSource)
	}

	seenCwd, seenID := false, false
	checkedCwd := make(map[string]cwdVerdict)
	// ownershipErr is proof the source is foreign and ends the scan. A cwd that
	// cannot be checked only defers: the scan goes on, because a later turn may
	// still prove the session crossed repositories.
	var ownershipErr, deferredErr error
	_, _, _, err = adapterruntime.TailJSONLWithStats(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		if ownershipErr != nil {
			return nil, nil
		}
		var meta struct {
			Type      string          `json:"type"`
			Cwd       json.RawMessage `json:"cwd"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if err := json.Unmarshal(line, &meta); err != nil {
			// The adapter skips malformed JSON too. Rejecting the entire file
			// would strand all later, valid turns after a single torn line.
			return nil, nil
		}
		var cwd, id string
		if len(meta.Cwd) > 0 && string(meta.Cwd) != "null" {
			if err := json.Unmarshal(meta.Cwd, &cwd); err != nil || cwd == "" {
				ownershipErr = fmt.Errorf("%w: invalid cwd metadata", ErrUntrustedSource)
				return nil, nil
			}
		}
		if len(meta.SessionID) > 0 && string(meta.SessionID) != "null" {
			if err := json.Unmarshal(meta.SessionID, &id); err != nil || id == "" {
				ownershipErr = fmt.Errorf("%w: invalid session ID metadata", ErrUntrustedSource)
				return nil, nil
			}
		}
		if id != "" {
			if id != fileID {
				ownershipErr = fmt.Errorf("%w: session ID changed", ErrUntrustedSource)
				return nil, nil
			}
			seenID = true
		}
		if cwd != "" {
			verdict, checked := checkedCwd[cwd]
			if !checked {
				var judgeErr error
				verdict, judgeErr = judgeCwd(repoRoot, cwd)
				checkedCwd[cwd] = verdict
				if verdict == cwdUnknown && deferredErr == nil {
					deferredErr = judgeErr
				}
			}
			switch verdict {
			case cwdForeign:
				ownershipErr = fmt.Errorf("%w: cwd outside repo", ErrUntrustedSource)
				return nil, nil
			case cwdOwned:
				seenCwd = true
			}
		}
		if (meta.Type == "user" || meta.Type == "assistant") && (cwd == "" || id == "") {
			ownershipErr = fmt.Errorf("%w: turn has missing ownership metadata", ErrUntrustedSource)
		}
		return nil, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	if err != nil {
		return err
	}
	if ownershipErr != nil {
		return ownershipErr
	}
	if deferredErr != nil {
		return deferredErr
	}
	if requireInitialIdentity && (!seenCwd || (sessionID != "" && !seenID)) {
		return fmt.Errorf("claude source has invalid or missing repository metadata")
	}
	return nil
}

type cwdVerdict int

const (
	cwdUnknown cwdVerdict = iota
	cwdOwned
	cwdForeign
)

// judgeCwd decides whether a directory a session visited lies inside the repo.
// The error is set only for cwdUnknown.
func judgeCwd(repoRoot, cwd string) (cwdVerdict, error) {
	// A directory the session visited may be gone by now (a build directory, an
	// archived workspace). Where it stood is still decidable, so it is judged by
	// its resolved location; only a path that cannot be resolved at all defers,
	// rather than permanently quarantining an uncheckable filesystem state.
	resolved, err := resolveVanished(cwd, true)
	if errors.Is(err, errNonCanonicalCwd) {
		return cwdForeign, nil
	}
	if err != nil {
		return cwdUnknown, fmt.Errorf("%w: cannot verify Claude source cwd: %s", ErrUncheckable, err.Error())
	}
	inside, err := withinRepo(repoRoot, resolved)
	if err != nil {
		// a marker that cannot be looked at (a directory that is readable but not
		// searchable, an I/O error) proves nothing about ownership: defer
		return cwdUnknown, fmt.Errorf("%w: cannot verify Claude source cwd: %s", ErrUncheckable, err.Error())
	}
	if inside {
		return cwdOwned, nil
	}
	return cwdForeign, nil
}

// resolveVanished resolves symlinks in the longest prefix of path that still
// exists and re-attaches the part that does not, so a deleted directory is
// placed where it stood rather than left unknown. With strict set, a vanished
// part that is not written in canonical form is refused: lexical text can only
// stand in for a place when it is unambiguous.
func resolveVanished(path string, strict bool) (string, error) {
	if !filepath.IsAbs(path) {
		return path, nil // withinRepo refuses relative paths
	}
	// resolve the path as written: EvalSymlinks follows ".." physically, which
	// cleaning it first would not
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if strict && path != filepath.Clean(path) {
		return "", errNonCanonicalCwd
	}
	var vanished []string
	for dir := filepath.Clean(path); ; {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			for i := len(vanished) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, vanished[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return "", err
		}
		if _, statErr := os.Lstat(dir); statErr == nil {
			// the entry exists but leads nowhere: a symlink whose target is gone
			// says nothing about where the session actually stood
			return "", err
		}
		vanished = append(vanished, filepath.Base(dir))
		dir = parent
	}
}

// withinRepo reports whether cwd, already symlink-resolved, lies inside
// repoRoot and outside any nested repository.
func withinRepo(repoRoot, cwd string) (bool, error) {
	if !filepath.IsAbs(cwd) {
		return false, nil
	}
	rel, err := filepath.Rel(repoRoot, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false, nil
	}
	// A nested Git worktree or initialized Ox project owns its own ledger;
	// containment in the parent's filesystem tree does not imply ownership. Only
	// "not there" counts as absence: any other failure to look (EACCES, I/O) is an
	// error, because a marker that cannot be seen cannot be ruled out.
	for dir := cwd; dir != repoRoot; {
		for _, marker := range []string{".git", filepath.Join(".sageox", "config.json"), filepath.Join(".sageox", "config.yaml")} {
			info, err := os.Lstat(filepath.Join(dir, marker))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("inspect %s: %w", filepath.Join(dir, marker), err)
			}
			// a submodule is checked out inside this repo and recorded in its
			// history; its .git file points back into this repo's own git dir
			if marker == ".git" && info.Mode().IsRegular() {
				own, ownErr := isOwnSubmodule(repoRoot, dir)
				if ownErr != nil {
					return false, ownErr
				}
				if own {
					continue
				}
			}
			return false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil // do not spin if a malformed root never matches
		}
		dir = parent
	}
	return true, nil
}

// isOwnSubmodule reports whether dir's .git file is a gitlink into repoRoot's
// own modules directory. A linked worktree or a separate clone also has a .git
// file, but its git dir lives elsewhere, so it stays foreign. A file that cannot
// be read is an error, not a verdict.
func isOwnSubmodule(repoRoot, dir string) (bool, error) {
	gitdir, ok, err := readGitlink(filepath.Join(dir, ".git"))
	if err != nil || !ok {
		return false, err
	}
	gitdir, err = filepath.EvalSymlinks(gitdir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // a gitlink to nowhere cannot be vouched for
	}
	if err != nil {
		return false, err
	}
	repoGitDir := filepath.Join(repoRoot, ".git")
	if info, statErr := os.Lstat(repoGitDir); statErr == nil && info.Mode().IsRegular() {
		// repoRoot is itself a linked worktree: its submodules live under
		// the git dir its own .git file names
		repoGitDir, ok, err = readGitlink(repoGitDir)
		if err != nil || !ok {
			return false, err
		}
	}
	repoGitDir, err = filepath.EvalSymlinks(repoGitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(filepath.Join(repoGitDir, "modules"), gitdir)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)), nil
}

// readGitlink returns the absolute git dir a ".git" file points at. ok is false
// for a file that is not a gitlink; err is set when it could not be read.
func readGitlink(path string) (target string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(data) > 4096 {
		return "", false, nil
	}
	target, ok = strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", false, nil
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return target, true, nil
}
