package session

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
)

// serveTeamToken binds a valid team SAGEOX_TOKEN to an endpoint whose
// introspection answer carries coworker verbatim. setupRecordingTest isolates
// the cache it lands in.
func serveTeamToken(t *testing.T, coworker string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":`+coworker+`}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
}

// Failure prevented: a coworker's session is recorded as human-driven, or a
// non-ASCII coworker name breaks the session name.
func TestStartRecording_TeamTokenCoworkerSessionIsAgentOrigin(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	// CI and subagent runs are already "agent"/"subagent" origin without a coworker.
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CLAUDE_CODE_ENTRY_POINT", "")
	serveTeamToken(t, `{"id":"agt_01ABC","display_name":"ロボ"}`)

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     "OxAiCw",
		AdapterName: "claude-code",
		Username:    identity.AttributionUsername(endpoint.GetForProject(projectRoot), ""),
	})
	require.NoError(t, err)

	assert.Equal(t, "agent", state.Origin)
	assert.Regexp(t, `^[A-Za-z0-9:._-]+-agt_01abc-OxAiCw$`, filepath.Base(state.SessionPath))
}

// Failure prevented: a team token with no coworker records a session under the
// machine's git identity.
func TestStartRecording_TeamTokenWithoutCoworkerRefuses(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	serveTeamToken(t, `null`)

	_, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: "OxNoCw", AdapterName: "claude-code", Username: "pod"})
	require.ErrorIs(t, err, auth.ErrNoCoworker)

	saved, err := LoadRecordingStateForAgent(projectRoot, "OxNoCw")
	require.NoError(t, err)
	assert.Nil(t, saved)
}
