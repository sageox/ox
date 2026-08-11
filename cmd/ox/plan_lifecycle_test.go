package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_lifecycle_test.go covers the cmd/ox layer of the lifecycle verbs:
// event-field wiring, --json shape, and the supersede --by guard. The
// AppendPlanEvent engine's own semantics (idempotency, status mapping,
// dual-write) are covered directly in internal/plan/lifecycle_test.go — this
// file only proves the CLI plumbing is wired correctly, per the test
// pyramid (tests/AGENTS.md).

// seedPlanEvents writes a single `created` event to a fresh temp dir via the
// exported plan.AppendEvent — the minimum on-disk state every lifecycle verb
// requires — and returns the dir.
func seedPlanEvents(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ev := plan.Event{PlanID: "pln_cmdtest0000000000001", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}
	if err := plan.AppendEvent(context.Background(), dir, ev); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	return dir
}

// runLifecycleVerbForTest wires an isolated cobra.Command (own flags, own
// buffer, own background context — never the global singletons or their
// shared flag state, mirroring plan_save_test.go's isolated-command
// pattern) and calls runPlanLifecycleVerbOnDir directly.
func runLifecycleVerbForTest(t *testing.T, planDir string, kind plan.EventKind, jsonOut bool, flags lifecycleVerbFlags) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", jsonOut, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runPlanLifecycleVerbOnDir(cmd, "", planDir, "test-slug", kind, flags)
	return out.String(), err
}

// decodeStrictLifecycleJSON unmarshals with DisallowUnknownFields — per
// AGENTS.md's JSON-payload rule, wire-shaped CLI JSON output is asserted on
// via the typed struct, never substring-matched.
func decodeStrictLifecycleJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("output is not the expected JSON shape: %v\noutput: %s", err, raw)
	}
	return v
}

func TestRunPlanLifecycleVerbOnDir_ApproveJSON(t *testing.T) {
	dir := seedPlanEvents(t)
	out, err := runLifecycleVerbForTest(t, dir, plan.EventApproved, true, lifecycleVerbFlags{})
	if err != nil {
		t.Fatalf("runPlanLifecycleVerbOnDir: %v", err)
	}

	got := decodeStrictLifecycleJSON[planLifecycleResult](t, out)
	if !got.Changed {
		t.Error("Changed = false, want true for a first approve")
	}
	if got.Status != plan.PlanStatusApproved {
		t.Errorf("Status = %q, want approved", got.Status)
	}
}

// TestRunPlanLifecycleVerbOnDir_NoOpReportsChangedFalse pins the no-op
// contract at the CLI layer: {"changed":false}, exit nil (not an error).
func TestRunPlanLifecycleVerbOnDir_NoOpReportsChangedFalse(t *testing.T) {
	dir := seedPlanEvents(t)
	if _, err := runLifecycleVerbForTest(t, dir, plan.EventApproved, true, lifecycleVerbFlags{}); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	out, err := runLifecycleVerbForTest(t, dir, plan.EventApproved, true, lifecycleVerbFlags{})
	if err != nil {
		t.Fatalf("second approve must exit 0 (no-op, not an error): %v", err)
	}

	got := decodeStrictLifecycleJSON[planLifecycleResult](t, out)
	if got.Changed {
		t.Error("Changed = true, want false for a no-op re-approve")
	}
}

// TestRunPlanLifecycleVerbOnDir_SessionFlagOverridesAmbient verifies
// --session (explicit) wins over whatever resolvePlanProvenance resolves
// ambiently — here, nothing (a test process has no live recording), so the
// flag's value must land on the appended event untouched.
func TestRunPlanLifecycleVerbOnDir_SessionFlagOverridesAmbient(t *testing.T) {
	dir := seedPlanEvents(t)
	if _, err := runLifecycleVerbForTest(t, dir, plan.EventWorked, false, lifecycleVerbFlags{Session: "ses_explicit0001"}); err != nil {
		t.Fatalf("runPlanLifecycleVerbOnDir: %v", err)
	}

	events, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[1].SessionID != "ses_explicit0001" {
		t.Fatalf("want the --session flag's value on the appended event, got %+v", events)
	}
}

// TestRunPlanLifecycleVerbOnDir_UnknownKindErrors proves a caller mistake
// (a kind AppendPlanEvent doesn't model) surfaces as a clear command error,
// not a panic — cheap defense-in-depth on top of the engine's own guard.
func TestRunPlanLifecycleVerbOnDir_UnknownKindErrors(t *testing.T) {
	dir := seedPlanEvents(t)
	_, err := runLifecycleVerbForTest(t, dir, plan.EventCreated, false, lifecycleVerbFlags{})
	if err == nil {
		t.Fatal("want an error for a non-lifecycle-verb kind")
	}
}

func TestRunPlanStatusOnDir_JSONShape(t *testing.T) {
	dir := seedPlanEvents(t)
	if _, err := runLifecycleVerbForTest(t, dir, plan.EventApproved, false, lifecycleVerbFlags{}); err != nil {
		t.Fatalf("seed approve: %v", err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanStatusOnDir(cmd, dir, "Test Topic", "test-slug", true); err != nil {
		t.Fatalf("runPlanStatusOnDir: %v", err)
	}

	got := decodeStrictLifecycleJSON[planStatusJSON](t, out.String())
	if got.Slug != "test-slug" {
		t.Errorf("Slug = %q, want test-slug", got.Slug)
	}
	if got.Status != plan.PlanStatusApproved {
		t.Errorf("Status = %q, want approved", got.Status)
	}
	if !strings.HasPrefix(got.PlanID, "pln_") {
		t.Errorf("PlanID = %q, want a pln_-prefixed id", got.PlanID)
	}
	if got.Authors == nil {
		t.Error("Authors must serialize as [] (non-nil empty), not null")
	}
	if got.Sessions == nil {
		t.Error("Sessions must serialize as [] (non-nil empty), not null")
	}
}

func TestRunPlanStatusOnDir_HumanTimeline(t *testing.T) {
	dir := seedPlanEvents(t)
	if _, err := runLifecycleVerbForTest(t, dir, plan.EventAbandoned, false, lifecycleVerbFlags{Reason: "no longer needed"}); err != nil {
		t.Fatalf("seed abandon: %v", err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanStatusOnDir(cmd, dir, "Test Topic", "test-slug", false); err != nil {
		t.Fatalf("runPlanStatusOnDir: %v", err)
	}

	got := out.String()
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("human mode emitted JSON:\n%s", got)
	}
	if !strings.Contains(got, string(plan.EventAbandoned)) || !strings.Contains(got, "no longer needed") {
		t.Errorf("human timeline missing the abandon event/reason:\n%s", got)
	}
}

// --- supersede's --by guard (validated before AppendPlanEvent is ever called) ---

func TestResolveSupersedeSuccessor_MissingByErrors(t *testing.T) {
	_, err := resolveSupersedeSuccessor("/any/git/root", "some-slug", "")
	if err == nil || !strings.Contains(err.Error(), "--by") {
		t.Fatalf("want a --by required error, got %v", err)
	}
}

func TestResolveSupersedeSuccessor_UnresolvableSlugErrors(t *testing.T) {
	newPlanEnrichTestRepo(t) // fresh temp git repo, isolated HOME — no ledger, so any slug is unresolvable
	_, err := resolveSupersedeSuccessor(findGitRoot(), "some-slug", "does-not-exist")
	if err == nil {
		t.Fatal("want an error when --by does not resolve to a real plan")
	}
}

// TestResolveSupersedeSuccessor_SelfSupersedeErrors is the regression test for
// the self-supersede guard: `ox plan supersede foo --by foo` must error before
// recording anything, not silently record SupersededBy pointing at itself.
func TestResolveSupersedeSuccessor_SelfSupersedeErrors(t *testing.T) {
	root := newPlanStatusTestRepo(t)
	meta := plan.Meta{Topic: "Self Supersede Guard", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	if _, _, err := plan.Save(root, plan.Input{Raw: "# Self Supersede Guard\n"}, plan.Result{}, nil, meta); err != nil {
		t.Fatalf("Save: %v", err)
	}
	slug := plan.Slugify(meta.Topic)

	_, err := resolveSupersedeSuccessor(root, slug, slug)
	if err == nil || !strings.Contains(err.Error(), "cannot be the plan being superseded") {
		t.Fatalf("want a self-supersede error, got %v", err)
	}
}

// TestPlanSupersedeCmd_MissingByErrorsBeforeAppend is the integration-level
// check on the real global command: --by unset must error before args[0]
// (the primary slug) is ever resolved or AppendPlanEvent is called.
func TestPlanSupersedeCmd_MissingByErrorsBeforeAppend(t *testing.T) {
	t.Cleanup(func() { _ = planSupersedeCmd.Flags().Set("by", "") })
	var out bytes.Buffer
	planSupersedeCmd.SetOut(&out)
	planSupersedeCmd.SetErr(&out)

	err := planSupersedeCmd.RunE(planSupersedeCmd, []string{"some-plan-that-is-never-touched"})
	if err == nil || !strings.Contains(err.Error(), "--by") {
		t.Fatalf("want a --by required error, got %v", err)
	}
}

// TestPlanApproveCmd_NoLedgerConfiguredErrorsCleanly is a thin wiring check
// on the real global command: a slug that can't be resolved (no ledger in a
// fresh repo) must surface a clear error, not panic or hang.
func TestPlanApproveCmd_NoLedgerConfiguredErrorsCleanly(t *testing.T) {
	newPlanEnrichTestRepo(t)
	var out bytes.Buffer
	planApproveCmd.SetOut(&out)
	planApproveCmd.SetErr(&out)

	err := planApproveCmd.RunE(planApproveCmd, []string{"nonexistent-slug"})
	if err == nil || !strings.Contains(err.Error(), "load plan") {
		t.Fatalf("want a clear 'load plan' error, got %v", err)
	}
}
