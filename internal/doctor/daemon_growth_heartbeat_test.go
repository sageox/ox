package doctor

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFDGrowthVerdict pins the E fix: FD growth is judged only within the
// current daemon lifetime (samples sharing the newest PID), never across
// restarts.
func TestFDGrowthVerdict(t *testing.T) {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		samples []fdHistorySample
		want    Status
	}{
		{
			// A history that spans a daemon restart (a low-count old PID
			// followed by a higher but flat new PID) must NOT warn. The
			// pre-fix code compared history[0] vs history[last] regardless of
			// PID, so this exact shape produced the spurious "delta +35 over
			// 81d" leak warning.
			name: "cross-restart counts are not a leak signal",
			samples: []fdHistorySample{
				{At: base, Count: 5, PID: 100},                                     // previous process lifetime
				{At: base.Add(81 * 24 * time.Hour), Count: 40, PID: 200},           // current process, first sample
				{At: base.Add(81*24*time.Hour + 7*time.Hour), Count: 40, PID: 200}, // current process, flat
			},
			want: StatusPass,
		},
		{
			// Same PID, growth past the delta threshold over more than the
			// minimum span — a genuine within-lifetime leak still warns.
			name: "same-PID growth within one lifetime warns",
			samples: []fdHistorySample{
				{At: base, Count: 10, PID: 300},
				{At: base.Add(7 * time.Hour), Count: 10 + fdGrowthWarnDelta + 5, PID: 300},
			},
			want: StatusWarn,
		},
		{
			name:    "unknown PID (0) seeds without a verdict",
			samples: []fdHistorySample{{At: base, Count: 10, PID: 0}},
			want:    StatusPass,
		},
		{
			name:    "single same-PID sample is insufficient data",
			samples: []fdHistorySample{{At: base, Count: 10, PID: 300}},
			want:    StatusPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := fdGrowthVerdict("fd", fdHistoryFile{Samples: tt.samples})
			assert.Equal(t, tt.want, res.Status)
		})
	}
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
	tests := []struct {
		name            string
		checkType       string
		findProjectRoot func() string
	}{
		{
			name:            "team checks are not project-root-derived",
			checkType:       "team",
			findProjectRoot: func() string { return "/whatever" },
		},
		{
			name:            "nil resolver yields no alternate",
			checkType:       "workspace",
			findProjectRoot: nil,
		},
		{
			name:            "empty project root yields no alternate",
			checkType:       "ledger",
			findProjectRoot: func() string { return "" },
		},
		{
			name:            "a root with no .sageox config has no repo_id",
			checkType:       "workspace",
			findProjectRoot: func() string { return t.TempDir() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := alternateHeartbeatCandidate(tt.checkType, tt.findProjectRoot)
			assert.False(t, ok)
		})
	}
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
