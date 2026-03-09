package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
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

	// appendQuery appends a user query to the agent source after session start.
	appendQuery func(t *testing.T, sourcePath, query string)
}

func TestManualPublishingSessionCapture_Matrix(t *testing.T) {
	fixtures := []agentSessionFixture{
		{
			name:                "codex",
			agentType:           "codex",
			createSessionSource: writeCodexSessionFile,
			appendQuery:         appendCodexUserQuery,
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
	// Fast local HTTP endpoint for auth/access checks (checkUploadAccess fail-open path).
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer apiServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", apiServer.URL)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

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

	// session start requires authentication.
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

	// (d) append pre-canned query after recording start so it survives timestamp filtering.
	preCannedQuery := "PRE-CANNED-QUERY: verify manual publishing captures this"
	fixture.appendQuery(t, sourcePath, preCannedQuery)

	// (e) stop session and gather stored session data.
	require.NoError(t, runAgentSessionStop(inst))

	// Read latest saved raw session from local cache store.
	contextPath := session.GetContextPath("test-repo-manual-publish")
	require.NotEmpty(t, contextPath)
	s, err := session.NewStore(contextPath)
	require.NoError(t, err)
	latest, err := s.GetLatestRaw()
	require.NoError(t, err)
	stored, err := s.ReadSessionRaw(latest.SessionName)
	require.NoError(t, err)

	// (f) assert session contains the pre-canned query.
	found := false
	for _, entry := range stored.Entries {
		if content, ok := entry["content"].(string); ok && content == preCannedQuery {
			found = true
			break
		}
	}
	require.True(t, found, "expected stored session to contain pre-canned query")
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

func appendCodexUserQuery(t *testing.T, sessionPath, query string) {
	t.Helper()

	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()

	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(map[string]any{
		"timestamp": time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano),
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{
					"type": "input_text",
					"text": query,
				},
			},
		},
	}))
}
