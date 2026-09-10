package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mark in the "Syncing" block must report SYNC state, not merely that a
// directory exists.
//
// Failure prevented: `ox status` showed a green ✓ for all three team contexts
// throughout a five-hour team-context sync outage, because the mark was driven
// by ws.Exists ("is this a git repo") while sitting under a heading that reads
// "Syncing". `ox daemon status`, reading the same LastSync field, reported
// "not synced" correctly the whole time. Anyone checking the obvious command
// was told everything was healthy.
func TestRenderDaemonSyncSection_MarkReportsSyncNotExistence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	now := time.Now()
	ds := &daemon.StatusData{
		Running: true,
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"ledger": {
				{ID: "ledger", Type: "ledger", Path: "/l", Exists: true, LastSync: now.Add(-30 * time.Second)},
			},
			"team-context": {
				// Cloned AND synced.
				{ID: "t1", Type: "team-context", TeamName: "Synced Team", Path: "/t1", Exists: true, LastSync: now.Add(-5 * time.Second)},
				// Cloned but never synced — the outage state. This is the one
				// that used to render as a bare ✓.
				{ID: "t2", Type: "team-context", TeamName: "Never Synced", Path: "/t2", Exists: true},
				// Not cloned at all.
				{ID: "t3", Type: "team-context", TeamName: "Not Cloned", CloneURL: "https://git.example.test/x.git"},
			},
		},
	}

	out := renderDaemonSyncSection(ds, nil, &config.LocalConfig{}, false, true)

	line := func(label string) string {
		t.Helper()
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, label) {
				return l
			}
		}
		require.Failf(t, "missing line", "no line for %q in:\n%s", label, out)
		return ""
	}

	// A synced workspace still earns the check, with its age.
	assert.Contains(t, line("Synced Team"), "✓")
	assert.Contains(t, line("Synced Team"), "just now")
	assert.Contains(t, line("ledger"), "✓")

	// A never-synced workspace must NOT be marked healthy, and must say so.
	neverSynced := line("Never Synced")
	assert.NotContains(t, neverSynced, "✓",
		"a cloned-but-never-synced team context must not render as healthy")
	assert.Contains(t, neverSynced, "not synced")

	// A missing checkout stays distinguishable from a stalled sync, and still
	// surfaces where it would come from.
	notCloned := line("Not Cloned")
	assert.NotContains(t, notCloned, "✓")
	assert.Contains(t, notCloned, "https://git.example.test/x.git")
}

// A workspace that exists but has never synced and has no clone URL must still
// be reported honestly rather than falling through to a blank mark.
func TestRenderDaemonSyncSection_NotClonedWithoutURLStillReported(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	ds := &daemon.StatusData{
		Running: true,
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"team-context": {
				{ID: "t1", Type: "team-context", TeamName: "Orphan", Path: "/t1"},
			},
		},
	}

	out := renderDaemonSyncSection(ds, nil, &config.LocalConfig{}, false, true)
	require.Contains(t, out, "Orphan")
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Orphan") {
			assert.NotContains(t, l, "✓", "an un-cloned workspace must not read as healthy")
			assert.Contains(t, l, "not cloned")
		}
	}
}
