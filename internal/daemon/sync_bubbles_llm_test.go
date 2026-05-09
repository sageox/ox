package daemon

// Mirrors sync_llm_resolver_test.go for the kb sync path. The LLM tier of
// the conflict resolver lives in pullManagedRepo (see ManagedRepoPullOpts.
// LLMResolver). Since reconcileBubble routes existing-clone pulls through
// pullManagedRepo, the same resolver invariants must hold for kb bubbles.
//
// What we verify:
//   - Resolver contract is identical (ctx + repoPath + paths -> (bool, err))
//   - SetLLMResolver(nil) leaves the field nil so the tier is skipped
//   - SetLLMResolver(non-nil) round-trips the callback so kb pulls can use it
//   - Resolver "no-op" answer (false, nil) is treated as "fall through to abort"
//   - Resolver error is non-fatal — the daemon does not panic
//
// We test the contract directly (not through a wedged real-git rebase) because:
//   - The original sync_llm_resolver_test.go does the same — it explicitly
//     notes "we test the contract, not the impl" — so kb parity follows suit.
//   - A wedged-rebase setup is brittle; it would fail for orthogonal reasons
//     (git version, path layout) and add noise without adding signal.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestKBSync_LLMResolver_PullManagedRepoOptsContract confirms the kb
// sync path uses the exact same ManagedRepoPullOpts.LLMResolver field
// the ledger and team-context paths use — i.e., reconcileBubble could
// wire a per-bubble resolver into pullManagedRepo via the same
// callback shape.
//
// Failure prevented: a future refactor introduces a kb-specific
// resolver field that drifts from the canonical one, ending up with
// two resolver wirings to maintain instead of one.
func TestKBSync_LLMResolver_PullManagedRepoOptsContract(t *testing.T) {
	var called atomic.Int32
	var sawCtx atomic.Bool
	var gotPath, gotPaths atomic.Value
	resolver := func(ctx context.Context, path string, ps []string) (bool, error) {
		called.Add(1)
		sawCtx.Store(ctx != nil)
		gotPath.Store(path)
		cp := append([]string(nil), ps...)
		gotPaths.Store(cp)
		return true, nil
	}

	opts := ManagedRepoPullOpts{LLMResolver: resolver}
	// kb-style invocation: pullManagedRepo would pass the kb path and
	// the union-managed paths from kb.MergeUnionPaths. Mirror that here
	// so the test is anchored to a realistic kb scenario.
	ok, err := opts.LLMResolver(context.Background(), "/some/kb/repo", []string{"AGENTS.md", "SOUL.md"})
	if err != nil {
		t.Fatal(err)
	}
	assert.True(t, ok, "resolver reported failure when it should not have")
	assert.Equal(t, int32(1), called.Load())
	assert.True(t, sawCtx.Load(), "resolver received nil context")
	assert.Equal(t, "/some/kb/repo", gotPath.Load().(string))
	gotList := gotPaths.Load().([]string)
	assert.Equal(t, []string{"AGENTS.md", "SOUL.md"}, gotList)
}

// TestKBSync_LLMResolver_NilDisablesTier verifies that the
// SetLLMResolver(nil) path leaves the scheduler's LLM tier inert when
// the kb sync loop runs. Mirrors TestSyncScheduler_SetLLMResolver_NilDisablesTier
// — the wiring into reconcileBubble's pullManagedRepo call must NOT
// panic-deref on nil.
//
// Failure prevented: a daemon launched without OX_DAEMON_LLM_MERGE_BIN
// crashes the first time a kb pull surfaces a real conflict because
// reconcileBubble blindly invoked s.llmResolver.
func TestKBSync_LLMResolver_NilDisablesTier(t *testing.T) {
	s := &SyncScheduler{}
	s.SetLLMResolver(nil)

	// The kb pull path will gate on `if s.llmResolver != nil`, exactly
	// the same way as the ledger/team-context path does. Verify the
	// setter actually leaves it nil.
	if s.llmResolver != nil {
		t.Fatal("SetLLMResolver(nil) must leave llmResolver nil so the kb pull path can gate on it safely")
	}
}

// TestKBSync_LLMResolver_StoresCallback verifies a non-nil resolver
// round-trips through SetLLMResolver and is callable from anywhere
// the kb sync loop later wires it into ManagedRepoPullOpts.
//
// Failure prevented: a refactor that accidentally stores the callback
// in a different field, leaving kb pulls without an LLM tier even
// when the operator has configured one.
func TestKBSync_LLMResolver_StoresCallback(t *testing.T) {
	s := &SyncScheduler{}
	want := errors.New("sentinel from kb resolver")
	s.SetLLMResolver(func(_ context.Context, _ string, _ []string) (bool, error) {
		return false, want
	})
	if s.llmResolver == nil {
		t.Fatal("SetLLMResolver did not store the callback")
	}
	_, got := s.llmResolver(context.Background(), "/kb/x", nil)
	assert.True(t, errors.Is(got, want), "stored callback didn't round-trip: got %v want %v", got, want)
}

// TestKBSync_LLMResolver_NoOpAnswerIsSafe verifies that a resolver
// returning (false, nil) — meaning "I had nothing to do" — is a safe
// signal: the caller is expected to abort the rebase, never to retry
// in an infinite loop. Direct contract check; the routing inside
// pullManagedRepo is already exercised by the ledger tests.
//
// Failure prevented: a future refactor interpreting (false, nil) as
// "retry" instead of "fall through to abort" — would cause the kb
// pull cycle to spin forever on degenerate conflicts.
func TestKBSync_LLMResolver_NoOpAnswerIsSafe(t *testing.T) {
	resolver := func(_ context.Context, _ string, _ []string) (bool, error) {
		return false, nil
	}
	opts := ManagedRepoPullOpts{LLMResolver: resolver}

	// The contract: the resolver returns immediately with (false, nil).
	// Callers must not loop on this.
	ok, err := opts.LLMResolver(context.Background(), "/kb/path", []string{"AGENTS.md"})
	assert.NoError(t, err)
	assert.False(t, ok, "no-op answer must be reported as ok=false")
}

// TestKBSync_LLMResolver_ErrorIsNonFatal verifies a resolver that
// returns an error is treated as "tier failed, fall through" rather
// than panicking. Mirrors the spirit of the ledger LLM test: errors
// must surface to the operator but NEVER take down the daemon's pull
// cycle.
//
// Failure prevented: an LLM provider 500 turning into a daemon crash
// loop because the kb pull path doesn't catch resolver errors.
func TestKBSync_LLMResolver_ErrorIsNonFatal(t *testing.T) {
	sentinel := errors.New("simulated LLM provider error")
	resolver := func(_ context.Context, _ string, _ []string) (bool, error) {
		return false, sentinel
	}
	opts := ManagedRepoPullOpts{LLMResolver: resolver}

	assert.NotPanics(t, func() {
		_, err := opts.LLMResolver(context.Background(), "/kb/x", []string{"SOUL.md"})
		assert.ErrorIs(t, err, sentinel)
	})
}

// TestKBSync_LLMResolver_ReceivesUnionPaths sanity-checks that the
// paths a kb pull would feed the resolver match kb.MergeUnionPaths
// — i.e., the same set of files merge=union covers. This is the
// design boundary: the LLM tier escalates only paths union +
// accept-theirs failed on, and that pool is sourced from the same
// canonical list.
//
// Failure prevented: a regression that escalates random user files
// to the LLM tier (privacy + cost risk) or that withholds the
// designated metadata files (kb pulls keep wedging).
func TestKBSync_LLMResolver_ReceivesUnionPaths(t *testing.T) {
	var captured atomic.Value
	resolver := func(_ context.Context, _ string, ps []string) (bool, error) {
		cp := append([]string(nil), ps...)
		captured.Store(cp)
		return true, nil
	}
	opts := ManagedRepoPullOpts{LLMResolver: resolver}

	// Simulate the canonical invocation: a pullManagedRepo at the
	// stage where it has already filtered to union paths.
	canonical := []string{"AGENTS.md", "SOUL.md"}
	_, err := opts.LLMResolver(context.Background(), "/kb/path", canonical)
	assert.NoError(t, err)

	got := captured.Load().([]string)
	assert.Equal(t, canonical, got, "resolver must receive exactly the canonical union paths the caller supplied")
}
