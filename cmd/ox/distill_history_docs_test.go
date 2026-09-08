package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDistillHistoryCmd_VisibleAfterReveal pins Unit 6's feature-flag flip:
// distillHistoryCmd and all three read subcommands must be user-visible
// (Hidden == false) once the reveal lands.
//
// Failure prevented: a later edit to the cobra.Command struct literal
// silently re-hides one of the commands, and nothing else catches it
// until a user reports the command is missing from `ox --help`.
func TestDistillHistoryCmd_VisibleAfterReveal(t *testing.T) {
	if distillHistoryCmd.Hidden {
		t.Errorf("distillHistoryCmd.Hidden = true — must be visible after Unit 6 reveal")
	}
	if distillHistoryListCmd.Hidden {
		t.Errorf("distillHistoryListCmd.Hidden = true — must be visible after Unit 6 reveal")
	}
	if distillHistoryShowCmd.Hidden {
		t.Errorf("distillHistoryShowCmd.Hidden = true — must be visible after Unit 6 reveal")
	}
	if distillHistorySinceCmd.Hidden {
		t.Errorf("distillHistorySinceCmd.Hidden = true — must be visible after Unit 6 reveal")
	}
}

// TestDistillHistoryReferenceDocs_Committed pins that the generated reference
// docs for `ox distill history list|show|since` have been committed and match
// the current command surface shape. This is the "golden" check: the
// files exist and advertise the exact flag set and subcommand usage
// string the implementation exposes.
//
// Failure prevented: a developer edits a flag name or adds a new one
// and forgets to regenerate `docs/reference/` via `ox docs --output
// docs/reference`. The committed .mdx files go stale and users land
// on docs that no longer match the binary.
//
// Update procedure: when this test fails after a legitimate cmd change,
// run `go build -o /tmp/ox-docs-tmp ./cmd/ox && /tmp/ox-docs-tmp docs
// --output docs/reference && rm /tmp/ox-docs-tmp` and recommit the
// distill/history/ subtree. The scope of this test is intentionally
// narrow — it does not regenerate on the fly (which would mask drift)
// and it does not check every line of the .mdx (which would be too
// brittle).
func TestDistillHistoryReferenceDocs_Committed(t *testing.T) {
	repoRoot := findRepoRootForDocsTest(t)
	docsDir := filepath.Join(repoRoot, "docs", "reference", "distill", "history")

	cases := []struct {
		file   string
		musts  []string
		absent []string
	}{
		{
			file: "index.mdx",
			musts: []string{
				`title: "ox distill history"`,
				"## ox distill history",
				"Inspect distilled daily/weekly/monthly summary entries",
				"[ox distill history list](/distill/history/list)",
				"[ox distill history show](/distill/history/show)",
				"[ox distill history since](/distill/history/since)",
			},
		},
		{
			file: "list.mdx",
			musts: []string{
				`title: "ox distill history list"`,
				"## ox distill history list",
				"ox distill history list [flags]",
				"--all-teams",
				"--format string",
				"--layer string",
				"--limit int",
				"--since string",
				"--team string",
				"--tz string",
				"--until string",
			},
			// --latest is plan-banned on every read command; it must
			// never leak into the generated docs.
			absent: []string{"--latest"},
		},
		{
			file: "show.mdx",
			musts: []string{
				`title: "ox distill history show"`,
				"## ox distill history show",
				"ox distill history show <id>... [flags]",
				"--format string",
				"--team string",
			},
			absent: []string{"--latest", "--since", "--until", "--tz"},
		},
		{
			file: "since.mdx",
			musts: []string{
				`title: "ox distill history since"`,
				"## ox distill history since",
				"ox distill history since <duration> [flags]",
				"--all-teams",
				"--format string",
				"--layer string",
				"--limit int",
				"--team string",
			},
			// since is relative-duration-only: --tz / --since / --until
			// / --latest are all banned by spec §3.6.
			absent: []string{"--latest", "--tz", "--since ", "--until"},
		},
	}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			path := filepath.Join(docsDir, c.file)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (did you regenerate docs/reference/?)", path, err)
			}
			content := string(raw)
			for _, want := range c.musts {
				if !strings.Contains(content, want) {
					t.Errorf("%s missing %q\n--- file ---\n%s", c.file, want, content)
				}
			}
			for _, ban := range c.absent {
				if strings.Contains(content, ban) {
					t.Errorf("%s contains banned fragment %q (did an unrelated flag leak into the docs?)", c.file, ban)
				}
			}
		})
	}
}

// findRepoRootForDocsTest walks up from the test's working directory
// until it finds a directory containing a go.mod — the Go convention
// for locating a module root from an arbitrary test location.
func findRepoRootForDocsTest(t *testing.T) string {
	t.Helper()
	// Walk up from the SOURCE tree, not the process working directory: TestMain
	// deliberately moves the process out of the repository so a cwd-resolved doctor
	// check cannot reconcile the developer's own checkout.
	dir := packageDir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from test cwd")
		}
		dir = parent
	}
}
