package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: the docs catalog naming a file the coworker cannot open
// without a shell. Before dir= existed, a read-only agent walked the data
// directory to find the doc prime had just listed — five extra tool calls in
// the 2026-09-11 eval pilot (case 04, 25 calls with prime vs 20 without).
// <rule> and <team-commands> carry a full path per row; docs all share one
// directory, so it is emitted once and each row stays a bare name.
func TestOutputAgentPrimeXML_DocsCatalogCarriesAbsolutePath(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:      "team-1",
			TeamName:    "TestTeam",
			ReadCommand: "ox agent team-ctx",
			TeamDocs: []teamdocs.TeamDoc{
				{Name: "adr-012-config-precedence.md", Title: "ADR-012", When: "touching configuration loading", Path: "/data/teams/team-1/docs/adr-012-config-precedence.md"},
				{Name: "angle<bracket>.md", Title: "escaped", When: "a & b", Path: "/data/teams/team-1/docs/angle<bracket>.md"},
			},
		},
	}
	_, err := outputAgentPrimeXML(cmd, output)
	require.NoError(t, err)

	xml := buf.String()
	start := strings.Index(xml, "<docs ")
	end := strings.Index(xml, "</docs>")
	require.True(t, start >= 0 && end > start, "expected a <docs> block")
	docs := xml[start:end]

	assert.Contains(t, docs, `<docs dir="/data/teams/team-1/docs"`, "the catalog must carry the docs directory once")
	assert.Contains(t, docs, "dir/Name", "the hint must tell the agent how to compose the readable path")
	assert.Contains(t, docs, "| adr-012-config-precedence.md | touching configuration loading |",
		"each row carries the name the agent joins to dir")
	assert.NotContains(t, docs, "angle<bracket>", "team-authored names must be XML-escaped")
	assert.Contains(t, docs, "a &amp; b", "team-authored hints must be XML-escaped")
}

// The --text renderer feeds hookless agents; it needs the same path.
func TestOutputAgentPrimeText_DocsCatalogCarriesPath(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		TeamContext: &teamContextInfo{
			TeamID:   "team-1",
			TeamName: "TestTeam",
			TeamDocs: []teamdocs.TeamDoc{
				{Name: "onboarding.md", Title: "Onboarding", When: "first week", Path: "/data/teams/team-1/docs/onboarding.md"},
			},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))
	assert.Contains(t, buf.String(), "Path: /data/teams/team-1/docs/onboarding.md")
}
