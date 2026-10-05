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
	if err := validateWatcherSource(aw, 0); err != nil {
		t.Fatalf("initial source: %v", err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(first+foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWatcherSource(aw, 0); err == nil {
		t.Fatal("watcher accepted source after foreign turn")
	}
}

// Turns before the recording began were never imported, so a directory visited
// then must not condemn a watcher restarted from its persisted start offset.
func TestValidateWatcherSource_ChecksOnlyWhatTheRecordingCouldHaveCaptured(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	before := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	recorded := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(before+recorded), 0o600); err != nil {
		t.Fatal(err)
	}
	aw := &activeWatcher{adapterName: "claude-code", projectRoot: repo, sessionFile: path}
	if err := validateWatcherSource(aw, 0); err == nil {
		t.Fatal("a recording that began at the top of the file owns every turn in it")
	}
	if err := validateWatcherSource(aw, int64(len(before))); err != nil {
		t.Fatalf("a directory visited before the recording began must not condemn it: %v", err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(before+recorded+foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWatcherSource(aw, int64(len(before))); err == nil {
		t.Fatal("a foreign turn inside the recorded range must still be refused")
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
				held, err := session.LoadQuarantinedRecordingsForAgent(repo, state.AgentID)
				return err == nil && len(held) == 1
			}, 5*time.Second, 10*time.Millisecond)
			mgr.StopAll()
			held, err := session.LoadQuarantinedRecordingsForAgent(repo, state.AgentID)
			require.NoError(t, err)
			require.Len(t, held, 1)
			updated := held[0]
			assert.Equal(t, int64(len(first)), updated.SourceOffset, "foreign turns must not advance the cursor")
			if contents, err := os.ReadFile(raw); err == nil {
				assert.Empty(t, contents, "foreign turns must not reach the raw capture")
			} else {
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

// A source that cannot be checked yet is not a source that crossed repositories.
// The watcher must keep its cursor and look again, not end capture until the
// daemon's slow restart pass.
func TestClaudeWatcherKeepsPollingAfterRetryableValidationError(t *testing.T) {
	if testing.Short() {
		t.Skip("short: live capture checks ownership on a polling interval")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	const repoID = "repo_watcher_retry"
	const endpoint = "https://test.sageox.ai"
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{RepoID: repoID, Endpoint: endpoint}))
	cache := filepath.Join(paths.LedgerSessionCacheBase(repoID, endpoint), "sessions", "retryable")
	require.NoError(t, os.MkdirAll(cache, 0o700))

	const nativeID = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, repo)
	// a path component that is a regular file cannot be resolved, which is
	// neither inside nor outside the repo: the turn is uncheckable for now
	blocker := filepath.Join(repo, "node")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory yet"), 0o600))
	second := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", nativeID, filepath.Join(blocker, "pkg"))
	mgr := newTestWatcherManager(t)
	source := filepath.Join(mgr.homeDir(), ".claude", "projects", "owned", nativeID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
	require.NoError(t, os.WriteFile(source, []byte(first), 0o600))
	state := session.RecordingState{
		AgentID: "OxWatcherRetry", WorkspacePath: repo, SessionPath: cache,
		AdapterName: "claude-code", SessionFile: source, WatchMode: "tail",
		SourceOffset: int64(len(first)), ParentPID: os.Getpid(),
	}
	writeRecordingState(t, filepath.Join(cache, recordingMarker), state)
	adapters.Register(&testAdapter{name: "claude-code"})
	t.Cleanup(func() {
		mgr.StopAll()
		adapters.Unregister("claude-code")
	})
	raw := filepath.Join(cache, "raw.jsonl")
	require.NoError(t, mgr.StartWatch("retryable", source, "claude-code", filepath.Dir(cache), cache))
	require.Eventually(t, func() bool { _, err := os.Stat(raw); return err == nil }, time.Second, 10*time.Millisecond)

	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(second)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	time.Sleep(pollInterval + 500*time.Millisecond) // at least one poll sees the uncheckable turn
	deferred, err := session.LoadRecordingStateForAgent(repo, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, deferred)
	assert.False(t, deferred.SourceRejected, "an uncheckable turn is not proof of a foreign one")
	assert.Equal(t, int64(len(first)), deferred.SourceOffset, "the cursor must wait at the uncheckable turn")

	// the directory becomes checkable; the same watcher must pick the turn up
	require.NoError(t, os.Remove(blocker))
	require.NoError(t, os.MkdirAll(blocker, 0o755))
	require.Eventually(t, func() bool {
		updated, err := session.LoadRecordingStateForAgent(repo, state.AgentID)
		return err == nil && updated != nil && updated.SourceOffset == int64(len(first)+len(second))
	}, 3*pollInterval, 50*time.Millisecond, "capture must resume once the turn can be checked")
	mgr.StopAll()
	assert.Equal(t, 1, countRawJSONLEntries(t, raw))
}
