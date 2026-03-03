package lfs

import (
	"fmt"
	"strings"
)

// pointerVersion is the Git LFS pointer spec version string.
// See: https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md
const pointerVersion = "https://git-lfs.github.com/spec/v1"

// FormatPointer returns canonical LFS pointer file content for the given OID and size.
// OID must include the "sha256:" prefix (matching FileRef.OID convention).
func FormatPointer(oid string, size int64) string {
	return fmt.Sprintf("version %s\noid %s\nsize %d\n", pointerVersion, oid, size)
}

// ParsePointer parses LFS pointer file content and returns the OID and size.
// Returns an error if the content is not a valid LFS pointer.
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
