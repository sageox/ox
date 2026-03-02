package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanPendingObservations_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	obsDir := filepath.Join(tmpDir, "memory", ".observations")

	obs, counts, err := scanPendingObservations(obsDir, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("expected 0 observations, got %d", len(obs))
	}
	if len(counts) != 0 {
		t.Errorf("expected 0 day counts, got %d", len(counts))
	}
}

func TestScanPendingObservations_WithFiles(t *testing.T) {
	tmpDir := t.TempDir()
	obsDir := filepath.Join(tmpDir, "memory", ".observations")
	dayDir := filepath.Join(obsDir, "2026-03-01")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
{"content":"first observation"}
{"content":"second observation"}
`
	if err := os.WriteFile(filepath.Join(dayDir, "test.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	obs, counts, err := scanPendingObservations(obsDir, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obs))
	}
	if counts["2026-03-01"] != 2 {
		t.Errorf("expected 2 for 2026-03-01, got %d", counts["2026-03-01"])
	}
	if obs[0].Content != "first observation" {
		t.Errorf("first content: got %q", obs[0].Content)
	}
}

func TestScanPendingObservations_SinceFilter(t *testing.T) {
	tmpDir := t.TempDir()
	obsDir := filepath.Join(tmpDir, "memory", ".observations")
	dayDir := filepath.Join(obsDir, "2026-02-28")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"schema_version":"1","recorded_at":"2026-02-28T12:00:00Z"}
{"content":"old observation"}
`
	if err := os.WriteFile(filepath.Join(dayDir, "old.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	since, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	obs, _, err := scanPendingObservations(obsDir, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("expected 0 observations after since filter, got %d", len(obs))
	}
}

func TestScanPendingObservations_MultipleDays(t *testing.T) {
	tmpDir := t.TempDir()
	obsDir := filepath.Join(tmpDir, "memory", ".observations")

	for _, day := range []string{"2026-02-28", "2026-03-01"} {
		dayDir := filepath.Join(obsDir, day)
		if err := os.MkdirAll(dayDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"schema_version":"1","recorded_at":"` + day + `T12:00:00Z"}
{"content":"obs from ` + day + `"}
`
		if err := os.WriteFile(filepath.Join(dayDir, "test.jsonl"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	obs, counts, err := scanPendingObservations(obsDir, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obs))
	}
	if len(counts) != 2 {
		t.Errorf("expected 2 day counts, got %d", len(counts))
	}
	// should be sorted chronologically
	if obs[0].Content != "obs from 2026-02-28" {
		t.Errorf("expected oldest first, got %q", obs[0].Content)
	}
}

func TestDistillState_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &distillState{
		SchemaVersion:    "1",
		LastDistilled:    "2026-03-01T12:00:00Z",
		ObservationCount: 42,
		TeamID:           "team_test",
	}

	if err := saveDistillState(tmpDir, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadDistillState(tmpDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.SchemaVersion != state.SchemaVersion {
		t.Errorf("schema_version: got %s, want %s", loaded.SchemaVersion, state.SchemaVersion)
	}
	if loaded.LastDistilled != state.LastDistilled {
		t.Errorf("last_distilled: got %s, want %s", loaded.LastDistilled, state.LastDistilled)
	}
	if loaded.ObservationCount != state.ObservationCount {
		t.Errorf("observation_count: got %d, want %d", loaded.ObservationCount, state.ObservationCount)
	}
	if loaded.TeamID != state.TeamID {
		t.Errorf("team_id: got %s, want %s", loaded.TeamID, state.TeamID)
	}
}

func TestParseObservationFile_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.jsonl")

	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
{"content":"observation one"}
{"content":"observation two"}
`
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	obs, err := parseObservationFile(filePath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obs))
	}
	if obs[0].Content != "observation one" {
		t.Errorf("first content: got %q", obs[0].Content)
	}
}

func TestParseObservationFile_SkipsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.jsonl")

	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
not json
{"content":"valid observation"}
{"no_content_field": true}
`
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	obs, err := parseObservationFile(filePath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 1 {
		t.Errorf("expected 1 valid observation, got %d", len(obs))
	}
}

func TestParseObservationFile_SinceSkipsFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.jsonl")

	content := `{"schema_version":"1","recorded_at":"2026-02-01T12:00:00Z"}
{"content":"old observation"}
`
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	since, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	obs, err := parseObservationFile(filePath, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("expected 0 observations (file before since), got %d", len(obs))
	}
}

func TestScanPendingObservations_SkipsNonDateDirs(t *testing.T) {
	tmpDir := t.TempDir()
	obsDir := filepath.Join(tmpDir, "memory", ".observations")

	// create a non-date directory
	if err := os.MkdirAll(filepath.Join(obsDir, "not-a-date"), 0o755); err != nil {
		t.Fatal(err)
	}
	// create a valid date directory
	dayDir := filepath.Join(obsDir, "2026-03-01")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
{"content":"valid"}
`
	if err := os.WriteFile(filepath.Join(dayDir, "test.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	obs, counts, err := scanPendingObservations(obsDir, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 1 {
		t.Errorf("expected 1 observation, got %d", len(obs))
	}
	if _, ok := counts["not-a-date"]; ok {
		t.Error("non-date directory should not appear in counts")
	}
}
