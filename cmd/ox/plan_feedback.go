package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
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
	var raw []byte
	var err error
	if from == "" || from == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(from)
	}
	if err != nil {
		return fmt.Errorf("read feedback: %w", err)
	}
	set, err := plan.ParseFeedback(raw)
	if err != nil {
		return err
	}
	if slug == "" {
		slug = set.Slug
	}
	if slug == "" {
		return fmt.Errorf("no slug: pass it as an argument or include it in the feedback JSON")
	}
	set.Slug = slug

	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	path, err := plan.SaveFeedback(info.Dir, set, time.Now())
	if err != nil {
		return err
	}
	if cerr := commitPlanToLedger(gitRoot, info.Dir); cerr != nil {
		cli.PrintHint("feedback saved locally; ledger commit deferred: " + cerr.Error())
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Applied %d feedback item(s) to %s\n\n", len(set.Items), cli.StyleFile.Render(path))
	if _, derr := printPlanReviewDigest(cmd, info.Dir); derr != nil {
		return derr
	}
	return nil
}

func runPlanFeedbackShow(cmd *cobra.Command, slug string, jsonOut bool) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	if jsonOut {
		items, err := plan.AssembleReview(info.Dir)
		if err != nil {
			return err
		}
		if items == nil {
			items = []plan.MergedItem{}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}
	shown, derr := printPlanReviewDigest(cmd, info.Dir)
	if derr != nil {
		return derr
	}
	if !shown {
		fmt.Fprintln(cmd.OutOrStdout(), "No review feedback for this plan yet.")
	}
	return nil
}

func runPlanFeedbackResolve(cmd *cobra.Command, slug, anchor, state, commit, note string) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	r := plan.Resolution{
		Anchor: anchor,
		State:  plan.ResolutionState(state),
		Commit: commit,
		Note:   note,
	}
	if err := plan.AppendResolution(info.Dir, r, time.Now()); err != nil {
		return err
	}
	if cerr := commitPlanToLedger(gitRoot, info.Dir); cerr != nil {
		cli.PrintHint("resolution saved locally; ledger commit deferred: " + cerr.Error())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Marked %s as %s.\n\n", anchor, state)
	if _, derr := printPlanReviewDigest(cmd, info.Dir); derr != nil {
		return derr
	}
	return nil
}

// printPlanReviewDigest writes the merged review digest for a plan dir. The bool
// reports whether anything was printed; a read failure is returned as an error so
// callers don't conflate "no feedback" with "couldn't read it".
func printPlanReviewDigest(cmd *cobra.Command, planDir string) (bool, error) {
	items, err := plan.AssembleReview(planDir)
	if err != nil {
		return false, fmt.Errorf("read review state: %w", err)
	}
	digest := plan.FeedbackDigest(items)
	if digest == "" {
		return false, nil
	}
	fmt.Fprint(cmd.OutOrStdout(), digest)
	return true, nil
}

func init() {
	planFeedbackApplyCmd.Flags().String("from", "", "feedback JSON file (use - or omit for stdin)")
	planFeedbackShowCmd.Flags().Bool("json", false, "emit the merged review items as JSON")
	planFeedbackResolveCmd.Flags().String("state", "addressed", "addressed | wontfix | verified")
	planFeedbackResolveCmd.Flags().String("commit", "", "commit SHA that made the change")
	planFeedbackResolveCmd.Flags().String("note", "", "what the agent did, or why wontfix")
	planFeedbackCmd.AddCommand(planFeedbackShowCmd)
	planFeedbackCmd.AddCommand(planFeedbackResolveCmd)
	planFeedbackCmd.AddCommand(planFeedbackApplyCmd)
	planCmd.AddCommand(planFeedbackCmd)
}
