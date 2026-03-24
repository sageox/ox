package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMurmurDateHourDir(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{
			name: "afternoon",
			time: time.Date(2026, 3, 22, 14, 30, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-03-22", "14"),
		},
		{
			name: "midnight",
			time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-01-01", "00"),
		},
		{
			name: "end of day",
			time: time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-12-31", "23"),
		},
		{
			name: "single digit month and day",
			time: time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC),
			want: filepath.Join("data", "murmurs", "2026-03-05", "09"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MurmurDateHourDir(tt.time)
			if got != tt.want {
				t.Errorf("MurmurDateHourDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMurmurFilePath(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		id   string
		want string
	}{
		{
			name: "standard path",
			time: time.Date(2026, 3, 22, 14, 0, 0, 0, time.UTC),
			id:   "01961234-5678-7abc-def0-123456789abc",
			want: filepath.Join("data", "murmurs", "2026-03-22", "14", "01961234-5678-7abc-def0-123456789abc.json"),
		},
		{
			name: "midnight boundary",
			time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			id:   "test-id",
			want: filepath.Join("data", "murmurs", "2026-01-01", "00", "test-id.json"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MurmurFilePath(tt.time, tt.id)
			if got != tt.want {
				t.Errorf("MurmurFilePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeMurmurDataPaths(t *testing.T) {
	t.Run("generates correct count", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(12)
		if len(paths) != 12 {
			t.Errorf("expected 12 paths, got %d", len(paths))
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(24)
		seen := make(map[string]bool)
		for _, p := range paths {
			if seen[p] {
				t.Errorf("duplicate path: %s", p)
			}
			seen[p] = true
		}
	})

	t.Run("all paths have trailing slash", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(6)
		for _, p := range paths {
			if !strings.HasSuffix(p, "/") {
				t.Errorf("path missing trailing slash: %s", p)
			}
		}
	})

	t.Run("all paths start with data/murmurs/", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(6)
		prefix := filepath.Join("data", "murmurs") + "/"
		for _, p := range paths {
			if !strings.HasPrefix(p, prefix) {
				t.Errorf("path %q does not start with %q", p, prefix)
			}
		}
	})

	t.Run("zero hours returns empty", func(t *testing.T) {
		paths := ComputeMurmurDataPaths(0)
		if len(paths) != 0 {
			t.Errorf("expected 0 paths, got %d", len(paths))
		}
	})
}

func TestComputeMurmurDataPaths_DateBoundaryCrossing(t *testing.T) {
	// Use a 25-hour window to guarantee spanning at least 2 calendar dates
	// regardless of what time the test runs (24h can land within a single
	// date if it runs near midnight UTC).
	paths := ComputeMurmurDataPaths(25)

	dates := make(map[string]bool)
	for _, p := range paths {
		// extract YYYY-MM-DD from "data/murmurs/YYYY-MM-DD/HH/"
		parts := strings.Split(p, "/")
		if len(parts) >= 4 {
			dates[parts[2]] = true
		}
	}

	// a 25-hour window must always span at least 2 calendar dates
	if len(dates) < 2 {
		t.Errorf("24-hour window should span at least 2 dates, got %d: %v", len(dates), dates)
	}
}

func TestWriteMurmur(t *testing.T) {
	t.Run("creates directory and file", func(t *testing.T) {
		baseDir := t.TempDir()
		ts := time.Date(2026, 3, 22, 14, 30, 0, 0, time.UTC)

		m := MurmurFile{
			SchemaVersion: "1",
			ID:            "test-murmur-001",
			Timestamp:     ts,
			AgentID:       "agent-123",
			AgentType:     "claude-code",
			Topic:         "architecture",
			Importance:    "normal",
			Content:       "This service should use connection pooling.",
		}

		relPath, err := WriteMurmur(baseDir, m)
		if err != nil {
			t.Fatalf("WriteMurmur: %v", err)
		}

		expectedRel := MurmurFilePath(ts, m.ID)
		if relPath != expectedRel {
			t.Errorf("relPath = %q, want %q", relPath, expectedRel)
		}

		// verify file exists and is valid JSON
		fullPath := filepath.Join(baseDir, relPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("read written file: %v", err)
		}

		var read MurmurFile
		if err := json.Unmarshal(data, &read); err != nil {
			t.Fatalf("unmarshal written file: %v", err)
		}

		if read.ID != m.ID {
			t.Errorf("ID = %q, want %q", read.ID, m.ID)
		}
		if read.Content != m.Content {
			t.Errorf("Content = %q, want %q", read.Content, m.Content)
		}
		if read.Topic != "architecture" {
			t.Errorf("Topic = %q, want %q", read.Topic, "architecture")
		}
	})

	t.Run("defaults schema version to 1", func(t *testing.T) {
		baseDir := t.TempDir()
		ts := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC)

		m := MurmurFile{
			ID:         "test-default-schema",
			Timestamp:  ts,
			Topic:      "test",
			Importance: "ambient",
			Content:    "hello",
			// SchemaVersion intentionally empty
		}

		relPath, err := WriteMurmur(baseDir, m)
		if err != nil {
			t.Fatalf("WriteMurmur: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(baseDir, relPath))
		if err != nil {
			t.Fatalf("read file: %v", err)
		}

		var read MurmurFile
		json.Unmarshal(data, &read)
		if read.SchemaVersion != "1" {
			t.Errorf("SchemaVersion = %q, want %q", read.SchemaVersion, "1")
		}
	})
}

func TestReadMurmursInWindow(t *testing.T) {
	t.Run("reads murmurs within window", func(t *testing.T) {
		baseDir := t.TempDir()
		now := time.Now().UTC()

		// write a murmur in the current hour
		m := MurmurFile{
			SchemaVersion: "1",
			ID:            "recent-murmur",
			Timestamp:     now,
			Topic:         "test",
			Importance:    "normal",
			Content:       "recent message",
		}
		if _, err := WriteMurmur(baseDir, m); err != nil {
			t.Fatalf("WriteMurmur: %v", err)
		}

		murmurs, err := ReadMurmursInWindow(baseDir, 1)
		if err != nil {
			t.Fatalf("ReadMurmursInWindow: %v", err)
		}

		if len(murmurs) != 1 {
			t.Fatalf("expected 1 murmur, got %d", len(murmurs))
		}
		if murmurs[0].ID != "recent-murmur" {
			t.Errorf("ID = %q, want %q", murmurs[0].ID, "recent-murmur")
		}
	})

	t.Run("excludes murmurs outside window", func(t *testing.T) {
		baseDir := t.TempDir()
		now := time.Now().UTC()

		// write a murmur 24 hours ago
		old := now.Add(-24 * time.Hour)
		m := MurmurFile{
			SchemaVersion: "1",
			ID:            "old-murmur",
			Timestamp:     old,
			Topic:         "test",
			Importance:    "normal",
			Content:       "old message",
		}
		if _, err := WriteMurmur(baseDir, m); err != nil {
			t.Fatalf("WriteMurmur: %v", err)
		}

		// read with 1 hour window — should not include the old murmur
		murmurs, err := ReadMurmursInWindow(baseDir, 1)
		if err != nil {
			t.Fatalf("ReadMurmursInWindow: %v", err)
		}

		if len(murmurs) != 0 {
			t.Errorf("expected 0 murmurs in 1-hour window, got %d", len(murmurs))
		}
	})

	t.Run("empty directories handled gracefully", func(t *testing.T) {
		baseDir := t.TempDir()

		murmurs, err := ReadMurmursInWindow(baseDir, 12)
		if err != nil {
			t.Fatalf("ReadMurmursInWindow: %v", err)
		}

		if len(murmurs) != 0 {
			t.Errorf("expected 0 murmurs, got %d", len(murmurs))
		}
	})

	t.Run("invalid JSON files are skipped", func(t *testing.T) {
		baseDir := t.TempDir()
		now := time.Now().UTC()

		// write a valid murmur
		valid := MurmurFile{
			SchemaVersion: "1",
			ID:            "valid-murmur",
			Timestamp:     now,
			Topic:         "test",
			Importance:    "normal",
			Content:       "valid",
		}
		if _, err := WriteMurmur(baseDir, valid); err != nil {
			t.Fatalf("WriteMurmur: %v", err)
		}

		// write an invalid JSON file in the same directory
		dir := filepath.Join(baseDir, MurmurDateHourDir(now))
		invalidPath := filepath.Join(dir, "bad-file.json")
		os.WriteFile(invalidPath, []byte("not valid json{{{"), 0o644)

		// write a non-JSON file (should also be skipped)
		txtPath := filepath.Join(dir, "notes.txt")
		os.WriteFile(txtPath, []byte("just a text file"), 0o644)

		murmurs, err := ReadMurmursInWindow(baseDir, 1)
		if err != nil {
			t.Fatalf("ReadMurmursInWindow: %v", err)
		}

		if len(murmurs) != 1 {
			t.Errorf("expected 1 valid murmur (skipping invalid), got %d", len(murmurs))
		}
		if len(murmurs) > 0 && murmurs[0].ID != "valid-murmur" {
			t.Errorf("ID = %q, want %q", murmurs[0].ID, "valid-murmur")
		}
	})
}

func TestMurmurRoundTrip(t *testing.T) {
	baseDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	original := MurmurFile{
		SchemaVersion: "1",
		ID:            "round-trip-test",
		Timestamp:     now,
		AgentID:       "agent-abc",
		AgentType:     "claude-code",
		PrincipalID:   "user-xyz",
		PrincipalType: "human",
		Topic:         "database-migration",
		Importance:    "critical",
		Content:       "Remember to add an index on the users table.",
		Metadata:      map[string]string{"table": "users", "column": "email"},
		Tags:          []string{"database", "performance"},
		Scope:         "ledger",
	}

	_, err := WriteMurmur(baseDir, original)
	if err != nil {
		t.Fatalf("WriteMurmur: %v", err)
	}

	murmurs, err := ReadMurmursInWindow(baseDir, 1)
	if err != nil {
		t.Fatalf("ReadMurmursInWindow: %v", err)
	}

	if len(murmurs) != 1 {
		t.Fatalf("expected 1 murmur, got %d", len(murmurs))
	}

	got := murmurs[0]
	if got.ID != original.ID {
		t.Errorf("ID = %q, want %q", got.ID, original.ID)
	}
	if got.SchemaVersion != "1" {
		t.Errorf("SchemaVersion = %q, want %q", got.SchemaVersion, "1")
	}
	if !got.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, original.Timestamp)
	}
	if got.AgentID != original.AgentID {
		t.Errorf("AgentID = %q, want %q", got.AgentID, original.AgentID)
	}
	if got.AgentType != original.AgentType {
		t.Errorf("AgentType = %q, want %q", got.AgentType, original.AgentType)
	}
	if got.PrincipalID != original.PrincipalID {
		t.Errorf("PrincipalID = %q, want %q", got.PrincipalID, original.PrincipalID)
	}
	if got.PrincipalType != original.PrincipalType {
		t.Errorf("PrincipalType = %q, want %q", got.PrincipalType, original.PrincipalType)
	}
	if got.Topic != original.Topic {
		t.Errorf("Topic = %q, want %q", got.Topic, original.Topic)
	}
	if got.Importance != original.Importance {
		t.Errorf("Importance = %q, want %q", got.Importance, original.Importance)
	}
	if got.Content != original.Content {
		t.Errorf("Content = %q, want %q", got.Content, original.Content)
	}
	if got.Scope != original.Scope {
		t.Errorf("Scope = %q, want %q", got.Scope, original.Scope)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "database" || got.Tags[1] != "performance" {
		t.Errorf("Tags = %v, want %v", got.Tags, original.Tags)
	}
	if got.Metadata["table"] != "users" || got.Metadata["column"] != "email" {
		t.Errorf("Metadata = %v, want %v", got.Metadata, original.Metadata)
	}
}

func TestMurmurOptionalFields(t *testing.T) {
	// verify optional fields are omitted from JSON when empty
	baseDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	minimal := MurmurFile{
		SchemaVersion: "1",
		ID:            "minimal-murmur",
		Timestamp:     now,
		Topic:         "test",
		Importance:    "ambient",
		Content:       "minimal message",
		// all optional fields left empty
	}

	relPath, err := WriteMurmur(baseDir, minimal)
	if err != nil {
		t.Fatalf("WriteMurmur: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(baseDir, relPath))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	// verify omitempty fields are absent from JSON
	jsonStr := string(data)
	for _, field := range []string{"agent_id", "agent_type", "principal_id", "principal_type", "metadata", "tags", "scope"} {
		if strings.Contains(jsonStr, fmt.Sprintf("%q", field)) {
			t.Errorf("expected field %q to be omitted from JSON when empty", field)
		}
	}

	// verify it still deserializes correctly
	var read MurmurFile
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if read.AgentID != "" {
		t.Errorf("AgentID should be empty, got %q", read.AgentID)
	}
	if read.Metadata != nil {
		t.Errorf("Metadata should be nil, got %v", read.Metadata)
	}
	if read.Tags != nil {
		t.Errorf("Tags should be nil, got %v", read.Tags)
	}
}

func TestWriteMurmur_MultipleInSameHour(t *testing.T) {
	baseDir := t.TempDir()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		m := MurmurFile{
			ID:         fmt.Sprintf("murmur-%d", i),
			Timestamp:  now,
			Topic:      "batch",
			Importance: "normal",
			Content:    fmt.Sprintf("message %d", i),
		}
		if _, err := WriteMurmur(baseDir, m); err != nil {
			t.Fatalf("WriteMurmur %d: %v", i, err)
		}
	}

	murmurs, err := ReadMurmursInWindow(baseDir, 1)
	if err != nil {
		t.Fatalf("ReadMurmursInWindow: %v", err)
	}

	if len(murmurs) != 3 {
		t.Errorf("expected 3 murmurs, got %d", len(murmurs))
	}
}

// --- Sparse checkout integration tests ---

func TestConfigureSparseCheckout_IncludesMurmurPaths(t *testing.T) {
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	outputStr := string(output)

	// base dirs must still be present
	for _, dir := range []string{".sync", "sessions", "audit"} {
		if !strings.Contains(outputStr, dir) {
			t.Errorf("sparse checkout missing base dir %q", dir)
		}
	}

	// current hour murmur path must be present
	now := time.Now().UTC()
	currentMurmurDir := MurmurDateHourDir(now)
	if !strings.Contains(outputStr, currentMurmurDir) {
		t.Errorf("sparse checkout missing current murmur hour path %q in output:\n%s", currentMurmurDir, outputStr)
	}

	// today's GitHub data path must still be present
	todayGitHub := fmt.Sprintf("data/github/%d/%02d/%02d", now.Year(), now.Month(), now.Day())
	if !strings.Contains(outputStr, todayGitHub) {
		t.Errorf("sparse checkout missing today's GitHub data path %q", todayGitHub)
	}
}

func TestConfigureSparseCheckout_MurmurPathCount(t *testing.T) {
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	murmurCount := 0
	for _, line := range lines {
		if strings.Contains(line, "data/murmurs/") {
			murmurCount++
		}
	}

	if murmurCount != DefaultMurmurWindowHours {
		t.Errorf("expected %d murmur paths in sparse checkout, got %d", DefaultMurmurWindowHours, murmurCount)
	}
}

func TestConfigureSparseCheckout_MurmurDoesNotBreakExisting(t *testing.T) {
	// verify that adding murmur paths doesn't remove or corrupt existing
	// sparse checkout entries (GitHub data, base dirs)
	tempDir := t.TempDir()

	initCmd := exec.Command("git", "init", tempDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	githubCount := 0
	for _, line := range lines {
		if strings.Contains(line, "data/github/") {
			githubCount++
		}
	}

	if githubCount != DefaultGitHubDataWindowDays {
		t.Errorf("expected %d GitHub data paths, got %d (murmur integration may have broken GitHub paths)", DefaultGitHubDataWindowDays, githubCount)
	}

	// total should be base dirs + GitHub + murmur
	expectedTotal := 3 + DefaultGitHubDataWindowDays + DefaultMurmurWindowHours
	if len(lines) != expectedTotal {
		t.Errorf("expected %d total sparse checkout entries (3 base + %d GitHub + %d murmur), got %d",
			expectedTotal, DefaultGitHubDataWindowDays, DefaultMurmurWindowHours, len(lines))
	}
}

func TestMostRecentMurmurTime(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()
	hourDir := filepath.Join(tmpDir, "data", "murmurs", now.Format("2006-01-02"), fmt.Sprintf("%02d", now.Hour()))
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write a murmur file
	m := MurmurFile{
		ID:        "test-1",
		Timestamp: now.Add(-2 * time.Second),
		AgentID:   "agent-1",
		Topic:     "test",
		Content:   "hello",
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(hourDir, "test-1.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// should find the murmur
	got := MostRecentMurmurTime(tmpDir, "agent-1")
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}

	// different agent should not find it
	got2 := MostRecentMurmurTime(tmpDir, "agent-2")
	if !got2.IsZero() {
		t.Fatal("expected zero time for different agent")
	}

	// empty dir should return zero
	got3 := MostRecentMurmurTime(t.TempDir(), "agent-1")
	if !got3.IsZero() {
		t.Fatal("expected zero time for empty dir")
	}
}

func TestDefaultMurmurWindowHours(t *testing.T) {
	if DefaultMurmurWindowHours != 12 {
		t.Errorf("DefaultMurmurWindowHours = %d, want 12", DefaultMurmurWindowHours)
	}
}
