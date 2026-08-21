package read

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxFolderNameLen bounds a discussion folder name. Real folder names are
// short date-slug strings; anything longer is hostile or corrupt.
const maxFolderNameLen = 255

// validateFolderName is the lexical half of the D16 join guard: a folder is
// one plain path element — no separators (either slash), no traversal, not
// absolute, no NUL, bounded length. Every discussion folder name — from
// INDEX.json today, from a server resolve response in the future — passes
// through here before it is used against the filesystem. The single-element
// guarantee also means no name ever reaches an os.Root method with a
// trailing separator (CVE-2026-39822: a final symlink component with a
// trailing slash must never be handed to Root path resolution).
func validateFolderName(folder string) *Error {
	if folder == "" {
		return newError(ErrCodeReadError, "discussion folder name is empty")
	}
	if len(folder) > maxFolderNameLen {
		return newError(ErrCodeReadError, fmt.Sprintf("discussion folder name exceeds %d bytes", maxFolderNameLen))
	}
	if strings.ContainsAny(folder, "/\\") || strings.ContainsRune(folder, 0) {
		return newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q contains a path separator", truncateID(folder)))
	}
	if folder == "." || folder == ".." {
		return newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q is a traversal element", folder))
	}
	if filepath.IsAbs(folder) || strings.Contains(folder, ":") {
		// ':' rejects Windows drive/volume spellings that IsAbs may miss on
		// non-Windows builds.
		return newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q is not a relative folder name", truncateID(folder)))
	}
	return nil
}

// openDiscussion is the single open guard (D16): it derives an open handle on
// one discussion folder from the held discussions-root *os.Root. The name is
// validated lexically, probed no-follow (Root.Lstat never follows the final
// component), and opened with Root.OpenRoot — every step resolves
// openat-style against the same directory descriptor, and the folder is
// never re-opened by absolute path after validation. That is the structural
// TOCTOU fix: os.OpenRoot follows symlinks while resolving its initial path
// argument, so a folder swapped for a symlink between validation and an
// absolute-path open could otherwise redirect reads outside the discussions
// root. Root.OpenRoot cannot escape the root even if the entry changes
// between the probe and the open.
//
// Returns (nil, nil) for an absent entry and for a symlinked or
// non-directory entry — both are deliberately never served, and the caller
// reports its typed absence. Every other filesystem failure is a retryable
// read_error, never conflated with absence.
func openDiscussion(root *os.Root, folder string) (*os.Root, *Error) {
	if verr := validateFolderName(folder); verr != nil {
		return nil, verr
	}
	info, statErr := root.Lstat(folder)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		return nil, nil
	case statErr != nil:
		return nil, newError(ErrCodeReadError, fmt.Sprintf("inspect discussion folder %q: %v", truncateID(folder), statErr))
	case !info.IsDir():
		// Symlinked entry (Lstat reports the link itself, never the target)
		// or a stray plain file: deliberately skipped, same as the index
		// pass. A symlink committed into the customer-writable, git-synced
		// team context can point anywhere on disk — never read through it.
		return nil, nil
	}
	droot, err := root.OpenRoot(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil // vanished between probe and open: phantom
	case err != nil:
		// Includes the entry being swapped for an escaping symlink between
		// the probe and the open: Root.OpenRoot refuses to resolve outside
		// the discussions root.
		return nil, newError(ErrCodeReadError, fmt.Sprintf("open discussion folder %q: %v", truncateID(folder), err))
	}
	return droot, nil
}

// readDiscussionFile reads one well-known file from an open discussion-folder
// handle, so no path component — including the file itself — can follow a
// symlink out of the folder. A missing file surfaces fs.ErrNotExist, which
// callers map to their typed absence; every other failure is wrapped with the
// file's name for context.
func readDiscussionFile(droot *os.Root, name string) ([]byte, error) {
	data, err := droot.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}
