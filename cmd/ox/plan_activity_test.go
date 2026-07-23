package main

// plan_activity_test.go covers postPlanActivityBestEffort's off-by-default
// gate (see plan_activity.go): the CLIENT half of the caller-driven
// plan_id -> plan index (bead sageox-gqgkg). The gate itself — not a live
// network round trip, which internal/api/plan_activity_test.go already
// covers against a mock server — is the property that matters here: an
// unconfigured project must never attempt a network call.

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// TestPostPlanActivityBestEffort_NoProjectConfigSkipsSilently verifies the
// off-by-default contract: a project with no .sageox/config.json (never
// `ox init`-linked to a team) returns immediately, without attempting any
// network I/O. Bounded by a generous timeout so an accidental fall-through to
// a real network attempt (which would block on the RepoClient's 10s timeout)
// fails the test quickly rather than hanging the suite.
func TestPostPlanActivityBestEffort_NoProjectConfigSkipsSilently(t *testing.T) {
	root := newPlanEnrichTestRepo(t) // fresh temp git repo, no .sageox/config.json — never ox init'd

	done := make(chan struct{})
	go func() {
		defer close(done)
		postPlanActivityBestEffort(root, root, plan.EventApproved)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("postPlanActivityBestEffort did not return promptly — the off-by-default gate did not short-circuit before a network attempt")
	}
}

// TestPostPlanActivityBestEffort_EmptyPlanDirSkipsSilently verifies a plan
// dir with no events.jsonl (nothing to derive a plan_id from) also returns
// promptly rather than attempting a call with an empty plan_id.
func TestPostPlanActivityBestEffort_EmptyPlanDirSkipsSilently(t *testing.T) {
	root := newPlanEnrichTestRepo(t)
	emptyPlanDir := t.TempDir()

	done := make(chan struct{})
	go func() {
		defer close(done)
		postPlanActivityBestEffort(root, emptyPlanDir, plan.EventWorked)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("postPlanActivityBestEffort did not return promptly for a plan dir with no events.jsonl")
	}
}
