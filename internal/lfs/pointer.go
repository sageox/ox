package lfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pointerVersion is the Git LFS pointer spec version string.
// Spec: https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md
const pointerVersion = "https://git-lfs.github.com/spec/v1"

// FormatPointer returns canonical LFS pointer file content for the given OID and size.
// OID must include the "sha256:" prefix (matching FileRef.OID convention).
//
// Per spec: version line first, then remaining keys in alphabetical order.
// "oid" < "size" lexicographically, so the ordering is: version, oid, size.
// Each line is "key SP value LF" with Unix line endings (\n, not \r\n).
func FormatPointer(oid string, size int64) string {
	return fmt.Sprintf("version %s\noid %s\nsize %d\n", pointerVersion, oid, size)
}

// ParsePointer parses LFS pointer file content and returns the OID and size.
// Returns an error if the content is not a valid LFS pointer.
//
// Per spec: version line must appear first; remaining keys ("oid", "size")
// are in alphabetical order. Unknown keys (e.g. "ext-0-*") are silently
// ignored, allowing forward compatibility with spec extensions.
func ParsePointer(content string) (oid string, size int64, err error) {
	lines := strings.Split(strings.TrimSpace(content), "\n")

	if len(lines) < 3 {
		return "", 0, fmt.Errorf("not an LFS pointer: expected at least 3 lines, got %d", len(lines))
	}

	if !strings.HasPrefix(lines[0], "version ") || !strings.Contains(lines[0], "git-lfs") {
		return "", 0, fmt.Errorf("not an LFS pointer: missing version line")
	}

	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "oid "):
			oid = strings.TrimPrefix(line, "oid ")
		case strings.HasPrefix(line, "size "):
			if _, err := fmt.Sscanf(line, "size %d", &size); err != nil {
				return "", 0, fmt.Errorf("parse size: %w", err)
			}
		}
	}

	if oid == "" {
		return "", 0, fmt.Errorf("not an LFS pointer: missing oid")
	}
	if size <= 0 {
		return "", 0, fmt.Errorf("not an LFS pointer: missing or invalid size")
	}

	return oid, size, nil
}

// --- File I/O layer (clean / dehydrate / detect) ---
//
// These functions implement the "clean" side of git-lfs (replacing content
// with pointers) without requiring .gitattributes or the git-lfs binary.
// We use the LFS Batch API directly for uploads (see client.go, transfer.go),
// then commit pointer files to git under the original filenames. Because we
// don't register filter=lfs in .gitattributes, git-lfs checkout will NOT
// auto-hydrate these files — hydration is handled by our own download path.

// WritePointerFile writes a standard LFS pointer file at path.
// Replaces any existing file (content is already uploaded to LFS).
func WritePointerFile(path string, ref FileRef) error {
	content := FormatPointer(ref.OID, ref.Size)
	return os.WriteFile(path, []byte(content), 0644)
}

// WritePointerFiles writes LFS pointer files for each entry in files.
// Keys are filenames written as dir/<key>. Returns sorted absolute paths
// of written files. Both sessions and imports use this to create the
// standard git-lfs pointer files that prevent garbage collection.
func WritePointerFiles(dir string, files map[string]FileRef) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}

	var paths []string
	for name, ref := range files {
		p := filepath.Join(dir, name)
		if err := WritePointerFile(p, ref); err != nil {
			return paths, fmt.Errorf("write pointer %s: %w", name, err)
		}
		paths = append(paths, p)
	}

	sort.Strings(paths)
	return paths, nil
}

// maxPointerSize is the upper bound for LFS pointer files we detect.
// The spec allows up to 1024 bytes (for extension keys), but our pointers
// are ~130 bytes (version + sha256 OID + size). 200 bytes gives headroom
// for long OIDs while skipping content files without reading them.
const maxPointerSize = 200

// IsPointerFile reports whether the file at path is an LFS pointer.
// Returns false for missing files, content files, or read errors.
// Detection is by content format (version + oid + size), not by filename
// or .gitattributes — matching how git-lfs itself identifies pointers.
func IsPointerFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxPointerSize {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, _, err = ParsePointer(string(data))
	return err == nil
}

// ReadPointerFile reads and parses an LFS pointer file, returning the FileRef.
func ReadPointerFile(path string) (FileRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileRef{}, fmt.Errorf("read pointer file: %w", err)
	}
	oid, size, err := ParsePointer(string(data))
	if err != nil {
		return FileRef{}, err
	}
	return FileRef{OID: oid, Size: size}, nil
}
