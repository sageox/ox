//go:build !short

package main

// doctor_plan_pending_test.go covers the plan-pending-writes doctor check.

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// refusedRound posts a review round to a remote whose pre-receive hook refuses
// every push, and returns the recovery entry the committed round leaves.
func refusedRound(t *testing.T, f *durableReviewFixture) string {
	t.Helper()
	hook := filepath.Join(f.origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("feedback: %d %s", code, body)
	}
	entries, err := filepath.Glob(filepath.Join(f.ledger, filepath.FromSlash(planReviewPendingDir), "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one recovery entry, got %v (err %v)", entries, err)
	}
	return entries[0]
}

// TestCheckPlanPendingWrites_ReportsAReviewThatNeverLands: each state that
// keeps a review's entry forever is reported with its reason and file, and the
// fix leaves the entry's bytes alone. Failure prevented: a review the coworker
// was told is saved never reaches the team, and nothing says so.
func TestCheckPlanPendingWrites_ReportsAReviewThatNeverLands(t *testing.T) {
	for _, tc := range []struct {
		name   string
		then   func(t *testing.T, f *durableReviewFixture, entry string)
		reason string
	}{
		{
			name:   "push always refused",
			then:   func(*testing.T, *durableReviewFixture, string) {},
			reason: "committed, but the remote does not hold it",
		},
		{
			name: "plan dir gone",
			then: func(t *testing.T, f *durableReviewFixture, _ string) {
				if err := os.RemoveAll(f.planDir); err != nil {
					t.Fatal(err)
				}
			},
			reason: "its plan dir is gone",
		},
		{
			name: "unreadable entry",
			then: func(t *testing.T, _ *durableReviewFixture, entry string) {
				if err := os.WriteFile(entry, []byte(`{"plan_dir":`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			reason: "unreadable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDurableReviewFixture(t)
			entry := refusedRound(t, f)
			tc.then(t, f, entry)
			before, err := os.ReadFile(entry)
			if err != nil {
				t.Fatal(err)
			}

			res := checkPlanPendingWrites(f.ledger, true)

			if after, err := os.ReadFile(entry); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("the fix must keep the entry and its bytes (err %v)", err)
			}
			if !res.warning || !strings.Contains(res.detail, tc.reason) || !strings.Contains(res.detail, entry) {
				t.Fatalf("want a warning naming %q and the entry %s, got %q: %s", tc.reason, entry, res.message, res.detail)
			}
		})
	}
}

// TestCheckPlanPendingWrites_ReportsPlanRetryMarkers: a plan retry the fix
// cannot land either is reported with its failed attempts and last error, and
// a marker no retry can read is reported too. Failure prevented: a plan save
// or approval whose push keeps failing is never reported.
func TestCheckPlanPendingWrites_ReportsPlanRetryMarkers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real ledger git repo whose push to a missing remote retries with backoff")
	}
	ledger := reviewTestLedger(t)
	runGitInDir(t, ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if err := os.MkdirAll(filepath.Join(ledger, "data", "plans", "2026-10-01-live"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePlanPushMarkerFile(t, ledger, "data__plans__2026-10-01-live.json",
		`{"plan_dir":"data/plans/2026-10-01-live","first_failed_at":"2026-10-01T00:00:00Z","last_error":"push refused","failed_attempts":3}`)
	writePlanPushMarkerFile(t, ledger, "torn.json", `{"plan_dir":`)

	res := checkPlanPendingWrites(ledger, true)

	for _, want := range []string{"plan data/plans/2026-10-01-live", "4 failed attempt(s), last: push plan", "torn.json", "no retry runs for it"} {
		if !strings.Contains(res.detail, want) {
			t.Errorf("detail must name %q, got %q", want, res.detail)
		}
	}
}

// TestDoctorFix_PublishesAReviewTheRemoteRefusedEarlier drives the real
// `ox doctor --fix-slug=plan-pending-writes` once the remote takes pushes
// again; naming the slug keeps the branch-status fix from pushing it instead.
// Failure prevented: `ox doctor --fix` reports a review it could publish and
// leaves it for the next `ox agent prime`, or no doctor phase runs the check.
func TestDoctorFix_PublishesAReviewTheRemoteRefusedEarlier(t *testing.T) {
	f := newDurableReviewFixture(t)
	refusedRound(t, f)
	if err := os.Remove(filepath.Join(f.origin, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	sandboxDoctorEnv(t)
	project := testGitRepo(t)
	t.Chdir(project)
	createFreshSageoxStructure(t, project)
	if err := config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: f.ledger}}); err != nil {
		t.Fatal(err)
	}

	categories, err := runDoctorChecks(context.Background(), doctorOptions{fix: true, fixSlugs: []string{CheckSlugPlanPendingWrites}, forceYes: true})
	if err != nil {
		t.Fatal(err)
	}
	check, found := findCheckByName(categories, planPendingCheckName)
	if !found {
		t.Fatalf("ox doctor has no %q line", planPendingCheckName)
	}
	if !check.passed || check.warning {
		t.Fatalf("ox doctor --fix must publish the review, got %q: %s", check.message, check.detail)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("the fix must publish the round, %d on the remote", n)
	}
}
