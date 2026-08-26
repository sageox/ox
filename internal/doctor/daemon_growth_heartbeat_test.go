package doctor

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFDGrowthVerdict_CrossRestartNotALeak pins the E fix: a history that
// spans a daemon restart (a low-count old PID followed by a higher but flat
// new PID) must NOT warn. The pre-fix code compared history[0] vs
// history[last] regardless of PID, so this exact shape produced the spurious
// "delta +35 over 81d" leak warning. Post-fix, only samples sharing the
// newest PID are compared, so the verdict is PASS.
func TestFDGrowthVerdict_CrossRestartNotALeak(t *testing.T) {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	h := fdHistoryFile{Samples: []fdHistorySample{
		{At: base, Count: 5, PID: 100},                                  // previous process lifetime
		{At: base.Add(81 * 24 * time.Hour), Count: 40, PID: 200},        // current process, first sample
		{At: base.Add(81*24*time.Hour + 7*time.Hour), Count: 40, PID: 200}, // current process, flat
	}}
	res := fdGrowthVerdict("fd", h)
	assert.Equal(t, StatusPass, res.Status,
		"cross-restart FD counts must not be compared as a leak signal")
}

// TestFDGrowthVerdict_SamePIDGrowthWarns ensures the E fix still catches a
// genuine within-lifetime leak: same PID, growth past the delta threshold
// over more than the minimum span.
func TestFDGrowthVerdict_SamePIDGrowthWarns(t *testing.T) {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	h := fdHistoryFile{Samples: []fdHistorySample{
		{At: base, Count: 10, PID: 300},
		{At: base.Add(7 * time.Hour), Count: 10 + fdGrowthWarnDelta + 5, PID: 300},
	}}
	res := fdGrowthVerdict("fd", h)
	assert.Equal(t, StatusWarn, res.Status,
		"real growth within one daemon lifetime must still warn")
}

// TestFDGrowthVerdict_SeedingCases covers the "not enough current-lifetime
// data" branches: an unknown PID (0), and a single same-PID sample.
func TestFDGrowthVerdict_SeedingCases(t *testing.T) {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	unknownPID := fdHistoryFile{Samples: []fdHistorySample{{At: base, Count: 10, PID: 0}}}
	assert.Equal(t, StatusPass, fdGrowthVerdict("fd", unknownPID).Status)

	singleSample := fdHistoryFile{Samples: []fdHistorySample{{At: base, Count: 10, PID: 300}}}
	assert.Equal(t, StatusPass, fdGrowthVerdict("fd", singleSample).Status)
}

// TestSamplesForCurrentLifetime verifies the segmentation helper returns only
// the trailing run sharing the newest PID.
func TestSamplesForCurrentLifetime(t *testing.T) {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	samples := []fdHistorySample{
		{At: base, Count: 5, PID: 100},
		{At: base.Add(time.Hour), Count: 6, PID: 100},
		{At: base.Add(2 * time.Hour), Count: 40, PID: 200},
		{At: base.Add(3 * time.Hour), Count: 41, PID: 200},
	}
	lifetime := samplesForCurrentLifetime(samples)
	require.Len(t, lifetime, 2)
	for _, s := range lifetime {
		assert.Equal(t, 200, s.PID)
	}
	assert.Nil(t, samplesForCurrentLifetime(nil))
}

// TestAlternateHeartbeatCandidate_Guards pins the F fix's guard logic: the
// alternate (daemon-derived) heartbeat identity is only computed for the
// project-root-derived check types, never for a nil resolver, an empty root,
// or a team check (team heartbeats key on team_id and are not subject to the
// workspace_id divergence).
func TestAlternateHeartbeatCandidate_Guards(t *testing.T) {
	_, _, ok := alternateHeartbeatCandidate("team", func() string { return "/whatever" })
	assert.False(t, ok, "team checks are not project-root-derived")

	_, _, ok = alternateHeartbeatCandidate("workspace", nil)
	assert.False(t, ok, "nil resolver yields no alternate")

	_, _, ok = alternateHeartbeatCandidate("ledger", func() string { return "" })
	assert.False(t, ok, "empty project root yields no alternate")

	_, _, ok = alternateHeartbeatCandidate("workspace", func() string { return t.TempDir() })
	assert.False(t, ok, "a root with no .sageox config has no repo_id, so no alternate")
}

// TestAlternateHeartbeatCandidate_DerivesDaemonWay pins the core of the F
// fix: when the resolver points at a project root that owns a .sageox config,
// the alternate identity is derived the SAME way the daemon derives it
// (config.GetRepoID + daemon.RepoBasedWorkspaceID). This is exactly the
// identity the doctor previously failed to match, producing false
// "not syncing" warnings when .sageox/ is not at the git top-level.
func TestAlternateHeartbeatCandidate_DerivesDaemonWay(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_testheartbeat"}))

	repoID, wsID, ok := alternateHeartbeatCandidate("workspace", func() string { return root })
	require.True(t, ok)
	assert.Equal(t, "repo_testheartbeat", repoID)
	assert.Equal(t, daemon.RepoBasedWorkspaceID(root), wsID,
		"alternate workspace_id must match the daemon's own derivation")
	assert.NotEmpty(t, wsID)
}
