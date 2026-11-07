//go:build !short

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/gitserver"
)

// Failure prevented: an AI coworker's run under a team token reaches the
// Ledger under its machine's git identity, `ox session wait` miscounts what
// it uploaded, or a revoked token keeps working with a cached PAT and names
// `ox login` as the fix.
func TestTeamTokenRun_EndToEnd(t *testing.T) {
	f := newWaitFixture(t)
	const bearer = "oxt_test_1ljPfr"
	var revoked atomic.Bool
	var refreshes atomic.Int32
	f.store.mu.Lock()
	f.store.fallback = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bearer || revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case auth.IntrospectEndpoint:
			_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":`+ripCoworker+`}`)
		case "/api/v1/cli/repos":
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "coworker-pat", ExpiresAt: time.Now().Add(24 * time.Hour)})
		default:
			http.NotFound(w, r)
		}
	}
	f.store.mu.Unlock()
	t.Setenv("SAGEOX_TOKEN", bearer)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "") // restores testenv's value afterwards
		require.NoError(t, os.Unsetenv(k))
	}
	require.NoError(t, gitserver.RefreshRemoteCredentials(f.ledgerPath, f.store.url))

	d := f.daemonOwnsUploads(t, true)
	const name = "2026-10-20T10-00-rip-OxRip1"
	f.leftover(t, name, "OxRip1", time.Now())
	out, err := runWait(t, time.Now().Add(-time.Hour), 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, []string{name}, d.asked)
	assert.Equal(t, sessionWaitOutput{Since: 1, InLedger: 1, OlderPending: []string{}, Failed: []sessionWaitFailure{}}, out)

	var meta struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/meta.json")), &meta))
	assert.Equal(t, "agt_rip", meta.UserID)
	assert.Equal(t, "Rip <agt_rip@ai-coworker.invalid>", runGit(t, f.barePath, "log", "-1", "--format=%an <%ae>"))

	revoked.Store(true)
	before := refreshes.Load()
	_, err = auth.RefreshGitCredentialsForEndpoint(context.Background(), f.store.url, true)
	require.ErrorIs(t, err, api.ErrUnauthorized)
	assert.Equal(t, before, refreshes.Load(), "a revoked token mints no PAT")
	remedy := auth.ReauthenticationRemedy(f.store.url)
	assert.Contains(t, remedy, "SAGEOX_TOKEN")
	assert.NotContains(t, remedy, "ox login")
}
