package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultMurmurWindowHours is the default rolling sparse checkout window.
const DefaultMurmurWindowHours = 12

// MurmurFile is the JSON schema for a murmur file stored in the ledger.
type MurmurFile struct {
	SchemaVersion string            `json:"schema_version"`           // "1"
	ID            string            `json:"id"`                       // UUIDv7
	Timestamp     time.Time         `json:"timestamp"`                // when the murmur was created
	AgentID       string            `json:"agent_id,omitempty"`       // which agent instance
	AgentType     string            `json:"agent_type,omitempty"`     // "claude-code", etc.
	PrincipalID   string            `json:"principal_id,omitempty"`   // who the agent works for
	PrincipalType string            `json:"principal_type,omitempty"` // "human" for now
	Topic         string            `json:"topic"`                    // freeform slug
	Importance    string            `json:"importance"`               // "critical", "normal", "ambient"
	Content       string            `json:"content"`                  // the spoken message
	Metadata      map[string]string `json:"metadata,omitempty"`       // optional structured context
	Tags          []string          `json:"tags,omitempty"`
	Scope         string            `json:"scope,omitempty"` // "ledger" or "team" (informational)
}

// MurmurDateHourDir returns the relative directory path for murmurs at a given time.
// Format: data/murmurs/YYYY-MM-DD/HH/
func MurmurDateHourDir(t time.Time) string {
	return filepath.Join("data", "murmurs", t.Format("2006-01-02"), fmt.Sprintf("%02d", t.Hour()))
}

// MurmurFilePath returns the relative path for a murmur file.
// Format: data/murmurs/YYYY-MM-DD/HH/<id>.json
func MurmurFilePath(t time.Time, id string) string {
	return filepath.Join(MurmurDateHourDir(t), id+".json")
}

// ComputeMurmurDataPaths generates sparse checkout paths for the last N hours.
// Returns paths like "data/murmurs/2026-03-22/14/" for each hour in the window.
// Correctly handles date boundary crossing (e.g., window spanning midnight).
func ComputeMurmurDataPaths(hours int) []string {
	now := time.Now().UTC()
	seen := make(map[string]bool)
	var paths []string

	for i := 0; i < hours; i++ {
		t := now.Add(-time.Duration(i) * time.Hour)
		p := MurmurDateHourDir(t) + "/"
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	return paths
}

// WriteMurmur writes a murmur file to the given base directory.
// Creates the hourly partition directory if needed. Returns the relative path
// of the written file within baseDir.
func WriteMurmur(baseDir string, m MurmurFile) (string, error) {
	if m.SchemaVersion == "" {
		m.SchemaVersion = "1"
	}

	relPath := MurmurFilePath(m.Timestamp, m.ID)
	fullPath := filepath.Join(baseDir, relPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("create murmur dir: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal murmur: %w", err)
	}

	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write murmur: %w", err)
	}

	return relPath, nil
}

// ReadMurmursInWindow reads all murmur files within the given time window.
// Skips invalid JSON files without error (best-effort).
// baseDir is the root of the ledger or team context checkout.
func ReadMurmursInWindow(baseDir string, windowHours int) ([]MurmurFile, error) {
	now := time.Now().UTC()
	var murmurs []MurmurFile

	for i := 0; i < windowHours; i++ {
		t := now.Add(-time.Duration(i) * time.Hour)
		dir := filepath.Join(baseDir, MurmurDateHourDir(t))

		entries, err := os.ReadDir(dir)
		if err != nil {
			// missing or unreadable directories are not fatal
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}

			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}

			var m MurmurFile
			if err := json.Unmarshal(data, &m); err != nil {
				continue // skip invalid JSON
			}

			murmurs = append(murmurs, m)
		}
	}

	return murmurs, nil
}
