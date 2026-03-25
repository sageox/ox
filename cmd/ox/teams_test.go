package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
