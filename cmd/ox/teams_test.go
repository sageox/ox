package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestTeamsOutputFormat(t *testing.T) {
	// verify JSON struct serializes correctly
	output := teamsOutput{
		PrimaryTeam: "team_abc",
		Teams: []teamEntry{
			{
				TeamID:   "team_abc",
				Name:     "My Team",
				Slug:     "my-team",
				Primary:  true,
				LastSync: "just now",
				Path:     "/tmp/teams/team_abc",
			},
			{
				TeamID:   "team_def",
				Name:     "Other Team",
				Slug:     "other-team",
				Primary:  false,
				LastSync: "2h ago",
				Path:     "/tmp/teams/team_def",
			},
		},
		Guidance: "Use 'ox agent team-ctx <slug>' to read a team's context.",
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		t.Fatalf("failed to encode: %v", err)
	}

	var decoded teamsOutput
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if decoded.PrimaryTeam != "team_abc" {
		t.Errorf("primary_team = %q, want %q", decoded.PrimaryTeam, "team_abc")
	}
	if len(decoded.Teams) != 2 {
		t.Fatalf("teams count = %d, want 2", len(decoded.Teams))
	}
	if !decoded.Teams[0].Primary {
		t.Error("first team should be primary")
	}
	if decoded.Teams[1].Primary {
		t.Error("second team should not be primary")
	}
	if decoded.Guidance == "" {
		t.Error("guidance should not be empty")
	}
}

func TestTeamsEntryFields(t *testing.T) {
	// verify all expected fields are present in JSON
	entry := teamEntry{
		TeamID:   "team_123",
		Name:     "Test Team",
		Slug:     "test-team",
		Primary:  true,
		LastSync: "5m ago",
		Path:     "/data/teams/team_123",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	jsonStr := string(data)
	for _, field := range []string{"team_id", "name", "slug", "primary", "last_sync", "path"} {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("JSON missing field %q: %s", field, jsonStr)
		}
	}

	// verify no endpoint or total fields
	for _, field := range []string{"endpoint", "total"} {
		if strings.Contains(jsonStr, field) {
			t.Errorf("JSON should not contain %q: %s", field, jsonStr)
		}
	}
}

// TestTeamsDeprecation_StderrNotice verifies that invoking `ox teams` writes
// a one-line deprecation notice to stderr pointing users at the canonical
// `ox kb list --type=team` form. The notice is the user's only signal that
// the alias is going away — a regression that drops it would silently let
// the command be removed in a future release without warning.
func TestTeamsDeprecation_StderrNotice(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	cmd := newTestTeamsCmd(&stdout, &stderr)

	// Invoke with --json so runTeams takes the structured path; we don't care
	// about the listing content here, just the notice channel.
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ox teams --json: %v", err)
	}

	if !strings.Contains(stderr.String(), "deprecated") {
		t.Errorf("stderr missing 'deprecated' notice; got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "ox kb list --type=team") {
		t.Errorf("stderr missing canonical replacement command; got: %q", stderr.String())
	}
	// The free-form notice line MUST NOT appear on stdout — that channel is
	// reserved for the listing (human or JSON). The JSON envelope's
	// `deprecated` field is intentionally additive and verified separately.
	if strings.Contains(stdout.String(), teamsDeprecationNotice) {
		t.Errorf("stdout MUST NOT contain the deprecation notice line (would corrupt human-readable consumers); got: %q", stdout.String())
	}
}

// TestTeamsDeprecation_JSONEnvelope verifies the JSON envelope carries a
// `deprecated` field with a stable explanatory string. Tooling that already
// parses `ox teams --json` can detect the alias programmatically without
// scraping stderr.
//
// Failure prevented: a regression that drops the JSON `deprecated` field
// leaves CI scripts unable to detect when they need to migrate to
// `ox kb list --type=team --json`.
func TestTeamsDeprecation_JSONEnvelope(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	cmd := newTestTeamsCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ox teams --json: %v", err)
	}

	var got teamsOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, stdout.String())
	}
	if got.Deprecated == "" {
		t.Errorf("JSON envelope missing `deprecated` field; got: %s", stdout.String())
	}
	if !strings.Contains(got.Deprecated, "ox kb list --type=team") {
		t.Errorf("JSON `deprecated` field missing canonical replacement; got %q", got.Deprecated)
	}
}

// newTestTeamsCmd reconstructs the teamsCmd in isolation so tests can
// capture stdout/stderr without colliding with global state. It mirrors
// the production wiring in init() — flag plus RunE.
func newTestTeamsCmd(stdout, stderr *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "teams",
		RunE: runTeams,
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

func TestTeamsPrimarySorting(t *testing.T) {
	// verify that primary team would be sorted first
	entries := []teamEntry{
		{TeamID: "team_b", Name: "Beta", Primary: false},
		{TeamID: "team_a", Name: "Alpha", Primary: true},
		{TeamID: "team_c", Name: "Gamma", Primary: false},
	}

	// simulate the sorting logic from runTeams
	var primary, others []teamEntry
	for _, e := range entries {
		if e.Primary {
			primary = append(primary, e)
		} else {
			others = append(others, e)
		}
	}
	sorted := append(primary, others...)

	if !sorted[0].Primary {
		t.Error("primary team should be first after sorting")
	}
	if sorted[0].TeamID != "team_a" {
		t.Errorf("first entry = %q, want %q", sorted[0].TeamID, "team_a")
	}
}
