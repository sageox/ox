// Package cursorpaths validates Cursor's observed native JSONL transcript layout.
package cursorpaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// ErrInvalidConversationID means the value cannot name an observed Cursor
	// conversation directory. Cursor's generation ID is deliberately not
	// accepted here: it identifies a turn, not a conversation.
	ErrInvalidConversationID = errors.New("invalid Cursor conversation ID")
	ErrInvalidSource         = errors.New("invalid Cursor transcript source")
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var projectKeySeparators = regexp.MustCompile(`[/.]+`)

// ValidateConversationID accepts only Cursor's canonical lowercase UUID form.
// The native nested directory and basename must use this exact identity.
func ValidateConversationID(id string) error {
	if !canonicalUUID.MatchString(id) {
		return ErrInvalidConversationID
	}
	return nil
}

// SessionPath returns Cursor's sole observed native transcript path for an
// initialized workspace and conversation. It never creates any Cursor paths.
func SessionPath(homeDir, repoRoot, conversationID string) (string, error) {
	if err := ValidateConversationID(conversationID); err != nil {
		return "", err
	}

	home, err := canonicalDirectory(homeDir, "home directory")
	if err != nil {
		return "", err
	}
	repo, err := canonicalDirectory(repoRoot, "repository root")
	if err != nil {
		return "", err
	}

	projectKey := projectKey(repo)
	return filepath.Join(home, ".cursor", "projects", projectKey, "agent-transcripts", conversationID, conversationID+".jsonl"), nil
}

// ValidateSource verifies that hint, when supplied, is exactly the native path
// determined by homeDir, repoRoot, and conversationID. It returns that
// canonical expected path even when Cursor has not created the transcript yet.
//
// Existing path components are checked without following a link. Cursor's
// observed native layout contains ordinary directories and a regular JSONL
// leaf; accepting a link here would let a later open read a different source.
func ValidateSource(homeDir, repoRoot, conversationID, hint string) (string, error) {
	expected, err := SessionPath(homeDir, repoRoot, conversationID)
	if err != nil {
		return "", err
	}

	if hint != "" {
		if !filepath.IsAbs(hint) || hasParentReference(hint) || filepath.Clean(hint) != expected {
			return "", fmt.Errorf("%w: hint does not match the expected conversation path", ErrInvalidSource)
		}
	}

	if err := validateExpectedPath(expected); err != nil {
		return "", err
	}
	return expected, nil
}

func canonicalDirectory(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %s must be an absolute directory", ErrInvalidSource, label)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", sourceError("resolve "+label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", sourceError("inspect "+label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidSource, label)
	}
	return filepath.Clean(resolved), nil
}

func projectKey(canonicalRepoRoot string) string {
	key := strings.TrimPrefix(filepath.ToSlash(canonicalRepoRoot), "/")
	return projectKeySeparators.ReplaceAllString(key, "-")
}

func hasParentReference(path string) bool {
	for _, component := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

// validateExpectedPath walks only the deterministic expected layout. A missing
// component is a pending transcript, provided all existing ancestors were
// ordinary directories. It does not create any paths.
func validateExpectedPath(expected string) error {
	volume := filepath.VolumeName(expected)
	parts := strings.FieldsFunc(strings.TrimPrefix(expected, volume), func(r rune) bool {
		return r == filepath.Separator
	})
	if len(parts) == 0 {
		return fmt.Errorf("%w: empty expected path", ErrInvalidSource)
	}

	current := volume + string(filepath.Separator)
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// current's parent is the nearest existing ancestor and was checked
			// above. Missing nested native paths are a normal pending source.
			return nil
		}
		if err != nil {
			return sourceError("inspect native source", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, err := filepath.EvalSymlinks(current); err != nil {
				return sourceError("dangling native source symlink", err)
			}
			return fmt.Errorf("%w: native source contains a symlink", ErrInvalidSource)
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: transcript is not a regular file", ErrInvalidSource)
			}
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: native source component is not a directory", ErrInvalidSource)
		}
	}
	return nil
}

// sourceError retains the operating-system category for errors.Is without
// returning the personal native path carried by an *os.PathError.
func sourceError(action string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = &os.PathError{Op: pathErr.Op, Path: "", Err: pathErr.Err}
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidSource, action, err)
}
