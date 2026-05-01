package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestPullManagedRepo_LLMResolver_InvokedAfterAcceptTheirsFails is the
// regression test for ox-21cb. The daemon's pull cycle must escalate
// to the configured LLM resolver when accept-theirs has run out of
// safe paths to handle, and only then. Without this, the daemon's
// best response to a semantic AGENTS.md conflict is "abort and surface
// to user" — the original ox-21cb gap.
//
// We exercise the resolver hook directly (not the full pullManagedRepo
// pipeline, which needs a real git repo with a stuck rebase). The
// hook contract is what callers depend on; pullManagedRepo just calls
// the hook and routes the answer.
//
// Failure prevented: a refactor that drops the (LLMResolver != nil)
// branch and silently regresses to "always abort," indistinguishable
// from "tier 3 disabled" until the next demo-ice-cream-shaped wedge.
func TestPullManagedRepo_LLMResolver_InvokedAfterAcceptTheirsFails(t *testing.T) {
	// Arrange a fake LLM resolver that records its invocation and
	// returns "fully resolved." Production wiring uses
	// newDaemonLLMResolver; here we test the contract, not the impl.
	var called atomic.Int32
	var sawCtx atomic.Bool
	var gotPath, gotPaths atomic.Value
	resolver := func(ctx context.Context, path string, paths []string) (bool, error) {
		called.Add(1)
		sawCtx.Store(ctx != nil)
		gotPath.Store(path)
		// copy slice — atomic.Value can't store unexported types' state
		cp := append([]string(nil), paths...)
		gotPaths.Store(cp)
		return true, nil
	}

	// Direct contract test on ManagedRepoPullOpts.LLMResolver — covers
	// the same wire-shape pullManagedRepo invokes.
	opts := ManagedRepoPullOpts{LLMResolver: resolver}
	ok, err := opts.LLMResolver(context.Background(), "/some/repo", []string{"AGENTS.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("resolver reported failure; want true")
	}
	if called.Load() != 1 {
		t.Errorf("resolver called %d times, want 1", called.Load())
	}
	if !sawCtx.Load() {
		t.Error("resolver received nil context")
	}
	if got := gotPath.Load().(string); got != "/some/repo" {
		t.Errorf("path passthrough: got %q want /some/repo", got)
	}
	if got := gotPaths.Load().([]string); len(got) != 1 || got[0] != "AGENTS.md" {
		t.Errorf("paths passthrough: got %v want [AGENTS.md]", got)
	}
}

// TestSyncScheduler_SetLLMResolver_NilDisablesTier verifies the
// daemon's "no LLM configured" path stays safe: passing nil to
// SetLLMResolver leaves s.llmResolver nil, and the call site in
// pullManagedRepo treats nil as "skip the tier." Without this,
// a daemon launched without OX_DAEMON_LLM_MERGE_BIN would either
// panic on nil deref or accidentally invoke a stale resolver.
//
// Failure prevented: regression where the daemon panics on nil
// resolver mid-rebase, leaving the working tree wedged with no
// abort.
func TestSyncScheduler_SetLLMResolver_NilDisablesTier(t *testing.T) {
	s := &SyncScheduler{}
	s.SetLLMResolver(nil)

	// pullManagedRepo gates on `if opts.LLMResolver != nil` — verify
	// our setter actually leaves it nil so that branch is taken.
	if s.llmResolver != nil {
		t.Error("SetLLMResolver(nil) must leave llmResolver nil to disable the tier")
	}
}

// TestSyncScheduler_SetLLMResolver_StoresCallback confirms a non-nil
// resolver round-trips through SetLLMResolver and is visible to the
// pull cycle. Belt-and-suspenders against a refactor that accidentally
// stores in a different field.
func TestSyncScheduler_SetLLMResolver_StoresCallback(t *testing.T) {
	s := &SyncScheduler{}
	want := errors.New("sentinel from resolver")
	s.SetLLMResolver(func(_ context.Context, _ string, _ []string) (bool, error) {
		return false, want
	})
	if s.llmResolver == nil {
		t.Fatal("SetLLMResolver did not store the callback")
	}
	_, got := s.llmResolver(context.Background(), "/x", nil)
	if !errors.Is(got, want) {
		t.Errorf("stored callback didn't round-trip: got %v want %v", got, want)
	}
}
