package main

import (
	"fmt"

	"github.com/sageox/ox/internal/claudemod"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/usagepane"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// usage_pane.go — `ox usage pane`, the /ox-usage pane in Claude Code.
//
// The pane is a Claude Code mod: `ox usage pane enable` writes it from the
// files embedded in this binary and installs it at user scope through the
// claude CLI, the same way `ox pet enable` installs Oxy.

// newUsagePaneInstaller is swapped in tests to inject a fake claude CLI.
var newUsagePaneInstaller = func() claudemod.Installer {
	return claudemod.Installer{
		Dir:         usagepane.Dir(),
		Plugin:      usagepane.PluginName,
		Marketplace: usagepane.MarketplaceName,
		Version:     version.Version,
		Claude:      claudemod.FindClaude(),
		FS:          usagepane.ModFS(),
		Root:        usagepane.ModRoot,
	}
}

const usagePaneReadyHint = "Run /reload-plugins in an open Claude Code session, then /ox-usage to open the pane."

var usagePaneCmd = &cobra.Command{
	Use:   "pane",
	Short: "Show this session's ox calls in a Claude Code pane",
	Long: `Show this session's ox calls in a Claude Code pane.

The pane keeps the ox usage summary on top (ox calls, estimated context
tokens, team items surfaced and used) and a scrollable list of every ox call
your AI coworker made in this session underneath.

The pane is installed as a Claude Code plugin (` + usagepane.PluginRef + `) at
user scope. Inside Claude Code, /ox-usage opens it and /ox-usage off closes it.`,
}

var usagePaneEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Install the usage pane into Claude Code, or refresh it after an ox upgrade",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newUsagePaneInstaller().Enable(cmd.Context())
		if err != nil {
			return fmt.Errorf("enable the usage pane: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			cli.PrintWarningTo(cmd.ErrOrStderr(), "Claude Code's CLI (claude) isn't on PATH, so the usage pane is written but not installed.")
			printSteps(w, "In Claude Code, run:", res.ManualSteps)
			return nil
		}
		cli.PrintSuccessTo(w, "The usage pane is installed in Claude Code.")
		cli.PrintHintTo(w, usagePaneReadyHint)
		return nil
	},
}

var usagePaneDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove the usage pane from Claude Code",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newUsagePaneInstaller().Disable(cmd.Context())
		if err != nil {
			return fmt.Errorf("disable the usage pane: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			printSteps(w, "The usage pane's files are removed. In Claude Code, also run:", res.ManualSteps)
			return nil
		}
		cli.PrintSuccessTo(w, "The usage pane is removed from Claude Code. Open sessions keep it until /reload-plugins.")
		return nil
	},
}

var usagePaneStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the usage pane is installed in Claude Code",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res, err := newUsagePaneInstaller().Status(cmd.Context())
		if err != nil {
			return fmt.Errorf("check the usage pane: %w", err)
		}
		if asJSON(cmd) {
			return cli.PrintJSONTo(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		switch {
		case res.Installed && res.InstalledVersion != "" && res.InstalledVersion != res.Version:
			cli.PrintWarningTo(w, fmt.Sprintf("The usage pane %s is installed; this ox is %s. Run `ox usage pane enable` to refresh it.", res.InstalledVersion, res.Version))
		case res.Installed:
			cli.PrintSuccessTo(w, "The usage pane is installed in Claude Code. /ox-usage opens it.")
		case res.FilesPresent:
			cli.PrintWarningTo(w, "The usage pane's files are written but Claude Code hasn't installed them. Run `ox usage pane enable`.")
		default:
			cli.PrintInfoTo(w, "The usage pane isn't enabled. Run `ox usage pane enable`.")
		}
		_, _ = fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render(res.Dir))
		return nil
	},
}

func init() {
	usagePaneCmd.AddCommand(usagePaneEnableCmd, usagePaneDisableCmd, usagePaneStatusCmd)
	usageCmd.AddCommand(usagePaneCmd)
}
