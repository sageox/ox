package main

// agent_session_plan_reconcile_test.go covers reconcileProducedPlansAtStop
// (see agent_session.go): session stop records the session in each produced
// plan's meta.json, never edits its events.jsonl, and commits only those two
// files.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// TestReconcileProducedPlansAtStop_CommitsOnlyTheFilesItWrites: session stop
// records a produced plan's session id and outcome in its meta.json, then
// commits and pushes the plan's event files alone, here just meta.json.
// Failure prevented: the stop publishes the author's unsaved edit to plan.md
// with them.
func TestReconcileProducedPlansAtStop_CommitsOnlyTheFilesItWrites(t *testing.T) {
	for _, state := range []string{"unstaged", "staged"} {
		t.Run(state, func(t *testing.T) {
			if testing.Short() {
				t.Skip("short: real git clone + push")
			}
			root := newPlanCaptureTestRepo(t)
			ledger, remote := newSharedLedger(t, root, true)
			meta := plan.Meta{Topic: "Stop commit scope", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
			dir, _, err := plan.Save(root, plan.Input{Raw: "# Stop commit scope\n"}, plan.Result{}, nil, meta)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			runGitInDir(t, ledger, "add", "data")
			runGitInDir(t, ledger, "commit", "-q", "-m", "plan: stop commit scope")
			runGitInDir(t, ledger, "push", "-q")
			if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Stop commit scope\n\nhalf-written\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if state == "staged" {
				runGitInDir(t, ledger, "add", filepath.ToSlash(filepath.Join("data", "plans", filepath.Base(dir), "plan.md")))
			}

			reconcileProducedPlansAtStop(root, []string{"stop-commit-scope"}, "ses_stopscope01")

			rel := "data/plans/" + filepath.Base(dir)
			if got := strings.Fields(runGitInDir(t, remote, "show", "--name-only", "--format=", "main")); !slices.Equal(got, []string{rel + "/meta.json"}) {
				t.Fatalf("the stop's commit on the remote carries %v, want only its meta.json", got)
			}
			if got := runGitInDir(t, remote, "show", "main:"+rel+"/plan.md"); got != "# Stop commit scope" {
				t.Fatalf("the stop published the author's unsaved plan.md: %q", got)
			}
			if got := runGitInDir(t, ledger, "diff", "HEAD", "--name-only"); got != rel+"/plan.md" {
				t.Fatalf("the author's edit must stay uncommitted, changed paths %q", got)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "plan.md")); err != nil || string(got) != "# Stop commit scope\n\nhalf-written\n" {
				t.Fatalf("the author's edit changed: %q (%v)", got, err)
			}
		})
	}
}

// TestReconcileProducedPlansAtStop_LeavesTheEventLogAlone pins the rule that
// keeps union-merged event logs free of duplicate lines: an edit to a line
// that sits next to another clone's append comes out of the merge twice.
func TestReconcileProducedPlansAtStop_LeavesTheEventLogAlone(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Stop leaves events", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, _, err := plan.Save(root, plan.Input{Raw: "# Stop leaves events\n"}, plan.Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	// the line a recording started by an older ox writes: a name, no id
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{
		PlanID: events[0].PlanID, Kind: plan.EventWorked, SessionName: "2026-07-01-person-a-Oxab12",
	}); err != nil {
		t.Fatalf("seed worked event: %v", err)
	}
	eventsPath := filepath.Join(dir, "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	reconcileProducedPlansAtStop(root, []string{"stop-leaves-events"}, "ses_stop01")

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("session stop edited events.jsonl:\nbefore %s\nafter  %s", before, after)
	}
	gotMeta, err := plan.ReadPlanMeta(root, "stop-leaves-events")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Provenance == nil || gotMeta.Provenance.SessionID != "ses_stop01" || gotMeta.Provenance.SessionOutcome != plan.SessionOutcomeStopped {
		t.Errorf("meta.json Provenance = %+v, want SessionID ses_stop01 and outcome stopped", gotMeta.Provenance)
	}
}

// TestReconcileProducedPlansAtStop_ContinuesPastBrokenSlug verifies the
// error-path behavior of the per-slug loop: a slug that can't be resolved
// (plan.Load fails, e.g. it was never saved or its dir is gone) must not
// stop reconciliation for the other slugs in the same stop call — each
// failure is independently debug-logged and skipped via `continue`.
func TestReconcileProducedPlansAtStop_ContinuesPastBrokenSlug(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Valid Alongside Broken", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	if _, _, err := plan.Save(root, plan.Input{Raw: "# Valid Alongside Broken\n"}, plan.Result{}, nil, meta); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "does-not-exist" precedes the valid slug so a bug that aborts the loop
	// on the first failure (instead of continuing) would be caught.
	slugs := []string{"does-not-exist", "valid-alongside-broken"}
	reconcileProducedPlansAtStop(root, slugs, "ses_mixedslugs01")

	gotMeta, err := plan.ReadPlanMeta(root, "valid-alongside-broken")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Provenance == nil || gotMeta.Provenance.SessionID != "ses_mixedslugs01" {
		t.Fatalf("valid slug must still be reconciled despite a broken slug earlier in the list: got %+v", gotMeta.Provenance)
	}
}

// TestReconcileProducedPlansAtStop_LeavesAnUnsharedSaveToItsRetry: when the
// session's save could not be shared (its upload failed), the stop commits
// nothing of that plan, and the prime retry publishes the save with the stop's
// session id. Failure prevented: the stop publishes the save's event log and
// meta.json without its page, so teammates see a plan with no content.
func TestReconcileProducedPlansAtStop_LeavesAnUnsharedSaveToItsRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, false)
	store.fail.Store(true)
	useLFSStore(t, store.client)
	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Unshared At Stop")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("save: %v", err)
	}
	plans, err := plan.List(root)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
	}
	head := runGitInDir(t, remote, "rev-parse", "main")

	reconcileProducedPlansAtStop(root, []string{plans[0].Slug}, "ses_unshared01")

	if got := runGitInDir(t, remote, "rev-parse", "main"); got != head {
		t.Fatalf("the stop published part of a save that is not shared: %s", runGitInDir(t, remote, "show", "--name-only", "--format=", "main"))
	}
	store.fail.Store(false)
	if ok, failed, err := flushPendingPlanPushes(context.Background(), ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	rel := "data/plans/" + filepath.Base(plans[0].Dir)
	if !strings.Contains(runGitInDir(t, remote, "show", "main:"+rel+"/meta.json"), "ses_unshared01") {
		t.Fatal("the retry must publish the stop's session id with the save")
	}
}
