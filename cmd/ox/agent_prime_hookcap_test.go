package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/prime"
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
	// Low-priority reference material goes first. Asserted as a rank boundary
	// rather than a fixed list of section names: this fixture sits ~1 KB over
	// budget, so which tail section loses the byte race shifts whenever prime
	// copy changes by a few dozen bytes, and the swap pass legitimately keeps a
	// small low-priority section it cannot trade for a larger high-priority one.
	// What must never shift is the boundary — nothing a coworker needs before
	// its first action may be deferred while reference material is kept.
	rank := make(map[string]int, len(hookCapSectionPriority))
	for i, name := range hookCapSectionPriority {
		rank[name] = i
	}
	rankOf := func(name string) int { // unlisted sections defer first, as in the trimmer
		if r, ok := rank[name]; ok {
			return r
		}
		return len(hookCapSectionPriority)
	}
	for _, name := range deferred {
		assert.Greater(t, rankOf(name), rankOf("commands"),
			"%s outranks the reference tier and must survive the trim", name)
	}
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

// A self-closing element (`<bulletin dir="…" hint="…"/>`) must open no
// section. openTag's attribute class admits the trailing slash, so before
// the guard the line was taken as an opening tag whose closing line never
// comes, and every later child of the split parent — team rules, indexed
// rules, memory — vanished from the trim candidates.
//
// Failure prevented: a hook-driven prime that has a bulletin board could not
// shed its team rules or memory under the hook cap, so a normal 14–24 KB
// prime would be cut by the host mid-document instead of trimmed by section.
func TestTopLevelSections_SelfClosingLineOpensNoSection(t *testing.T) {
	doc := "<ox-prime>\n\n<team-knowledge>\n\n<docs>\nx\n</docs>\n\n<bulletin dir=\"/t/bulletin/general/posts\" hint=\"notes, check expires_at\"/>\n\n<team-rules>\n<rule name=\"a\">\nbody\n</rule>\n</team-rules>\n\n<memory>\nm\n</memory>\n\n</team-knowledge>\n\n</ox-prime>\n"
	var names []string
	for _, s := range trimCandidates(doc) {
		names = append(names, s.name)
	}
	assert.Equal(t, []string{"team-knowledge/docs", "team-knowledge/team-rules", "team-knowledge/memory"}, names,
		"every child after the self-closing bulletin line must still be a trim candidate")
}

// Both bulletin pointers are self-closing and ride with the <team-knowledge>
// wrapper, so the trimmer never offers either as a candidate and never defers
// one. The github pointer adds ~0.5 KB that nothing can shed; this pins that a
// prime carrying both still fits the cap, keeps both pointers, and still trims
// the sections that follow them.
//
// Failure prevented: the github pointer being swallowed into a pseudo-section
// (or deferred) under the hook cap, so a hook-driven session never learns the
// board exists — or its extra bytes pushing a normal prime back over 10,000
// characters and returning to the 2 KB-preview failure.
func TestOutputAgentPrimeXML_HookBudgetKeepsBothBulletinPointers(t *testing.T) {
	fullPath := filepath.Join(t.TempDir(), "prime", "Oxcap2-full.xml")
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	out := agentPrimeOutput{
		AgentID:            "Oxcap2",
		Status:             "fresh",
		HookOutputBudget:   primeHookBudget,
		HookFullBundlePath: fullPath,
		TeamContext: &teamContextInfo{
			TeamID: "team-1", TeamName: "Acme",
			BulletinHint:  "/t/bulletin/general/posts",
			GitHubBoard:   &prime.GitHubBoardInfo{Dir: "/t/bulletin/github/posts", ThisRepo: "acme-api-*", Live: 12},
			TeamRules:     []teamdocs.TeamRule{{Name: "retry-policy", Visibility: "always", AbsPath: "/t/r.md", Body: strings.Repeat("retry with backoff. ", 60)}},
			MemoryContent: strings.Repeat("- memory\n", 40),
		},
	}
	for i := 0; i < 30; i++ {
		out.TeamContext.TeamDocs = append(out.TeamContext.TeamDocs, teamdocs.TeamDoc{Name: fmt.Sprintf("doc-%02d.md", i), Title: "Doc", When: strings.Repeat("when the upload service retries ", 3), Path: "/t/docs/x.md"})
	}
	_, err := outputAgentPrimeXML(cmd, out)
	require.NoError(t, err)

	emitted := buf.String()
	assert.LessOrEqual(t, len(emitted), primeHookBudget)
	require.Contains(t, emitted, "<deferred path=\"", "the fixture must be over the cap to exercise the trimmer")
	assert.Contains(t, emitted, `<bulletin dir="/t/bulletin/general/posts"`, "the general pointer must survive the trim")
	assert.Contains(t, emitted, `<bulletin board="github" dir="/t/bulletin/github/posts" this-repo="acme-api-*" live="12"`, "the github pointer must survive the trim")
	assert.Contains(t, emitted, "retry-policy", "the always-rule must survive the trim")
	wellFormed(t, emitted)

	var names []string
	for _, s := range trimCandidates(emitted) {
		names = append(names, s.name)
	}
	for _, n := range names {
		assert.NotContains(t, n, "bulletin", "a self-closing pointer is never a trim candidate")
	}
}

// TestFitPrimeToHookCap_KeepsTheHeldBackSkillReportOverCatalogs: the report is a
// defect notice — a team skill that was published and never arrived — so when
// the hook cap leaves room for only ONE of it and a catalog, the report wins.
// Removing the entry from hookCapSectionPriority leaves it unlisted, and an
// unlisted section loses every such contest, which is exactly the wrong end.
//
// The budget is calibrated from the trimmer's own accounting (everything
// deferred, plus what the catalog costs to keep) so the two sections genuinely
// compete: slack for both would not test the order, and slack for neither would
// not either.
func TestFitPrimeToHookCap_KeepsTheHeldBackSkillReportOverCatalogs(t *testing.T) {
	// several skills, so the section outweighs the <deferred> pointer's own
	// ~330-byte header; a one-skill report is smaller than the notice that
	// would replace it, and deferring it would not even save space.
	skills := make([]prime.WithheldSkill, 0, 6)
	for _, name := range []string{"deploy-prod", "rotate-keys", "restore-backup", "release-notes", "triage-oncall", "db-migrate"} {
		skills = append(skills, prime.WithheldSkill{Name: name, Reason: "withheld, the manifest itself needs approval: allowed-tools (SKILL.md)"})
	}
	var held strings.Builder
	emitWithheldTeamSkills(&held, newBookkeeper(&held), skills)
	docs := "\n<docs>\n" + strings.Repeat("a catalog row a coworker can Read on demand\n", 40) + "</docs>\n"
	require.Greater(t, len(docs), len(held.String()), "the catalog must be the larger section or the contest is not a contest")

	doc := "<ox-prime>\n\n<team-knowledge>\n" + held.String() + docs + "\n</team-knowledge>\n\n</ox-prime>\n"
	const path = "/cache/prime/Oxheld1-full.xml"

	// a zero budget defers every candidate, which is the document at its smallest
	// plus the pointer; keeping the catalog costs its span less its pointer line.
	allDeferred, _ := fitPrimeToHookCap(doc, 0, path)
	budget := len(allDeferred) + len(docs) - len(deferredLine("team-knowledge/docs"))

	trimmed, deferred := fitPrimeToHookCap(doc, budget, path)

	assert.Equal(t, []string{"team-knowledge/docs"}, deferred,
		"the catalog must be deferred before the held-back report")
	assert.Contains(t, trimmed, "<team-skills-held", "the held-back report lost the byte race to a catalog")
}

// TestHookCapDeferredHints_EveryDeferrableSectionHasOne: a deferred section is
// only worth a Read if its pointer line says when. The always-kept head of the
// list is exempt; everything after it is deferrable, so a new section added to
// hookCapSectionPriority without a hint is a pointer the agent has no reason to
// follow — the one that most needs it being a report of a skill that never
// arrived.
func TestHookCapDeferredHints_EveryDeferrableSectionHasOne(t *testing.T) {
	deferrable := false
	for _, name := range hookCapSectionPriority {
		if name == "consult-first" {
			deferrable = true // everything after the always-kept head
			continue
		}
		if !deferrable {
			continue
		}
		assert.NotEmpty(t, hookCapDeferredHints[name],
			"%s is deferrable but its <deferred> pointer line carries no hint", name)
	}
}
