package lfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

	maxSize := MaxObjectSize()
	if size > maxSize {
		return "", 0, fmt.Errorf("LFS object size %d exceeds maximum %d (set OX_LFS_MAX_OBJECT_SIZE to override)", size, maxSize)
	}

	return oid, size, nil
}

// NestedPointer reports whether content is a valid LFS pointer stored as the
// object referenced by outer. That shape is never valid content for an
// LFS-tracked artifact: checkout resolves outer to another pointer, leaving the
// worktree permanently dirty against the index.
//
// The outer size check is deliberate. It prevents a caller from treating an
// arbitrary small pointer-shaped payload as evidence about an unrelated LFS
// object.
func NestedPointer(outer FileRef, content []byte) (FileRef, bool) {
	if !outer.IsLFS() || outer.Size != int64(len(content)) || len(content) > maxPointerSize {
		return FileRef{}, false
	}
	oid, size, err := ParsePointer(string(content))
	if err != nil {
		return FileRef{}, false
	}
	return FileRef{Storage: StorageLFS, OID: oid, Size: size}, true
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
//
// It takes an UploadedRef, not a bare FileRef: a pointer may only be written for
// a blob whose upload is proven (see uploaded.go). This makes GH #810 — a
// pointer minted for content that was never uploaded — a compile error.
//
// Refuses to replace real content that does not match the ref OID — see
// guardPointerOverwrite.
func WritePointerFile(path string, uploaded UploadedRef) error {
	ref := uploaded.ref
	if err := guardPointerOverwrite(path, ref); err != nil {
		return err
	}
	content := FormatPointer(ref.OID, ref.Size)
	return os.WriteFile(path, []byte(content), 0644)
}

// guardPointerOverwrite refuses to replace real on-disk content with a pointer
// that does not describe that content.
//
// The happy path legitimately pointerizes real bytes: session upload writes
// content, uploads it to LFS, then replaces it with a pointer. That stays
// allowed, because the content hashes to ref.OID — it is provably in the store.
//
// The dangerous case is a meta.json whose Files map disagrees with the blob on
// disk, which is exactly what independently-resolved merge conflicts produce.
// UpdateMetaSummary calls WritePointerFiles over the WHOLE Files map, so a
// single stale OID would silently destroy the only local copy of a recording —
// and if that OID was never uploaded, the next push is rejected with "LFS
// objects are missing", after which the reconcile path blanks the file to zero
// bytes. Refusing here converts unrecoverable data loss into a loud error.
func guardPointerOverwrite(path string, ref FileRef) error {
	existing, err := os.ReadFile(path)
	if err != nil || len(existing) == 0 {
		return nil // nothing on disk to lose
	}
	if _, _, perr := ParsePointer(string(existing)); perr == nil {
		// Already a pointer. Swapping one pointer for another loses nothing —
		// a redaction pass legitimately installs a new OID.
		return nil
	}
	if ComputeOID(existing) == ref.BareOID() {
		return nil // content matches the OID: safely uploaded, pointerizing is correct
	}
	return fmt.Errorf(
		"refusing to overwrite %s with an LFS pointer: on-disk content does not "+
			"match OID %s (meta.json and the blob disagree — resolve before pointerizing)",
		filepath.Base(path), ref.BareOID())
}

// WritePointerFiles writes LFS pointer files for each LFS-stored entry in
// files. Keys are filenames written as dir/<key>. Returns sorted absolute
// paths of written files. Both sessions and imports use this to create the
// standard git-lfs pointer files that prevent garbage collection.
//
// Entries with Storage=git (committed directly to git, e.g. summary.json)
// are skipped — writing a pointer file there would clobber the real content
// with empty bytes. Legacy entries (no Storage field) are treated as LFS
// per FileRef.EffectiveStorage().
func WritePointerFiles(dir string, files map[string]UploadedRef) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}

	var paths []string
	for name, uploaded := range files {
		if !uploaded.ref.IsLFS() {
			continue // Storage=git: real content stays in place
		}
		if err := ValidateRelativePath(name); err != nil {
			return paths, fmt.Errorf("unsafe pointer filename: %w", err)
		}
		p := filepath.Join(dir, name)
		if err := WritePointerFile(p, uploaded); err != nil {
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

// DefaultMaxObjectSize is the upper bound for LFS objects we accept. Prevents
// malicious pointers from triggering unbounded disk writes. Override
// with OX_LFS_MAX_OBJECT_SIZE env var for legitimate large files.
const DefaultMaxObjectSize int64 = 5 * 1024 * 1024 * 1024 // 5 GiB

// MaxObjectSize returns the configured maximum LFS object size.
// Reads OX_LFS_MAX_OBJECT_SIZE env var, falling back to DefaultMaxObjectSize.
func MaxObjectSize() int64 {
	if v := os.Getenv("OX_LFS_MAX_OBJECT_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxObjectSize
}

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
	return FileRef{Storage: StorageLFS, OID: oid, Size: size}, nil
}
