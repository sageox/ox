package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_review_await.go implements `ox plan review await <slug>` — the AGENT side of
// the real-time review loop. Rather than parking the authoring coworker in a serve
// loop (where it has no turn to act), the agent runs this and BLOCKS until reviewers
// submit; it then prints every reviewer's open items as JSON and exits, so the agent
// acts in-context, resolves, re-renders (the reviewers' page auto-reloads), and calls
// await again. It reads the ledger directly (no socket), so it is multi-user by
// construction — feedback from any number of reviewers is merged — and cross-harness:
// any coding agent can run a blocking command and read its stdout.
var planReviewAwaitCmd = &cobra.Command{
	Use:   "await <slug>",
	Short: "Block until reviewers submit feedback, then print open items as JSON (agent loop)",
	Long: `Block until human reviewers submit review feedback, then emit the open items as JSON.

This is the agent side of the review loop. Instead of parking the authoring agent in
a serve loop (where it cannot act), the agent runs:

    ox plan review await <slug>

which blocks until there is open, unaddressed feedback from ANY reviewer (multi-user),
prints it as JSON on stdout, and exits. The agent then addresses each item, runs
` + "`ox plan feedback resolve <slug> <anchor> --state addressed --commit <sha>`" + `, re-renders
with ` + "`ox plan render`" + ` (the reviewers' page auto-reloads over SSE), and calls await
again — a real turn-based loop that keeps the authoring agent in context.

Because this command BLOCKS (up to --timeout), CONFIRM WITH THE USER before entering
the loop — do not silently park an autonomous ("auto") agent run waiting on a human.
Ask first, or pass a short --timeout and poll between other work.

Emits immediately if feedback is already waiting (never strands a race). Exits with
status "timeout" if nothing arrives within --timeout, or "approved" once a reviewer
approves the plan (the loop is done).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		timeout, _ := cmd.Flags().GetDuration("timeout")
		return runPlanReviewAwait(cmd, args[0], timeout)
	},
}

// awaitResult is the JSON contract the agent consumes from stdout.
type awaitResult struct {
	Slug      string            `json:"slug"`
	Status    string            `json:"status"` // feedback | approved | timeout
	Open      []plan.MergedItem `json:"open"`
	Contested []string          `json:"contested,omitempty"`
	Counts    awaitCounts       `json:"counts"`
}

type awaitCounts struct {
	Open      int `json:"open"`
	Reviewers int `json:"reviewers"`
}

func runPlanReviewAwait(cmd *cobra.Command, slug string, timeout time.Duration) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	planDir := info.Dir

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// emit immediately if feedback is already waiting or the plan is already approved
	if res, done := awaitSnapshot(planDir); done {
		return emitAwait(cmd, slug, res)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watch feedback: %w", err)
	}
	defer w.Close()
	_ = os.MkdirAll(filepath.Join(planDir, "feedback"), 0o755)
	_ = w.Add(planDir)
	_ = w.Add(filepath.Join(planDir, "feedback"))

	// Re-snapshot AFTER registering the watcher: feedback written in the gap between
	// the first snapshot and watch registration would otherwise be seen by neither
	// (the watcher didn't exist yet) and strand the agent until --timeout. This
	// closes that race.
	if res, done := awaitSnapshot(planDir); done {
		return emitAwait(cmd, slug, res)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for reviewer feedback on %q … (Ctrl-C to stop)\n", slug)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	recheck := make(chan struct{}, 1)
	trigger := func() {
		select {
		case recheck <- struct{}{}:
		default:
		}
	}
	var debounce *time.Timer
	for {
		select {
		case <-ctx.Done():
			return emitAwait(cmd, slug, awaitResult{Status: "timeout"})
		case <-timer.C:
			return emitAwait(cmd, slug, awaitResult{Status: "timeout"})
		case _, ok := <-w.Events:
			if !ok {
				return emitAwait(cmd, slug, awaitResult{Status: "timeout"})
			}
			// coalesce write bursts (a round write, a resolve, a re-render) into one recheck
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(200*time.Millisecond, trigger)
		case <-recheck:
			if res, done := awaitSnapshot(planDir); done {
				return emitAwait(cmd, slug, res)
			}
		case _, ok := <-w.Errors:
			if !ok {
				return emitAwait(cmd, slug, awaitResult{Status: "timeout"})
			}
		}
	}
}

// awaitSnapshot reads current review state; done is true when there is open feedback
// (from any reviewer) or the plan has been approved — the two conditions that end a
// wait and hand the agent something to do.
func awaitSnapshot(planDir string) (awaitResult, bool) {
	if meta, err := plan.LoadMeta(planDir); err == nil && meta.Status == plan.PlanStatusApproved {
		return awaitResult{Status: "approved"}, true
	}
	items, err := plan.AssembleReview(planDir)
	if err != nil {
		return awaitResult{}, false
	}
	var open []plan.MergedItem
	reviewers := map[string]bool{}
	for _, it := range items {
		if it.Open && it.Status != plan.FeedbackApprove {
			open = append(open, it)
			if it.Reviewer != "" {
				reviewers[it.Reviewer] = true
			}
		}
	}
	if len(open) == 0 {
		return awaitResult{}, false
	}
	var contested []string
	for a := range plan.ContestedAnchors(items) {
		contested = append(contested, a)
	}
	sort.Strings(contested)
	return awaitResult{
		Status:    "feedback",
		Open:      open,
		Contested: contested,
		Counts:    awaitCounts{Open: len(open), Reviewers: len(reviewers)},
	}, true
}

func emitAwait(cmd *cobra.Command, slug string, res awaitResult) error {
	res.Slug = slug
	if res.Open == nil {
		res.Open = []plan.MergedItem{}
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func init() {
	planReviewAwaitCmd.Flags().Duration("timeout", 30*time.Minute, "block at most this long before exiting with status \"timeout\"")
	planReviewCmd.AddCommand(planReviewAwaitCmd)
}
