package agentwork

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorWatcherConversationID = "123e4567-e89b-12d3-a456-426614174088"

type cursorWatcherAdapter struct{}

func (*cursorWatcherAdapter) Name() string { return "cursor" }
func (*cursorWatcherAdapter) Detect() bool { return false }
func (*cursorWatcherAdapter) FindSessionFile(adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (*cursorWatcherAdapter) Read(string) ([]adapters.RawEntry, error)               { return nil, nil }
func (*cursorWatcherAdapter) ReadMetadata(string) (*adapters.SessionMetadata, error) { return nil, nil }
func (*cursorWatcherAdapter) Watch(context.Context, string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}

// ReadFromOffset is a deliberately small Cursor JSONL parser: complete native
// role rows produce entries while terminal and metadata rows advance bytes only.
func (*cursorWatcherAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, offset, err
	}
	var entries []adapters.RawEntry
	next := offset
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		next += int64(len(line) + 1)
		var row struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolName   string `json:"tool_name"`
			ToolInput  string `json:"tool_input"`
			ToolOutput string `json:"tool_output"`
			IsError    bool   `json:"is_error"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, offset, err
		}
		if row.Role != "" {
			entries = append(entries, adapters.RawEntry{Role: row.Role, Content: row.Content, ToolName: row.ToolName, ToolInput: row.ToolInput, ToolOutput: row.ToolOutput, IsError: row.IsError})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, offset, err
	}
	return entries, next, nil
}

type cursorWatcherFixture struct {
	projectRoot string
	homeDir     string
	ledgerPath  string
	cachePath   string
	sessionName string
	sourcePath  string
	state       session.RecordingState
}

func installCursorWatcherAdapter(t *testing.T) {
	t.Helper()
	prior, priorErr := adapters.GetAdapter("cursor")
	if priorErr == nil {
		adapters.Unregister("cursor")
	}
	adapters.Register(&cursorWatcherAdapter{})
	t.Cleanup(func() {
		adapters.Unregister("cursor")
		if priorErr == nil {
			adapters.Register(prior)
		}
	})
}

func newCursorWatcherFixture(t *testing.T, source string, pending bool, underLedger bool) *cursorWatcherFixture {
	t.Helper()
	homeDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	projectRoot := t.TempDir()
	sourcePath, err := cursorpaths.SessionPath(homeDir, projectRoot, cursorWatcherConversationID)
	require.NoError(t, err)
	if !pending {
		require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o755))
		require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o600))
	}
	ledgerPath := t.TempDir()
	sessionName := "cursor-session"
	cachePath := t.TempDir()
	if underLedger {
		cachePath = filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	}
	require.NoError(t, os.MkdirAll(cachePath, 0o700))
	state := session.RecordingState{
		AgentID: "OxCursorWatch", AgentSessionID: cursorWatcherConversationID,
		AdapterName: "cursor", WatchMode: "tail", WorkspacePath: projectRoot,
		SessionPath: cachePath, SessionFile: sourcePath, ParentPID: os.Getpid(),
		StartOffsetKnown: true, SourcePrefixSHA256: watcherSHA256(nil),
	}
	if pending {
		state.SessionFile = ""
	}
	writeRecordingState(t, filepath.Join(cachePath, recordingMarker), state)
	return &cursorWatcherFixture{projectRoot: projectRoot, homeDir: homeDir, ledgerPath: ledgerPath, cachePath: cachePath, sessionName: sessionName, sourcePath: sourcePath, state: state}
}

func watcherSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func loadCursorWatcherState(t *testing.T, f *cursorWatcherFixture) session.RecordingState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.cachePath, recordingMarker))
	require.NoError(t, err)
	var state session.RecordingState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func newCursorWatcherManager(t *testing.T, home string) *SessionWatcherManager {
	t.Helper()
	mgr := NewSessionWatcherManager(slog.New(slog.DiscardHandler))
	mgr.SetHomeDirForTest(home)
	t.Cleanup(mgr.StopAll)
	return mgr
}

func startCursorWatch(t *testing.T, mgr *SessionWatcherManager, f *cursorWatcherFixture) {
	t.Helper()
	require.NoError(t, mgr.StartWatch(f.sessionName, f.sourcePath, "cursor", f.ledgerPath, f.cachePath))
}

func TestCursorWatcher_CapturesExactSourceAndMetadataAdvance(t *testing.T) {
	installCursorWatcherAdapter(t)
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	source := old + `{"type":"metadata","model":"fixture"}` + "\n" +
		`{"role":"user","content":"first"}` + "\n" +
		`{"role":"assistant","content":"second"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, source, false, false)
	f.state.StartOffset = int64(len(old))
	f.state.SourceOffset = int64(len(old))
	f.state.SourcePrefixSHA256 = watcherSHA256([]byte(old))
	writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)

	require.Eventually(t, func() bool {
		state := loadCursorWatcherState(t, f)
		return state.SourceOffset == int64(len(source)) && state.EntryCount == 2
	}, time.Second, 10*time.Millisecond)
	state := loadCursorWatcherState(t, f)
	assert.Equal(t, watcherSHA256([]byte(source)), state.SourcePrefixSHA256)
	assert.Equal(t, 2, countRawJSONLEntries(t, filepath.Join(f.cachePath, artifactRaw)))
	raw, err := os.ReadFile(filepath.Join(f.cachePath, artifactRaw))
	assert.NotContains(t, string(raw), "old", "pre-recording prefix must not be replayed")
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"ts":"0001-01-01T00:00:00Z"`, "unknown native timestamps are retained")
}

func TestCursorWatcher_RepairsLegacyCheckpointOnStart(t *testing.T) {
	installCursorWatcherAdapter(t)
	for _, restart := range []bool{false, true} {
		name := "start"
		if restart {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			source := `{"role":"user","content":"recovered"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
			f := newCursorWatcherFixture(t, source, false, restart)
			path := filepath.Join(f.cachePath, recordingMarker)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(data, []byte(`stale previous checkpoint"}`)...), 0o600))
			mgr := newCursorWatcherManager(t, f.homeDir)
			if restart {
				require.Equal(t, 1, mgr.DetectAndRestart(f.ledgerPath))
			} else {
				startCursorWatch(t, mgr, f)
			}
			require.Eventually(t, func() bool {
				return loadCursorWatcherState(t, f).EntryCount == 1
			}, time.Second, 10*time.Millisecond)
			assert.Equal(t, int64(len(source)), loadCursorWatcherState(t, f).SourceOffset)
		})
	}
}

func TestCursorWatcher_QuarantineRefusesStartAndStopsLiveOwner(t *testing.T) {
	installCursorWatcherAdapter(t)
	for _, live := range []bool{false, true} {
		name := "before start"
		if live {
			name = "live owner"
		}
		t.Run(name, func(t *testing.T) {
			source := `{"role":"user","content":"captured"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
			f := newCursorWatcherFixture(t, source, false, false)
			mgr := newCursorWatcherManager(t, f.homeDir)
			if live {
				startCursorWatch(t, mgr, f)
				require.Eventually(t, func() bool {
					return loadCursorWatcherState(t, f).EntryCount == 1
				}, time.Second, 10*time.Millisecond)
			}
			require.NoError(t, session.SetSourceRejectedAt(f.cachePath, f.state.SessionID, true))
			if live {
				mgr.Cleanup()
				assert.Equal(t, 1, countRawJSONLEntries(t, filepath.Join(f.cachePath, artifactRaw)))
			} else {
				err := mgr.StartWatch(f.sessionName, f.sourcePath, "cursor", f.ledgerPath, f.cachePath)
				require.ErrorContains(t, err, "quarantined")
				assert.NoFileExists(t, filepath.Join(f.cachePath, artifactRaw))
			}
			assert.Empty(t, mgr.ActiveSessions())
			assert.True(t, loadCursorWatcherState(t, f).SourceRejected)
		})
	}
}

func TestCursorWatcher_ExistingStartWatchWakesLiveOwnerWithoutDuplicate(t *testing.T) {
	installCursorWatcherAdapter(t)
	first := `{"role":"user","content":"first"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, first, false, false)
	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 1 }, time.Second, 10*time.Millisecond)

	second := `{"role":"assistant","content":"second"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(first+second), 0o600))
	started := time.Now()
	startCursorWatch(t, mgr, f) // existing watcher: coalesced drain, not a duplicate owner
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 2 }, 700*time.Millisecond, 10*time.Millisecond)
	assert.Less(t, time.Since(started), pollInterval, "wake must not wait for the regular poll")
	assert.Equal(t, 2, countRawJSONLEntries(t, filepath.Join(f.cachePath, artifactRaw)))
}

func TestCursorWatcher_PendingKnownZeroBindsExactExport(t *testing.T) {
	installCursorWatcherAdapter(t)
	f := newCursorWatcherFixture(t, "", true, false)
	// Safe pending admission requires the controlled native parent to exist;
	// the leaf remains absent until Cursor exports its first JSONL rows.
	require.NoError(t, os.MkdirAll(filepath.Dir(f.sourcePath), 0o755))
	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool { return len(mgr.ActiveSessions()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Zero(t, loadCursorWatcherState(t, f).EntryCount)

	source := `{"role":"user","content":"first"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(source), 0o600))
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool {
		state := loadCursorWatcherState(t, f)
		return state.SessionFile == f.sourcePath && state.SourceOffset == int64(len(source)) && state.EntryCount == 1
	}, time.Second, 10*time.Millisecond)
}

func TestCursorWatcher_RefusesWrongNativePathOrIdentity(t *testing.T) {
	installCursorWatcherAdapter(t)
	for _, tc := range []struct {
		name   string
		mutate func(*cursorWatcherFixture)
	}{
		{
			name: "alternate path",
			mutate: func(f *cursorWatcherFixture) {
				f.state.SessionFile = filepath.Join(f.homeDir, ".cursor", "projects", "other.jsonl")
				writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
			},
		},
		{
			name: "conversation identity mismatch",
			mutate: func(f *cursorWatcherFixture) {
				f.state.AgentSessionID = "123e4567-e89b-12d3-a456-426614174077"
				writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorWatcherFixture(t, `{"type":"turn_ended","status":"success"}`+"\n", false, false)
			tc.mutate(f)
			mgr := newCursorWatcherManager(t, f.homeDir)
			err := mgr.StartWatch(f.sessionName, f.sourcePath, "cursor", f.ledgerPath, f.cachePath)
			require.Error(t, err)
			assert.ErrorContains(t, err, "invalid-source-path")
			assert.Empty(t, mgr.ActiveSessions())
		})
	}
}

func TestCursorWatcher_RawOwnerContentionThenRestartPreservesPrefix(t *testing.T) {
	installCursorWatcherAdapter(t)
	first := `{"role":"user","content":"first"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, first, false, false)
	rawPath := filepath.Join(f.cachePath, artifactRaw)
	locked := make(chan struct{})
	release := make(chan struct{})
	var lockWG sync.WaitGroup
	lockErr := make(chan error, 1)
	lockWG.Add(1)
	go func() {
		defer lockWG.Done()
		lockErr <- fileutil.WithFileLock(context.Background(), rawPath, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)
	assert.Never(t, func() bool { return loadCursorWatcherState(t, f).EntryCount != 0 }, 120*time.Millisecond, 10*time.Millisecond)
	close(release)
	lockWG.Wait()
	require.NoError(t, <-lockErr)
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 1 }, time.Second, 10*time.Millisecond)
	mgr.StopAll()

	second := `{"role":"assistant","content":"second"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(first+second), 0o600))
	resumed := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, resumed, f)
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 2 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, countRawJSONLEntries(t, rawPath), "restart resumes durable prefix without duplicate rows")
}

func TestCursorWatcher_DetectAndRestartKeepsLateStopDrainAlive(t *testing.T) {
	installCursorWatcherAdapter(t)
	first := `{"role":"user","content":"first"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, first, false, true)
	mgr := newCursorWatcherManager(t, f.homeDir)
	require.Equal(t, 1, mgr.DetectAndRestart(f.ledgerPath))
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 1 }, time.Second, 10*time.Millisecond)

	// A stop hook requests a drain through the existing watch-start path. It
	// must leave this owner alive for Cursor's later terminal export.
	late := `{"role":"assistant","content":"late final"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(first+late), 0o600))
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 2 }, time.Second, 10*time.Millisecond)
	assert.Len(t, mgr.ActiveSessions(), 1)
}
