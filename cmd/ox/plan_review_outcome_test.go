package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanReview_ReportsHowTheSessionEnded runs the real review command for
// each way a session ends and checks what its usage event will say — the
// command exits 0 on all of them. A served session also reports the rounds,
// items, and highlights reviewers submitted during it, and not earlier rounds.
// Failure prevented: usage data can't tell an approved review from an
// abandoned one, or how many comments are highlights.
func TestPlanReview_ReportsHowTheSessionEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("short: serves real review sessions")
	}
	gitRoot := newPlanStatusTestRepo(t)
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: "# Outcome\n\n## Risks\n\nThe retry path can double-fire under load.\n"},
		plan.Result{}, nil, plan.Meta{Topic: "Outcome", Slug: "outcome"})
	require.NoError(t, err)
	// submitted before any session below: never counted
	_, err = plan.SaveFeedback(dir, plan.FeedbackSet{Slug: "outcome", Items: []plan.FeedbackItem{
		{Anchor: "hearlier1", Label: "earlier", Status: plan.FeedbackComment},
	}}, time.Now().Add(-time.Hour))
	require.NoError(t, err)

	t.Chdir(gitRoot)
	t.Setenv("SKIP_BROWSER", "1")
	t.Setenv("TMPDIR", t.TempDir()) // where the static export is written
	// not headless, so the loop serves: Linux CI has no display, and an SSH
	// session counts as headless too
	t.Setenv("DISPLAY", ":0")
	for _, k := range []string{"SSH_CLIENT", "SSH_CONNECTION", "SSH_TTY"} {
		t.Setenv(k, "")
	}
	saved := cliCtx
	t.Cleanup(func() { cliCtx = saved })
	dirName := filepath.Base(dir)

	// review runs the command; during, when given, runs once its server answers.
	review := func(noServe bool, idle time.Duration, during func(base, token string)) map[string]any {
		t.Helper()
		cliCtx = &cli.Context{}
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		done := make(chan error, 1)
		go func() { done <- runPlanReview(cmd, "outcome", noServe, idle) }()
		if during != nil {
			st := waitForReviewServer(t, gitRoot, dirName)
			during(fmt.Sprintf("http://127.0.0.1:%d", st.Port), st.Token)
		}
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(20 * time.Second):
			t.Fatal("the review session never ended")
		}
		return cliCtx.Outcome()
	}

	approved := review(false, time.Minute, func(base, token string) {
		round := `{"reviewer":"Devon","items":[` +
			`{"anchor":"q1a2b3c4d","section":"Risks","label":"double-fire","quote":"double-fire","status":"request-change"},` +
			`{"anchor":"hsection1","section":"Risks","label":"Risks","status":"comment"}]}`
		require.Equal(t, http.StatusOK, reviewPOST(t, base+"/feedback", token, round))
		require.Equal(t, http.StatusOK, reviewPOST(t, base+"/approve", token, `{}`))
	})
	assert.Equal(t, map[string]any{"review_outcome": "approved", "rounds": 1, "items": 2, "highlights": 1}, approved)

	assert.Equal(t, map[string]any{"review_outcome": "idle", "rounds": 0, "items": 0, "highlights": 0},
		review(false, 300*time.Millisecond, nil), "a session nobody used reports no rounds")

	// a server for this plan already answers on its address
	st, ok := plan.LoadReviewServerState(gitRoot, dirName)
	require.True(t, ok)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", st.Port))
	require.NoError(t, err)
	running := &http.Server{Handler: liveReviewHandler(gitRoot, "outcome", dir, "http://"+ln.Addr().String(), st.Token,
		newBroadcaster(), make(chan int, 1), make(chan struct{}, 1))}
	go func() { _ = running.Serve(ln) }()
	t.Cleanup(func() { _ = running.Close() })
	assert.Equal(t, map[string]any{"review_outcome": "reused"}, review(false, time.Minute, nil))

	assert.Equal(t, map[string]any{"review_outcome": "static"}, review(true, time.Minute, nil))
}

// TestPlanReviewAwait_ReportsWhatItReturned checks the await command's usage
// event says whether it returned feedback or timed out: it exits 0 either way.
// Failure prevented: usage data can't tell an AI coworker that got feedback
// from one that waited out its timeout.
func TestPlanReviewAwait_ReportsWhatItReturned(t *testing.T) {
	gitRoot := newPlanStatusTestRepo(t)
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: "# Await Outcome\n\n## Risks\n\nThe retry path can double-fire.\n"},
		plan.Result{}, nil, plan.Meta{Topic: "Await Outcome", Slug: "await-outcome"})
	require.NoError(t, err)
	t.Chdir(gitRoot)
	saved := cliCtx
	t.Cleanup(func() { cliCtx = saved })

	await := func() any {
		t.Helper()
		cliCtx = &cli.Context{}
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		require.NoError(t, runPlanReviewAwait(cmd, "await-outcome", 20*time.Millisecond))
		return cliCtx.Outcome()["await_status"]
	}

	assert.Equal(t, "timeout", await())
	_, err = plan.SaveFeedback(dir, plan.FeedbackSet{Slug: "await-outcome", Items: []plan.FeedbackItem{
		{Anchor: "hrisk0001", Label: "retry", Status: plan.FeedbackRequestChange},
	}}, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "feedback", await())
}

// waitForReviewServer returns the address and token the review session for the
// plan persisted, once its server answers.
func waitForReviewServer(t *testing.T, gitRoot, dirName string) plan.ReviewServerState {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st, ok := plan.LoadReviewServerState(gitRoot, dirName); ok && probeReviewServer(st.Port, dirName) {
			return st
		}
	}
	t.Fatal("the review server never answered")
	return plan.ReviewServerState{}
}
