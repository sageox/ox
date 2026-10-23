package teamskills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

const orgSyncEndpoint = "https://sageox.ai"

// isolateOrgSyncCache points the cache at a temp dir. Not parallel-safe
// (t.Setenv), which is the point: the file is shared machine state.
func isolateOrgSyncCache(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	file := paths.ClaudeOrgSyncFile(orgSyncEndpoint)
	require.NotEmpty(t, file)
	return file
}

// TestClaudeOrgSync_NothingRecordedIsNotConnected: the overwhelmingly common
// state — no org sync anywhere — must read as "copy everything", with no error.
func TestClaudeOrgSync_NothingRecordedIsNotConnected(t *testing.T) {
	isolateOrgSyncCache(t)
	got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", time.Now())
	require.NoError(t, err)
	require.False(t, got.Active())
	require.False(t, got.Has("grill-me"))
	require.Empty(t, got.Revision())

	got, err = LoadClaudeOrgSync("", "team_a", time.Now())
	require.NoError(t, err)
	require.False(t, got.Active(), "an unknown endpoint produced a record")
}

// TestClaudeOrgSync_OldServerLeavesNoTrace: a server that predates the field
// sends teams without it. Recording that must not create a file, so a machine
// talking to an old server is byte-for-byte where it was before.
func TestClaudeOrgSync_OldServerLeavesNoTrace(t *testing.T) {
	file := isolateOrgSyncCache(t)
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{
		{TeamID: "team_a", TeamSlug: "acme"}, {TeamID: "team_b"},
	}, time.Now()))
	require.NoFileExists(t, file)
}

// TestClaudeOrgSync_RoundTripKeepsOnlyUsableNames: the server's list becomes an
// exact-match set of names ox would accept. A name ox refuses could never match
// a skill ox projects; a case variant is a DIFFERENT skill on a case-sensitive
// install and must still be copied.
func TestClaudeOrgSync_RoundTripKeepsOnlyUsableNames(t *testing.T) {
	isolateOrgSyncCache(t)
	now := time.Now()
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{{
		TeamID: "team_a", TeamSlug: "acme",
		SyncedSkills: []string{"grill-me", "Deploy", "../escape", "a/b", "", "grill-me", "notes", "x."},
	}}, now))

	got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, got.Active())
	require.Equal(t, "acme", got.TeamSlug)
	require.Equal(t, []string{"grill-me", "notes"}, got.Skills)
	require.True(t, got.Has("grill-me"))
	require.False(t, got.Has("Deploy"))
	require.False(t, got.Has("deploy"), "a case-folded match deferred a skill the org does not carry")
	require.False(t, got.Has("Grill-Me"), "matching is exact; a case variant is a different skill to a case-sensitive reader")
	require.False(t, got.Has("grill"), "a prefix matched")

	other, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_b", now)
	require.NoError(t, err)
	require.False(t, other.Active(), "one team's org sync leaked to another team")
}

// TestClaudeOrgSync_StaleOrFutureRecordIsNotBelieved: a record is only as good
// as the last refresh. A day without one (daemon stopped, laptop offline) must
// stop the deferral, because the org may no longer deliver these skills; a
// record dated in the future is a moved clock, not a confirmation.
func TestClaudeOrgSync_StaleOrFutureRecordIsNotBelieved(t *testing.T) {
	isolateOrgSyncCache(t)
	written := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{{
		TeamID: "team_a", SyncedSkills: []string{"grill-me"},
	}}, written))

	for _, tt := range []struct {
		name   string
		at     time.Time
		active bool
	}{
		{"just written", written, true},
		{"one minute short of the limit", written.Add(ClaudeOrgSyncMaxAge - time.Minute), true},
		{"at the limit", written.Add(ClaudeOrgSyncMaxAge), false},
		{"days later", written.Add(72 * time.Hour), false},
		{"clock moved back", written.Add(-time.Minute), false},
	} {
		got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", tt.at)
		require.NoError(t, err, tt.name)
		require.Equal(t, tt.active, got.Active(), tt.name)
		require.Equal(t, tt.active, got.Has("grill-me"), tt.name)
		require.Equal(t, tt.active, got.Revision() != "", tt.name)
		require.Equal(t, []string{"grill-me"}, got.Skills, "%s: the record itself must stay visible to diagnostics", tt.name)
	}
}

// TestClaudeOrgSync_CorruptCacheIsAnErrorNotAConnection: an unreadable record is
// reported (so `ox skills status` can say so) and never treated as active. The
// next refresh rebuilds it instead of staying wedged.
func TestClaudeOrgSync_CorruptCacheIsAnErrorNotAConnection(t *testing.T) {
	file := isolateOrgSyncCache(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))

	for name, content := range map[string]string{
		"truncated":    `{"schema_version":1,"teams":[{"team_id":"team_a","synced_skills":["grill-me"`,
		"newer schema": `{"schema_version":2,"teams":[{"team_id":"team_a","synced_skills":["grill-me"],"fetched_at":"` + time.Now().Format(time.RFC3339) + `"}]}`,
	} {
		require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
		got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", time.Now())
		require.Error(t, err, name)
		require.False(t, got.Active(), "%s: an unreadable record deferred skills to the org", name)
	}

	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{{
		TeamID: "team_a", SyncedSkills: []string{"notes"},
	}}, time.Now()))
	got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", time.Now())
	require.NoError(t, err, "a corrupt cache was not rebuilt by the next refresh")
	require.Equal(t, []string{"notes"}, got.Skills)
}

// TestClaudeOrgSync_MergesPerTeamAndAgesOutTheRest: a process authenticated as
// one team sees only that team. Replacing the file would erase every other
// team's record; keeping it forever would defer to an org the user may have
// left. Merge, and let the rest age out.
func TestClaudeOrgSync_MergesPerTeamAndAgesOutTheRest(t *testing.T) {
	isolateOrgSyncCache(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{
		{TeamID: "team_a", SyncedSkills: []string{"a-skill"}},
		{TeamID: "team_b", SyncedSkills: []string{"b-skill"}},
	}, t0))

	// A team-token refresh an hour later reports only team_a, and team_a's org
	// now delivers a second skill.
	t1 := t0.Add(time.Hour)
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{
		{TeamID: "team_a", SyncedSkills: []string{"a-skill", "a-two"}},
	}, t1))
	a, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", t1)
	require.NoError(t, err)
	require.Equal(t, []string{"a-skill", "a-two"}, a.Skills)
	b, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_b", t1)
	require.NoError(t, err)
	require.True(t, b.Active(), "a single-team refresh erased another team's record")
	require.Equal(t, t0, b.FetchedAt.UTC(), "an unreported team's record was re-dated as if confirmed")

	// A day later team_b has still not been confirmed: pruned on write.
	t2 := t0.Add(ClaudeOrgSyncMaxAge + time.Hour)
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{
		{TeamID: "team_a", SyncedSkills: []string{"a-skill"}},
	}, t2))
	b, err = LoadClaudeOrgSync(orgSyncEndpoint, "team_b", t2)
	require.NoError(t, err)
	require.Empty(t, b.Skills, "an expired team record was kept forever")
}

// TestClaudeOrgSync_DisconnectRemovesTheTeam: when the server stops listing a
// team's skills (org sync revoked, or Claude never pulled again), the next
// refresh must drop the record — and the file once nothing is left.
func TestClaudeOrgSync_DisconnectRemovesTheTeam(t *testing.T) {
	file := isolateOrgSyncCache(t)
	now := time.Now()
	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{
		{TeamID: "team_a", SyncedSkills: []string{"grill-me"}},
	}, now))
	require.FileExists(t, file)

	require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{{TeamID: "team_a"}}, now.Add(time.Hour)))
	got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, got.Active(), "a revoked org sync kept deferring skills")
	require.NoFileExists(t, file)
}

// TestClaudeOrgSync_RevisionTracksTheSetNotItsOrder: the revision re-plans the
// skill inventory when the delivered set changes, and must not churn when the
// server merely reorders it.
func TestClaudeOrgSync_RevisionTracksTheSetNotItsOrder(t *testing.T) {
	isolateOrgSyncCache(t)
	now := time.Now()
	record := func(skills ...string) string {
		t.Helper()
		require.NoError(t, RecordClaudeOrgSync(orgSyncEndpoint, []ClaudeOrgTeam{{TeamID: "team_a", SyncedSkills: skills}}, now))
		got, err := LoadClaudeOrgSync(orgSyncEndpoint, "team_a", now)
		require.NoError(t, err)
		return got.Revision()
	}
	ab := record("alpha", "beta")
	require.NotEmpty(t, ab)
	require.Equal(t, ab, record("beta", "alpha", "beta"))
	require.NotEqual(t, ab, record("alpha"))
	require.NotEqual(t, ab, record("alpha", "beta", "gamma"))
}
