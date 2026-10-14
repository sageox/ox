package main

import (
	"fmt"
	"io"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// pet.go — `ox pet`, Oxy in Claude Code's thinking line.
//
// Claude Code's startup mascot is compiled in and cannot be swapped; its
// thinking line can, through a Claude Code mod. `ox pet enable` writes that
// mod (with frames rendered from the sprite sheet embedded in this binary)
// and installs it at user scope through the claude CLI.

// newPetInstaller is swapped in tests to inject a fake claude CLI.
var newPetInstaller = func() pet.Installer {
	return pet.Installer{Dir: pet.Dir(), Version: version.Version, Claude: pet.FindClaude()}
}

var petCmd = &cobra.Command{
	Use:   "pet",
	Short: "Show Oxy, SageOx's mascot, in Claude Code's thinking line",
	Long: `Show Oxy, SageOx's mascot, in Claude Code's thinking line.

While Claude works, Oxy animates beside the thinking word and follows what
Claude is doing: thinking, typing at his laptop, chattering out an answer,
and his own routines for ox prime, ox query, ox plan, ox murmur, and ox
doctor. When a turn ends, Oxy throws confetti on the closing line, or falls
flat if it failed.

Oxy is installed as a Claude Code plugin (ox-cli-oxy@ox-cli-pets) at user scope.
Inside Claude Code, /hatch-pet shows him, /hatch-pet off hides him, and
/hatch-pet small|medium|large sizes him.`,
}

var petEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Install Oxy into Claude Code, or refresh it after an ox upgrade",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newPetInstaller().Enable(cmd.Context())
		if err != nil {
			return fmt.Errorf("enable Oxy: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			cli.PrintWarningTo(cmd.ErrOrStderr(), "Claude Code's CLI (claude) isn't on PATH, so Oxy is written but not installed.")
			printSteps(w, "In Claude Code, run:", res.ManualSteps)
			return nil
		}
		cli.PrintSuccessTo(w, "Oxy will show up in Claude Code's thinking line.")
		cli.PrintHintTo(w, "Run /reload-plugins in an open session, or start a new one. /hatch-pet off hides him.")
		return nil
	},
}

var petDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove Oxy from Claude Code",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newPetInstaller().Disable(cmd.Context())
		if err != nil {
			return fmt.Errorf("disable Oxy: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			printSteps(w, "Oxy's files are removed. In Claude Code, also run:", res.ManualSteps)
			return nil
		}
		cli.PrintSuccessTo(w, "Oxy is removed from Claude Code. Open sessions keep it until /reload-plugins.")
		return nil
	},
}

var petStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether Oxy is installed in Claude Code",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newPetInstaller().Status(cmd.Context())
		if err != nil {
			return fmt.Errorf("check Oxy: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		switch {
		case res.Installed && res.InstalledVersion != "" && res.InstalledVersion != res.Version:
			cli.PrintWarningTo(w, fmt.Sprintf("Oxy %s is installed; this ox is %s. Run `ox pet enable` to refresh it.", res.InstalledVersion, res.Version))
		case res.Installed:
			cli.PrintSuccessTo(w, "Oxy is installed in Claude Code.")
		case res.FilesPresent:
			cli.PrintWarningTo(w, "Oxy's files are written but Claude Code hasn't installed them. Run `ox pet enable`.")
		default:
			cli.PrintInfoTo(w, "Oxy isn't enabled. Run `ox pet enable`.")
		}
		fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render(res.Dir))
		return nil
	},
}

func asJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

func printSteps(w io.Writer, intro string, steps []string) {
	fmt.Fprintln(w, intro)
	for _, s := range steps {
		fmt.Fprintf(w, "  %s\n", s)
	}
}

func init() {
	petCmd.AddCommand(petEnableCmd, petDisableCmd, petStatusCmd)
	rootCmd.AddCommand(petCmd)
}
