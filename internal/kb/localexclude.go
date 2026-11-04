package kb

// Local-only ignore rules for Knowledge Bubble checkouts.
//
// Ledger and team-context checkouts get a COMMITTED .sageox/.gitignore
// (gitserver.EnsureCheckoutGitignore) whose blanket `*` rule hides every
// daemon-written file under .sageox/. That file is wrong for a bubble: the
// server-side Curator commits platform artifacts under .sageox/curator/
// (marks/, synopses/), and once a `*` rule reaches a bubble's main the
// Curator's own `git add -A` silently skips its save-mark. The server then
// concludes the synthesis never happened and re-drives it every hour,
// forever. Two production bubbles hit exactly this on 2026-08-18.
//
// So a bubble checkout never carries an ox-authored ignore file in the
// tree. Bubble sync is pull-only (internal/daemon/sync_bubbles.go: the
// daemon never adds, commits, or pushes into a bubble), which means the
// only ignore surface it may touch is the per-clone, never-committed
// .git/info/exclude. Rules there are invisible to the server and to every
// other clone, and they cannot mask a tracked file — pulled Curator
// artifacts always arrive tracked — so nothing the daemon writes here can
// alter what the Curator commits.
//
// The rule set is an explicit list of files the daemon writes into a
// bubble checkout, not a blanket `*`: a blanket rule in exclude would be
// harmless to the server, but it would also hide any new server-written
// subtree from a local `git status` and make this file the same kind of
// trap the committed one was. Extend the list when adding a daemon writer.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// localExcludeRelPath is the per-clone ignore file git consults after the
// tree's .gitignore files. It lives under .git/, so it is never committed,
// pushed, or seen by another clone.
const localExcludeRelPath = ".git/info/exclude"

// managed-block markers for the exclude file; same discipline as the
// merge-attrs block in mergeattrs.go.
const (
	localExcludeHeader = "# BEGIN ox-managed local excludes — do not edit between markers"
	localExcludeFooter = "# END ox-managed local excludes"
)

// LocalExcludePatterns are the daemon-written paths hidden from `git
// status` in a bubble checkout. Patterns are repo-root anchored. Exported
// so tooling and tests can introspect the canonical list.
var LocalExcludePatterns = []string{
	"/.sageox/meta.json",       // writeKBMeta (internal/daemon/sync_bubbles.go)
	"/.sageox/meta.json.tmp-*", // its atomic-write temp file
	"/.sageox/cache/",          // local-only derived data (.claude/rules/ledger-cache.md)
}

// renderLocalExcludeBlock returns the canonical ox-managed exclude block.
func renderLocalExcludeBlock() string {
	var b strings.Builder
	b.WriteString(localExcludeHeader + "\n")
	b.WriteString("# Daemon-written files in this Knowledge Bubble checkout. Kept out of\n")
	b.WriteString("# the committed tree on purpose: a committed .sageox/.gitignore would\n")
	b.WriteString("# hide the server Curator's own .sageox/curator/ artifacts from it.\n")
	for _, p := range LocalExcludePatterns {
		b.WriteString(p + "\n")
	}
	b.WriteString(localExcludeFooter + "\n")
	return b.String()
}

// EnsureLocalExcludes writes or updates the ox-managed block in the bubble
// checkout's .git/info/exclude. Idempotent; preserves content outside the
// block; atomic write. Returns true if the file changed.
//
// It never touches .sageox/.gitignore and never stages or commits
// anything — see the package comment above for why that is load-bearing.
func EnsureLocalExcludes(repoPath string) (changed bool, err error) {
	if repoPath == "" {
		return false, fmt.Errorf("repo path is empty")
	}
	if info, statErr := os.Stat(filepath.Join(repoPath, ".git")); statErr != nil || !info.IsDir() {
		return false, fmt.Errorf("not a git repository: %s", repoPath)
	}

	full := filepath.Join(repoPath, localExcludeRelPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, fmt.Errorf("create info dir: %w", err)
	}

	existing, readErr := os.ReadFile(full)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read info/exclude: %w", readErr)
	}

	desired := mergeManagedBlockBetween(string(existing), renderLocalExcludeBlock(), localExcludeHeader, localExcludeFooter)
	if string(existing) == desired {
		return false, nil
	}
	if err := writeFileAtomic(full, desired); err != nil {
		return false, fmt.Errorf("write info/exclude: %w", err)
	}
	return true, nil
}
