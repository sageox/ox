package agentwork

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

func TestRecoverUndiscoveredClaudeHookSourcePreservesMarker(t *testing.T) {
	project := t.TempDir()
	sessionDir := t.TempDir()
	recPath := filepath.Join(sessionDir, recordingMarker)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	header := []byte(`{"type":"header","metadata":{}}` + "\n")
	if err := os.WriteFile(rawPath, header, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecordingState(t, recPath, session.RecordingState{
		AgentID: "OxUndiscovered", AdapterName: "claude-code", WatchMode: "hook", WorkspacePath: project,
	})
	if recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath); recovered || err == nil {
		t.Fatalf("undiscovered native source must defer, recovered=%v err=%v", recovered, err)
	}
	if _, err := os.Stat(recPath); err != nil {
		t.Fatalf("uncertain source must retain marker: %v", err)
	}
	got, err := os.ReadFile(rawPath)
	if err != nil || !bytes.Equal(got, header) {
		t.Fatalf("uncertain source must retain cached header: %v", err)
	}
}

func TestRecoverPiWithoutWorkspacePathPreservesMarker(t *testing.T) {
	sessionDir := t.TempDir()
	recPath := filepath.Join(sessionDir, recordingMarker)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	writeRecordingState(t, recPath, session.RecordingState{
		AgentID: "OxPiLegacy", AdapterName: "pi", SessionFile: filepath.Join(t.TempDir(), "native.jsonl"),
	})
	if recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath); recovered || err == nil {
		t.Fatalf("unscoped Pi recovery must defer rather than guess, recovered=%v err=%v", recovered, err)
	}
	if _, err := os.Stat(recPath); err != nil {
		t.Fatalf("ownership failure must preserve recording marker: %v", err)
	}
}

type claudeRecoveryTestAdapter struct{ mockReadAdapter }

func (*claudeRecoveryTestAdapter) Name() string { return "claude-code" }

func TestRecoverClaudeWithoutWorkspacePathRequiresMatchingHeaderRepo(t *testing.T) {
	adapters.Register(&claudeRecoveryTestAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"repo-allowed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const nativeID = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	for _, tt := range []struct {
		name, headerRepo         string
		allowed, foreign, prefix bool
	}{
		{name: "matching header", headerRepo: "repo-allowed", allowed: true},
		{name: "foreign turn", headerRepo: "repo-allowed", foreign: true},
		{name: "hook prefix valid", headerRepo: "repo-allowed", allowed: true, prefix: true},
		{name: "hook prefix foreign turn", headerRepo: "repo-allowed", foreign: true, prefix: true},
		{name: "wrong repository", headerRepo: "repo-other"},
		{name: "missing repository"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessionDir := t.TempDir()
			recPath := filepath.Join(sessionDir, recordingMarker)
			rawPath := filepath.Join(sessionDir, artifactRaw)
			source := filepath.Join(t.TempDir(), nativeID+".jsonl")
			startedAt := time.Now().Add(-time.Hour)
			turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q,\"role\":\"user\",\"content\":\"safe\",\"timestamp\":%q}\n", nativeID, project, startedAt.Add(time.Minute).UTC().Format(time.RFC3339))
			if tt.foreign {
				turn += fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, filepath.Dir(project))
			}
			if err := os.WriteFile(source, []byte(turn), 0o600); err != nil {
				t.Fatal(err)
			}
			header := fmt.Sprintf("{\"type\":\"header\",\"metadata\":{\"repo_id\":%q}}\n", tt.headerRepo)
			originalRaw := header
			if tt.prefix {
				originalRaw += `{"type":"user","content":"captured prefix"}` + "\n"
			}
			if err := os.WriteFile(rawPath, []byte(originalRaw), 0o600); err != nil {
				t.Fatal(err)
			}
			writeRecordingState(t, recPath, session.RecordingState{
				AgentID: "OxLegacy", AgentSessionID: nativeID, AdapterName: "claude-code",
				SessionFile: source, StartedAt: startedAt,
			})
			recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath, project)
			if tt.allowed {
				wantEntries := 2 // header and recovered turn, or header and captured prefix
				if tt.prefix {
					// A validated hook prefix retains a footer carrying its stop boundary
					// when the stale recording marker is reclaimed.
					wantEntries++
				}
				if err != nil || !recovered || countRawJSONLEntries(t, rawPath) != wantEntries {
					t.Fatalf("expected safe recovery, recovered=%v err=%v", recovered, err)
				}
				return
			}
			if recovered || err == nil {
				t.Fatalf("must not import untrusted source, recovered=%v err=%v", recovered, err)
			}
			if tt.foreign {
				marker, readErr := os.ReadFile(recPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				var saved session.RecordingState
				decoder := json.NewDecoder(bytes.NewReader(marker))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&saved); err != nil || !saved.SourceRejected {
					t.Fatalf("foreign source must be quarantined without deleting marker, decode error=%v", err)
				}
				if again, retryErr := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath, project); again || retryErr == nil {
					t.Fatalf("quarantined source must not retry, recovered=%v err=%v", again, retryErr)
				}
			}
			contents, readErr := os.ReadFile(rawPath)
			if readErr != nil || string(contents) != originalRaw {
				t.Fatalf("failure must preserve captured header, read error=%v", readErr)
			}
		})
	}
}
