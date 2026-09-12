// Package kb is the CLI-side home of Knowledge Bubble handling (catalog
// fetch, git-transport resilience) — see ox ADR-028 for what a bubble is.
//
// One deliberate scope note: the merge-attributes guard in this file is
// shared BY ledger and team-context clones too. Those are conversation
// stores, not bubbles — but all three are managed git working trees the
// daemon pulls (and, for the stores, the CLI pushes to), with the same
// multi-writer hazards: server-side seed, CLI-side seed, several coworkers
// committing in parallel. Transport-level resilience that is true for every
// managed clone lives here; store-specific behavior stays in its own
// package (internal/ledger/*, the team-context layer in cmd/ox +
// internal/teamdocs).
package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mergeAttributesRelPath is where ox writes its merge-driver rules
// inside any KB clone. We use $GIT_DIR/info/attributes (per-clone, not
// version-controlled, never enters the working tree) instead of a
// tracked .gitattributes for three reasons:
//
//  1. Loaded by git unconditionally on every merge — no chicken-and-egg
//     where the rules must already be committed before they apply (the
//     committed-.gitattributes path can't fix the very first conflict
//     on a fresh KB repo, which is exactly the case we need to fix).
//  2. Invisible to the user — no extra tracked file, no interaction
//     with a user's git-lfs install or with their own .gitattributes
//     if any.
//  3. No coordination problem with server-side seeds: each clone
//     manages its own copy; we rewrite it on every Init / pull
//     pre-flight / push pre-flight so old clones get healed
//     automatically.
const mergeAttributesRelPath = ".git/info/attributes"

// MergeUnionPaths lists the root metadata files where multiple writers
// (server-side seed, CLI seed, coworker edits, doctor) routinely touch
// the same file concurrently in BOTH ledger and team-context repos.
// Declaring merge=union for these tells git to concatenate both sides
// on conflict instead of halting the rebase, which turns "wedged repo
// after first push" into "repo keeps moving; duplicate lines can be
// deduped later by a doctor pass."
//
// merge=union is the same driver git uses for changelogs and NEWS files.
// It is safe for append-mostly metadata. It is NOT applied to session,
// history, murmur, or memory entry paths — those are conflict-free by
// construction since each entry has a unique timestamp- or id-based
// path.
//
// Files included:
//
//   - AGENTS.md, CLAUDE.md, README.md, CONVENTIONS.md — appear at the
//     root of both repo types; touched by humans, AI coworkers, server
//     seed, ox prime injection.
//   - SOUL.md — team-context "soul" doc; multi-coworker writes are the
//     point of the file.
//   - .gitignore — both repo types may have ignore patterns added by
//     either side.
//
// Exported so external tooling and tests can introspect the canonical
// list without re-deriving it.
var MergeUnionPaths = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"README.md",
	"CONVENTIONS.md",
	"SOUL.md",
	".gitignore",
}

// managed-block markers. Anything between the header and footer is
// rewritten by EnsureMergeAttributes; content outside is preserved so
// any hand-edited info/attributes (rare but possible) is left alone.
const (
	mergeAttrsHeader = "# BEGIN ox-managed merge rules — do not edit between markers"
	mergeAttrsFooter = "# END ox-managed merge rules"
)

// renderManagedBlock returns the canonical ox-managed attributes block.
func renderManagedBlock() string {
	var b strings.Builder
	b.WriteString(mergeAttrsHeader + "\n")
	b.WriteString("# Concatenate both sides on conflict for KB metadata files.\n")
	b.WriteString("# These are append-mostly and written by multiple coworkers + the\n")
	b.WriteString("# server seed flow. merge=union lets the rebase keep moving instead\n")
	b.WriteString("# of wedging on a first-push race or concurrent edits. Applies to\n")
	b.WriteString("# ledger and team-context clones identically.\n")
	for _, p := range MergeUnionPaths {
		fmt.Fprintf(&b, "%-16s merge=union\n", p)
	}
	b.WriteString(mergeAttrsFooter + "\n")
	return b.String()
}

// EnsureMergeAttributes writes or updates the ox-managed merge-driver
// block in the KB repo's per-clone attributes file
// (.git/info/attributes). Idempotent. Preserves any content outside the
// managed block. Returns true if the file changed, false if it was
// already up to date.
//
// The repo must already be a git working tree (have a .git directory).
// Caller passes the working-tree root, not the .git directory.
//
// Designed to be safe to call on every pull/push cycle: idempotent,
// atomic write (temp + rename), best-effort error handling.
func EnsureMergeAttributes(repoPath string) (changed bool, err error) {
	if repoPath == "" {
		return false, fmt.Errorf("repo path is empty")
	}

	// fail fast if .git is missing — silently MkdirAll-ing into a
	// non-git directory would mask path/caller bugs and leave the rules
	// where git will never read them.
	if info, statErr := os.Stat(filepath.Join(repoPath, ".git")); statErr != nil || !info.IsDir() {
		return false, fmt.Errorf("not a git repository: %s", repoPath)
	}

	full := filepath.Join(repoPath, mergeAttributesRelPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, fmt.Errorf("create info dir: %w", err)
	}

	managed := renderManagedBlock()

	existing, readErr := os.ReadFile(full)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read info/attributes: %w", readErr)
	}

	desired := mergeManagedBlock(string(existing), managed)
	if string(existing) == desired {
		return false, nil
	}

	// Atomic write: a direct WriteFile interrupted mid-write (process kill,
	// power loss) would leave a truncated info/attributes that git silently
	// treats as "no rules", which is exactly the wedged-rebase state this
	// whole pre-flight is trying to prevent.
	if err := writeFileAtomic(full, desired); err != nil {
		return false, fmt.Errorf("write info/attributes: %w", err)
	}
	return true, nil
}

// mergeManagedBlock returns the desired info/attributes content given
// the existing file (possibly empty) and the canonical managed block.
//
// Behavior:
//   - If the header marker is absent, the managed block is appended
//     (with a separating blank line if there's pre-existing content).
//   - If the header is present, the entire block from header to footer
//     is replaced. Content outside is preserved verbatim.
//   - If the header is present but the footer is missing (corrupted /
//     legacy), everything from the header to EOF is replaced.
func mergeManagedBlock(existing, managed string) string {
	return mergeManagedBlockBetween(existing, managed, mergeAttrsHeader, mergeAttrsFooter)
}

// mergeManagedBlockBetween is mergeManagedBlock for an arbitrary
// header/footer pair, shared with the .git/info/exclude writer.
func mergeManagedBlockBetween(existing, managed, header, footer string) string {
	headerIdx := strings.Index(existing, header)
	if headerIdx < 0 {
		if existing == "" {
			return managed
		}
		sep := ""
		if !strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		return existing + sep + "\n" + managed
	}

	before := existing[:headerIdx]
	after := ""
	footerIdx := strings.Index(existing[headerIdx:], footer)
	if footerIdx >= 0 {
		end := headerIdx + footerIdx + len(footer)
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		after = existing[end:]
	}
	return before + managed + after
}

// writeFileAtomic writes content to full via temp + fsync + rename so a
// crash mid-write can never leave a truncated file behind. Rename is
// atomic on the same filesystem; the temp file is created next to the
// target for that reason.
func writeFileAtomic(full, content string) error {
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(full)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// best-effort cleanup if we bail before rename
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
