package session

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a failed auth-cache write repeats network identity lookup
// while Cursor holds its prompt-admission lock.
func TestStartRecordingPreparedIdentityDoesNotRefetchWithoutCache(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "default startup"
		if prepared {
			name = "prepared startup"
		}
		t.Run(name, func(t *testing.T) {
			root := setupRecordingTest(t, t.TempDir())
			t.Setenv("CI", "")
			t.Setenv("GITHUB_ACTIONS", "")
			t.Setenv("CLAUDE_CODE_ENTRY_POINT", "")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":{"id":"agt_preflight","display_name":"Prepared"}}`)
			}))
			t.Cleanup(server.Close)
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
			// A directory at the file destination makes cache persistence fail
			// without relying on permissions or running as an unprivileged user.
			cachePath := filepath.Join(paths.CacheDir(), "token_meta.json")
			require.NoError(t, os.MkdirAll(cachePath, 0o700))
			opts := StartRecordingOptions{AgentID: "OxReady", AdapterName: "cursor", Username: "prepared"}
			if prepared {
				var err error
				opts.PreparedIdentity, err = PrepareRecordingIdentity(root)
				require.NoError(t, err)
				require.Equal(t, int32(1), requests.Load(), "preflight must perform canonical token validation")
			}
			state, err := StartRecording(root, opts)
			require.NoError(t, err)
			assert.Equal(t, "agent", state.Origin, "the prepared coworker still determines recording origin")
			assert.Equal(t, int32(1), requests.Load(), "startup must not retry identity lookup after preflight")
			info, err := os.Stat(cachePath)
			require.NoError(t, err)
			assert.True(t, info.IsDir(), "the test must leave auth metadata uncached")
		})
	}
}

// Failure prevented: a preflight from another project, or a fabricated zero
// value, bypasses the canonical identity validation for this recording.
func TestStartRecordingPreparedIdentityRejectsWrongProject(t *testing.T) {
	for _, zero := range []bool{false, true} {
		name := "different project"
		if zero {
			name = "zero value"
		}
		t.Run(name, func(t *testing.T) {
			root := setupRecordingTest(t, t.TempDir())
			t.Setenv("SAGEOX_TOKEN", "")
			prepared := &PreparedRecordingIdentity{}
			if !zero {
				var err error
				prepared, err = PrepareRecordingIdentity(t.TempDir())
				require.NoError(t, err)
			}
			state, err := StartRecording(root, StartRecordingOptions{
				AgentID: "OxWrong", AdapterName: "cursor", PreparedIdentity: prepared,
			})
			require.ErrorContains(t, err, "prepared recording identity belongs to another project")
			assert.Nil(t, state)
			saved, err := LoadRecordingStateForAgent(root, "OxWrong")
			require.NoError(t, err)
			assert.Nil(t, saved)
		})
	}
}

// Failure prevented: moving validation outside startup accepts a team token
// which the server explicitly says has no coworker attached.
func TestPrepareRecordingIdentityPreservesValidation(t *testing.T) {
	root := setupRecordingTest(t, t.TempDir())
	serveTeamToken(t, `null`)
	prepared, err := PrepareRecordingIdentity(root)
	require.ErrorIs(t, err, auth.ErrNoCoworker)
	assert.Nil(t, prepared)
	prepared, err = PrepareRecordingIdentity("")
	require.ErrorIs(t, err, ErrEmptyPath)
	assert.Nil(t, prepared)
}
