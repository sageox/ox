package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// contextBudgetBlock pulls the body of <context-budget> out of real prime output.
var contextBudgetBlock = regexp.MustCompile(`(?s)<context-budget[^>]*>\n(.*?)</context-budget>`)

// teamRuleWarningLines renders prime against a Team Context on disk and returns the
// lines the warnings added to <context-budget>.
//
// TeamRules stays empty on purpose: a rule with a native projection is dropped from
// that list before prime renders, and its cost is exactly what the warning reports.
func teamRuleWarningLines(t *testing.T, team string) []string {
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
	match := contextBudgetBlock.FindStringSubmatch(buf.String())
	require.NotNil(t, match, "prime output has no <context-budget> block")
	lines := strings.Split(strings.TrimSuffix(match[1], "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	return lines[2:] // the first two lines are the block's fixed explanation
}

func writeTeamRule(t *testing.T, team, rel, content string) {
	t.Helper()
	path := filepath.Join(team, "agents", "rules", filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

const teamRuleWarningHeader = "Team rule warnings (see `ox guide team-rules`):"

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
