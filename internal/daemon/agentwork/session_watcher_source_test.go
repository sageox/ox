package agentwork

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWatcherSource_RejectsAppendedForeignTurn(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	aw := &activeWatcher{adapterName: "claude-code", projectRoot: repo, sessionFile: path}
	if err := validateWatcherSource(aw); err != nil {
		t.Fatalf("initial source: %v", err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(first+foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWatcherSource(aw); err == nil {
		t.Fatal("watcher accepted source after foreign turn")
	}
}

type appendForeignDuringCatchUpAdapter struct {
	*testAdapter
	foreignTurn string
}

func (a *appendForeignDuringCatchUpAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	entries, consumed, err := a.testAdapter.ReadFromOffset(path, offset)
	if err != nil {
		return nil, offset, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return nil, offset, err
	}
	_, writeErr := f.WriteString(a.foreignTurn)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, offset, writeErr
	}
	return entries, consumed, closeErr
}

func TestClaudeWatcherPreservesCursorAcrossOwnershipChanges(t *testing.T) {
	for _, scenario := range []string{"foreign-on-restart", "safe-catch-up", "foreign-during-catch-up", "foreign-during-live-poll"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "foreign-during-live-poll" && testing.Short() {
				t.Skip("short: live capture checks ownership on a polling interval")
			}
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			repo := t.TempDir()
			repo, err := filepath.EvalSymlinks(repo)
			require.NoError(t, err)
			const repoID = "repo_watcher_source"
			const endpoint = "https://test.sageox.ai"
			require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{RepoID: repoID, Endpoint: endpoint}))
			cache := filepath.Join(paths.LedgerSessionCacheBase(repoID, endpoint), "sessions", scenario)
			require.NoError(t, os.MkdirAll(cache, 0o700))
			const nativeID = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
			first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, repo)
			second := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, repo)
			foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, filepath.Dir(repo))
			mgr := newTestWatcherManager(t)
			source := filepath.Join(mgr.homeDir(), ".claude", "projects", "owned", nativeID+".jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
			data := first
			switch scenario {
			case "foreign-on-restart":
				data += foreign
			case "safe-catch-up", "foreign-during-catch-up":
				data += second
			}
			require.NoError(t, os.WriteFile(source, []byte(data), 0o600))
			state := session.RecordingState{
				AgentID: "OxWatcher", WorkspacePath: repo, SessionPath: cache,
				AdapterName: "claude-code", SessionFile: source, WatchMode: "tail",
				SourceOffset: int64(len(first)), ParentPID: os.Getpid(),
			}
			writeRecordingState(t, filepath.Join(cache, recordingMarker), state)
			var adapter adapters.Adapter = &testAdapter{name: "claude-code"}
			if scenario == "foreign-during-catch-up" {
				adapter = &appendForeignDuringCatchUpAdapter{testAdapter: &testAdapter{name: "claude-code"}, foreignTurn: foreign}
			}
			adapters.Register(adapter)
			t.Cleanup(func() {
				mgr.StopAll()
				adapters.Unregister("claude-code")
			})
			raw := filepath.Join(cache, "raw.jsonl")
			require.NoError(t, mgr.StartWatch(scenario, source, "claude-code", filepath.Dir(cache), cache))
			if scenario == "safe-catch-up" {
				require.Eventually(t, func() bool {
					updated, err := session.LoadRecordingStateForAgent(repo, state.AgentID)
					return err == nil && updated != nil && updated.SourceOffset == int64(len(data))
				}, 3*time.Second, 10*time.Millisecond)
				mgr.StopAll()
				assert.Equal(t, 1, countRawJSONLEntries(t, raw), "catch-up must only import the uncaptured turn")
				return
			}
			if scenario == "foreign-during-live-poll" {
				require.Eventually(t, func() bool { _, err := os.Stat(raw); return err == nil }, time.Second, 10*time.Millisecond)
				f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString(foreign)
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}
			require.Eventually(t, func() bool {
				updated, err := session.LoadRecordingStateForAgent(repo, state.AgentID)
				return err == nil && updated != nil && updated.SourceRejected
			}, 5*time.Second, 10*time.Millisecond)
			mgr.StopAll()
			updated, err := session.LoadRecordingStateForAgent(repo, state.AgentID)
			require.NoError(t, err)
			require.NotNil(t, updated)
			assert.Equal(t, int64(len(first)), updated.SourceOffset, "foreign turns must not advance the cursor")
			if contents, err := os.ReadFile(raw); err == nil {
				assert.Empty(t, contents, "foreign turns must not reach the raw capture")
			} else {
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}
