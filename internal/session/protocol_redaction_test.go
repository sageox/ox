package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// Every adapter converges on this protocol before persistence. Exercise its
// content surfaces once, independently of each adapter's native parser format.
func TestProtocolSessionRedactionAcrossPersistenceBoundaries(t *testing.T) {
	// Fabricated detector fixtures only; never derive these from real sessions.
	canaries := []struct{ name, value string }{
		{"AWS_ACCESS_KEY_ID", "AKIA" + "QWERTYUIOPASDFGH"},
		{"AWS_SECRET_ACCESS_KEY", strings.Repeat("aB3+", 10)},
		{"GEMINI_API_KEY", "AIza" + strings.Repeat("a", 35)},
		{"OPENAI_API_KEY", "sk-proj-" + strings.Repeat("a", 80)},
		{"OPENAI_LEGACY_KEY", "sk-" + strings.Repeat("a", 24) + "T3Blbk" + "FJ" + strings.Repeat("b", 24)},
		{"E2B_API_KEY", "e2b_" + strings.Repeat("a", 40)},
		{"MAILLAB_API_KEY", strings.Repeat("a1", 24)},
	}
	const marker = "benign-session-continuity-marker"
	parts := []string{marker}
	for _, canary := range canaries {
		parts = append(parts, canary.name+"="+canary.value)
	}
	payload := strings.Join(parts, "; ")
	nested, err := json.Marshal(map[string]any{"nested": []any{map[string]string{"content": payload}}})
	if err != nil {
		t.Fatal(err)
	}
	entries := []adapterprotocol.RawEntry{
		{Role: "user", Content: payload},
		{Role: "assistant", Content: payload},
		{Role: "system", Content: payload},
		{Role: "tool", ToolName: "fixture-read", ToolInput: string(nested), ToolOutput: payload, Content: payload, CallID: "fixture-call"},
	}
	newHistory := func() *session.CapturedHistory {
		return session.ConvertProtocolEntriesToHistory(entries, "OxSynthetic", "synthetic")
	}

	for _, mode := range []string{"live", "store_entries", "store_nested_maps", "import_history", "reconstructed_history"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "raw.jsonl")
			history := newHistory()
			switch mode {
			case "live":
				w, err := session.NewRawWriter(path, "")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = w.Close() })
				if err := w.WriteEntries(history.ToSessionEntries()); err != nil {
					t.Fatal(err)
				}
				if err := w.CloseAndSync(); err != nil {
					t.Fatal(err)
				}
			case "store_entries", "store_nested_maps":
				store, err := session.NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				w, err := store.CreateRaw("synthetic-import")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = w.Close() })
				for _, entry := range history.ToSessionEntries() {
					if mode == "store_entries" {
						err = w.WriteEntry(protocolWritableEntry{entry})
					} else {
						err = w.WriteRaw(map[string]any{"type": "tool", "data": map[string]any{"nested": []any{map[string]any{"entry": entry}}}})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				path = w.FilePath()
			case "import_history":
				if err := session.WriteHistoryJSONL(path, history); err != nil {
					t.Fatal(err)
				}
			case "reconstructed_history":
				if err := session.MergeHistoryWithRecording(dir, history); err != nil {
					t.Fatal(err)
				}
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range canaries {
				if strings.Contains(string(body), canary.value) {
					t.Errorf("synthetic %s credential survived persistence", canary.name)
				}
			}
			var persisted []session.SessionEntry
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				var item map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &item); err != nil {
					t.Fatal("persisted JSONL is invalid")
				}
				if item["_meta"] != nil || string(item["type"]) == `"header"` || string(item["type"]) == `"footer"` {
					continue
				}
				entryJSON := []byte(line)
				if mode == "store_entries" {
					entryJSON = item["data"]
				}
				if mode == "store_nested_maps" {
					var wrapper struct {
						Data struct {
							Nested []struct {
								Entry json.RawMessage `json:"entry"`
							} `json:"nested"`
						} `json:"data"`
					}
					if err := json.Unmarshal(entryJSON, &wrapper); err != nil || len(wrapper.Data.Nested) != 1 {
						t.Fatal("nested entry framing changed")
					}
					entryJSON = wrapper.Data.Nested[0].Entry
				}
				var entry session.SessionEntry
				if err := json.Unmarshal(entryJSON, &entry); err != nil {
					t.Fatal("persisted session entry is invalid")
				}
				persisted = append(persisted, entry)
			}
			if len(persisted) != len(entries) {
				t.Fatalf("persisted %d entries, want %d", len(persisted), len(entries))
			}
			for i, entry := range persisted {
				original := entries[i]
				if string(entry.Type) != original.Role || entry.ToolName != original.ToolName || entry.CallID != original.CallID {
					t.Errorf("entry %d lost its role or tool context", i)
				}
				for surface, text := range map[string]string{"content": entry.Content, "tool_input": entry.ToolInput, "tool_output": entry.ToolOutput} {
					if i != len(entries)-1 && surface != "content" {
						continue
					}
					if !strings.Contains(text, marker) || !strings.Contains(text, "[REDACTED") {
						t.Errorf("entry %d lost benign content or redaction evidence on %s", i, surface)
					}
				}
			}
		})
	}
}

type protocolWritableEntry struct{ session.SessionEntry }

func (e protocolWritableEntry) EntryType() string { return string(e.Type) }
