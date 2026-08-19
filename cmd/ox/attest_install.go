package main

import (
	"fmt"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var attestInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install optional Attest skills for AI coworkers",
	Long: `Install the optional Attest playbooks for supported AI coworkers.

This installs customer-journey BDD creation and evidence-recording guidance.
It does not install lifecycle hooks: use 'ox integrate install' for those.
The Attest CLI itself remains available on every supported AI coworker.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}
		installed, err := installAttestSkills(repoRoot)
		if err != nil {
			return err
		}
		if installed == 0 {
			cli.PrintPreserved("Attest skills already up to date")
		} else {
			cli.PrintSuccess(fmt.Sprintf("Reconciled %d Attest skill file(s)", installed))
		}
		return nil
	},
}

func installAttestSkills(repoRoot string) (int, error) {
	plan, err := addSkillBundle(repoRoot, "attest")
	if err != nil {
		return 0, err
	}
	if len(plan.Conflicts) > 0 {
		return len(plan.WrittenPaths()), fmt.Errorf("attest skills have %d preserved user-owned or modified file conflict(s)", len(plan.Conflicts))
	}
	return len(plan.WrittenPaths()), nil
}

func init() {
	attestCmd.AddCommand(attestInstallCmd)
}
