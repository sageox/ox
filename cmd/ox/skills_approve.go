package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/cli"
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

var skillsApproveCmd = &cobra.Command{
	Use:   "approve [name...]",
	Short: "Approve an executable team skill so it can materialize here",
	Long: `Approve team skills that ox is withholding because they can execute code.

Run without arguments to list what is waiting and why, including the exact file
or line that made each one executable. Read that before deciding: a team skill
comes from a remote any teammate can push to, and nothing on the pull path
verifies signatures.

An approval is pinned to the exact bytes you approved and is recorded in
.sageox/team-skills.approvals.json, which is COMMITTED on purpose — the decision
belongs to the project, is inherited by teammates, and is reviewable in a pull
request. If the skill changes afterwards it returns to needing approval.

Approving lets an agent READ the skill. Bundled scripts are still dropped unless
you also pass --allow-scripts, which is the larger, separate decision to put
runnable files on disk.`,
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
	// State is one of: approved, already-approved, no-approval-needed, unreadable.
	State        string `json:"state"`
	Digest       string `json:"digest,omitempty"`
	Capabilities string `json:"capabilities,omitempty"`
	AllowScripts bool   `json:"allow_scripts,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

type skillsApproveOutput struct {
	Pending  []skillApprovalRow `json:"pending,omitempty"`
	Approved []skillApprovalRow `json:"approved,omitempty"`
	Guidance string             `json:"guidance,omitempty"`
}

const (
	approveStateApproved   = "approved"
	approveStateAlready    = "already-approved"
	approveStateNotNeeded  = "no-approval-needed"
	approveStateUnreadable = "unreadable"
)

func runSkillsApprove(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	allowScripts, _ := cmd.Flags().GetBool("allow-scripts")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	candidates, err := skillmanager.ClassifyTeamSkills(gitRoot)
	if err != nil {
		return err
	}
	byName := map[string]skillmanager.TeamSkillCandidate{}
	for _, c := range candidates {
		byName[c.Name] = c
	}

	store, err := teamskills.LoadApprovals(gitRoot)
	if err != nil {
		// Same refusal the reconcile path makes: a store ox cannot parse is not an
		// empty store, and writing a fresh one here would silently discard every
		// approval the project had already recorded.
		return err
	}

	if len(args) == 0 {
		out := pendingApprovals(candidates, store)
		return emitApprovals(cmd.OutOrStdout(), out, asJSON)
	}

	var out skillsApproveOutput
	for _, name := range args {
		c, ok := byName[name]
		if !ok {
			return fmt.Errorf("no team skill named %q applies to this repository%s",
				name, availableSuffix(candidates))
		}
		switch {
		case c.LoadErr != nil:
			// Refuse rather than record. An approval is a statement about bytes; if
			// ox could not read the bytes it has nothing to pin, and a stored digest
			// over a partial read would be worse than no approval at all.
			return fmt.Errorf("cannot approve %q: %w", name, c.LoadErr)
		case !c.Verdict.Executable:
			out.Approved = append(out.Approved, skillApprovalRow{
				Name: name, State: approveStateNotNeeded,
				Detail: "prose only — it already materializes without approval",
			})
			continue
		case store.Decide(name, c.Verdict) == teamskills.DecisionMaterialize &&
			store.ScriptsExecutable(name, c.Verdict) == allowScripts:
			out.Approved = append(out.Approved, skillApprovalRow{
				Name: name, State: approveStateAlready, Digest: c.Verdict.Digest,
				Capabilities: c.Verdict.Describe(), AllowScripts: allowScripts,
			})
			continue
		}
		store.Approve(name, c.Verdict, allowScripts)
		out.Approved = append(out.Approved, skillApprovalRow{
			Name: name, State: approveStateApproved, Digest: c.Verdict.Digest,
			Capabilities: c.Verdict.Describe(), AllowScripts: allowScripts,
		})
	}

	if err := store.Save(gitRoot); err != nil {
		return err
	}

	// Materialize in the same command. Recording an approval and then telling the
	// human to run a second command leaves the repository in the exact state the
	// approval was meant to end — the skill still absent — which reads as the
	// approval not having worked.
	if _, err := reconcileCommittedSkills(gitRoot); err != nil {
		return fmt.Errorf("approval recorded, but installing the skill failed: %w", err)
	}

	out.Pending = pendingApprovals(candidates, store).Pending
	out.Guidance = approveGuidance(out, gitRoot)
	return emitApprovals(cmd.OutOrStdout(), out, asJSON)
}

// pendingApprovals lists what is still withheld after consulting the store.
func pendingApprovals(candidates []skillmanager.TeamSkillCandidate, store *teamskills.ApprovalStore) skillsApproveOutput {
	out := skillsApproveOutput{}
	for _, c := range candidates {
		if c.LoadErr != nil {
			out.Pending = append(out.Pending, skillApprovalRow{
				Name: c.Name, State: approveStateUnreadable, Detail: c.LoadErr.Error(),
			})
			continue
		}
		if store.Decide(c.Name, c.Verdict) != teamskills.DecisionNeedsApproval {
			continue
		}
		out.Pending = append(out.Pending, skillApprovalRow{
			Name: c.Name, State: "needs-approval", Digest: c.Verdict.Digest,
			Capabilities: c.Verdict.Describe(),
		})
	}
	sort.Slice(out.Pending, func(i, j int) bool { return out.Pending[i].Name < out.Pending[j].Name })
	out.Guidance = approveGuidance(out, "")
	return out
}

// approveGuidance is the single next action, carried in the JSON so an AI
// coworker reading this gets the same answer a human reads off the terminal.
func approveGuidance(out skillsApproveOutput, _ string) string {
	for _, row := range out.Pending {
		if row.State == approveStateUnreadable {
			return fmt.Sprintf("team skill %q could not be read: %s", row.Name, row.Detail)
		}
	}
	if len(out.Pending) > 0 {
		p := out.Pending[0]
		return fmt.Sprintf("%q needs approval because of %s. Read that in your Team Context, then run `ox skills approve %s` (add --allow-scripts to also put its bundled scripts on disk).",
			p.Name, p.Capabilities, p.Name)
	}
	if len(out.Approved) > 0 {
		return "Approved. The skill is installed — your AI coworkers can use it now."
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
		case approveStateAlready:
			p("%-24s already approved at this digest", row.Name)
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
