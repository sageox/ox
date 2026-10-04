package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/spf13/cobra"
)

var skillsRevokeCmd = &cobra.Command{
	Use:     "revoke <name>...",
	Aliases: []string{"unapprove"},
	Short:   "Revoke approvals for runnable Team Skill content",
	Long: `Revoke digest-pinned approvals for runnable Team Skill content.

The approval is removed from .sageox/team-skills.approvals.json and the current
repository is reconciled immediately. Content that required the approval leaves
every selected skill target; readable prose that never required approval may
remain installed.

Revocation is all-or-nothing across the names in one invocation: a misspelled or
unapproved name refuses the run before any approval changes. If cleanup cannot
finish, the approval stays revoked and the command returns an error so the next
reconcile can finish removing the installed content.

This withdraws the whole approval. To keep a skill's instructions approved and
withdraw only its bundled scripts, run ` + "`ox skills approve --allow-scripts=false <name>`" + `
instead.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSkillsRevoke,
}

type skillsRevokeOutput struct {
	Revoked  []string `json:"revoked"`
	Guidance string   `json:"guidance"`
}

func init() {
	skillsRevokeCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsRevokeCmd)
}

func runSkillsRevoke(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	output, err := executeRevocations(gitRoot, args)
	if err != nil {
		return err
	}
	return emitRevocations(cmd.OutOrStdout(), output, asJSON)
}

// executeRevocations serializes approval-store mutation with approval and
// scripts-grant commands. The approval is intentionally not restored when
// reconcile fails: revocation is an authority change, and preserving that
// decision lets the next healthy reconcile finish the cleanup.
func executeRevocations(gitRoot string, names []string) (skillsRevokeOutput, error) {
	output := skillsRevokeOutput{Revoked: []string{}}
	err := fileutil.WithFileLock(context.Background(), teamskills.ApprovalPath(gitRoot), func() error {
		store, err := teamskills.LoadApprovals(gitRoot)
		if err != nil {
			return err
		}

		names = dedupeNames(names)
		sort.Strings(names)
		approved := make(map[string]struct{}, len(store.Approvals))
		for _, approval := range store.Approvals {
			approved[approval.Name] = struct{}{}
		}
		for _, name := range names {
			if _, ok := approved[name]; !ok {
				return fmt.Errorf("no team-skill approval is recorded for %q; nothing was revoked%s",
					name, approvedNamesSuffix(approved))
			}
		}

		for _, name := range names {
			store.Revoke(name)
		}
		if err := store.Save(gitRoot); err != nil {
			return fmt.Errorf("record revocation in %s: %w", teamskills.ApprovalPath(gitRoot), err)
		}
		output.Revoked = append(output.Revoked, names...)

		plan, err := reconcileExactSelectedSkills(gitRoot)
		if err != nil {
			return fmt.Errorf("approval revoked, but removing installed Team Skill content failed: %w", err)
		}
		if reason := plan.RetainedTeamReason(); reason != "" {
			output.Guidance = "Approval revoked. Installed Team Skill content is retained until ox can read Team Context: " + reason + ". Run `ox sync` after Team Context is available."
			return nil
		}
		output.Guidance = "Approval revoked. Content that required it was removed from every selected skill target; readable prose may remain."
		return nil
	})
	return output, err
}

func approvedNamesSuffix(approved map[string]struct{}) string {
	if len(approved) == 0 {
		return "; this repository has no Team Skill approvals"
	}
	names := make([]string, 0, len(approved))
	for name := range approved {
		names = append(names, name)
	}
	sort.Strings(names)
	return "; recorded approvals: " + strings.Join(names, ", ")
}

func emitRevocations(w io.Writer, output skillsRevokeOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, output)
	}
	for _, name := range output.Revoked {
		fmt.Fprintf(w, "%s %s\n", cli.StyleSuccess.Render("revoked"), name)
	}
	fmt.Fprintln(w, output.Guidance)
	return nil
}
