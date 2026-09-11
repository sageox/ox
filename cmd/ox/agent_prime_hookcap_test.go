package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wellFormed(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		require.NoError(t, err, "not well-formed XML:\n%s", doc)
	}
}

// renderFullPrime renders a realistic, over-cap prime the way a hook-driven
// Claude Code session would see it before trimming.
func renderFullPrime(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	out := agentPrimeOutput{
		AgentID: "Oxcap1",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:   "team-1",
			TeamName: "Acme",
			TeamDocs: []teamdocs.TeamDoc{
				{Name: "adr-007.md", Title: "ADR-007", When: "retry logic", Path: "/t/docs/adr-007.md"},
				{Name: "adr-012.md", Title: "ADR-012", When: "config loading", Path: "/t/docs/adr-012.md"},
			},
			TeamRules: []teamdocs.TeamRule{
				{Name: "retry-policy", Description: "how clients retry", Visibility: "always", AbsPath: "/t/agents/rules/retry-policy.md", Body: strings.Repeat("Retry with capped exponential backoff, max 3 attempts. ", 20)},
			},
			MemoryContent: strings.Repeat("- team memory line\n", 20),
		},
	}
	_, err := outputAgentPrimeXML(cmd, out)
	require.NoError(t, err)
	return buf.String()
}

// Failure prevented: Claude Code persisting the whole prime to a file and
// injecting a 2 KB preview — the coworker starts with "Team rules I can see:
// none" (eval case 09, 2026-09-11, 3 of 3 runs) while every delivery test
// stays green.
func TestFitPrimeToHookCap_FitsAndKeepsWhatMattersMost(t *testing.T) {
	full := renderFullPrime(t)
	require.Greater(t, len(full), primeHookBudget, "fixture must be over the cap to exercise the trimmer")
	wellFormed(t, full)

	trimmed, deferred := fitPrimeToHookCap(full, primeHookBudget, "/cache/prime/Oxcap1-full.xml")

	assert.LessOrEqual(t, len(trimmed), primeHookBudget, "trimmed prime must fit the hook budget")
	assert.Less(t, len(trimmed), claudeHookOutputCap, "and stay under the host cap")
	wellFormed(t, trimmed)
	assert.NotEmpty(t, deferred)

	// the sections a coworker needs before its first action survive
	for _, keep := range []string{"<instructions>", "<consult-first>", "<team-knowledge>", "retry-policy"} {
		assert.Contains(t, trimmed, keep, "%s must survive the trim", keep)
	}
	// the pointer names what was left out and where the rest lives
	assert.Contains(t, trimmed, `<deferred path="/cache/prime/Oxcap1-full.xml"`)
	assert.Contains(t, trimmed, "</deferred>")
	for _, name := range deferred {
		assert.Contains(t, trimmed, "- "+name, "deferred section %s must be named in the pointer", name)
		assert.NotContains(t, trimmed, "<"+name+">", "deferred section %s must not also be emitted", name)
	}
	// low-priority reference material goes first
	assert.Contains(t, deferred, "context-budget")
	assert.Contains(t, deferred, "visualization-guidance")
}

func TestFitPrimeToHookCap_UnderBudgetIsUntouched(t *testing.T) {
	doc := "<ox-prime>\n\n<instructions>\nhi\n</instructions>\n\n</ox-prime>\n"
	out, deferred := fitPrimeToHookCap(doc, 10_000, "/x")
	assert.Equal(t, doc, out)
	assert.Nil(t, deferred)
}

// The scanner must only see top-level elements: a <rule> nested inside
// <team-knowledge> is not a section and must never be dropped on its own.
func TestTopLevelSections_IgnoresNestedElements(t *testing.T) {
	doc := "<ox-prime>\n\n<instructions>\nx\n</instructions>\n\n<team-knowledge>\n\n<team-rules>\n<rule name=\"a\">\nbody\n</rule>\n</team-rules>\n\n</team-knowledge>\n\n<context-budget total=\"1\">\nz\n</context-budget>\n\n</ox-prime>\n"
	var names []string
	for _, s := range topLevelSections(doc) {
		names = append(names, s.name)
		assert.Equal(t, "\n", doc[s.end-1:s.end], "section span must end on its closing newline")
	}
	assert.Equal(t, []string{"instructions", "team-knowledge", "context-budget"}, names)
}

// The renderer applies the cap only when told to (hook-driven Claude Code
// prime), writes the full bundle where the pointer says, and leaves direct
// invocations alone.
func TestOutputAgentPrimeXML_HookBudgetTrimsAndWritesFullBundle(t *testing.T) {
	fullPath := filepath.Join(t.TempDir(), "prime", "Oxcap1-full.xml")
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	out := agentPrimeOutput{
		AgentID:            "Oxcap1",
		Status:             "fresh",
		HookOutputBudget:   primeHookBudget,
		HookFullBundlePath: fullPath,
		TeamContext: &teamContextInfo{
			TeamID: "team-1", TeamName: "Acme",
			TeamRules:     []teamdocs.TeamRule{{Name: "retry-policy", Visibility: "always", AbsPath: "/t/r.md", Body: strings.Repeat("retry with backoff. ", 60)}},
			MemoryContent: strings.Repeat("- memory\n", 40),
		},
	}
	_, err := outputAgentPrimeXML(cmd, out)
	require.NoError(t, err)

	emitted := buf.String()
	assert.LessOrEqual(t, len(emitted), primeHookBudget)
	assert.Contains(t, emitted, "retry-policy")
	assert.Contains(t, emitted, "<deferred path=\""+fullPath+"\"")
	wellFormed(t, emitted)

	full, err := os.ReadFile(fullPath)
	require.NoError(t, err, "the full bundle must be written where the pointer says")
	assert.Greater(t, len(full), len(emitted))
	assert.Contains(t, string(full), "<context-budget", "the full bundle is the untrimmed prime")
	assert.NotContains(t, string(full), "<deferred", "the full bundle carries no pointer to itself")

	// no budget → no trim, no file
	buf.Reset()
	out.HookOutputBudget = 0
	out.HookFullBundlePath = filepath.Join(t.TempDir(), "never.xml")
	_, err = outputAgentPrimeXML(cmd, out)
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "<deferred")
	_, statErr := os.Stat(out.HookFullBundlePath)
	assert.True(t, os.IsNotExist(statErr), "a direct invocation must not write a bundle")
}
