// Package claudesource verifies that Claude's native JSONL belongs to one repo.
package claudesource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// ErrUntrustedSource means a complete record proves the source cannot belong
// to this recording. It should be preserved locally, not retried or uploaded.
var ErrUntrustedSource = errors.New("claude source has untrusted ownership")

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
	if err != nil {
		return err
	}
	if before == nil || !os.SameFile(before, after) || after.Size() < before.Size() ||
		(after.Size() == before.Size() && !after.ModTime().Equal(before.ModTime())) {
		return fmt.Errorf("claude source changed while reading; retry without advancing cursor")
	}
	return nil
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
	repoRoot = filepath.Clean(repoRoot)
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = resolved
	} else {
		return fmt.Errorf("resolve Claude source repository root: %w", err)
	}
	fileID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if sessionID != "" && fileID != sessionID {
		return fmt.Errorf("%w: session ID does not match filename", ErrUntrustedSource)
	}

	seenCwd, seenID := false, false
	checkedCwd := make(map[string]bool)
	var ownershipErr error
	_, _, _, err := adapterruntime.TailJSONLWithStats(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
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
			valid, checked := checkedCwd[cwd]
			if !checked {
				// A deleted cwd may have been an ordinary subdirectory or a
				// separate worktree. Its absence proves neither: defer rather
				// than permanently quarantine an uncheckable filesystem state.
				if _, err := filepath.EvalSymlinks(cwd); err != nil {
					ownershipErr = fmt.Errorf("cannot verify Claude source cwd: %w", err)
					return nil, nil
				}
				valid = withinRepo(repoRoot, cwd)
				checkedCwd[cwd] = valid
			}
			if !valid {
				ownershipErr = fmt.Errorf("%w: cwd outside repo", ErrUntrustedSource)
				return nil, nil
			}
			seenCwd = true
		}
		if (meta.Type == "user" || meta.Type == "assistant") && (cwd == "" || id == "") {
			ownershipErr = fmt.Errorf("%w: turn has missing ownership metadata", ErrUntrustedSource)
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	if ownershipErr != nil {
		return ownershipErr
	}
	if requireInitialIdentity && (!seenCwd || (sessionID != "" && !seenID)) {
		return fmt.Errorf("claude source has invalid or missing repository metadata")
	}
	return nil
}

func withinRepo(repoRoot, cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false // a deleted directory or symlink cannot prove its former ownership
	}
	cwd = resolved
	rel, err := filepath.Rel(repoRoot, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	// A nested Git worktree or initialized Ox project owns its own ledger;
	// containment in the parent's filesystem tree does not imply ownership.
	for dir := cwd; dir != repoRoot; {
		for _, marker := range []string{".git", filepath.Join(".sageox", "config.json"), filepath.Join(".sageox", "config.yaml")} {
			if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil || !os.IsNotExist(err) {
				return false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false // do not spin if a malformed root never matches
		}
		dir = parent
	}
	return true
}
