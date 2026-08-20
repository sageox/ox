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
// through here before it is used against the filesystem.
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

// rejectSymlinkedFolder refuses a discussions/<folder> entry that is itself a
// symlink. The name resolves openat-style under the root's descriptor
// (Root.Lstat never follows the final component), so the check cannot be
// redirected by a component swap the way a bare os.Lstat on a joined path
// could. A clean single element can still be a symlink committed into the
// (customer-writable, git-synced) team context, pointing anywhere on disk —
// reject it outright. A missing entry passes: the caller's own read reports
// the typed absence.
func rejectSymlinkedFolder(root *os.Root, folder string) *Error {
	info, err := root.Lstat(folder)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return newError(ErrCodeReadError, fmt.Sprintf("discussion folder %q is a symlink; symlinked discussion entries are not read", truncateID(folder)))
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return newError(ErrCodeReadError, fmt.Sprintf("inspect discussion folder %q: %v", truncateID(folder), err))
	}
}

// joinDiscussion is the single path-join guard (D16): every discussion
// folder path passes through here before it touches the filesystem, and the
// result is confined to the team's discussions/ root.
//
// The name is validated lexically, the joined result is re-verified to sit
// strictly inside the root (defense in depth — with the element checks the
// verification cannot fail, but the guard holds even if the checks drift),
// and the entry is probed no-follow through an os.Root over the discussions
// root so a symlinked entry is rejected. The returned path is only the
// display/join form: actual content reads go through os.Root as well (the
// format loaders open their own root over it), so symlinks below the folder
// can never escape either.
func joinDiscussion(discussionsRoot, folder string) (string, *Error) {
	if verr := validateFolderName(folder); verr != nil {
		return "", verr
	}

	joined := filepath.Join(discussionsRoot, folder)
	rel, err := filepath.Rel(discussionsRoot, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q escapes the discussions root", truncateID(folder)))
	}

	root, rootErr := os.OpenRoot(discussionsRoot)
	if rootErr != nil {
		if errors.Is(rootErr, fs.ErrNotExist) {
			// No discussions root at all: the entry is missing, and the
			// caller's own read reports the typed absence.
			return joined, nil
		}
		return "", newError(ErrCodeReadError, fmt.Sprintf("open discussions root: %v", rootErr))
	}
	defer root.Close()
	if serr := rejectSymlinkedFolder(root, folder); serr != nil {
		return "", serr
	}
	return joined, nil
}

// readDiscussionFile reads one well-known file from a guarded discussion
// folder through an os.Root, so no path component — including the file
// itself — can follow a symlink out of the folder. A missing folder reads as
// a missing file: both surface fs.ErrNotExist, which callers map to their
// typed absence; every other failure is wrapped with context.
func readDiscussionFile(folderPath, name string) ([]byte, error) {
	root, err := os.OpenRoot(folderPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("open %s: %w", folderPath, err)
	}
	defer root.Close()
	data, err := root.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read %s: %w", filepath.Join(folderPath, name), err)
	}
	return data, nil
}
