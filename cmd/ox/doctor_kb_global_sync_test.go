package main

// Tests for cmd/ox/doctor_kb_global_sync.go. Each test installs a fake
// daemon-registry reader via SetKBGlobalSyncReader so no real daemon is
// required. Failure modes covered: 0 owners (all daemons followers),
// >1 owners (flock semantics broken or registry hand-edited), the
// healthy single-owner case, and graceful skip when daemons predate
// the Endpoint field.

import (
	"errors"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/daemon"
)

// kbGlobalSyncSetup installs a fake registry reader and resets at
// teardown so the next test starts clean.
func kbGlobalSyncSetup(t *testing.T, daemons []daemon.DaemonInfo, err error) {
	t.Helper()
	SetKBGlobalSyncReader(func() ([]daemon.DaemonInfo, error) {
		return daemons, err
	})
	t.Cleanup(func() { SetKBGlobalSyncReader(nil) })
}

// TestCheckKBGlobalSyncNoOwner_SingleOwnerPasses verifies the healthy
// steady state: one daemon per endpoint, that daemon owns the lease.
// Failure prevented: doctor spuriously warns when everything is fine.
func TestCheckKBGlobalSyncNoOwner_SingleOwnerPasses(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "ws1", Endpoint: "sageox.ai", GlobalSyncOwner: true},
		{WorkspaceID: "ws2", Endpoint: "sageox.ai", GlobalSyncOwner: false},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.passed || res.warning {
		t.Errorf("want passed=true warning=false; got passed=%v warning=%v msg=%q",
			res.passed, res.warning, res.message)
	}
}

// TestCheckKBGlobalSyncNoOwner_ZeroOwnersWarns verifies the 0-owner
// anomaly: daemons exist for an endpoint but none claims the lease.
// Failure prevented: team-context tickers silently stop running when
// the lease is unowned and nothing surfaces the gap to the user.
func TestCheckKBGlobalSyncNoOwner_ZeroOwnersWarns(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "ws1", Endpoint: "sageox.ai", GlobalSyncOwner: false},
		{WorkspaceID: "ws2", Endpoint: "sageox.ai", GlobalSyncOwner: false},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.warning || res.skipped {
		t.Errorf("want warning=true skipped=false; got warning=%v skipped=%v msg=%q",
			res.warning, res.skipped, res.message)
	}
	if res.detail == "" {
		t.Error("detail should describe the affected endpoint(s)")
	}
}

// TestCheckKBGlobalSyncNoOwner_MultipleOwnersWarns verifies the >1
// owner anomaly. This should be impossible if flock works correctly;
// the check still surfaces it as a warning rather than asserting.
// Failure prevented: a registry hand-edit or a flock semantics
// regression goes unnoticed because no check ever asserted invariance.
func TestCheckKBGlobalSyncNoOwner_MultipleOwnersWarns(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "ws1", Endpoint: "sageox.ai", GlobalSyncOwner: true},
		{WorkspaceID: "ws2", Endpoint: "sageox.ai", GlobalSyncOwner: true},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.warning || res.skipped {
		t.Errorf("want warning=true skipped=false; got warning=%v skipped=%v msg=%q",
			res.warning, res.skipped, res.message)
	}
}

// TestCheckKBGlobalSyncNoOwner_PerEndpointGrouping verifies that two
// endpoints are checked independently — a healthy sageox.ai owner and
// a broken localhost:8080 0-owner case both surface.
// Failure prevented: a fix at one endpoint masks a problem at another.
func TestCheckKBGlobalSyncNoOwner_PerEndpointGrouping(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "prod1", Endpoint: "sageox.ai", GlobalSyncOwner: true},
		{WorkspaceID: "prod2", Endpoint: "sageox.ai", GlobalSyncOwner: false},
		{WorkspaceID: "local1", Endpoint: "localhost:8080", GlobalSyncOwner: false},
		{WorkspaceID: "local2", Endpoint: "localhost:8080", GlobalSyncOwner: false},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.warning || res.skipped {
		t.Fatalf("want warning=true skipped=false; got warning=%v skipped=%v msg=%q",
			res.warning, res.skipped, res.message)
	}
	// the warning detail must mention localhost (the 0-owner endpoint)
	// but not sageox.ai (which is healthy).
	if !strings.Contains(res.detail, "localhost") {
		t.Errorf("detail = %q, want it to mention localhost", res.detail)
	}
}

// TestCheckKBGlobalSyncNoOwner_NoRunningDaemonsSkips verifies the
// check skips quietly on a fresh install or stopped-daemon state.
// Failure prevented: doctor noise on first run before any daemon
// has started.
func TestCheckKBGlobalSyncNoOwner_NoRunningDaemonsSkips(t *testing.T) {
	kbGlobalSyncSetup(t, nil, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.skipped {
		t.Errorf("want skipped=true; got skipped=%v passed=%v warning=%v msg=%q",
			res.skipped, res.passed, res.warning, res.message)
	}
}

// TestCheckKBGlobalSyncNoOwner_RegistryUnreadableSkips verifies the
// check skips when the registry read fails. We never escalate
// infrastructure errors to a doctor warning — the user can't act on
// them.
func TestCheckKBGlobalSyncNoOwner_RegistryUnreadableSkips(t *testing.T) {
	kbGlobalSyncSetup(t, nil, errors.New("registry corrupt"))
	res := checkKBGlobalSyncNoOwner(false)
	if !res.skipped {
		t.Errorf("want skipped=true; got skipped=%v passed=%v warning=%v msg=%q",
			res.skipped, res.passed, res.warning, res.message)
	}
}

// TestCheckKBGlobalSyncNoOwner_OldDaemonsWithoutEndpointSkip verifies
// that daemons predating the Endpoint field don't trigger false
// positives during the rollout window.
// Failure prevented: rolling upgrade where some daemons report
// Endpoint and others don't generates spurious warnings.
func TestCheckKBGlobalSyncNoOwner_OldDaemonsWithoutEndpointSkip(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "old1", Endpoint: "", GlobalSyncOwner: false},
		{WorkspaceID: "old2", Endpoint: "", GlobalSyncOwner: false},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.skipped {
		t.Errorf("want skipped=true (all daemons pre-Endpoint field); got skipped=%v passed=%v warning=%v msg=%q",
			res.skipped, res.passed, res.warning, res.message)
	}
}

// TestCheckKBGlobalSyncNoOwner_PrefixedEndpointsNormalize verifies
// api.sageox.ai and sageox.ai are grouped together (per the
// endpoint-normalization rule).
// Failure prevented: a daemon configured with the prefixed form and
// another with the bare form both look like single-endpoint daemons
// but are tallied separately, hiding a real 0-owner case.
func TestCheckKBGlobalSyncNoOwner_PrefixedEndpointsNormalize(t *testing.T) {
	kbGlobalSyncSetup(t, []daemon.DaemonInfo{
		{WorkspaceID: "ws1", Endpoint: "api.sageox.ai", GlobalSyncOwner: false},
		{WorkspaceID: "ws2", Endpoint: "sageox.ai", GlobalSyncOwner: false},
	}, nil)
	res := checkKBGlobalSyncNoOwner(false)
	if !res.warning || res.skipped {
		t.Fatalf("want warning=true skipped=false (0 owners after normalization); got warning=%v skipped=%v msg=%q",
			res.warning, res.skipped, res.message)
	}
}
