package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pending stop keeps its publishing choice in the recording. The next native
// Stop may run in an auto-publishing environment, but may not reveal a held
// session through either its registration request or its Ledger placeholder.
func TestMaybePublishSessionDraft_PersistedManualSuppressesAllMetadata(t *testing.T) {
	for _, adapter := range []string{"cursor", "claude-code"} {
		for _, mode := range []string{config.SessionPublishingManual, config.SessionPublishingAuto} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				f := newDraftHookLedgerFixture(t)
				t.Chdir(f.projectRoot)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
				f.setPublishing(t, config.SessionPublishingAuto)
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}))
				t.Cleanup(server.Close)
				t.Setenv("SAGEOX_ENDPOINT", server.URL)
				require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
					AccessToken: "test-access-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
					UserInfo: auth.UserInfo{UserID: "test-user", Email: "test@example.com"},
				}))

				const sessionName = "2026-10-13T00-00-testuser-OxHeldDraft"
				state := f.seedState(t, "OxHeldDraft", sessionName, config.DraftPublishTurn-1)
				state.AdapterName = adapter
				state.AgentType = adapter
				state.EntryCount = 1
				state.PublishingMode = mode
				state.LifecycleRegistrationState = "deferred"
				if adapter == "cursor" {
					now := time.Now().UTC()
					state.StoppedAt = &now
					state.CursorFinalDrainPending = true
				}
				require.NoError(t, session.SaveRecordingState(f.projectRoot, state))
				require.Equal(t, config.SessionPublishingAuto, config.GetSessionPublishing(f.projectRoot))

				f.stopHook(state.AgentID)

				updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
				require.NoError(t, err)
				require.NotNil(t, updated)
				assert.Equal(t, config.DraftPublishTurn, updated.TurnCount, "local turn counting continues for held sessions")
				assert.Equal(t, mode, updated.PublishingMode)
				metaPath := filepath.Join(f.ledgerPath, "sessions", sessionName, "meta.json")
				if mode == config.SessionPublishingManual {
					assert.Zero(t, requests.Load(), "a saved manual choice must suppress registration before any request")
					assert.Equal(t, "deferred", updated.LifecycleRegistrationState)
					assert.Zero(t, updated.DraftAttemptTurn)
					assert.Nil(t, updated.DraftPublishedAt)
					assert.Zero(t, f.draftCommitCount(t, sessionName), "held identity and activity counts must not enter the Ledger")
					assert.NoFileExists(t, metaPath)
				} else {
					assert.Equal(t, int32(1), requests.Load(), "the same fixture must register an auto session")
					assert.Equal(t, "confirmed", updated.LifecycleRegistrationState)
					assert.Equal(t, config.DraftPublishTurn, updated.DraftPublishedTurn)
					assert.Equal(t, 1, f.draftCommitCount(t, sessionName))
					assert.FileExists(t, metaPath)
				}
			})
		}
	}
}
