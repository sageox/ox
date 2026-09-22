// Package pointer parses Git LFS pointers without depending on git operations.
package pointer

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Parse reads an LFS pointer using the same rules as lfs.ParsePointer.
func Parse(content string) (oid string, size int64, err error) {
	lines := strings.Split(strings.TrimSpace(content), "\n")

	if len(lines) < 3 {
		return "", 0, fmt.Errorf("not an LFS pointer: expected at least 3 lines, got %d", len(lines))
	}

	if !IsVersionLine(lines[0]) {
		return "", 0, fmt.Errorf("not an LFS pointer: missing version line")
	}

	sawSize := false
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "oid "):
			oid = strings.TrimPrefix(line, "oid ")
		case strings.HasPrefix(line, "size "):
			if _, err := fmt.Sscanf(line, "size %d", &size); err != nil {
				return "", 0, fmt.Errorf("parse size: %w", err)
			}
			sawSize = true
		}
	}

	if oid == "" {
		return "", 0, fmt.Errorf("not an LFS pointer: missing oid")
	}
	// size 0 is a real pointer: an empty artifact (an unwritten context-trace.jsonl)
	// is tracked like any other, and rejecting it made one empty file fail a whole
	// ledger read. Only an absent or negative size is malformed.
	if !sawSize || size < 0 {
		return "", 0, fmt.Errorf("not an LFS pointer: missing or invalid size")
	}

	maxSize := MaxObjectSize()
	if size > maxSize {
		return "", 0, fmt.Errorf("LFS object size %d exceeds maximum %d (set OX_LFS_MAX_OBJECT_SIZE to override)", size, maxSize)
	}

	return oid, size, nil
}

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

// IsVersionLine reports whether line is an LFS pointer's version line, as
// ParsePointer reads one: a line split on LF may still end in CR.
func IsVersionLine(line string) bool {
	return strings.HasPrefix(line, "version ") && strings.Contains(line, "git-lfs")
}
