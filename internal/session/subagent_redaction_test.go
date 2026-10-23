package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Free-form completion summaries are shared session content too, even though
// the registry is not raw.jsonl and the surrounding fields are only metadata.
func TestSubagentSummaryCrossesMandatoryRedaction(t *testing.T) {
	dir := t.TempDir()
	registry, err := NewSubagentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "AKIA" + "QWERTYUIOPASDFGH"
	completed := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, summary := range []string{"benign-summary " + secret, "second benign-summary"} {
		if err := registry.Register(&SubagentSession{SubagentID: "OxTest", ParentSessionID: "OxParent", Summary: summary, EntryCount: 3, CompletedAt: completed}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, subagentsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Error("synthetic credential survived subagent summary persistence")
	}
	if !strings.Contains(string(data), "[REDACTED") {
		t.Error("redaction marker missing")
	}
	rows, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d completion records, want 2", len(rows))
	}
	for _, row := range rows {
		if !strings.Contains(row.Summary, "benign-summary") || row.SubagentID != "OxTest" || row.ParentSessionID != "OxParent" || row.EntryCount != 3 || !row.CompletedAt.Equal(completed) {
			t.Error("benign completion metadata did not survive")
		}
	}
	info, err := os.Stat(filepath.Join(dir, subagentsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Error("subagent completion registry must be private")
	}
}
