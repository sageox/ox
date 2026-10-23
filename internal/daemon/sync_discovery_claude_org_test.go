package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTeamDiscovery_RecordsClaudeOrgSync: the hourly team discovery is the only
// refresh of which team skills a team's Claude organization delivers. It must
// record the server's list even when the Git credentials did not change (the
// usual case), forget it when the server stops sending it, and leave nothing on
// disk for a server that predates the field.
// Failure prevented: an org connected after the PAT was cached is never
// deduplicated; or a revoked org keeps skills out of Claude Code.
func TestTeamDiscovery_RecordsClaudeOrgSync(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	var synced atomic.Value
	synced.Store([]string(nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
		_ = json.NewEncoder(w).Encode(api.ReposResponse{
			Token: "same-pat", ExpiresAt: expires,
			Teams: []api.TeamMembership{
				{ID: "team_a", Slug: "acme", Role: "member", ClaudeOrgSyncedSkills: synced.Load().([]string)},
				{ID: "team_b", Slug: "beta", Role: "member"},
			},
		})
	}))
	t.Cleanup(server.Close)
	s, _ := newCredentialDiscoveryScheduler(t, server)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "same-pat", ExpiresAt: expires,
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
		Repos:           map[string]gitserver.RepoEntry{},
	}))
	ep := endpoint.GetForProject(s.config.ProjectRoot)
	discover := func() teamskills.ClaudeOrgSync {
		t.Helper()
		s.lastTeamDiscovery = time.Time{}
		s.discoverTeams(context.Background())
		got, err := teamskills.LoadClaudeOrgSync(ep, "team_a", time.Now())
		require.NoError(t, err)
		return got
	}

	require.False(t, discover().Active(), "a server without the field produced a record")

	synced.Store([]string{"grill-me", "notes"})
	got := discover()
	require.True(t, got.Active(), "unchanged credentials skipped the org sync refresh")
	require.Equal(t, []string{"grill-me", "notes"}, got.Skills)
	require.Equal(t, "acme", got.TeamSlug)
	other, err := teamskills.LoadClaudeOrgSync(ep, "team_b", time.Now())
	require.NoError(t, err)
	require.False(t, other.Active())

	synced.Store([]string(nil))
	require.False(t, discover().Active(), "a revoked org sync was still deferred to")
}
