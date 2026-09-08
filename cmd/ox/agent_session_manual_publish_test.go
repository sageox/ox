package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/require"
)

// agentSessionFixture defines the minimal agent-specific behavior needed by this
// test harness. To add coverage for a new coding agent, implement these two
// functions and add an entry to TestManualPublishingSessionCapture_Matrix.
//
// Why this exists:
// - We want one reusable flow for "manual publishing" validation.
// - Each coding agent has different source session-file formats.
// - This keeps future agent additions cheap and consistent.
type agentSessionFixture struct {
	name string

	// agentType is the value stored on the agent instance (e.g., "codex").
	agentType string

	// createSessionSource creates the agent's native session source file(s) in
	// the isolated test environment and returns a handle/path for follow-up writes.
	createSessionSource func(t *testing.T, homeDir, cwd string) string

	// appendMessage appends a native message after session start.
	appendMessage func(t *testing.T, sourcePath, role, content string)

	stopDuringCapture bool
}

// blockingStopCodexAdapter holds the watcher's final native read in flight while
// the CLI starts stopping. Only that first read blocks; an unlocked CLI drain
// can still run, exposing duplicate capture instead of hiding it behind the test.
type blockingStopCodexAdapter struct {
	testCodexAdapter
	response string
	entered  chan struct{}
	release  chan struct{}
	blocked  atomic.Bool
}

func (a *blockingStopCodexAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	entries, next, err := a.testCodexAdapter.ReadFromOffset(path, offset)
	for _, entry := range entries {
		if entry.Content == a.response && a.blocked.CompareAndSwap(false, true) {
			close(a.entered)
			<-a.release
			break
		}
	}
	return entries, next, err
}

func TestManualPublishingSessionCapture_Matrix(t *testing.T) {
	// The test binary may itself run inside Codex, Claude Code, or another AI
	// coworker. Do not let that ambient session identity make
	// ensurePrimeBeforeSession recursively execute the test binary as `ox`.
	for _, key := range []string{
		"AMP_THREAD_URL",
		"CLAUDE_CODE_SESSION_ID",
		"CODEX_THREAD_ID",
		"GC_RUN_ID",
		"OMP_SESSION_ID",
		"PI_SESSION_ID",
	} {
		t.Setenv(key, "")
	}

	adapters.Register(&testCodexAdapter{})
	t.Cleanup(func() { adapters.Unregister("codex") })

	fixtures := []agentSessionFixture{
		{
			name:                "codex",
			agentType:           "codex",
			createSessionSource: writeCodexSessionFile,
			appendMessage:       appendCodexMessage,
		},
		{
			name:                "codex_stop_during_capture",
			agentType:           "codex",
			createSessionSource: writeCodexSessionFile,
			appendMessage:       appendCodexMessage,
			stopDuringCapture:   true,
		},
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			runManualPublishingSessionCaptureTest(t, fixture)
		})
	}
}

func runManualPublishingSessionCaptureTest(t *testing.T, fixture agentSessionFixture) {
	if fixture.stopDuringCapture && testing.Short() {
		t.Skip("short: waits for native recording polls and concurrent CLI stop")
	}

	// Fast local HTTP endpoint for auth/access checks (checkUploadAccess fail-open path).
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer apiServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", apiServer.URL)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SAGEOX_DAEMON", "false")

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:    "test-repo-manual-publish",
		Endpoint:  apiServer.URL,
		ProjectID: "test-project",
		TeamID:    "test-team",
	})

	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chdir(origCwd)
	})
	require.NoError(t, os.Chdir(projectRoot))
	actualProjectCWD, err := os.Getwd()
	require.NoError(t, err)
	require.Nil(t, daemon.TryConnect(), "fixture must not connect to a live daemon")

	// runAgentSessionStart/Stop read output mode from global cfg, which is normally
	// initialized by Cobra PersistentPreRun in CLI execution.
	oldGlobalCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() {
		cfg = oldGlobalCfg
	})

	// (a) configure session_publishing=manual while preserving old value.
	projectCfg, err := config.LoadProjectConfig(projectRoot)
	require.NoError(t, err)
	oldPublishing := projectCfg.SessionPublishing
	projectCfg.SessionPublishing = config.SessionPublishingManual
	require.NoError(t, config.SaveProjectConfig(projectRoot, projectCfg))
	t.Cleanup(func() {
		cfg, loadErr := config.LoadProjectConfig(projectRoot)
		require.NoError(t, loadErr)
		cfg.SessionPublishing = oldPublishing
		require.NoError(t, config.SaveProjectConfig(projectRoot, cfg))
	})

	// save auth token for identity enrichment (no longer required for session start,
	// but still used for best-effort attribution via identity.ResolveAttribution)
	require.NoError(t, auth.SaveToken(&auth.StoredToken{
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
		UserInfo: auth.UserInfo{
			UserID: "test-user-id",
			Email:  "test@example.com",
			Name:   "Test User",
		},
	}))

	// (b) start a coding agent (simulated via the fixture's source writer).
	sourcePath := fixture.createSessionSource(t, os.Getenv("HOME"), actualProjectCWD)
	require.FileExists(t, sourcePath)

	// (c) prime ox (simulated by creating an agent instance) and start a session.
	inst := &agentinstance.Instance{
		AgentID:         "OxT123",
		ServerSessionID: "oxsid_test_manual_publish_capture",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AgentType:       fixture.agentType,
	}
	store, err := getInstanceStore(projectRoot)
	require.NoError(t, err)
	require.NoError(t, store.Add(inst))
	require.NoError(t, runAgentSessionStart(inst, nil))
	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "tail", state.WatchMode, "manual Codex start without hooks must start tail capture")

	// (d) append pre-canned query after recording start so it survives timestamp filtering.
	preCannedQuery := "PRE-CANNED-QUERY: verify manual publishing captures this"
	fixture.appendMessage(t, sourcePath, "user", preCannedQuery)

	// (e) stop session and gather stored session data.
	const finalResponse = "FINAL-CODEX-RESPONSE: the recording contains the completed work"
	if fixture.stopDuringCapture {
		// Failure prevented: stopping between a source read and its persisted
		// cursor used to make the CLI drain that same batch a second time.
		blocking := &blockingStopCodexAdapter{
			response: finalResponse, entered: make(chan struct{}), release: make(chan struct{}),
		}
		previousAdapter, err := adapters.GetAdapter("codex")
		require.NoError(t, err)
		adapters.Unregister("codex")
		adapters.Register(blocking)
		t.Cleanup(func() {
			adapters.Unregister("codex")
			adapters.Register(previousAdapter)
		})
		manager := agentwork.NewSessionWatcherManager(slog.Default())
		manager.SetHomeDirForTest(os.Getenv("HOME"))
		t.Cleanup(func() {
			select {
			case <-blocking.release:
			default:
				close(blocking.release)
			}
			manager.StopAll()
		})
		ledgerPath := deriveLedgerPath(state.SessionPath)
		require.NotEmpty(t, ledgerPath)
		require.Equal(t, 1, manager.DetectAndRestart(ledgerPath))
		firstBatch, err := os.Stat(sourcePath)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			current, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
			return err == nil && current != nil && current.SourceOffset == firstBatch.Size() && current.EntryCount == 1
		}, 8*time.Second, 10*time.Millisecond, "first prompt must be captured before the final read")

		fixture.appendMessage(t, sourcePath, "assistant", finalResponse)
		select {
		case <-blocking.entered:
		case <-time.After(8 * time.Second):
			t.Fatal("watcher did not begin the final native read")
		}
		stopResult := make(chan error, 1)
		stopFinished := make(chan struct{})
		go func() {
			defer close(stopFinished)
			stopResult <- runAgentSessionStop(inst)
		}()
		t.Cleanup(func() {
			select {
			case <-blocking.release:
			default:
				close(blocking.release)
			}
			select {
			case <-stopFinished:
			case <-time.After(15 * time.Second):
				t.Error("CLI stop did not finish during cleanup")
			}
		})
		require.Eventually(t, func() bool {
			return session.HasExplicitStop(projectRoot, inst.AgentID)
		}, 3*time.Second, 10*time.Millisecond, "CLI must mark explicit stop before waiting for the watcher")
		close(blocking.release)
		select {
		case err := <-stopResult:
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			t.Fatal("CLI stop did not finish without daemon IPC")
		}
		require.Eventually(t, func() bool { return len(manager.ActiveSessions()) == 0 }, 3*time.Second, 10*time.Millisecond,
			"watcher must observe explicit stop without IPC")
		require.NoFileExists(t, filepath.Join(state.SessionPath, ".recording.json"))
		require.Zero(t, manager.DetectAndRestart(ledgerPath), "stopped recording must not restart")
	} else {
		require.NoError(t, runAgentSessionStop(inst))
	}

	var stored *session.StoredSession
	if fixture.stopDuringCapture {
		// Incremental stop finalizes the existing recording cache in place.
		stored, err = session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
		require.NoError(t, err)
	} else {
		// A session with no live capture is reconstructed into the store.
		contextPath := session.GetContextPath("test-repo-manual-publish")
		require.NotEmpty(t, contextPath)
		s, err := session.NewStore(contextPath)
		require.NoError(t, err)
		latest, err := s.GetLatestRaw()
		require.NoError(t, err)
		stored, err = s.ReadSessionRaw(latest.SessionName)
		require.NoError(t, err)
	}

	// (f) assert session contains the pre-canned query.
	found := false
	for _, entry := range stored.Entries {
		if content, ok := entry["content"].(string); ok && content == preCannedQuery {
			found = true
			break
		}
	}
	require.True(t, found, "expected stored session to contain pre-canned query")
	if fixture.stopDuringCapture {
		counts := make(map[string]int)
		for _, entry := range stored.Entries {
			if content, ok := entry["content"].(string); ok {
				counts[content]++
			}
		}
		require.Equal(t, 1, counts[preCannedQuery], "initial prompt must remain captured once")
		require.Equal(t, 1, counts[finalResponse], "final response must be drained exactly once")
	}
}

func writeCodexSessionFile(t *testing.T, homeDir, cwd string) string {
	t.Helper()

	dateDir := filepath.Join(homeDir, ".codex", "sessions",
		time.Now().Format("2006"), time.Now().Format("01"), time.Now().Format("02"))
	require.NoError(t, os.MkdirAll(dateDir, 0o755))

	sessionPath := filepath.Join(dateDir, "rollout-test-manual-publish.jsonl")
	f, err := os.Create(sessionPath)
	require.NoError(t, err)
	defer f.Close()

	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":          "codex-test-session",
			"cwd":         cwd,
			"cli_version": "0.0.0-test",
		},
	}))
	require.NoError(t, enc.Encode(map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "turn_context",
		"payload": map[string]any{
			"model": "gpt-test",
		},
	}))

	return sessionPath
}

func appendCodexMessage(t *testing.T, sessionPath, role, content string) {
	t.Helper()

	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()

	contentType := "input_text"
	if role == "assistant" {
		contentType = "output_text"
	}
	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(map[string]any{
		"timestamp": time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano),
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message",
			"role": role,
			"content": []map[string]any{
				{
					"type": contentType,
					"text": content,
				},
			},
		},
	}))
}
