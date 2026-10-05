package agentwork

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/claudesource"
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

// A hook capture was ownership-checked batch by batch as it was appended, so a
// dead recording whose native source can no longer be rechecked must still
// finalize: the check can never succeed again, and deferring it forever strands
// a validated transcript behind a retry loop.
func TestRecoverClaudeHookUncheckableNativeSourceFinalizesValidatedCapture(t *testing.T) {
	const nativeID = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, project string) string // returns the native source path recorded in the marker
	}{
		{"transcript pruned", func(t *testing.T, project string) string {
			return filepath.Join(t.TempDir(), "missing.jsonl")
		}},
		{"visited directory deleted", func(t *testing.T, project string) string {
			gone := filepath.Join(project, "build")
			if err := os.MkdirAll(gone, 0o755); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), nativeID+".jsonl")
			turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, gone)
			if err := os.WriteFile(source, []byte(turn), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(gone); err != nil {
				t.Fatal(err)
			}
			return source
		}},
		{"workspace archived", func(t *testing.T, project string) string {
			source := filepath.Join(t.TempDir(), nativeID+".jsonl")
			turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, project)
			if err := os.WriteFile(source, []byte(turn), 0o600); err != nil {
				t.Fatal(err)
			}
			return source
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := tt.setup(t, project)
			if tt.name == "workspace archived" {
				// the workspace directory is gone by the time the daemon looks
				if err := os.RemoveAll(project); err != nil {
					t.Fatal(err)
				}
			}
			sessionDir := t.TempDir()
			recPath := filepath.Join(sessionDir, recordingMarker)
			rawPath := filepath.Join(sessionDir, artifactRaw)
			captured := []byte(`{"type":"header","metadata":{}}` + "\n" + `{"type":"user","content":"captured prefix"}` + "\n")
			if err := os.WriteFile(rawPath, captured, 0o600); err != nil {
				t.Fatal(err)
			}
			writeRecordingState(t, recPath, session.RecordingState{
				AgentID: "OxUncheckable", AgentSessionID: nativeID, AdapterName: "claude-code", WatchMode: "hook",
				WorkspacePath: project, SessionFile: source,
			})
			recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath)
			if err != nil || !recovered {
				t.Fatalf("a validated capture must finalize when its native source cannot be rechecked, recovered=%v err=%v", recovered, err)
			}
			got, err := os.ReadFile(rawPath)
			if err != nil || !bytes.HasPrefix(got, captured) {
				t.Fatalf("finalizing must keep the captured prefix intact: %v", err)
			}
			marker, err := os.ReadFile(recPath)
			if err != nil || bytes.Contains(marker, []byte("source_rejected")) {
				t.Fatalf("an uncheckable source is not proof of a foreign turn and must not quarantine: %v", err)
			}
		})
	}
}

// Archiving the workspace (a Conductor worktree, a deleted checkout) must not
// strand a recording the daemon could finalize before: its turns stood inside
// the repo, and a turn that stood elsewhere is still foreign.
func TestRecoverClaudeFromAnArchivedWorkspace(t *testing.T) {
	adapters.Register(&claudeRecoveryTestAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	const nativeID = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	for _, tt := range []struct {
		name, watchMode string
		foreignTurn     bool
	}{
		{name: "hook recording that only holds a header", watchMode: "hook"},
		{name: "tail recording", watchMode: "tail"},
		{name: "tail recording with a turn from another repository", watchMode: "tail", foreignTurn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			outer, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			project := filepath.Join(outer, "archived-workspace")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			startedAt := time.Now().Add(-time.Hour)
			stamp := startedAt.Add(time.Minute).UTC().Format(time.RFC3339)
			cwd := project
			if tt.foreignTurn {
				cwd = outer
			}
			source := filepath.Join(home, ".claude", "projects", "bucket", nativeID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q,\"role\":\"user\",\"content\":\"worked in the archived workspace\",\"timestamp\":%q}\n", nativeID, cwd, stamp)
			if err := os.WriteFile(source, []byte(turn), 0o600); err != nil {
				t.Fatal(err)
			}
			sessionDir := t.TempDir()
			recPath := filepath.Join(sessionDir, recordingMarker)
			rawPath := filepath.Join(sessionDir, artifactRaw)
			header := `{"type":"header","metadata":{}}` + "\n"
			if err := os.WriteFile(rawPath, []byte(header), 0o600); err != nil {
				t.Fatal(err)
			}
			writeRecordingState(t, recPath, session.RecordingState{
				AgentID: "OxArchived", AgentSessionID: nativeID, AdapterName: "claude-code", WatchMode: tt.watchMode,
				WorkspacePath: project, SessionFile: source, StartedAt: startedAt,
			})
			if err := os.RemoveAll(project); err != nil {
				t.Fatal(err)
			}

			recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath)
			if tt.foreignTurn {
				if recovered || !errors.Is(err, claudesource.ErrUntrustedSource) {
					t.Fatalf("a turn from outside the archived workspace must be refused, recovered=%v err=%v", recovered, err)
				}
				return
			}
			if err != nil || !recovered {
				t.Fatalf("an archived workspace must not strand its recording, recovered=%v err=%v", recovered, err)
			}
			if got := countRawJSONLEntries(t, rawPath); got < 2 {
				t.Fatalf("the native turn must reach raw.jsonl, entries=%d", got)
			}
		})
	}
}

// A tail recording whose transcript is gone has nothing to drain. That is not a
// reason to discard the recording: it stays for a later pass, marker intact.
func TestRecoverClaudeTailRecordingWithAGoneTranscriptKeepsItsMarker(t *testing.T) {
	adapters.Register(&claudeRecoveryTestAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	source := filepath.Join(home, ".claude", "projects", "bucket", "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()
	recPath := filepath.Join(sessionDir, recordingMarker)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	header := []byte(`{"type":"header","metadata":{}}` + "\n")
	if err := os.WriteFile(rawPath, header, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecordingState(t, recPath, session.RecordingState{
		AgentID: "OxGone", AdapterName: "claude-code", WatchMode: "tail", WorkspacePath: project,
		SessionFile: source, StartedAt: time.Now().Add(-time.Hour),
	})
	recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath)
	if recovered || err == nil || !strings.Contains(err.Error(), "stat native session before recovery") {
		t.Fatalf("a gone transcript defers the recovery, recovered=%v err=%v", recovered, err)
	}
	if _, err := os.Stat(recPath); err != nil {
		t.Fatalf("the marker must survive: %v", err)
	}
}

// A hook recording whose state never named a native source still holds what its
// hooks validated; with nothing to recheck it finalizes that capture.
func TestRecoverClaudeHookCaptureWithNoRecordedSourceFinalizes(t *testing.T) {
	project := t.TempDir()
	sessionDir := t.TempDir()
	recPath := filepath.Join(sessionDir, recordingMarker)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	captured := []byte(`{"type":"header","metadata":{}}` + "\n" + `{"type":"user","content":"captured prefix"}` + "\n")
	if err := os.WriteFile(rawPath, captured, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecordingState(t, recPath, session.RecordingState{
		AgentID: "OxNoSource", AdapterName: "claude-code", WatchMode: "hook", WorkspacePath: project,
	})
	recovered, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath)
	if err != nil || !recovered {
		t.Fatalf("the validated capture must finalize, recovered=%v err=%v", recovered, err)
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
		// a header that names a repository is the opposite of one that names none
		{name: "hook prefix wrong repository", headerRepo: "repo-other", prefix: true},
		{name: "hook prefix names no repository", allowed: true, prefix: true},
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
