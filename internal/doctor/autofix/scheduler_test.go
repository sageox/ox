package autofix

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// TestScheduler_RunOnce_FixesInitReverted exercises the structural
// path the daemon depends on: registry → scheduler → check → workspace
// fixed. Without this, ox-0xgx is just a refactor with no proof that
// the periodic auto-fix actually repairs the workspace.
//
// Failure prevented: a future change to the registry / scheduler API
// silently breaks the contract that registered checks see the
// workspace path passed in and can write to it.
func TestScheduler_RunOnce_FixesInitReverted(t *testing.T) {
	// Arrange a workspace in the same shape as demo-ice-cream after
	// `git reset` removed init artifacts: .sageox/ exists,
	// config.local.toml encodes a repo_id via the ledger path, but
	// .sageox/config.json is missing.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocalConfig(root, &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: "/x/.local/share/sageox/sageox.ai/ledgers/repo_recover-test",
		},
	}); err != nil {
		t.Fatal(err)
	}

	reg := Default()
	var emitted []CheckResult
	s := NewScheduler(reg, nil, func() []string { return []string{root} }, func(r CheckResult) {
		emitted = append(emitted, r)
	})

	results := s.RunOnce(context.Background())

	// Find the init-reverted result — must be Fixed.
	var got *CheckResult
	for i := range results {
		if results[i].Slug == "init-reverted" {
			got = &results[i]
			break
		}
	}
	if got == nil {
		t.Fatal("init-reverted check did not run")
	}
	if got.Status != StatusFixed {
		t.Errorf("init-reverted status = %v, want StatusFixed; summary=%q", got.Status, got.Summary)
	}

	// Verify the on-disk repair actually happened.
	repoID := config.GetRepoID(root)
	if repoID != "repo_recover-test" {
		t.Errorf("repo_id after autofix = %q, want repo_recover-test", repoID)
	}

	// Emit sink saw the Fixed result (Clean is filtered out).
	var sawFixed bool
	for _, r := range emitted {
		if r.Slug == "init-reverted" && r.Status == StatusFixed {
			sawFixed = true
			break
		}
	}
	if !sawFixed {
		t.Error("emit sink did not receive the Fixed result")
	}
}

// TestScheduler_RunOnce_RepairsClaudeHooksFormat covers the ported
// hooks-format repair path. The on-disk file MUST end up in the
// canonical array shape Claude Code accepts.
//
// Failure prevented: a regression in the hooks-format check that
// ships the daemon's parser-reject failure mode unattended.
func TestScheduler_RunOnce_RepairsClaudeHooksFormat(t *testing.T) {
	root := t.TempDir()
	settingsDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"PostToolUse":"echo legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := Default()
	s := NewScheduler(reg, nil, func() []string { return []string{root} }, nil)
	_ = s.RunOnce(context.Background())

	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("repaired file invalid JSON: %v\n%s", err, body)
	}
	hooks := top["hooks"]
	if len(hooks) == 0 || hooks[0] != '{' {
		t.Fatalf("hooks should be an object: %s", hooks)
	}
	var events map[string]json.RawMessage
	_ = json.Unmarshal(hooks, &events)
	if len(events["PostToolUse"]) == 0 || events["PostToolUse"][0] != '[' {
		t.Errorf("PostToolUse still in legacy form: %s", events["PostToolUse"])
	}
}

// TestCheck_ShouldRun_RespectsMinInterval pins the throttling
// invariant: a check with MinInterval=1h must NOT run twice in the
// same minute. Critical so a daemon spawning ticks every 30s during a
// hot loop can't DOS a check that's expensive to run.
func TestCheck_ShouldRun_RespectsMinInterval(t *testing.T) {
	c := &Check{Slug: "x", MinInterval: time.Hour}
	now := time.Now()
	if !c.shouldRun(now) {
		t.Fatal("first call must always run")
	}
	if c.shouldRun(now.Add(time.Minute)) {
		t.Error("second call within MinInterval must be throttled")
	}
	if !c.shouldRun(now.Add(time.Hour + time.Second)) {
		t.Error("call past MinInterval must run")
	}
}

// TestScheduler_RunNow_BypassesThrottle pins the contract that
// user-triggered Doctor() RPCs (e.g., `ox doctor --force-session-uploads`)
// actually perform their auto-fix work even when the slow ticker just
// finished a round. Without this, the user-facing button silently
// becomes a no-op for 30 minutes after every scheduler tick.
//
// Failure prevented: a future refactor that gates RunNow on shouldRun
// ships a regressed user experience.
func TestScheduler_RunNow_BypassesThrottle(t *testing.T) {
	var calls atomic.Int32
	reg := NewRegistry()
	reg.Register(&Check{
		Slug:        "throttled",
		MinInterval: time.Hour,
		Run: func(_ context.Context, _ string) CheckResult {
			calls.Add(1)
			return CheckResult{Status: StatusFixed, Summary: "did the thing"}
		},
	})
	s := NewScheduler(reg, nil, func() []string { return []string{"/x"} }, nil)

	// First RunOnce establishes the throttle clock.
	_ = s.RunOnce(context.Background())
	if got := calls.Load(); got != 1 {
		t.Fatalf("first RunOnce: calls=%d, want 1", got)
	}

	// Second RunOnce within MinInterval should be throttled — no call.
	_ = s.RunOnce(context.Background())
	if got := calls.Load(); got != 1 {
		t.Errorf("RunOnce within throttle: calls=%d, want still 1", got)
	}

	// RunNow MUST run the check despite the throttle.
	results := s.RunNow(context.Background())
	if got := calls.Load(); got != 2 {
		t.Errorf("RunNow within throttle: calls=%d, want 2", got)
	}
	if len(results) != 1 || results[0].Slug != "throttled" {
		t.Errorf("RunNow results = %+v, want one result for slug=throttled", results)
	}

	// And after RunNow, the throttle clock advances — a follow-up RunOnce
	// is throttled again, not perpetually open.
	_ = s.RunOnce(context.Background())
	if got := calls.Load(); got != 2 {
		t.Errorf("RunOnce after RunNow within throttle: calls=%d, want still 2", got)
	}
}

// TestRegistry_Register_OverwritesSlug guarantees the test-stub use
// case: a test can swap a check by re-registering its slug, without
// having to invent a removal API. Documenting via test so a future
// "make Register strict" change doesn't break tests silently.
func TestRegistry_Register_OverwritesSlug(t *testing.T) {
	r := NewRegistry()
	var first, second atomic.Int32
	r.Register(&Check{Slug: "x", Run: func(_ context.Context, _ string) CheckResult {
		first.Add(1)
		return CheckResult{}
	}})
	r.Register(&Check{Slug: "x", Run: func(_ context.Context, _ string) CheckResult {
		second.Add(1)
		return CheckResult{}
	}})
	if got := len(r.All()); got != 1 {
		t.Fatalf("registry has %d entries, want 1 after overwrite", got)
	}
	r.All()[0].Run(context.Background(), "")
	if first.Load() != 0 || second.Load() != 1 {
		t.Errorf("overwrite did not replace the original: first=%d second=%d", first.Load(), second.Load())
	}
}
