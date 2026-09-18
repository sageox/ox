package main

// Tests for cmd/ox/doctor_kb.go — the three knowledge-bubble doctor checks
// (orphans, failed-provision, stale-sync). Each test installs a focused
// kbDoctorHooks override and operates on a tmpdir-backed kb root, so no
// network, no daemon, and no real XDG data home is touched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbTestSetup wires kbDoctorActiveHooks to a fake kb root + injectable
// API/sync/GC behavior, and resets the hooks at teardown so the next test
// starts clean. The returned root is the parent of every <kb_id>/ dir; tests
// seed local bubbles by mkdir'ing under it.
func kbTestSetup(t *testing.T) (root string, hooks *kbDoctorHooks) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.MkdirAll(root, 0o755))
	h := &kbDoctorHooks{
		Root: func() (string, error) { return root, nil },
		Now:  time.Now,
	}
	SetKBDoctorHooks(*h)
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })
	return root, h
}

// seedKBDir creates <root>/<kbID>/ as if it were a cloned bubble. Optionally
// writes a meta.json with a specific last_sync timestamp so stale-sync
// scenarios are deterministic.
func seedKBDir(t *testing.T, root, kbID string, lastSync *time.Time) string {
	t.Helper()
	dir := filepath.Join(root, kbID)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
	if lastSync != nil {
		meta := map[string]any{"last_sync": lastSync.Format(time.RFC3339Nano)}
		data, err := json.Marshal(meta)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "meta.json"), data, 0o644))
	}
	return dir
}

// applyHooks re-applies the in-memory hooks struct to the package-global
// after a test mutates it (e.g. swapping the lister mid-test). Without
// this, the package-global keeps the original copy from kbTestSetup.
func applyHooks(h *kbDoctorHooks) { SetKBDoctorHooks(*h) }

// ----------------------------------------------------------------------
// Check 1: orphan detection
// ----------------------------------------------------------------------

// TestCheckKBOrphans_DetectsLocalDirNotInAPI verifies the orphan check fires
// when a kb_id exists locally but is missing from the kb API list.
//
// Failure prevented: revoked or deleted bubbles silently consume disk space
// forever because doctor never notices them.
func TestCheckKBOrphans_DetectsLocalDirNotInAPI(t *testing.T) {
	root, h := kbTestSetup(t)
	seedKBDir(t, root, "kb_orphan", nil)
	seedKBDir(t, root, "kb_kept", nil)

	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{{KBID: "kb_kept", Slug: "kept"}}, nil
	}
	applyHooks(h)

	result := checkKBOrphans(false)
	assert.True(t, result.passed && result.warning, "orphan must surface as warning")
	assert.Contains(t, result.message, "kb_orphan")
	assert.NotContains(t, result.message, "kb_kept")
}

// TestCheckKBOrphans_AutoFixTriggersGCAndRechecksClean verifies the AutoFix
// path: GC hook is invoked, and a follow-up readdir confirms the orphan
// is gone before the check returns Passed.
//
// Failure prevented: the autofix is invoked but the check optimistically
// returns success without confirming the orphan was actually moved.
func TestCheckKBOrphans_AutoFixTriggersGCAndRechecksClean(t *testing.T) {
	root, h := kbTestSetup(t)
	orphan := seedKBDir(t, root, "kb_orphan", nil)

	gcCalls := 0
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{}, nil
	}
	h.GC = func(_ context.Context) error {
		gcCalls++
		// simulate the daemon's kb-GC moving the orphan to .trash/.
		return os.Rename(orphan, filepath.Join(root, ".trash-"+filepath.Base(orphan)))
	}
	applyHooks(h)

	result := checkKBOrphans(true)
	assert.Equal(t, 1, gcCalls, "GC hook must be invoked exactly once")
	assert.True(t, result.passed && !result.warning, "post-fix must be clean pass; got %+v", result)
	assert.NoDirExists(t, orphan, "orphan dir must be moved aside")
}

// TestCheckKBOrphans_AutoFixThroughDaemon_MovesOnlyThisProjectsOrphans runs the
// orphan autofix from a git project bound to one team, with the production GC
// and root hooks, over a real socket, into a real SyncScheduler kb GC pass for
// the same project. Only the kb API is faked. The kb root also holds another
// team's bubble, as it does for anyone on two teams.
//
// Failures prevented:
//   - the autofix sent trigger_gc, which recloned every team context and the
//     ledger instead of running kb GC. With 8 team contexts it timed out at
//     30s, and the orphan stayed in place either way. kb-orphans is
//     FixLevelAuto, so that happened on every plain `ox doctor`.
//   - the autofix moving another team's bubble into .trash/, because this
//     project's scoped kb list never includes it.
func TestCheckKBOrphans_AutoFixThroughDaemon_MovesOnlyThisProjectsOrphans(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "") // legacy mode would ignore the XDG dirs below
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")
	ep := endpoint.Get()

	// Doctor and the scheduler both derive the kb scopes they judge from this
	// project's team binding.
	project := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", project).Run())
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{Endpoint: ep, TeamID: "team_kbgc"}))
	require.NoError(t, os.MkdirAll(config.DefaultTeamContextPath("team_kbgc", ep), 0o755))
	t.Chdir(project)

	kbRoot := paths.KBDir(ep, "")
	orphan := seedScopedKBDir(t, kbRoot, "kb_orphan", "team_kbgc")
	kept := seedScopedKBDir(t, kbRoot, "kb_kept", "team_kbgc")
	otherTeam := seedScopedKBDir(t, kbRoot, "kb_other_team", "team_other")

	lister := &compatFakeKBSource{listFn: func() ([]api.KB, error) {
		return []api.KB{{KBID: "kb_kept", ScopeType: api.KBScopeTypeTeam, ScopeID: "team_kbgc"}}, nil
	}}
	SetKBDoctorHooks(kbDoctorHooks{
		List: func(ctx context.Context) ([]api.KB, error) { return lister.ListBubbles(ctx, api.KBScope{}) },
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	cfg := daemon.DefaultConfig()
	cfg.ProjectRoot = project
	scheduler := daemon.NewSyncScheduler(cfg, slog.New(slog.DiscardHandler))
	scheduler.SetKBBubbleListerFactory(func(_, _ string) daemon.KBBubbleLister { return lister })

	// The kb lock dir is resolved once per process. Resolve it before
	// XDG_RUNTIME_DIR moves to a temp dir that cleanup deletes, so a later
	// test never inherits a lock dir that no longer exists.
	unlock, _, err := daemon.AcquireKBLock("kb_kept")
	require.NoError(t, err)
	if unlock != nil {
		unlock()
	}
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-kbgc-") // short: macOS caps socket paths at 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	ctx, cancel := context.WithCancel(context.Background())
	var reclones atomic.Int32
	server := daemon.NewServerWithService(slog.New(slog.DiscardHandler), &testutil.MockService{
		TriggerKBGCFunc: func() { scheduler.TriggerKBGC(ctx) },
		TriggerGCFunc: func() *daemon.TriggerGCResponse {
			reclones.Add(1)
			return scheduler.TriggerGC(ctx)
		},
	})
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, daemon.IsRunning, 2*time.Second, 10*time.Millisecond, "IPC server never answered ping")

	result := checkKBOrphans(true)

	assert.True(t, result.passed && !result.warning, "autofix must clear the orphan; got %+v", result)
	assert.Equal(t, "triaged 1 orphan(s) to .trash/", result.message, "only this project's orphan may be reported")
	assert.NoDirExists(t, orphan, "the orphan must leave the kb root")
	assert.DirExists(t, filepath.Join(kbRoot, ".trash"), "the orphan must be moved to .trash/, not deleted")
	assert.DirExists(t, kept, "a bubble the kb API still lists must stay")
	assert.DirExists(t, otherTeam, "another team's bubble must stay")
	assert.Zero(t, reclones.Load(), "the autofix must not start trigger_gc's reclone sweep")
}

// seedScopedKBDir creates <root>/<kbID>/ with the team scope the daemon
// records in .sageox/meta.json for a synced bubble.
func seedScopedKBDir(t *testing.T, root, kbID, teamID string) string {
	t.Helper()
	dir := seedKBDir(t, root, kbID, nil)
	meta := fmt.Sprintf(`{"scope_type":%q,"scope_id":%q}`, api.KBScopeTypeTeam, teamID)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "meta.json"), []byte(meta), 0o644))
	return dir
}

// TestCheckKBOrphans_AutoFixTimedOutButDaemonFinished checks that the result
// comes from disk when the daemon call fails. The daemon does not abandon a
// pass because the client stopped waiting, so triage can land after the
// client's deadline elapses.
//
// Failure prevented (#941): reporting a failed auto-fix for work that
// succeeded — the misdirection that issue was filed about.
func TestCheckKBOrphans_AutoFixTimedOutButDaemonFinished(t *testing.T) {
	root, h := kbTestSetup(t)
	orphan := seedKBDir(t, root, "kb_orphan", nil)
	h.List = func(context.Context) ([]api.KB, error) { return nil, nil }
	h.GC = func(context.Context) error {
		// the daemon triaged the orphan, then our read deadline elapsed
		trash := filepath.Join(root, ".trash")
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return err
		}
		if err := os.Rename(orphan, filepath.Join(trash, "kb_orphan-2026-09-17T00:00:00Z")); err != nil {
			return err
		}
		return errors.New("read: read unix ->/tmp/sageox/daemon/daemon-752e705d.sock: i/o timeout")
	}
	applyHooks(h)

	result := checkKBOrphans(true)

	assert.True(t, result.passed && !result.warning, "disk shows the orphan gone; got %+v", result)
	assert.Equal(t, "1 orphan(s) no longer in the kb root", result.message,
		"the move-aside was never confirmed, so the message must not claim it")
	assert.NoDirExists(t, orphan)
}

// TestCheckKBOrphans_AutoFixFailureDetail pins the detail line of a failed
// orphan autofix: it names the command to run next instead of relaying a raw
// IPC error.
func TestCheckKBOrphans_AutoFixFailureDetail(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, h *kbDoctorHooks)
		wantContain []string
		wantAbsent  []string
	}{
		{
			// Failure prevented: right after an upgrade, a daemon started by
			// the previous ox answered with a bare "unknown message type".
			name: "daemon predates trigger_kb_gc",
			setup: func(t *testing.T, _ *kbDoctorHooks) {
				t.Setenv("OX_XDG_DISABLE", "") // legacy mode would ignore XDG_RUNTIME_DIR
				runtimeDir, err := os.MkdirTemp("/tmp", "ox-kbgc-")
				require.NoError(t, err)
				t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
				t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
				oldDaemon := testutil.NewMockDaemon() // answers trigger_kb_gc with "unknown message type"
				oldDaemon.Start(t)
				t.Cleanup(oldDaemon.Stop)
			},
			wantContain: []string{"ox daemon restart"},
			wantAbsent:  []string{"unknown message type"},
		},
		{
			// Failure prevented: "Auto-fix failed: read: read unix ->
			// <socket>: i/o timeout" sent the reader after the socket.
			name: "client deadline passes",
			setup: func(_ *testing.T, h *kbDoctorHooks) {
				h.GC = func(context.Context) error {
					return errors.New("read: read unix ->/tmp/sageox/daemon/daemon-752e705d.sock: i/o timeout")
				}
			},
			wantContain: []string{"daemon did not complete the auto-fix", "ox daemon status"},
			wantAbsent:  []string{"Auto-fix failed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, h := kbTestSetup(t)
			seedKBDir(t, root, "kb_orphan", nil)
			h.List = func(context.Context) ([]api.KB, error) { return nil, nil }
			tc.setup(t, h)
			applyHooks(h)

			result := checkKBOrphans(true)

			assert.False(t, result.passed, "the orphan is still present; got %+v", result)
			for _, want := range tc.wantContain {
				assert.Contains(t, result.detail, want)
			}
			for _, absent := range tc.wantAbsent {
				assert.NotContains(t, result.detail, absent)
			}
		})
	}
}

// TestCheckKBOrphans_NoOrphansPasses verifies the happy path: every local
// kb_id is in the API list, so the check passes without invoking GC.
//
// Failure prevented: false-positive orphan reports churning the user with
// noise on every doctor run.
func TestCheckKBOrphans_NoOrphansPasses(t *testing.T) {
	root, h := kbTestSetup(t)
	seedKBDir(t, root, "kb_one", nil)
	seedKBDir(t, root, "kb_two", nil)

	gcCalls := 0
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{
			{KBID: "kb_one"},
			{KBID: "kb_two"},
		}, nil
	}
	h.GC = func(_ context.Context) error { gcCalls++; return nil }
	applyHooks(h)

	result := checkKBOrphans(true) // even with fix=true, no GC should run
	assert.True(t, result.passed && !result.warning, "must pass cleanly")
	assert.Equal(t, 0, gcCalls, "GC must not run when there are no orphans")
}

// TestCheckKBOrphans_APIUnavailable_Skips verifies that when the kb API
// returns ErrKBAPIUnavailable, the orphan check skips entirely rather than
// surfacing a doctor warning.
//
// Failure prevented: users without the knowledge-bubbles flag (or behind a
// transient API outage) see false orphan warnings on every doctor run.
func TestCheckKBOrphans_APIUnavailable_Skips(t *testing.T) {
	root, h := kbTestSetup(t)
	seedKBDir(t, root, "kb_local", nil)

	h.List = func(_ context.Context) ([]api.KB, error) {
		return nil, api.ErrKBAPIUnavailable
	}
	applyHooks(h)

	result := checkKBOrphans(false)
	assert.True(t, result.skipped, "API-unavailable must skip, not warn")
	assert.False(t, !result.passed && !result.skipped, "must not be a hard failure")
}

// ----------------------------------------------------------------------
// Check 2: failed-provisioning
// ----------------------------------------------------------------------

// TestCheckKBFailedProvision_FlagsFailedRow verifies the check surfaces
// any kb row with lifecycle_state="provision-failed" as agent-required
// (no autofix possible — server-side state).
//
// Failure prevented: a stuck-in-provisioning bubble silently eats quota
// without the user noticing.
func TestCheckKBFailedProvision_FlagsFailedRow(t *testing.T) {
	_, h := kbTestSetup(t)
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{
			{KBID: "kb_ok", Slug: "ok", LifecycleState: "active"},
			{KBID: "kb_bad", Slug: "bad", LifecycleState: "provision-failed"},
		}, nil
	}
	applyHooks(h)

	result := checkKBFailedProvision(false)
	assert.True(t, result.requiresAgent, "failed provisioning must route to agent/human bucket")
	assert.True(t, result.warning, "must be a warning rather than hard fail")
	assert.Contains(t, result.message, "bad")
	assert.NotContains(t, result.message, "ok")
}

// TestCheckKBFailedProvision_AllActivePasses verifies the check is silent
// when every bubble's lifecycle is healthy.
//
// Failure prevented: noise on every doctor run for users with healthy
// bubbles.
func TestCheckKBFailedProvision_AllActivePasses(t *testing.T) {
	_, h := kbTestSetup(t)
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{
			{KBID: "kb_a", LifecycleState: "active"},
			{KBID: "kb_b", LifecycleState: ""},
		}, nil
	}
	applyHooks(h)

	result := checkKBFailedProvision(false)
	assert.True(t, result.passed && !result.warning, "must be clean pass")
	assert.False(t, result.requiresAgent)
}

// TestCheckKBFailedProvision_APIUnavailable_Skips verifies the check skips
// when the kb API can't be reached, mirroring the orphan check's posture.
//
// Failure prevented: the check generates spurious warnings during an API
// outage when we have no source-of-truth to consult.
func TestCheckKBFailedProvision_APIUnavailable_Skips(t *testing.T) {
	_, h := kbTestSetup(t)
	h.List = func(_ context.Context) ([]api.KB, error) {
		return nil, api.ErrKBAPIUnavailable
	}
	applyHooks(h)

	result := checkKBFailedProvision(false)
	assert.True(t, result.skipped, "API-unavailable must skip")
}

// ----------------------------------------------------------------------
// Check 3: stale sync
// ----------------------------------------------------------------------

// TestCheckKBStaleSync_FlagsOldMeta verifies a bubble whose meta.json
// last_sync is older than 1h is reported as stale.
//
// Failure prevented: the daemon stops syncing a bubble (crashed, network,
// auth) and the user has no signal that their context is stale.
func TestCheckKBStaleSync_FlagsOldMeta(t *testing.T) {
	root, h := kbTestSetup(t)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	seedKBDir(t, root, "kb_stale", &twoHoursAgo)
	freshTs := time.Now().Add(-5 * time.Minute)
	seedKBDir(t, root, "kb_fresh", &freshTs)
	applyHooks(h)

	result := checkKBStaleSync(false)
	assert.True(t, result.passed && result.warning, "stale must surface as warning")
	assert.Contains(t, result.message, "kb_stale")
	assert.NotContains(t, result.message, "kb_fresh")
}

// TestCheckKBStaleSync_AutoFixKicksDaemonAndConfirms verifies AutoFix calls
// the sync hook and rechecks meta.json afterwards. Simulates the daemon
// re-writing a fresh last_sync.
//
// Failure prevented: AutoFix returns "fixed" without verifying the daemon
// actually did anything.
func TestCheckKBStaleSync_AutoFixKicksDaemonAndConfirms(t *testing.T) {
	root, h := kbTestSetup(t)
	old := time.Now().Add(-3 * time.Hour)
	staleDir := seedKBDir(t, root, "kb_stale", &old)

	syncCalls := 0
	h.Sync = func(_ context.Context) error {
		syncCalls++
		// simulate daemon completing its pull and bumping last_sync.
		fresh := time.Now()
		meta := map[string]any{"last_sync": fresh.Format(time.RFC3339Nano)}
		data, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(staleDir, ".sageox", "meta.json"), data, 0o644)
	}
	applyHooks(h)

	result := checkKBStaleSync(true)
	assert.Equal(t, 1, syncCalls, "sync hook must be invoked")
	assert.True(t, result.passed && !result.warning, "must pass after refresh; got %+v", result)
}

// TestCheckKBStaleSync_DaemonNotRunning_HintsUser verifies the auto-fix
// surfaces a useful hint when the daemon is unreachable instead of failing
// silently.
//
// Failure prevented: the user runs `ox doctor --fix` and sees the stale
// bubble still warned without any explanation of what to do.
func TestCheckKBStaleSync_DaemonNotRunning_HintsUser(t *testing.T) {
	root, h := kbTestSetup(t)
	old := time.Now().Add(-2 * time.Hour)
	seedKBDir(t, root, "kb_stale", &old)

	h.Sync = func(_ context.Context) error {
		return errors.New("daemon not running — run `ox daemon start` to enable kb sync")
	}
	applyHooks(h)

	result := checkKBStaleSync(true)
	assert.True(t, result.warning, "must remain a warning so user sees the hint")
	assert.Contains(t, result.detail, "daemon not running")
}

// TestCheckKBStaleSync_NoMetaSkips verifies bubbles without meta.json yet
// (initial-clone-pending) are not flagged as stale — the daemon hasn't had
// a chance to write the file.
//
// Failure prevented: every fresh clone gets one false stale warning until
// the next daemon pass writes meta.json.
func TestCheckKBStaleSync_NoMetaSkips(t *testing.T) {
	root, h := kbTestSetup(t)
	// seed a bubble dir but do NOT write meta.json
	require.NoError(t, os.MkdirAll(filepath.Join(root, "kb_pending"), 0o755))
	applyHooks(h)

	result := checkKBStaleSync(false)
	// "no meta.json yet" path returns SkippedCheck
	assert.True(t, result.skipped, "missing meta.json must skip, not warn; got %+v", result)
}

// TestCheckKBStaleSync_RunsEvenWithoutAPI verifies the stale-sync check
// does NOT depend on the kb API: it walks meta.json files locally and
// kicks the daemon for fixes. This is critical because staleness is most
// likely exactly when the API is also flaky.
//
// Failure prevented: an API outage hides stale bubbles from the user even
// though the local signal is perfectly available.
func TestCheckKBStaleSync_RunsEvenWithoutAPI(t *testing.T) {
	root, h := kbTestSetup(t)
	old := time.Now().Add(-2 * time.Hour)
	seedKBDir(t, root, "kb_stale", &old)

	h.List = func(_ context.Context) ([]api.KB, error) {
		return nil, api.ErrKBAPIUnavailable
	}
	applyHooks(h)

	result := checkKBStaleSync(false)
	assert.True(t, result.warning, "stale-sync must run independent of API availability")
	assert.Contains(t, result.message, "kb_stale")
}

// ----------------------------------------------------------------------
// runKBChecks aggregator: independent failure isolation
// ----------------------------------------------------------------------

// TestRunKBChecks_SkipsAPIChecksWhenAPIUnavailable_RunsStaleSync verifies
// that an unreachable kb API disables checks 1+2 but leaves check 3
// (stale-sync) running normally.
//
// Failure prevented: a single API failure dropping ALL kb signals,
// including the local-only stale-sync check.
func TestRunKBChecks_SkipsAPIChecksWhenAPIUnavailable_RunsStaleSync(t *testing.T) {
	root, h := kbTestSetup(t)
	old := time.Now().Add(-2 * time.Hour)
	seedKBDir(t, root, "kb_stale", &old)

	h.List = func(_ context.Context) ([]api.KB, error) {
		return nil, api.ErrKBAPIUnavailable
	}
	applyHooks(h)

	// stub the global-sync owner registry reader so this test doesn't
	// race against the developer's real running daemons.
	SetKBGlobalSyncReader(func() ([]daemon.DaemonInfo, error) { return nil, nil })
	t.Cleanup(func() { SetKBGlobalSyncReader(nil) })

	results := runKBChecks(doctorOptions{fix: false})
	// Seven checks expected, in order: orphans, provisioning, stale-sync,
	// missing-clone, wedged, sparse-checkout (repo-health parity), then
	// global-sync-owner (ox-6zme).
	require.Len(t, results, 7, "all kb checks must run")

	// orphans (idx 0), provisioning (idx 1), missing-clone (idx 3) all depend
	// on the kb API and must skip when it's unavailable.
	assert.True(t, results[0].skipped, "orphans must skip when API unavailable")
	assert.True(t, results[1].skipped, "provisioning must skip when API unavailable")
	assert.True(t, results[3].skipped, "missing-clone must skip when API unavailable")

	// stale-sync (idx 2) must still produce a real signal — it's local-only.
	assert.True(t, results[2].warning, "stale-sync must surface even without API; got %+v", results[2])
	assert.Contains(t, results[2].message, "kb_stale")

	// wedged (idx 4) and sparse-checkout (idx 5) are local-only repo-health
	// checks; with only a metadata-seeded (non-git) bubble they run cleanly.
	assert.True(t, results[4].passed, "wedged must run locally; got %+v", results[4])
	assert.NotEmpty(t, results[5].name, "sparse-checkout check must run")

	// global-sync-owner (idx 6) is independent of the kb API; it reads the
	// daemon registry. Just assert it ran (no panic).
	assert.NotEmpty(t, results[6].name, "global-sync-owner check must run")
}

// TestRunKBChecks_PanicInOneCheckDoesNotCascade verifies the per-check
// recover wrapper: if the orphan check panics for any reason, stale-sync
// still runs.
//
// Failure prevented: a single buggy check taking down all kb diagnostics,
// hiding real issues from the user.
func TestRunKBChecks_PanicInOneCheckDoesNotCascade(t *testing.T) {
	root, h := kbTestSetup(t)
	old := time.Now().Add(-2 * time.Hour)
	seedKBDir(t, root, "kb_stale", &old)

	h.List = func(_ context.Context) ([]api.KB, error) {
		panic("simulated kb api crash")
	}
	applyHooks(h)

	// stub the global-sync owner registry reader so this test doesn't
	// race against the developer's real running daemons.
	SetKBGlobalSyncReader(func() ([]daemon.DaemonInfo, error) { return nil, nil })
	t.Cleanup(func() { SetKBGlobalSyncReader(nil) })

	results := runKBChecks(doctorOptions{fix: false})
	// Seven checks expected: orphans, provisioning, stale-sync, missing-clone,
	// wedged, sparse-checkout, global-sync-owner. The List panic only takes
	// down the API-backed checks; the rest run independently.
	require.Len(t, results, 7, "every check must run independently")

	// orphan check (which calls List) panicked → recovered → reported as failure
	assert.False(t, results[0].passed, "orphan check should fail after panic")

	// stale-sync (idx 2) is local-only; panic in orphan must not affect it
	assert.True(t, results[2].warning, "stale-sync must run even after orphan panic")
	assert.Contains(t, results[2].message, "kb_stale")

	// missing-clone (idx 3) also calls List → recovered panic → not passed.
	assert.False(t, results[3].passed, "missing-clone should fail after List panic")

	// global-sync-owner (idx 6) is independent of the kb API panic.
	assert.NotEmpty(t, results[6].name, "global-sync-owner check must run despite orphan panic")
}
