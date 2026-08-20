package read

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxFolderNameLen bounds a discussion folder name. Real folder names are
// short date-slug strings; anything longer is hostile or corrupt.
const maxFolderNameLen = 255

// joinDiscussion is the single path-join guard (D16): every discussion
// folder path — from INDEX.json today, from a server resolve response in the
// future — passes through here before it touches the filesystem, and the
// result is confined to the team's discussions/ root.
//
// A folder is one plain path element: no separators (either slash), no
// traversal, not absolute, no NUL, bounded length. The joined result is
// re-verified to sit strictly inside the root (defense in depth — with the
// element checks above the verification cannot fail, but the guard holds
// even if the checks drift).
func joinDiscussion(discussionsRoot, folder string) (string, *Error) {
	if folder == "" {
		return "", newError(ErrCodeReadError, "discussion folder name is empty")
	}
	if len(folder) > maxFolderNameLen {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name exceeds %d bytes", maxFolderNameLen))
	}
	if strings.ContainsAny(folder, "/\\") || strings.ContainsRune(folder, 0) {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q contains a path separator", truncateID(folder)))
	}
	if folder == "." || folder == ".." {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q is a traversal element", folder))
	}
	if filepath.IsAbs(folder) || strings.Contains(folder, ":") {
		// ':' rejects Windows drive/volume spellings that IsAbs may miss on
		// non-Windows builds.
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q is not a relative folder name", truncateID(folder)))
	}

	joined := filepath.Join(discussionsRoot, folder)
	rel, err := filepath.Rel(discussionsRoot, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder name %q escapes the discussions root", truncateID(folder)))
	}

	// The name checks above are purely lexical: a clean single element can
	// still be a symlink committed into the (customer-writable, git-synced)
	// team context, pointing anywhere on disk. filepath.Rel cannot see
	// through it, so reject symlinked entries outright — Lstat never follows
	// the link. A missing entry passes: the caller's own read reports the
	// typed absence.
	if info, lerr := os.Lstat(joined); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", newError(ErrCodeReadError, fmt.Sprintf("discussion folder %q is a symlink; symlinked discussion entries are not read", truncateID(folder)))
	}
	return joined, nil
}
