package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConversationReferenceDocs_Committed pins that the generated reference
// docs for the `ox conversation` family have been committed and match the
// current command surface shape. This is the "golden" check (same pattern as
// TestDistillHistoryReferenceDocs_Committed): the files exist and advertise
// the exact flag set and usage string the implementation exposes.
//
// Failure prevented: a developer edits a flag name or adds a new one and
// forgets to regenerate `docs/reference/` via `ox docs --output
// docs/reference`. The committed .mdx files go stale and users land on docs
// that no longer match the binary.
//
// Update procedure: when this test fails after a legitimate cmd change, run
// `go build -o /tmp/ox-docs-tmp ./cmd/ox && /tmp/ox-docs-tmp docs --output
// docs/reference && rm /tmp/ox-docs-tmp` and recommit the conversation/
// subtree. The scope is intentionally narrow — it does not regenerate on the
// fly (which would mask drift) and it does not check every line of the .mdx
// (which would be too brittle).
func TestConversationReferenceDocs_Committed(t *testing.T) {
	repoRoot := findRepoRootForDocsTest(t)
	docsDir := filepath.Join(repoRoot, "docs", "reference", "conversation")

	cases := []struct {
		file   string
		musts  []string
		absent []string
	}{
		{
			file: "index.mdx",
			musts: []string{
				`title: "ox conversation"`,
				"## ox conversation",
				"[ox conversation list](/conversation/list)",
				"[ox conversation show](/conversation/show)",
				"[ox conversation topics](/conversation/topics)",
				"[ox conversation topic](/conversation/topic)",
				"[ox conversation transcript](/conversation/transcript)",
			},
		},
		{
			file: "list.mdx",
			musts: []string{
				`title: "ox conversation list"`,
				"## ox conversation list",
				"ox conversation list [flags]",
				"--format string",
				"--limit int",
				"--since string",
				"--text",
			},
			// the family is single-team by design (plan D18); a team
			// selector must never leak into the docs.
			absent: []string{"--team", "--all-teams"},
		},
		{
			file: "show.mdx",
			musts: []string{
				`title: "ox conversation show"`,
				"## ox conversation show",
				"ox conversation show <id> [flags]",
				"--format string",
				"--text",
			},
			absent: []string{"--limit", "--since", "--team"},
		},
		{
			file: "topics.mdx",
			musts: []string{
				`title: "ox conversation topics"`,
				"## ox conversation topics",
				"ox conversation topics <id> [flags]",
				"--format string",
			},
			absent: []string{"--include-superseded", "--team"},
		},
		{
			file: "topic.mdx",
			musts: []string{
				`title: "ox conversation topic"`,
				"## ox conversation topic",
				"ox conversation topic <id> <tp_id> [flags]",
				"--format string",
				"--include-superseded",
			},
			absent: []string{"--team"},
		},
		{
			file: "transcript.mdx",
			musts: []string{
				`title: "ox conversation transcript"`,
				"## ox conversation transcript",
				"ox conversation transcript <id> [flags]",
				"--cues string",
				"--format string",
				"--from string",
				"--to string",
				"--full",
				// the --full escape hatch must keep telling agents it is
				// meant for humans (plan D15).
				"intended for humans",
			},
			absent: []string{"--team"},
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
