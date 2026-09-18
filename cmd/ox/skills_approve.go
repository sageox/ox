package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/spf13/cobra"
)

// skills_approve.go — `ox skills approve`.
//
// This is the write half of the team-skill trust boundary. The read half shipped
// complete: TeamSkillSource classifies every discovered skill, withholds the
// executable ones, and `ox skills status` / `ox doctor` both report the hold with
// the evidence that caused it. What did not exist was any way to ACT on that
// report — ApprovalStore.Approve had no production caller, so an executable team
// skill was withheld permanently and the documented remedy was "hand-edit a JSON
// file containing a sha256 a human cannot compute."
//
// A gate nobody can open is not a safe default; it is a broken feature that
// looks like a safe default. The digest pinning below is what keeps it a gate:
// approval names bytes, so a skill that gains a script after approval returns to
// being withheld.
//
// The split between runSkillsApprove and decideApprovals is deliberate, and
// mirrors the sibling `ox skills status`: everything that needs cobra or the
// process's working directory stays in the RunE, and the decision itself takes
// the repository root as a parameter. That is what lets the decision be tested
// without faking a command.

var skillsApproveCmd = &cobra.Command{
	Use:   "approve [name...]",
	Short: "Approve runnable team-skill content for this repository",
	Long: `Approve runnable team-skill content that ox has withheld or omitted.

Run without arguments to list what is waiting and why, including the exact file
or line that made each one executable. Read that before deciding: a team skill
comes from a remote any teammate can push to, and nothing on the pull path
verifies signatures.

An approval is pinned to the exact bytes you approved and is recorded in
.sageox/team-skills.approvals.json, which is COMMITTED on purpose — the decision
belongs to the project, is inherited by teammates, and is reviewable in a pull
request. If the skill changes afterwards it returns to needing approval.

Approving lets an AI coworker READ the skill. Bundled scripts are still dropped
unless you also pass --allow-scripts, which is the larger, separate decision to
put runnable files on disk. Omitting the flag preserves an existing scripts
grant; pass --allow-scripts=false explicitly to revoke it.`,
	RunE: runSkillsApprove,
}

func init() {
	skillsApproveCmd.Flags().Bool("allow-scripts", false,
		"Also materialize the skill's bundled scripts as runnable files")
	skillsApproveCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsApproveCmd)
}

// skillApprovalRow is one line of the approval surface, in both renderings.
type skillApprovalRow struct {
	Name string `json:"name"`
	// State is one of the approveState* constants below.
	State        string `json:"state"`
	Digest       string `json:"digest,omitempty"`
	Capabilities string `json:"capabilities,omitempty"`
	// AllowScripts is deliberately not omitempty. False is the ANSWER to the
	// larger of the two decisions, and an absent key cannot be told apart from an
	// ox too old to report it — so omitting it made a parser fail open on exactly
	// the field that decides whether runnable files land on disk.
	AllowScripts bool   `json:"allow_scripts"`
	Detail       string `json:"detail,omitempty"`
	// ManifestRunnable selects the precise remediation but is not part of the
	// stable JSON contract; capabilities remain the public evidence.
	ManifestRunnable bool `json:"-"`
}

// skillsApproveOutput is the JSON shape AI coworkers parse.
//
// Pending, Approved and Guidance are always present — arrays are `[]` when
// empty, never absent and never null — matching the sibling
// `ox skills status --json`, which already always emits `team_skills: []`. A key
// that appears only sometimes forces every reader to guess whether its absence
// means "none" or "this ox does not report that."
type skillsApproveOutput struct {
	Pending  []skillApprovalRow `json:"pending"`
	Approved []skillApprovalRow `json:"approved"`
	Guidance string             `json:"guidance"`
}

const (
	approveStateApproved       = "approved"
	approveStateAlready        = "already-approved"
	approveStateNotNeeded      = "no-approval-needed"
	approveStateUnreadable     = "unreadable"
	approveStateNeeded         = "needs-approval"
	approveStateScriptsNeeded  = "scripts-need-approval"
	approveStateScriptsRevoked = "scripts-revoked"
)

// approveRequest is everything the decision needs. The repository root is a
// parameter rather than something the decision goes looking for, so a test can
// point it at a scratch repo without moving the process.
type approveRequest struct {
	GitRoot      string
	Names        []string
	AllowScripts bool
	// AllowScriptsSet distinguishes omission (preserve an existing grant) from
	// the explicit --allow-scripts=false revocation Cobra supports.
	AllowScriptsSet bool
}

// approveDecision is what decideApprovals concluded: what to render, the store
// as it should now read, and whether any of that differs from disk.
//
// Changed is what keeps a command that decided nothing from writing anything.
// .sageox/team-skills.approvals.json is COMMITTED; approving a prose-only skill
// used to save it unconditionally, dropping `{"schema_version": 1}` into the
// user's working tree for a command that recorded no decision at all.
type approveDecision struct {
	Output         skillsApproveOutput
	Store          *teamskills.ApprovalStore
	PreviousStore  *teamskills.ApprovalStore
	Changed        bool
	Reconcile      bool
	RevokedScripts bool
}

func runSkillsApprove(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	allowScripts, _ := cmd.Flags().GetBool("allow-scripts")
	allowScriptsSet := cmd.Flags().Changed("allow-scripts")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	decision, err := executeApprovals(approveRequest{
		GitRoot: gitRoot, Names: args, AllowScripts: allowScripts,
		AllowScriptsSet: allowScriptsSet,
	})
	if err != nil {
		return err
	}

	return emitApprovals(cmd.OutOrStdout(), decision.Output, asJSON)
}

// executeApprovals keeps the approval store's entire read-modify-write,
// reconcile, and possible rollback under one cross-process lock. Atomic store
// replacement prevents torn JSON, but without this transaction two concurrent
// commands can both read the same snapshot and the later save silently discards
// the earlier command's grant.
func executeApprovals(req approveRequest) (approveDecision, error) {
	var decision approveDecision
	err := fileutil.WithFileLock(context.Background(), teamskills.ApprovalPath(req.GitRoot), func() error {
		var err error
		decision, err = decideApprovals(req)
		if err != nil {
			return err
		}

		if decision.Changed {
			if err := decision.Store.Save(req.GitRoot); err != nil {
				// Name the file. Every read this command already did also lives under
				// .sageox/, so a bare filesystem error here is indistinguishable from a
				// config that could not be loaded — and those need opposite fixes.
				return fmt.Errorf("record the approval in %s: %w", teamskills.ApprovalPath(req.GitRoot), err)
			}
		}

		if !decision.Reconcile {
			return nil
		}
		// Materialize in the same command. Recording an approval and then telling
		// the human to run a second command leaves the repository in the exact
		// state the approval was meant to end — the skill still absent — which
		// reads as the approval not having worked.
		if _, err := reconcileExactSelectedSkills(req.GitRoot); err != nil {
			if decision.RevokedScripts && decision.PreviousStore != nil {
				if restoreErr := decision.PreviousStore.Save(req.GitRoot); restoreErr != nil {
					return fmt.Errorf("revoking scripts failed: %w; restoring the previous approval also failed: %w", err, restoreErr)
				}
				return fmt.Errorf("revoking scripts failed and the previous approval was restored: %w", err)
			}
			if decision.Changed {
				return fmt.Errorf("approval recorded, but installing the skill failed: %w", err)
			}
			return fmt.Errorf("installing the skill failed: %w", err)
		}
		return nil
	})
	return decision, err
}

// decideApprovals resolves a request against the team checkout and the committed
// store, WITHOUT writing anything. The caller persists only if Changed.
func decideApprovals(req approveRequest) (approveDecision, error) {
	candidates, err := skillmanager.ClassifyTeamSkills(req.GitRoot)
	if err != nil {
		return approveDecision{}, err
	}

	store, err := teamskills.LoadApprovals(req.GitRoot)
	if err != nil {
		// Same refusal the reconcile path makes: a store ox cannot parse is not an
		// empty store, and writing a fresh one here would silently discard every
		// approval the project had already recorded.
		return approveDecision{}, err
	}

	decision := approveDecision{
		Store: store, PreviousStore: cloneApprovalStore(store), Output: newApproveOutput(),
	}
	if len(req.Names) == 0 {
		decision.Output = pendingApprovals(candidates, store)
		return decision, nil
	}

	byName := make(map[string]skillmanager.TeamSkillCandidate, len(candidates))
	for _, c := range candidates {
		byName[c.Name] = c
	}

	seen := make(map[string]bool, len(req.Names))
	for _, name := range req.Names {
		c, ok := byName[name]
		if !ok {
			// Refused before anything is persisted, so a run naming several skills
			// is all-or-nothing: a typo in the last name cannot leave the earlier
			// ones half-approved with no record of which.
			return approveDecision{}, fmt.Errorf("no team skill named %q applies to this repository%s%s",
				name, availableSuffix(candidates), nothingApprovedSuffix(req.Names))
		}
		if seen[name] {
			// One decision, one row. Without this, `approve deploy deploy` reported
			// the skill as approved and then as already-approved by its own first
			// pass, which reads as two skills or as a race.
			continue
		}
		seen[name] = true

		switch {
		case c.LoadErr != nil:
			// Refuse rather than record. An approval is a statement about bytes; if
			// ox could not read the bytes it has nothing to pin, and a stored digest
			// over a partial read would be worse than no approval at all.
			return approveDecision{}, fmt.Errorf("cannot approve %q: %w", name, c.LoadErr)
		case !c.Verdict.Executable:
			decision.Output.Approved = append(decision.Output.Approved, skillApprovalRow{
				Name: name, State: approveStateNotNeeded,
				Detail: "prose only — it already materializes without approval",
			})
			continue
		}

		approved := store.Decide(name, c.Verdict) == teamskills.DecisionMaterialize
		previousScripts := store.ScriptsExecutable(name, c.Verdict)
		desiredScripts := req.AllowScripts
		if !req.AllowScriptsSet && approved {
			desiredScripts = previousScripts
		}

		// A bundled-script-only skill is already readable: reconciliation installs
		// its prose before any approval exists. A bare approval would record a
		// meaningless manifest grant and still omit the only pending capability, so
		// leave the store untouched and name the decision the user actually needs.
		if !c.ManifestRunnable && !desiredScripts && (!approved || !previousScripts || !req.AllowScriptsSet) {
			decision.Output.Approved = append(decision.Output.Approved, skillApprovalRow{
				Name: name, State: approveStateScriptsNeeded, Digest: c.Verdict.Digest,
				Capabilities: c.Verdict.Describe(), AllowScripts: false,
				Detail: fmt.Sprintf("instructions are already installed; run `ox skills approve --allow-scripts %s` to materialize bundled scripts", name),
			})
			continue
		}

		switch {
		case approved && previousScripts == desiredScripts:
			decision.Output.Approved = append(decision.Output.Approved, skillApprovalRow{
				Name: name, State: approveStateAlready, Digest: c.Verdict.Digest,
				Capabilities: c.Verdict.Describe(), AllowScripts: desiredScripts,
			})
			decision.Reconcile = true
			continue
		}

		// Reaching here means the recorded decision is about to change. Only one
		// combination shrinks a grant: these exact bytes are already approved, and
		// this run withholds the scripts the last one allowed.
		row := skillApprovalRow{
			Name: name, State: approveStateApproved, Digest: c.Verdict.Digest,
			Capabilities: c.Verdict.Describe(), AllowScripts: desiredScripts,
		}
		if !desiredScripts && approved && previousScripts {
			row.State = approveStateScriptsRevoked
			row.Detail = "bundled scripts are no longer materialized here; the skill stays approved for its instructions"
			decision.RevokedScripts = true
		}
		store.Approve(name, c.Verdict, desiredScripts)
		decision.Changed = true
		decision.Reconcile = true
		decision.Output.Approved = append(decision.Output.Approved, row)
	}

	decision.Output.Pending = pendingApprovals(candidates, store).Pending
	decision.Output.Guidance = approveGuidance(decision.Output)
	return decision, nil
}

// newApproveOutput starts both lists non-nil so the JSON carries `[]` rather
// than `null` for an empty result.
func newApproveOutput() skillsApproveOutput {
	return skillsApproveOutput{Pending: []skillApprovalRow{}, Approved: []skillApprovalRow{}}
}

func cloneApprovalStore(store *teamskills.ApprovalStore) *teamskills.ApprovalStore {
	clone := *store
	clone.Approvals = append([]teamskills.Approval(nil), store.Approvals...)
	for i := range clone.Approvals {
		clone.Approvals[i].Capabilities = append([]teamskills.Capability(nil), store.Approvals[i].Capabilities...)
	}
	return &clone
}

// pendingApprovals lists what is still withheld after consulting the store.
func pendingApprovals(candidates []skillmanager.TeamSkillCandidate, store *teamskills.ApprovalStore) skillsApproveOutput {
	out := newApproveOutput()
	for _, c := range candidates {
		if c.LoadErr != nil {
			out.Pending = append(out.Pending, skillApprovalRow{
				Name: c.Name, State: approveStateUnreadable, Detail: c.LoadErr.Error(),
			})
			continue
		}
		needsManifest := c.ManifestRunnable && store.Decide(c.Name, c.Verdict) == teamskills.DecisionNeedsApproval
		needsScripts := candidateHasCapability(c, teamskills.CapBundledScript) && !store.ScriptsExecutable(c.Name, c.Verdict)
		if !needsManifest && !needsScripts {
			continue
		}
		out.Pending = append(out.Pending, skillApprovalRow{
			Name: c.Name, State: approveStateNeeded, Digest: c.Verdict.Digest,
			Capabilities: c.Verdict.Describe(), ManifestRunnable: needsManifest,
		})
	}
	sort.Slice(out.Pending, func(i, j int) bool { return out.Pending[i].Name < out.Pending[j].Name })
	out.Guidance = approveGuidance(out)
	return out
}

func candidateHasCapability(candidate skillmanager.TeamSkillCandidate, want teamskills.Capability) bool {
	for _, capability := range candidate.Verdict.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

// approveGuidance is the single next action, carried in the JSON so an AI
// coworker reading this gets the same answer a human reads off the terminal.
//
// Every branch has to be true of the run that produced it. "Approved. The skill
// is installed" is the answer to exactly one thing — a new grant — and saying it
// after a no-op, or after a grant was taken away, teaches a reader that the line
// means nothing.
func approveGuidance(out skillsApproveOutput) string {
	for _, row := range out.Pending {
		if row.State == approveStateUnreadable {
			return fmt.Sprintf("team skill %q could not be read: %s", row.Name, row.Detail)
		}
	}
	if len(out.Pending) > 0 {
		p := out.Pending[0]
		if !p.ManifestRunnable {
			return fmt.Sprintf("%q is installed without its bundled scripts because of %s. Read them in your Team Context, then run `ox skills approve --allow-scripts %s`.",
				p.Name, p.Capabilities, p.Name)
		}
		return fmt.Sprintf("%q needs approval because its manifest is runnable: %s. Read it in your Team Context, then run `ox skills approve %s` (add --allow-scripts to also put bundled scripts on disk).",
			p.Name, p.Capabilities, p.Name)
	}

	var revoked string
	var scriptsNeeded string
	var sawApproved, sawAlready, sawNotNeeded bool
	for _, row := range out.Approved {
		switch row.State {
		case approveStateScriptsRevoked:
			if revoked == "" {
				revoked = row.Name
			}
		case approveStateScriptsNeeded:
			if scriptsNeeded == "" {
				scriptsNeeded = row.Name
			}
		case approveStateApproved:
			sawApproved = true
		case approveStateAlready:
			sawAlready = true
		case approveStateNotNeeded:
			sawNotNeeded = true
		}
	}
	switch {
	case revoked != "":
		return fmt.Sprintf("%q is approved for its instructions only — its bundled scripts were REMOVED from this repository. Run `ox skills approve --allow-scripts %s` to put them back.", revoked, revoked)
	case scriptsNeeded != "":
		return fmt.Sprintf("%q is already readable; no approval was recorded. Run `ox skills approve --allow-scripts %s` to materialize its bundled scripts.", scriptsNeeded, scriptsNeeded)
	case sawApproved:
		return "Approved. The skill is installed — your AI coworkers can use it now."
	case sawAlready:
		return "Already approved at these exact bytes. Nothing changed."
	case sawNotNeeded:
		return "No approval was needed — that skill is prose only and already materializes."
	}
	return "No team skills are waiting for approval."
}

// availableSuffix names what the user could have meant, because a bare "no such
// skill" cannot distinguish a typo from a skill whose repos: filter excludes
// this repository — and those need opposite fixes.
func availableSuffix(candidates []skillmanager.TeamSkillCandidate) string {
	if len(candidates) == 0 {
		return " (this repository sees no team skills at all — run `ox skills status` to find out why)"
	}
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return "; it sees: " + strings.Join(names, ", ")
}

// nothingApprovedSuffix says the run was all-or-nothing, but only when more than
// one name was given — that is the only case where a reader could reasonably
// wonder whether the earlier names took effect.
func nothingApprovedSuffix(names []string) string {
	if len(names) < 2 {
		return ""
	}
	return "; nothing was approved"
}

func emitApprovals(w io.Writer, out skillsApproveOutput, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	for _, row := range out.Approved {
		switch row.State {
		case approveStateNotNeeded:
			p("%-24s %s", row.Name, row.Detail)
		case approveStateScriptsNeeded:
			p("%-24s %s", row.Name, row.Detail)
		case approveStateAlready:
			p("%-24s already approved at this digest", row.Name)
		case approveStateScriptsRevoked:
			p("%s %s", cli.StyleWarning.Render("scripts revoked"), row.Name)
			p("  covers      %s", row.Capabilities)
			p("  digest      %s", row.Digest)
			p("  materialize instructions only — the bundled scripts were removed from this repository")
		default:
			scripts := "instructions only"
			if row.AllowScripts {
				scripts = "instructions AND bundled scripts are now runnable"
			}
			p("%s %s", cli.StyleAccent.Render("approved"), row.Name)
			p("  covers      %s", row.Capabilities)
			p("  digest      %s", row.Digest)
			p("  materialize %s", scripts)
		}
	}

	if len(out.Pending) > 0 {
		if len(out.Approved) > 0 {
			p("")
		}
		p("%s", cli.StyleWarning.Render("Waiting for approval"))
		for _, row := range out.Pending {
			if row.State == approveStateUnreadable {
				p("  %-22s UNREADABLE — %s", row.Name, row.Detail)
				continue
			}
			p("  %-22s %s", row.Name, row.Capabilities)
		}
	}
	if out.Guidance != "" {
		p("")
		p("%s", out.Guidance)
	}
	return nil
}
