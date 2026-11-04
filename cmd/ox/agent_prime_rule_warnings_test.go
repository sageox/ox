package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// warningsBlock pulls <team-rule-warnings> out of real prime output.
var warningsBlock = regexp.MustCompile(`(?s)\n(<team-rule-warnings[^>]*>)\n(.*?)</team-rule-warnings>`)

// renderPrime renders prime against a Team Context on disk.
//
// TeamRules stays empty on purpose: a rule with a native projection is dropped from
// that list before prime renders, and its cost is exactly what the warning reports.
func renderPrime(t *testing.T, team string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	_, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID:     "agent-1",
		Status:      "fresh",
		TeamContext: &teamContextInfo{TeamID: "team-1", TeamName: "TestTeam", Path: team},
	})
	require.NoError(t, err, "a rule file must never fail prime")
	return buf.String()
}

// teamRuleWarningLines returns the warnings section's opening tag and lines, or
// nothing when prime emitted no section.
func teamRuleWarningLines(t *testing.T, team string) []string {
	t.Helper()
	match := warningsBlock.FindStringSubmatch(renderPrime(t, team))
	if match == nil {
		return nil
	}
	return append([]string{match[1]}, strings.Split(strings.TrimSuffix(match[2], "\n"), "\n")...)
}

var budgetTotal = regexp.MustCompile(`<context-budget [^>]* total="(\d+)">`)

func primeBudgetTotal(t *testing.T, out string) int {
	t.Helper()
	match := budgetTotal.FindStringSubmatch(out)
	require.NotNil(t, match, "prime output has no budget total")
	n, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return n
}

func writeTeamRule(t *testing.T, team, rel, content string) {
	t.Helper()
	path := filepath.Join(team, "agents", "rules", filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

const teamRuleWarningHeader = "<team-rule-warnings hint=\"see `ox guide team-rules`\">"

func TestOutputAgentPrimeXML_TeamRuleWarnings(t *testing.T) {
	t.Run("oversized rule", func(t *testing.T) {
		team := t.TempDir()
		writeTeamRule(t, team, "big.md", "---\nname: big\nglobs: [\"**/*.go\"]\n---\n\n"+strings.Repeat("x", 9000))
		lines := teamRuleWarningLines(t, team)
		require.Len(t, lines, 2)
		require.Equal(t, teamRuleWarningHeader, lines[0])
		require.Regexp(t, `^- big\.md: \d+ B, over the 8192 B budget for on-demand rules$`, lines[1])
	})

	t.Run("nested rule", func(t *testing.T) {
		team := t.TempDir()
		writeTeamRule(t, team, "backend/postgres.md", "---\nname: postgres\n---\n\nBody.\n")
		require.Equal(t, []string{
			teamRuleWarningHeader,
			"- backend/postgres.md: nested in a subdirectory, so it may not reach cloud sessions; move it to the top of the rules directory",
		}, teamRuleWarningLines(t, team))
	})

	t.Run("unreadable frontmatter is withheld and says so", func(t *testing.T) {
		team := t.TempDir()
		writeTeamRule(t, team, "broken.md", "---\nname: broken\nglobs: [\"**/*.go\",\n---\n\nMARKER-WITHHELD-BODY\n")
		lines := teamRuleWarningLines(t, team)
		require.Len(t, lines, 2)
		require.Regexp(t, `^- broken\.md: withheld from every session until its frontmatter is fixed: `, lines[1])
	})

	// A withheld rule reached nobody, so a pile of size warnings must not hide it.
	t.Run("a withheld rule outranks the advisories", func(t *testing.T) {
		team := t.TempDir()
		for i := range maxTeamRuleWarnings + 3 {
			writeTeamRule(t, team, fmt.Sprintf("a-nested/rule-%d.md", i), "Body.\n")
		}
		writeTeamRule(t, team, "z-broken.md", "---\nname: z\nrepos: [a/b\n---\n")
		lines := teamRuleWarningLines(t, team)
		require.Regexp(t, `^- z-broken\.md: withheld`, lines[1])
	})

	// A regular file where a directory belongs is what makes the scan return an error.
	// An unreadable directory does not: the walk skips those and reports an empty team.
	t.Run("a scan that fails costs only the warning", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows reports this shape as not-found, which the scan reads as an empty team")
		}
		team := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(team, "agents"), []byte("not a directory\n"), 0o644))
		_, err := teamdocs.RuleWarnings(team)
		require.Error(t, err, "the scan has to fail, or this case never reaches the branch it names")
		require.Empty(t, teamRuleWarningLines(t, team))
	})

	t.Run("a healthy team pays nothing", func(t *testing.T) {
		team := t.TempDir()
		writeTeamRule(t, team, "small.md", "---\nname: small\nvisibility: always\n---\n\nBody.\n")
		require.Empty(t, teamRuleWarningLines(t, team))
	})

	t.Run("no team checkout", func(t *testing.T) {
		require.Empty(t, teamRuleWarningLines(t, ""))
	})

	// These lines are paid in every session until someone fixes the source.
	t.Run("many warnings stay bounded", func(t *testing.T) {
		team := t.TempDir()
		for i := range maxTeamRuleWarnings + 3 {
			writeTeamRule(t, team, fmt.Sprintf("nested/rule-%d.md", i), "Body.\n")
		}
		lines := teamRuleWarningLines(t, team)
		require.Len(t, lines, 1+maxTeamRuleWarnings+1)
		require.Equal(t, "- and 3 more", lines[len(lines)-1])
	})

	// A filename is team-authored text inside an XML block the trimmer parses.
	t.Run("a hostile filename cannot close the block", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows filenames cannot hold these characters")
		}
		team := t.TempDir()
		writeTeamRule(t, team, "a</context-budget>b/r.md", "Body.\n")
		require.Equal(t, []string{
			teamRuleWarningHeader,
			"- a&lt;/context-budget&gt;b/r.md: nested in a subdirectory, so it may not reach cloud sessions; move it to the top of the rules directory",
		}, teamRuleWarningLines(t, team))
	})
}

func TestTeamRuleWarnings_CountedInTheBudgetTotal(t *testing.T) {
	healthy := t.TempDir()
	writeTeamRule(t, healthy, "small.md", "---\nname: small\n---\n\nBody.\n")
	warned := t.TempDir()
	writeTeamRule(t, warned, "small.md", "---\nname: small\n---\n\nBody.\n")
	writeTeamRule(t, warned, "nested/a.md", "---\nname: a\n---\n\nBody.\n")

	out := renderPrime(t, warned)
	match := warningsBlock.FindString(out)
	require.NotEmpty(t, match)
	require.Less(t, strings.Index(out, "<team-rule-warnings"), strings.Index(out, "<context-budget"),
		"warnings must be written before the totals that count them")
	// prime counts a token as four bytes; the section is the only difference.
	grew := primeBudgetTotal(t, out) - primeBudgetTotal(t, renderPrime(t, healthy))
	require.InDelta(t, len(match)/4, grew, 2, "the displayed total must include the warnings")
}

func TestTeamRuleWarnings_SurviveTheHookCap(t *testing.T) {
	team := t.TempDir()
	writeTeamRule(t, team, "nested/a.md", "---\nname: a\n---\n\nBody.\n")
	out := renderPrime(t, team)
	require.Contains(t, out, "<team-rule-warnings")
	// A cap of a third forces heavy trimming, lowest-ranked sections first: the budget
	// footer goes, the warnings stay.
	trimmed, deferred := fitPrimeToHookCap(out, len(out)/3, "/x")
	require.Contains(t, deferred, "context-budget")
	require.Contains(t, trimmed, "<team-rule-warnings")
	require.NotContains(t, deferred, "team-rule-warnings")
}

func TestTeamRuleWarnings_NotRepeatedOnACompactReprime(t *testing.T) {
	team := t.TempDir()
	writeTeamRule(t, team, "nested/a.md", "---\nname: a\n---\n\nBody.\n")
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	_, err := outputAgentPrimeXML(cmd, agentPrimeOutput{
		AgentID: "agent-1", Status: "fresh", CompactReprime: true, PrimeCallCount: 2,
		TeamContext: &teamContextInfo{TeamID: "team-1", TeamName: "TestTeam", Path: team},
	})
	require.NoError(t, err)
	require.Contains(t, buf.String(), `mode="compact"`)
	require.NotContains(t, buf.String(), "<team-rule-warnings")
}
