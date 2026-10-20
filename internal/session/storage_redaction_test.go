package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These are fabricated detector fixtures, never credentials from a recording.
const storageSecretSentinel = "AKIAIOSFODNN7EXAMPLE"

type storageSensitiveEntry struct {
	Content    string `json:"content"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	Nested     struct {
		Output string `json:"output"`
	} `json:"nested"`
}

func (storageSensitiveEntry) EntryType() string { return "tool" }

func assertStorageRedacted(t *testing.T, path string, sentinels ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(string(data), sentinel) {
			t.Error("session artifact retained a supplied synthetic secret")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("session artifact mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStorageBoundaryRedactsStoreRecords(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.CreateRaw("synthetic-storage")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.WriteHeader(&StoreMeta{Version: "1.0", CreatedAt: time.Now(), Model: storageSecretSentinel}); err != nil {
		t.Fatal(err)
	}
	entry := storageSensitiveEntry{Content: storageSecretSentinel}
	entry.Nested.Output = storageSecretSentinel
	if err := w.WriteEntry(entry); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRaw(map[string]any{"type": "tool", "content": storageSecretSentinel, "tool_input": storageSecretSentinel, "tool_output": storageSecretSentinel}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	assertStorageRedacted(t, w.FilePath(), storageSecretSentinel)
}

func TestStorageBoundaryRedactsHistoryRecords(t *testing.T) {
	history := &CapturedHistory{
		Meta:    &HistoryMeta{SchemaVersion: "1", Source: "agent_reconstruction", AgentID: "synthetic", SessionTitle: storageSecretSentinel},
		Entries: []HistoryEntry{{Seq: 1, Type: "tool", Content: storageSecretSentinel, ToolInput: storageSecretSentinel, ToolOutput: storageSecretSentinel, Summary: storageSecretSentinel}},
	}
	t.Run("snapshot", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), historyFilename)
		if err := WriteHistoryJSONL(path, history); err != nil {
			t.Fatal(err)
		}
		assertStorageRedacted(t, path, storageSecretSentinel)
	})
	t.Run("merge", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, rawFilename)
		existing := "{\"type\":\"header\",\"metadata\":{\"model\":\"" + storageSecretSentinel + "\"}}\n{\"type\":\"user\",\"content\":\"" + storageSecretSentinel + "\",\"seq\":1}\n"
		if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
			t.Fatal(err)
		}
		if err := MergeHistoryWithRecording(dir, history); err != nil {
			t.Fatal(err)
		}
		assertStorageRedacted(t, path, storageSecretSentinel)
	})
}

func TestStorageBoundaryRedactsSave(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("synthetic-save", "raw", &StoreMeta{Model: storageSecretSentinel}, []map[string]any{{"type": "user", "content": storageSecretSentinel}}); err != nil {
		t.Fatal(err)
	}
	assertStorageRedacted(t, filepath.Join(store.BasePath(), "synthetic-save", rawFilename), storageSecretSentinel)
}

func TestStorageBoundaryRedactsSplitCredentialCommands(t *testing.T) {
	const opaque = "fabricated-opaque-credential-result"
	const helmSecret = "fabricated-helm-secret-that-has-no-provider-prefix"
	command := `{"command":["/bin/bash","-lc","gh auth token"]}`
	history := &CapturedHistory{
		Meta: &HistoryMeta{SchemaVersion: "1", Source: "agent_reconstruction", AgentID: "synthetic"},
		Entries: []HistoryEntry{
			{Seq: 1, Type: "tool", CallID: "synthetic-call", ToolInput: command},
			{Seq: 2, Type: "tool", CallID: "synthetic-call", ToolOutput: opaque, Content: opaque},
			{Seq: 3, Type: "tool", ToolOutput: "helm template --set-string api.env.AWS_SECRET_ACCESS_KEY=" + helmSecret},
		},
	}
	for _, mode := range []string{"store maps", "store typed entries", "history", "merged history"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, rawFilename)
			switch mode {
			case "store maps", "store typed entries":
				store, err := NewStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				w, err := store.CreateRaw("synthetic-commands")
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range history.Entries {
					if mode == "store maps" {
						err = w.WriteRaw(map[string]any{"type": entry.Type, "tool_input": entry.ToolInput, "tool_output": entry.ToolOutput, "content": entry.Content, "call_id": entry.CallID})
					} else {
						err = w.WriteEntry(storageSensitiveEntry{Content: entry.Content, ToolInput: entry.ToolInput, ToolOutput: entry.ToolOutput, CallID: entry.CallID})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				path = w.FilePath()
			case "history":
				if err := WriteHistoryJSONL(path, history); err != nil {
					t.Fatal(err)
				}
			case "merged history":
				if err := MergeHistoryWithRecording(dir, history); err != nil {
					t.Fatal(err)
				}
			}
			assertStorageRedacted(t, path, opaque, helmSecret)
		})
	}
}

func writeStoragePolicy(t *testing.T, policy string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte("```redact\n"+policy+"\n```\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStorageBoundaryAppliesProjectPolicy(t *testing.T) {
	const custom = "synthetic-project-private-value"
	root := writeStoragePolicy(t, "literal \""+custom+"\" -> [REDACTED_PROJECT]")
	t.Run("store", func(t *testing.T) {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		w, err := store.CreateRawForProject("synthetic", root)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteHeader(&StoreMeta{Model: custom}); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteEntry(storageSensitiveEntry{Content: custom}); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		assertStorageRedacted(t, w.FilePath(), custom)
	})
	history := &CapturedHistory{Meta: &HistoryMeta{SchemaVersion: "1", Source: "agent_reconstruction", AgentID: "synthetic", SessionTitle: custom}, Entries: []HistoryEntry{{Seq: 1, Type: "user", Content: custom, Summary: custom}}}
	t.Run("history", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), historyFilename)
		if err := WriteHistoryJSONLForProject(path, history, root); err != nil {
			t.Fatal(err)
		}
		assertStorageRedacted(t, path, custom)
		loaded, err := ParseHistoryFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Entries) != 1 || loaded.Entries[0].Seq != 1 || loaded.Entries[0].Type != "user" {
			t.Fatal("history root entry framing changed")
		}
	})
	t.Run("merged history", func(t *testing.T) {
		dir := t.TempDir()
		if err := MergeHistoryWithRecordingForProject(dir, history, root); err != nil {
			t.Fatal(err)
		}
		assertStorageRedacted(t, filepath.Join(dir, rawFilename), custom)
	})
}

func TestStorageBoundaryRejectsMalformedPolicy(t *testing.T) {
	root := writeStoragePolicy(t, "regex \"[\" -> [REDACTED_INVALID]")
	t.Run("store", func(t *testing.T) {
		store, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if w, err := store.CreateRawForProject("refused", root); err == nil {
			_ = w.Close()
			t.Fatal("invalid project policy accepted")
		}
		if _, err := os.Stat(filepath.Join(store.BasePath(), "refused", rawFilename)); !os.IsNotExist(err) {
			t.Fatal("invalid project policy created session content")
		}
	})
	for _, mode := range []string{"history", "merged history"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, rawFilename)
			const original = "{\"type\":\"user\",\"content\":\"unchanged\",\"seq\":1}\n"
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			history := &CapturedHistory{Meta: &HistoryMeta{}, Entries: []HistoryEntry{{Seq: 1, Type: "user", Content: storageSecretSentinel}}}
			var err error
			if mode == "history" {
				err = WriteHistoryJSONLForProject(path, history, root)
			} else {
				err = MergeHistoryWithRecordingForProject(dir, history, root)
			}
			if err == nil {
				t.Fatal("invalid project policy accepted")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatal("failed policy changed existing artifact")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 {
				t.Fatal("failed policy retained temporary artifact")
			}
		})
	}
}

func TestStorageBoundarySnapshotRollsBackEncodingFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, rawFilename)
	const original = "{\"type\":\"user\",\"content\":\"previous safe recording\"}\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeMergedRawJSONL(path, map[string]any{"type": "header", "metadata": map[string]any{"model": storageSecretSentinel}}, []map[string]any{{"content": storageSecretSentinel, "unsupported": make(chan int)}})
	if err == nil {
		t.Fatal("unsupported JSON value was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatal("failed encoding replaced the existing recording")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("failed encoding retained a temporary artifact")
	}
}

func TestStoreCapturedHistoryEnforcesProjectPolicy(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "new history"
		if active {
			name = "active recording"
		}
		t.Run(name, func(t *testing.T) {
			projectRoot := setupRecordingTest(t, t.TempDir())
			t.Chdir(projectRoot)
			const agentID = "OxSyntheticHistoryPolicy"
			if active {
				if _, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: agentID, AdapterName: "generic"}); err != nil {
					t.Fatal(err)
				}
			}
			const custom = "synthetic-import-private-value"
			policyPath := filepath.Join(projectRoot, ".sageox", "REDACT.md")
			policy := "```redact\nliteral \"" + custom + "\" -> [REDACTED_PROJECT]\n```\n"
			if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
				t.Fatal(err)
			}
			nestedDir := filepath.Join(projectRoot, "nested", "source")
			if err := os.MkdirAll(nestedDir, 0755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(nestedDir)
			history := validCursorGuardHistory(agentID)
			history.Entries[0].Content = custom
			path, err := StoreCapturedHistory(history, agentID, active)
			if err != nil {
				t.Fatal(err)
			}
			assertStorageRedacted(t, path, custom)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(policyPath, []byte("```redact\nregex \"[\" -> [REDACTED_INVALID]\n```\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := StoreCapturedHistory(history, agentID, active); err == nil {
				t.Fatal("capture-prior ignored a malformed project policy")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("rejected capture-prior changed existing content")
			}
		})
	}
}
