package main

import (
	"bytes"
	"testing"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The --text rendering of team content had no test at all, so the team
// commands, coworkers, and docs tables could change shape — or state something
// false — without any gate noticing. That is how the emitter came to tell every
// agent "Invoke commands via slash prefix (e.g., /deploy)" while ox installs no
// slash command for team commands at all.

// TestOutputAgentPrimeText_TeamCommands_PathNotSlashClaim pins the corrected
// contract: the table carries a Path the agent can actually open, and the
// emitter never promises a host slash command ox did not install.
func TestOutputAgentPrimeText_TeamCommands_PathNotSlashClaim(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:   "team-1",
			TeamName: "TestTeam",
			CoworkerCommands: []claude.Command{
				{
					Name:        "deploy",
					Trigger:     "/deploy",
					Description: "Ship to production.",
					Path:        "/team/agents/commands/deploy.md",
				},
				// no Description — exercises the "(no description)" fallback
				{Name: "review", Trigger: "/review", Path: "/team/agents/commands/review.md"},
			},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))

	got := buf.String()
	assert.Contains(t, got, "## Team Commands")
	assert.Contains(t, got, "| Command | Trigger | Description | Path |",
		"table must carry a Path column — without it the catalog is unusable")
	assert.Contains(t, got, "/team/agents/commands/deploy.md")
	assert.Contains(t, got, "(no description)", "commands with no description must still render")
	assert.Contains(t, got, "ox does not install these as slash commands")
	assert.NotContains(t, got, "Invoke commands via slash prefix",
		"ox installs no slash command for team commands; never tell the agent to invoke one")
}

// TestOutputAgentPrimeText_CoworkersAndDocs covers the sibling team sections in
// --text mode, including the index-path hint and the description fallbacks.
func TestOutputAgentPrimeText_CoworkersAndDocs(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:          "team-1",
			TeamName:        "TestTeam",
			AgentsIndexPath: "/team/agents/profiles/index.md",
			Coworkers: []claude.Agent{
				{Name: "postgres-pro", Description: "Postgres expert"},
				{Name: "no-desc"}, // exercises the "(no description)" fallback
			},
			TeamDocs: []teamdocs.TeamDoc{
				{Name: "onboarding", Title: "Onboarding guide", When: "new teammate joins"},
				{Name: "bare"}, // no Title/When — title falls back to Name
			},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))

	got := buf.String()
	assert.Contains(t, got, "## Expert Coworkers")
	assert.Contains(t, got, "ox coworker load <name>")
	assert.Contains(t, got, "/team/agents/profiles/index.md", "the catalog path hint must be emitted")
	assert.Contains(t, got, "postgres-pro")
	assert.Contains(t, got, "(no description)")
	assert.Contains(t, got, "Team Docs")
	assert.Contains(t, got, "onboarding")
	assert.Contains(t, got, "new teammate joins")
	assert.Contains(t, got, "bare")
}
