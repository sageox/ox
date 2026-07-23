package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_lifecycle.go implements the `ox plan` lifecycle verbs — approve, work,
// realize, abandon, supersede — plus the pure `status` reader. All 5 verbs
// are thin sugar over the single plan.AppendPlanEvent engine (see
// internal/plan/lifecycle.go); the review server's /approve endpoint
// (plan_review.go) calls the exact same engine, so browser Approve and
// `ox plan approve` are one mechanism, never two.
//
// Each verb command resolves <slug> to its ledger directory via plan.Load,
// then delegates to a "...OnDir" core that takes the directory directly —
// split out so the event-construction/JSON-shape logic is unit-testable
// without standing up a real ledger (see plan_lifecycle_test.go).

var planApproveCmd = &cobra.Command{
	Use:   "approve <slug>",
	Short: "Mark a saved plan approved",
	Long: `Mark a saved plan approved — the same action a reviewer's Approve button
in 'ox plan review' takes (both go through the same event-log engine).
Idempotent: approving an already-approved plan is a no-op (changed:false).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlanLifecycleVerb(cmd, args[0], plan.EventApproved, lifecycleVerbFlags{})
	},
}

var planWorkCmd = &cobra.Command{
	Use:   "work <slug>",
	Short: "Record that a coding session worked on a saved plan",
	Long: `Record that a coding session worked on a saved plan. Edge-only: it never
changes the plan's Status, only its session history. --session overrides the
ambient session detected from a live recording (explicit beats inferred).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		session, _ := cmd.Flags().GetString("session")
		return runPlanLifecycleVerb(cmd, args[0], plan.EventWorked, lifecycleVerbFlags{Session: session})
	},
}

var planRealizeCmd = &cobra.Command{
	Use:   "realize <slug>",
	Short: "Mark a saved plan realized (its intent was carried out)",
	Long: `Mark a saved plan realized — the artifact-agnostic terminal status: the
plan's intent was carried out by some means, not necessarily "implemented"
(code shipped). One event records both the status change and the session
edge in a single append. --produced records what shipped (a PR URL, commit,
etc.); --session overrides the ambient session.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		produced, _ := cmd.Flags().GetString("produced")
		session, _ := cmd.Flags().GetString("session")
		return runPlanLifecycleVerb(cmd, args[0], plan.EventRealized, lifecycleVerbFlags{Produced: produced, Session: session})
	},
}

var planAbandonCmd = &cobra.Command{
	Use:   "abandon <slug>",
	Short: "Mark a saved plan abandoned",
	Long:  `Mark a saved plan abandoned. --reason records why, for the status timeline.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		return runPlanLifecycleVerb(cmd, args[0], plan.EventAbandoned, lifecycleVerbFlags{Reason: reason})
	},
}

var planSupersedeCmd = &cobra.Command{
	Use:   "supersede <slug>",
	Short: "Mark a saved plan superseded by a successor plan",
	Long: `Mark a saved plan superseded by a successor plan. --by <successor-slug> is
required and must resolve to another saved plan — validated before anything
is recorded.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		by, _ := cmd.Flags().GetString("by")
		successorSlug, err := resolveSupersedeSuccessor(findGitRoot(), args[0], by)
		if err != nil {
			return err
		}
		return runPlanLifecycleVerb(cmd, args[0], plan.EventSuperseded, lifecycleVerbFlags{SupersededBy: successorSlug})
	},
}

// resolveSupersedeSuccessor validates --by is set and resolves it to a real
// saved plan (via plan.Load) other than slug itself, returning the
// successor's canonical slug to record as SupersededBy. Split out from
// planSupersedeCmd's RunE so all three guards — missing --by, a --by that
// doesn't resolve, and a --by that resolves back to the plan being
// superseded — run (and are testable) BEFORE runPlanLifecycleVerb ever calls
// AppendPlanEvent.
func resolveSupersedeSuccessor(gitRoot, slug, by string) (string, error) {
	if by == "" {
		return "", fmt.Errorf("--by <successor-slug> is required")
	}
	_, _, successor, err := plan.Load(gitRoot, by)
	if err != nil {
		return "", fmt.Errorf("resolve --by successor %q: %w", by, err)
	}
	if successor.Slug == slug {
		return "", fmt.Errorf("--by %q cannot be the plan being superseded", by)
	}
	return successor.Slug, nil
}

var planStatusCmd = &cobra.Command{
	Use:   "status <slug>",
	Short: "Show a saved plan's lifecycle timeline and current status",
	Long: `Show a saved plan's lifecycle: the folded current status plus the full
events.jsonl timeline (created/revised/approved/worked/realized/abandoned/
superseded), one line per event. Pure reader — never writes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		gitRoot := findGitRoot()
		_, _, info, err := plan.Load(gitRoot, args[0])
		if err != nil {
			return fmt.Errorf("load plan %q: %w", args[0], err)
		}
		return runPlanStatusOnDir(cmd, info.Dir, info.Topic, info.Slug, jsonOut)
	},
}

// lifecycleVerbFlags carries the verb-specific flag values
// runPlanLifecycleVerb layers onto the ambient provenance from
// resolvePlanProvenance.
type lifecycleVerbFlags struct {
	Session      string // work/realize: explicit SessionID override (explicit beats ambient)
	Produced     string // realize
	Reason       string // abandon
	SupersededBy string // supersede: already-resolved + validated successor slug
}

// runPlanLifecycleVerb resolves <slug> to its ledger directory and delegates
// to runPlanLifecycleVerbOnDir.
func runPlanLifecycleVerb(cmd *cobra.Command, slug string, kind plan.EventKind, flags lifecycleVerbFlags) error {
	gitRoot := findGitRoot()
	_, _, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	return runPlanLifecycleVerbOnDir(cmd, gitRoot, info.Dir, slug, kind, flags)
}

// runPlanLifecycleVerbOnDir builds the event fields from the live provenance
// (resolvePlanProvenance — never re-derived) layered with the verb's own
// flags, calls the single AppendPlanEvent engine, and prints
// {"changed":...,"status":...} with --json, else a short human line.
func runPlanLifecycleVerbOnDir(cmd *cobra.Command, gitRoot, planDir, slug string, kind plan.EventKind, flags lifecycleVerbFlags) error {
	jsonOut, _ := cmd.Flags().GetBool("json")

	fields := planEventFieldsFromProvenance(gitRoot)
	if flags.Session != "" {
		fields.SessionID = flags.Session // explicit beats ambient
	}
	fields.Produced = flags.Produced
	fields.Reason = flags.Reason
	fields.SupersededBy = flags.SupersededBy

	changed, err := plan.AppendPlanEvent(cmd.Context(), planDir, kind, fields)
	if err != nil {
		return fmt.Errorf("record %s event for plan %q: %w", kind, slug, err)
	}
	if changed {
		// best-effort, off-by-default caller-driven index report — see
		// plan_activity.go. Only on an actual change, matching the no-op
		// re-run contract the rest of this function already honors.
		postPlanActivityBestEffort(gitRoot, planDir, kind)
	}

	status := currentPlanStatus(planDir)
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(planLifecycleResult{Changed: changed, Status: status})
	}
	return writePlanLifecycleHuman(cmd, slug, changed, status)
}

// planEventFieldsFromProvenance builds the provenance portion of
// plan.PlanEventFields from resolvePlanProvenance's ambient detection.
// AppendPlanEvent's provenance is always the caller's resolved value, never
// re-derived a different way — mirrors Save's own contract (see
// plan_collaboration.go).
func planEventFieldsFromProvenance(gitRoot string) plan.PlanEventFields {
	prov, _ := resolvePlanProvenance(gitRoot)
	if prov == nil {
		return plan.PlanEventFields{}
	}
	return plan.PlanEventFields{
		SessionID:   prov.SessionID,
		SessionName: prov.SessionName,
		AgentID:     prov.AgentID,
		AgentEnv:    prov.AgentType,
		Model:       prov.Model,
		Author:      prov.AuthorName,
	}
}

// currentPlanStatus re-folds a plan's event log for its current status after
// AppendPlanEvent's write (or no-op) has settled. "" on any read error — the
// verb itself already succeeded, so a status read hiccup here must not turn
// into a command failure.
func currentPlanStatus(planDir string) plan.PlanStatus {
	events, err := plan.LoadEvents(planDir)
	if err != nil {
		return ""
	}
	return plan.Fold(events).Status
}

// planLifecycleResult is the `--json` shape for every lifecycle verb:
// {"changed":bool,"status":"..."}. A no-op (changed:false) still reports the
// plan's current status — useful to a caller without a follow-up
// `ox plan status`.
type planLifecycleResult struct {
	Changed bool            `json:"changed"`
	Status  plan.PlanStatus `json:"status,omitempty"`
}

func writePlanLifecycleHuman(cmd *cobra.Command, slug string, changed bool, status plan.PlanStatus) error {
	out := cmd.OutOrStdout()
	if !changed {
		fmt.Fprintf(out, "No change — nothing new to record for %q (status: %s).\n", slug, status)
		return nil
	}
	fmt.Fprintf(out, "%s %q — status: %s.\n", cli.StyleSuccess.Render("✓"), slug, status)
	return nil
}

// runPlanStatusOnDir is the testable core of `ox plan status`: LoadEvents →
// Fold → print, split from planStatusCmd's RunE so it doesn't need a real
// ledger to test.
func runPlanStatusOnDir(cmd *cobra.Command, planDir, topic, slug string, jsonOut bool) error {
	events, err := plan.LoadEvents(planDir)
	if err != nil {
		return fmt.Errorf("load plan events %q: %w", slug, err)
	}
	folded := plan.Fold(events)

	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(planStatusJSON{
			PlanID:     folded.PlanID,
			Slug:       slug,
			Status:     folded.Status,
			Authors:    nonNilStrings(folded.Authors),
			CreatedAt:  folded.CreatedAt,
			UpdatedAt:  folded.UpdatedAt,
			Visibility: folded.Visibility,
			Sessions:   toSessionsJSON(folded.Sessions),
		})
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, cli.StyleBrand.Render(topic))
	fmt.Fprintf(out, "slug: %s   status: %s\n", slug, folded.Status)
	if len(folded.Authors) > 0 {
		fmt.Fprintf(out, "authors: %s\n", strings.Join(folded.Authors, ", "))
	}
	fmt.Fprintln(out)
	if len(events) == 0 {
		fmt.Fprintln(out, "No lifecycle events recorded yet.")
		return nil
	}
	for _, ev := range events {
		fmt.Fprintf(out, "%s  %-14s", ev.Timestamp.Local().Format("2006-01-02 15:04"), ev.Kind)
		if actor := eventActorLabel(ev); actor != "" {
			fmt.Fprintf(out, " %s", actor)
		}
		if ev.Status != "" {
			fmt.Fprintf(out, " → %s", ev.Status)
		}
		if ev.Reason != "" {
			fmt.Fprintf(out, " (%s)", ev.Reason)
		}
		if ev.SupersededBy != "" {
			fmt.Fprintf(out, " by %s", ev.SupersededBy)
		}
		if ev.Produced != "" {
			fmt.Fprintf(out, " produced %s", ev.Produced)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// eventActorLabel prefers the human author name, falling back to the agent
// id, for the status timeline's per-line actor label.
func eventActorLabel(ev plan.Event) string {
	if ev.Author != "" {
		return ev.Author
	}
	if ev.AgentID != "" {
		return ev.AgentID
	}
	return ""
}

// nonNilStrings normalizes a nil slice to an empty one so the --json output
// always serializes "authors":[] rather than "authors":null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// planStatusJSON is the `ox plan status --json` output shape — a
// current-state projection of a plan's event log matching api-go's PlanMeta
// field names (plan_id, slug, status, authors, created_at, updated_at,
// visibility, sessions) so the CLI and web agree on shape without either
// importing the other's type across repos.
type planStatusJSON struct {
	PlanID     string            `json:"plan_id"`
	Slug       string            `json:"slug"`
	Status     plan.PlanStatus   `json:"status"`
	Authors    []string          `json:"authors"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Visibility string            `json:"visibility"`
	Sessions   []planSessionJSON `json:"sessions"`
}

type planSessionJSON struct {
	SessionName string           `json:"session_name,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	Author      string           `json:"author,omitempty"`
	Kinds       []plan.EventKind `json:"kinds"`
	FirstAt     time.Time        `json:"first_at"`
	LastAt      time.Time        `json:"last_at"`
}

func toSessionsJSON(refs []plan.SessionRef) []planSessionJSON {
	out := make([]planSessionJSON, 0, len(refs))
	for _, r := range refs {
		out = append(out, planSessionJSON{
			SessionName: r.SessionName,
			SessionID:   r.SessionID,
			Author:      r.Author,
			Kinds:       r.Kinds,
			FirstAt:     r.FirstAt,
			LastAt:      r.LastAt,
		})
	}
	return out
}
