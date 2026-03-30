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

	// total should be base dirs (.sync, sessions, audit) + GitHub + murmur
	expectedTotal := 3 + DefaultGitHubDataWindowDays + DefaultMurmurWindowHours
	if len(lines) != expectedTotal {
		t.Errorf("expected %d total sparse checkout entries (3 base + %d GitHub + %d murmur), got %d",
			expectedTotal, DefaultGitHubDataWindowDays, DefaultMurmurWindowHours, len(lines))
	}
}

// Regression test: repeated ConfigureSparseCheckout must not delete untracked
// files under .sageox/cache/. Before the fix, "sparse-checkout init --cone"
// ran on every call and wiped the codedb directory.
func TestConfigureSparseCheckout_IdempotentPreservesSageoxCache(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// First call: initializes sparse checkout
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// Create a sentinel file simulating the codedb cache
	sentinel := filepath.Join(tempDir, ".sageox", "cache", "codedb", "sentinel.db")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatalf("mkdir sentinel dir: %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second call: must NOT destroy the sentinel
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel under .sageox/cache/codedb/ must survive repeated ConfigureSparseCheckout: %v", err)
	}
}

// Regression test: repeated ConfigureSparseCheckout must preserve ALL categories
// of local files — not just codedb. Users may have pending commits, bleve indexes,
// whisper DBs, or other local state. Before the fix, "sparse-checkout init --cone"
// ran on every 60s sync cycle and wiped everything outside the cone.
func TestConfigureSparseCheckout_PreservesAllLocalFileCategories(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// first call: initializes sparse checkout
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// create files across every known local-only category
	localFiles := map[string]string{
		// codedb SQLite + bleve indexes (nested)
		".sageox/cache/codedb/index.db":                     "sqlite-data",
		".sageox/cache/codedb/bleve/store/segment.vx":       "bleve-segment",
		".sageox/cache/codedb/bleve/index_meta.json":        "bleve-meta",
		// whisper cache
		".sageox/cache/whisper/whisper.db":                   "whisper-data",
		// github sync state
		".sageox/cache/github_sync/state.json":               "sync-state",
		// telemetry queue
		".sageox/cache/telemetry.jsonl":                      "event-data",
		// health metadata
		".sageox/cache/health.json":                          "health-data",
		// arbitrary user file outside .sageox (simulates pending commit)
		"user-notes.txt":                                     "user-data",
	}

	for relPath, content := range localFiles {
		absPath := filepath.Join(tempDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	// simulate 3 scheduler cycles (initial + 2 refreshes)
	for i := 2; i <= 4; i++ {
		if err := ConfigureSparseCheckout(tempDir); err != nil {
			t.Fatalf("ConfigureSparseCheckout call %d: %v", i, err)
		}
	}

	// every file must survive
	for relPath := range localFiles {
		absPath := filepath.Join(tempDir, relPath)
		if _, err := os.Stat(absPath); err != nil {
			t.Errorf("file %q destroyed by repeated ConfigureSparseCheckout: %v", relPath, err)
		}
	}
}

// Verify that sparse-checkout list output is identical whether init --cone runs
// (first call) or is skipped (subsequent calls). Catches regressions where the
// init guard inadvertently changes sparse behavior.
func TestConfigureSparseCheckout_IdempotentSparseSet(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// first call: runs init --cone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	firstOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after first call: %v", err)
	}

	// second call: skips init --cone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	secondOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after second call: %v", err)
	}

	if string(firstOutput) != string(secondOutput) {
		t.Errorf("sparse-checkout list changed between calls:\nfirst:\n%s\nsecond:\n%s",
			string(firstOutput), string(secondOutput))
	}

	// third call: verify stability beyond two calls
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("third ConfigureSparseCheckout: %v", err)
	}

	thirdOutput, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list after third call: %v", err)
	}

	if string(firstOutput) != string(thirdOutput) {
		t.Errorf("sparse-checkout list drifted after third call:\nfirst:\n%s\nthird:\n%s",
			string(firstOutput), string(thirdOutput))
	}
}

// Verify that .sageox/ is accessible after ConfigureSparseCheckout. The .sageox/
// directory hosts the gitignored cache/ tree; even though it's not in the cone dirs
// list, untracked/gitignored files must remain intact across sparse operations.
func TestConfigureSparseCheckout_SageoxCacheAccessible(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	// create .sageox/cache/ hierarchy (simulating what daemon creates at runtime)
	cacheDir := filepath.Join(tempDir, ".sageox", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir .sageox/cache: %v", err)
	}

	// verify we can create and read files in the cache directory
	testFile := filepath.Join(cacheDir, "test.json")
	if err := os.WriteFile(testFile, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("write to .sageox/cache after sparse checkout: %v", err)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read from .sageox/cache after sparse checkout: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("unexpected content in .sageox/cache file: %s", string(data))
	}
}

// Regression test: staged files in directories outside the computed cone must
// survive ConfigureSparseCheckout. Without the dirtyDirsOutsideCone guard,
// "sparse-checkout set" removes tracked files outside the cone from the working
// tree — destroying data the CLI has staged but not yet committed.
func TestConfigureSparseCheckout_PreservesStagedFilesOutsideCone(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", "-b", "main", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// configure git user for commits in test dir
	exec.Command("git", "-C", tempDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run()

	// first call: initialize sparse checkout + create initial commit so HEAD exists
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("first ConfigureSparseCheckout: %v", err)
	}

	// create and commit a file so HEAD is valid (required for staging to work)
	readmePath := filepath.Join(tempDir, "sessions", "README")
	if err := os.MkdirAll(filepath.Dir(readmePath), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(readmePath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	exec.Command("git", "-C", tempDir, "add", "--sparse", "sessions/README").Run()
	exec.Command("git", "-C", tempDir, "commit", "-m", "init").Run()

	// simulate CLI staging files in a directory NOT in the cone
	// (e.g., "data/linear/", "data/custom/", or any non-standard import path)
	customDir := filepath.Join(tempDir, "data", "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir data/custom: %v", err)
	}
	stagedFile := filepath.Join(customDir, "report.csv")
	stagedContent := "user,score\nalice,100\n"
	if err := os.WriteFile(stagedFile, []byte(stagedContent), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := exec.Command("git", "-C", tempDir, "add", "--sparse", "data/custom/report.csv").Run(); err != nil {
		t.Fatalf("git add --sparse: %v", err)
	}

	// second ConfigureSparseCheckout (simulating the 60s sync scheduler)
	// this calls "sparse-checkout set" which, without the fix, would delete
	// data/custom/report.csv from disk
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("second ConfigureSparseCheckout: %v", err)
	}

	// the staged file must survive on disk
	content, err := os.ReadFile(stagedFile)
	if err != nil {
		t.Fatalf("staged file data/custom/report.csv destroyed by ConfigureSparseCheckout: %v", err)
	}
	if string(content) != stagedContent {
		t.Errorf("staged file content changed: got %q, want %q", string(content), stagedContent)
	}
}

// Regression test: modified tracked files outside the rolling window must
// survive ConfigureSparseCheckout. A committed file in data/github/2025/01/01/
// (outside the 30-day window) that has local modifications should not be
// deleted from the working tree.
func TestConfigureSparseCheckout_PreservesModifiedFilesOutsideWindow(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", "-b", "main", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	exec.Command("git", "-C", tempDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run()

	// initialize sparse checkout with a wide cone that includes old data
	exec.Command("git", "-C", tempDir, "sparse-checkout", "init", "--cone").Run()
	exec.Command("git", "-C", tempDir, "sparse-checkout", "set", "sessions", "data/github/2025/01/01").Run()

	// create and commit a file in old data path
	oldDataDir := filepath.Join(tempDir, "data", "github", "2025", "01", "01")
	if err := os.MkdirAll(oldDataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oldFile := filepath.Join(oldDataDir, "prs.json")
	if err := os.WriteFile(oldFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sessDir := filepath.Join(tempDir, "sessions")
	os.MkdirAll(sessDir, 0o755)
	os.WriteFile(filepath.Join(sessDir, ".gitkeep"), []byte(""), 0o644)
	exec.Command("git", "-C", tempDir, "add", "--sparse", ".").Run()
	exec.Command("git", "-C", tempDir, "commit", "-m", "init with old data").Run()

	// modify the old data file (simulates user editing committed data)
	modifiedContent := "modified-by-user"
	if err := os.WriteFile(oldFile, []byte(modifiedContent), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	// ConfigureSparseCheckout won't include data/github/2025/01/01 (outside 30-day window),
	// but the modified file should be protected by dirtyDirsOutsideCone
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	content, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("modified file destroyed by ConfigureSparseCheckout: %v", err)
	}
	if string(content) != modifiedContent {
		t.Errorf("content changed: got %q, want %q", string(content), modifiedContent)
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
