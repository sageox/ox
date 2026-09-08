package main

import (
	"os"
	"strings"
	"testing"
)

// TestLoadBearingCommentPresent_LocalRecall pins the load-bearing privacy
// comment that lives above shellLocalQueryRunner and gates the call to
// `ox query --local`. The comment is the only thing standing between
// future contributors and a well-intentioned PR that "improves" the
// recall hook by pointing it at the cloud API — silently exfiltrating
// every user prompt on every turn.
//
// If this test breaks, DO NOT delete the test. Either:
//  1. Restore the comment, or
//  2. Update this test with reviewer-approved language that still
//     covers privacy, latency, relevance, and the cloud-opt-in escape.
//
// Failure prevented: a routine refactor silently strips the privacy
// rationale, leaving `--local` looking like an arbitrary choice that
// the next refactor "cleans up" into a remote call.
func TestLoadBearingCommentPresent_LocalRecall(t *testing.T) {
	path := repoPath("agent_hook_recall.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)

	// Phrases that together encode the contract. Each phrase must
	// survive any reformatting of the comment block.
	mustContain := []string{
		"LOAD-BEARING COMMENT",
		"--local",
		// privacy
		"Privacy:",
		"pasted secrets",
		// latency
		"Latency:",
		"100ms",
		// relevance
		"Relevance:",
		// cloud opt-in escape hatch
		"hooks.userpromptsubmit.cloud_query",
		"ox-8wmo",
	}
	for _, want := range mustContain {
		if !strings.Contains(src, want) {
			t.Errorf("load-bearing recall comment missing required phrase %q in %s", want, path)
		}
	}

	// Ensure the comment sits next to the actual call site, not orphaned
	// somewhere else in the file. We check that `--local` appears in the
	// exec.CommandContext arglist (post-comment) AND that the phrase
	// "LOAD-BEARING COMMENT" appears before the first exec call.
	loadIdx := strings.Index(src, "LOAD-BEARING COMMENT")
	execIdx := strings.Index(src, "exec.CommandContext(ctx, oxPath")
	if loadIdx < 0 || execIdx < 0 || loadIdx > execIdx {
		t.Errorf("load-bearing comment must precede the exec.CommandContext call (loadIdx=%d execIdx=%d)", loadIdx, execIdx)
	}
}
