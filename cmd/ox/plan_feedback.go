package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
	"github.com/spf13/cobra"
)

// plan_feedback.go is the CLI side of the agent-driven review loop. The primary
// entry is `ox plan review` (render + serve + collect). These subcommands are the
// supporting surface: `show` (agent pulls the open/addressed digest on resume),
// `resolve` (agent marks an item addressed/wontfix/verified with a commit), and
// the hidden `apply` primitive (clipboard fallback when there is no server).
// Everything reads/writes the plan's ledger dir — no socket, no daemon routing.

var planFeedbackCmd = &cobra.Command{
	Use:    "feedback",
	Hidden: true, // agent tier: the human path is `ox plan review`; agents use show/resolve via prime
	Short:  "View and resolve in-document review feedback on a saved plan",
	Long: `Supporting commands for the plan review loop (see ` + "`ox plan review`" + `).

  show     print the open/addressed digest for a plan (agent pulls on resume)
  resolve  mark a review item addressed | wontfix | verified, with a commit + note
  apply    (hidden) ingest a feedback JSON export — clipboard fallback when the
           review server isn't used

Review rounds and resolutions live in the plan's ledger dir, committed with the
plan, so the committed plan carries the whole review trail.`,
}

var planFeedbackShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "Show open + resolved review feedback for a saved plan",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		return runPlanFeedbackShow(cmd, args[0], jsonOut)
	},
}

var planFeedbackResolveCmd = &cobra.Command{
	Use:   "resolve <slug> <anchor>",
	Short: "Mark a review item addressed/wontfix/verified (with a commit + note)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		state, _ := cmd.Flags().GetString("state")
		commit, _ := cmd.Flags().GetString("commit")
		note, _ := cmd.Flags().GetString("note")
		return runPlanFeedbackResolve(cmd, args[0], args[1], state, commit, note)
	},
}

var planFeedbackApplyCmd = &cobra.Command{
	Use:    "apply [slug]",
	Short:  "Ingest a feedback JSON export into a plan's ledger (clipboard fallback)",
	Hidden: true, // the loop uses `ox plan review`; this is the no-server primitive
	Args:   cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		from, _ := cmd.Flags().GetString("from")
		slug := ""
		if len(args) == 1 {
			slug = args[0]
		}
		return runPlanFeedbackApply(cmd, slug, from)
	},
}

func runPlanFeedbackApply(cmd *cobra.Command, slug, from string) error {
	in := cmd.InOrStdin()
	if from != "" && from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return fmt.Errorf("read feedback: %w", err)
		}
		defer f.Close()
		in = f
	}
	// the stored round outgrows its request, and no record over the limit is
	// ever stored, so a larger export cannot apply
	raw, err := io.ReadAll(io.LimitReader(in, planreview.MaxRecordBytes+1))
	if err != nil {
		return fmt.Errorf("read feedback: %w", err)
	}
	if len(raw) > planreview.MaxRecordBytes {
		return fmt.Errorf("feedback export is over the %d-byte limit", planreview.MaxRecordBytes)
	}
	q, err := planreview.ParseRoundRequest(raw)
	if err != nil {
		return err
	}
	if slug == "" {
		slug = q.Slug
	}
	if slug == "" {
		return fmt.Errorf("no slug: pass it as an argument or include it in the feedback JSON")
	}
	// The CLI freezes an export, so it freezes it canonically (keys sorted at
	// every level, no insignificant whitespace, numbers as written), and names
	// one without an id by its content: applying the same export twice (a
	// retried paste, a re-run script) is one round, whatever an editor did to
	// its whitespace or key order.
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&obj) // ParseRoundRequest decoded raw as an object
	if q.ID == "" {
		obj["id"] = planreview.ContentRoundID(q.Round)
	}
	frozen, _ := json.Marshal(obj) // decoded JSON values: Marshal cannot fail
	if q, err = planreview.ParseRoundRequest(frozen); err != nil {
		return err
	}

	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	rec, err := q.Record(slug, q.Reviewer, time.Now())
	if err != nil {
		return err
	}
	st, receipt, err := publishReviewRecord(context.Background(), gitRoot, info.Dir, rec)
	if err != nil {
		return fmt.Errorf("apply round %s: %w", q.ID, err)
	}
	path := filepath.Join(info.Dir, filepath.FromSlash(receipt.Path))
	out := cmd.OutOrStdout()
	if !st.Pushed {
		cli.PrintHintTo(out, "feedback saved locally, not yet shared with your team (`ox agent prime` retries it): "+st.Err)
	}
	if receipt.State == planreview.ReceiptAlreadyPublished {
		// Already stored by an earlier apply, which notified: exit 0 — the
		// caller's intent is met.
		if _, werr := fmt.Fprintf(out, "Feedback already applied (round %s): %s\n", q.ID, cli.StyleFile.Render(path)); werr != nil {
			return fmt.Errorf("write output: %w", werr)
		}
		return nil
	}
	if err := enqueuePlanFeedbackTask(gitRoot, info.Dir, slug, len(q.Items)); err != nil {
		cli.PrintWarning("could not notify the plan's authoring coworker automatically — tell them directly, or they'll miss this round of feedback")
	}
	fmt.Fprintf(out, "Applied %d feedback item(s) to %s\n\n", len(q.Items), cli.StyleFile.Render(path))
	printPlanReviewDigest(cmd, info.Dir)
	return nil
}

// enqueuePlanFeedbackTask notifies the AI coworker that authored a plan that new
// human review feedback arrived, by enqueuing a `plan-feedback` agent-task into
// the project's task queue. The task carries NO instructions — only its kind and
// the plan slug in the payload — so the coworker acts via the fixed agent-task
// protocol (read `ox plan feedback show <slug>`, address, resolve), never from
// task text. Routed to the authoring agent TYPE (the queue targets a type, not an
// instance) and deduped per (agent, plan) so repeated rounds don't pile up.
//
// An unlinked plan or a missing queue is a silent, error-free no-op — there is
// nobody to notify. An actual enqueue failure is different: the feedback the
// human just submitted already landed in the ledger, but the coworker will never
// see it unless something surfaces the gap. It is retried once (the DedupKey
// makes a repeat Add safe) since the one intermittent failure observed looked
// like a transient store-open/lock error rather than a real, repeatable one; a
// non-nil return still never blocks or reverts the feedback write — the caller
// decides how (or whether) to tell the human.
func enqueuePlanFeedbackTask(gitRoot, planDir, slug string, items int) error {
	if gitRoot == "" || planDir == "" || slug == "" {
		return nil
	}
	meta, err := plan.LoadMeta(planDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no meta.json at all → nobody was ever recorded to notify
		}
		// meta.json exists but is unreadable/corrupt — a real failure, not "no
		// author recorded." Reporting nil here would tell the human it landed
		// (notified:true) when nothing was ever enqueued.
		slog.Warn("plan feedback: could not load plan meta — cannot notify authoring coworker",
			"error", err, "slug", slug, "plan_dir", planDir)
		return err
	}
	if meta.Provenance == nil || meta.Provenance.AgentID == "" {
		return nil // no authoring coworker recorded → nobody to notify
	}
	prov := meta.Provenance
	title := fmt.Sprintf("Review feedback on plan %q (%d item(s) this round)", slug, items)
	task := &agenttask.Task{
		Title:       title,
		Kind:        agenttask.KindPlanFeedback,
		Priority:    30, // above routine chores: a human is waiting on the response
		Source:      "plan-review",
		TargetAgent: prov.AgentType, // type-level routing; "" = any coworker
		DedupKey:    "plan-feedback:" + prov.AgentID + ":" + slug,
		Payload:     map[string]string{"plan_slug": slug},
	}
	_, err = agenttask.Enqueue(gitRoot, task)
	if err != nil && !enqueueFailureIsSettled(err) {
		time.Sleep(100 * time.Millisecond)
		_, err = agenttask.Enqueue(gitRoot, task)
	}
	if err != nil {
		slog.Warn("plan feedback: enqueue notify task failed — authoring coworker not notified",
			"error", err, "slug", slug, "agent_id", prov.AgentID)
		return err
	}
	return nil
}

// enqueueSettledFailurePrefixes are agenttask.Enqueue error messages that are
// deterministic for a given task and gitRoot: retrying the exact same call
// reproduces the identical failure, so a retry only adds latency. This is a
// negative list — anything NOT matched here is treated as possibly-transient
// (DB open/lock/write contention) and gets one bounded retry.
var enqueueSettledFailurePrefixes = []string{
	"project root cannot be empty",
	"failed to resolve project root",
	"failed to create task directory", // MkdirAll blocked by a non-directory — waiting cannot fix this
	"task cannot be nil",
	"task title cannot be empty",
	"unknown task kind",
	"task title exceeds",
	"task body exceeds",
	"task payload exceeds",
	"task payload key",
	"new tasks must start",
	"failed to encode payload",
}

// enqueueFailureIsSettled reports whether err is a known, deterministic
// agenttask.Enqueue failure — a structural directory problem or a static task
// validation error — that a retry cannot fix.
func enqueueFailureIsSettled(err error) bool {
	msg := err.Error()
	for _, prefix := range enqueueSettledFailurePrefixes {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

// planFeedbackShowJSON is the `ox plan feedback show --json` shape. Items is
// the merged review. Complete is false when the read failed somewhere:
// Failures says where and why, and CorruptRounds names the round files that
// could not be read, so a consumer can tell "no feedback" from "feedback we
// failed to load". Conflicts are operations stored with differing content.
type planFeedbackShowJSON struct {
	Items         []plan.MergedItem     `json:"items"`
	CorruptRounds []string              `json:"corrupt_rounds"`
	Complete      bool                  `json:"complete"`
	Failures      []planreview.Failure  `json:"failures"`
	Conflicts     []planreview.Conflict `json:"conflicts"`
}

func runPlanFeedbackShow(cmd *cobra.Command, slug string, jsonOut bool) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	rev := plan.ReadReview(info.Dir)
	if jsonOut {
		out := planFeedbackShowJSON{Items: rev.Items, CorruptRounds: []string{}, Complete: rev.Problems() == 0,
			Failures: rev.Failures, Conflicts: rev.Conflicts}
		if out.Items == nil {
			out.Items = []plan.MergedItem{}
		}
		if out.Failures == nil {
			out.Failures = []planreview.Failure{}
		}
		if out.Conflicts == nil {
			out.Conflicts = []planreview.Conflict{}
		}
		for _, f := range rev.Failures {
			// an altered round is still read and shown
			if strings.HasPrefix(f.Path, "feedback/round-") && f.Reason != planreview.ReasonAltered {
				out.CorruptRounds = append(out.CorruptRounds, filepath.Join(info.Dir, filepath.FromSlash(f.Path)))
			}
		}
		return cli.PrintJSONTo(cmd.OutOrStdout(), out)
	}
	if !printPlanReviewDigest(cmd, info.Dir) {
		if _, werr := fmt.Fprintln(cmd.OutOrStdout(), "No review feedback for this plan yet."); werr != nil {
			return fmt.Errorf("write output: %w", werr)
		}
	}
	return nil
}

func runPlanFeedbackResolve(cmd *cobra.Command, slug, anchor, state, commit, note string) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	rec, err := newResolutionRecord(anchor, planreview.State(state), commit, note, time.Now())
	if err != nil {
		return err
	}
	st, _, err := publishReviewRecord(context.Background(), gitRoot, info.Dir, rec)
	if err != nil {
		return err
	}
	if !st.Pushed {
		cli.PrintHint("resolution saved locally, not yet shared with your team (`ox agent prime` retries it): " + st.Err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Marked %s as %s.\n\n", anchor, state)
	printPlanReviewDigest(cmd, info.Dir)
	return nil
}

// printPlanReviewDigest writes a plan's review digest, led by anything the
// read could not use, on stdout so an agent reading the digest cannot miss
// that it is incomplete. It reports whether anything was printed.
func printPlanReviewDigest(cmd *cobra.Command, planDir string) bool {
	rev := plan.ReadReview(planDir)
	text := plan.ReviewWarnings(rev) + plan.FeedbackDigest(rev.Items)
	if text == "" {
		return false
	}
	fmt.Fprint(cmd.OutOrStdout(), text)
	return true
}

func init() {
	planFeedbackApplyCmd.Flags().String("from", "", "feedback JSON file (use - or omit for stdin)")
	planFeedbackShowCmd.Flags().Bool("json", false, "emit {items, corrupt_rounds, complete, failures, conflicts} as JSON")
	planFeedbackResolveCmd.Flags().String("state", "addressed", "addressed | wontfix | verified")
	planFeedbackResolveCmd.Flags().String("commit", "", "commit SHA that made the change")
	planFeedbackResolveCmd.Flags().String("note", "", "what the agent did, or why wontfix")
	planFeedbackCmd.AddCommand(planFeedbackShowCmd)
	planFeedbackCmd.AddCommand(planFeedbackResolveCmd)
	planFeedbackCmd.AddCommand(planFeedbackApplyCmd)
	planCmd.AddCommand(planFeedbackCmd)
}
