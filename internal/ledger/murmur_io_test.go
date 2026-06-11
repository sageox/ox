package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		_ = json.Unmarshal(data, &read)
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
		_ = os.WriteFile(invalidPath, []byte("not valid json{{{"), 0o644)

		// write a non-JSON file (should also be skipped)
		txtPath := filepath.Join(dir, "notes.txt")
		_ = os.WriteFile(txtPath, []byte("just a text file"), 0o644)

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
