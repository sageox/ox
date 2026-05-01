package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ledgerMergeAttributesRelPath is where ox writes its merge-driver rules
// inside a ledger clone. We use $GIT_DIR/info/attributes (per-clone, not
// version-controlled, never enters the working tree) instead of a tracked
// .gitattributes for three reasons:
//
//  1. Loaded by git unconditionally on every merge — no chicken-and-egg
//     where the rules must already be committed before they apply (the
//     committed-.gitattributes path can't fix the very first conflict on
//     a fresh ledger, which is exactly the case we need to fix).
//  2. Invisible to the user — no extra tracked file, no interaction with
//     a user's git-lfs install or with their own .gitattributes if any.
//  3. No coordination problem with server-side ledger seeds: each clone
//     manages its own copy; we rewrite it on every Init and on push
//     pre-flight so old clones get healed automatically.
const ledgerMergeAttributesRelPath = ".git/info/attributes"

// ledgerMergeUnionPaths lists the root metadata files where multiple writers
// (server-side seed, CLI seed, coworker edits, doctor) routinely touch the
// same file concurrently. Declaring merge=union for these tells git to
// concatenate both sides on conflict instead of halting the rebase, which
// turns "wedged ledger after first session push" into "ledger keeps moving;
// duplicate lines can be deduped later by doctor."
//
// merge=union is the same driver git uses for changelogs and NEWS files.
// It's safe for append-mostly metadata; it is NOT applied to session/
// history/murmur paths (those are conflict-free by construction since each
// path is unique by timestamp+id).
var ledgerMergeUnionPaths = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"README.md",
	"CONVENTIONS.md",
	".gitignore",
}

// managed-block markers. Anything between the header and footer is rewritten
// by EnsureMergeAttributes; content outside is preserved so a hand-edited
// info/attributes (rare but possible) is left alone.
const (
	mergeAttrsHeader = "# BEGIN ox-managed merge rules — do not edit between markers"
	mergeAttrsFooter = "# END ox-managed merge rules"
)

// renderManagedBlock returns the canonical ox-managed attributes block.
func renderManagedBlock() string {
	var b strings.Builder
	b.WriteString(mergeAttrsHeader + "\n")
	b.WriteString("# Concatenate both sides on conflict for ledger metadata files.\n")
	b.WriteString("# These are append-mostly and written by multiple coworkers + the\n")
	b.WriteString("# server seed flow. merge=union lets the rebase keep moving instead\n")
	b.WriteString("# of wedging the ledger on a first-push race.\n")
	for _, p := range ledgerMergeUnionPaths {
		fmt.Fprintf(&b, "%-16s merge=union\n", p)
	}
	b.WriteString(mergeAttrsFooter + "\n")
	return b.String()
}

// EnsureMergeAttributes writes or updates the ox-managed merge-driver block
// in the ledger's per-clone attributes file (.git/info/attributes).
// Idempotent. Preserves any content outside the managed block. Returns true
// if the file changed, false if it was already up to date.
//
// The ledger must already be a git repo (have a .git directory). Caller
// passes the ledger working-tree root, not the .git directory.
func EnsureMergeAttributes(ledgerPath string) (changed bool, err error) {
	if ledgerPath == "" {
		return false, fmt.Errorf("ledger path is empty")
	}

	// fail fast if .git is missing — silently MkdirAll-ing into a non-git
	// directory would mask path/caller bugs and leave the rules where git
	// will never read them.
	if info, statErr := os.Stat(filepath.Join(ledgerPath, ".git")); statErr != nil || !info.IsDir() {
		return false, fmt.Errorf("not a git repository: %s", ledgerPath)
	}

	full := filepath.Join(ledgerPath, ledgerMergeAttributesRelPath)
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

	// Atomic write: temp + fsync + rename. A direct WriteFile interrupted
	// mid-write (process kill, power loss) would leave a truncated
	// info/attributes that git silently treats as "no rules", which is
	// exactly the wedged-rebase state this whole pre-flight is trying to
	// prevent. Rename is atomic on the same filesystem.
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".attributes.tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp info/attributes: %w", err)
	}
	tmpName := tmp.Name()
	// best-effort cleanup if we bail before rename
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(desired); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write temp info/attributes: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("fsync temp info/attributes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp info/attributes: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return false, fmt.Errorf("chmod temp info/attributes: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return false, fmt.Errorf("rename info/attributes: %w", err)
	}
	return true, nil
}

// mergeManagedBlock returns the desired info/attributes content given the
// existing file (possibly empty) and the canonical managed block.
//
// Behavior:
//   - If the header marker is absent, the managed block is appended (with
//     a separating blank line if there's pre-existing content).
//   - If the header is present, the entire block from header to footer is
//     replaced. Content outside is preserved verbatim.
//   - If the header is present but the footer is missing (corrupted /
//     legacy), everything from the header to EOF is replaced.
func mergeManagedBlock(existing, managed string) string {
	headerIdx := strings.Index(existing, mergeAttrsHeader)
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
	footerIdx := strings.Index(existing[headerIdx:], mergeAttrsFooter)
	if footerIdx >= 0 {
		end := headerIdx + footerIdx + len(mergeAttrsFooter)
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		after = existing[end:]
	}
	return before + managed + after
}
