// Package sageoxignore owns the gitignore entries ox manages on a user's
// behalf, and the minimal line-level primitives for maintaining them.
//
// It exists because the same three helpers used to live twice — once in
// cmd/ox (package main, so nothing can import it) and once in
// internal/daemon. That duplication is what let ox init and the daemon
// disagree about where the knowledge-bubble ignore rule belongs, and is
// the direct cause of GH #732: both wrote `.sageox/kb/` into the
// developer's *root* .gitignore, and fixing only one writer left the
// other to put it straight back on the next sync pass.
//
// The package deliberately depends on nothing but the standard library so
// any layer of ox can import it.
package sageoxignore

import (
	"fmt"
	"os"
	"strings"
)

// KBEntry is the ignore rule for daemon-materialized knowledge-bubble
// symlinks, written into <project>/.sageox/.gitignore.
//
// Patterns in a nested .gitignore are relative to that file's own
// directory, so `kb/` here covers exactly what `.sageox/kb/` covered
// from the repo root — the same paths, without touching a file the
// developer considers theirs.
const KBEntry = "kb/"

// LegacyRootKBLine is what ox used to append to the project's root
// .gitignore before GH #732. Retained solely so existing installs can be
// cleaned up; nothing writes it any more.
const LegacyRootKBLine = ".sageox/kb/"

// HasEntry reports whether content already lists entry as a live rule.
//
// Comment- and blank-aware, so a documentary `# .sageox/kb/` line is not
// mistaken for the real thing. Matching is exact after trimming: `kb/`
// does not match `kb`, `kb/*`, or `!kb/`. That strictness is deliberate —
// see RemoveLine.
func HasEntry(content, entry string) bool {
	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == entry {
			return true
		}
	}
	return false
}

// EnsureEntry appends entry to the .gitignore at path exactly once,
// creating the file if it does not exist.
//
// Returns added=false when the entry was already present, so callers can
// stay quiet on the common no-op pass. created reports whether the file
// itself had to be made, which callers use to decide between "track as
// created" and "track as modified" for rollback.
func EnsureEntry(path, entry string) (added bool, created bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, false, fmt.Errorf("read %s: %w", path, err)
		}
		existing = nil
		created = true
	}
	if HasEntry(string(existing), entry) {
		return false, false, nil
	}

	var buf strings.Builder
	buf.Write(existing)
	// append on its own line even when the existing file lacked a
	// trailing newline, or we'd silently corrupt the last rule.
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(entry)
	buf.WriteString("\n")

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return false, false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, created, nil
}

// RemoveLine deletes every occurrence of line from the .gitignore at
// path, matching only on an exact trimmed match.
//
// The exactness is the whole safety argument for the #732 cleanup: this
// removes `.sageox/kb/` and nothing else. It will not touch `.sageox/`,
// `.sageox/kb`, `/.sageox/kb/`, `.sageox/kb/*`, `!.sageox/kb/`, or a
// commented `# .sageox/kb/`. Comments, blank lines, ordering, and the
// file's trailing-newline shape are all preserved — splitting and
// rejoining on "\n" round-trips the terminal empty element.
//
// A missing file is not an error; it reports removed=false.
func RemoveLine(path, line string) (removed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(existing), "\n")
	kept := make([]string, 0, len(lines))
	for _, raw := range lines {
		if strings.TrimSpace(raw) == line {
			removed = true
			continue
		}
		kept = append(kept, raw)
	}
	if !removed {
		return false, nil
	}

	out := strings.Join(kept, "\n")
	// Terminate a non-empty result. Deleting an unterminated final line
	// would otherwise strip the newline from the line now promoted to
	// last — mutating a rule we were never asked to touch. An empty
	// result stays empty rather than becoming a lone newline.
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}

	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
