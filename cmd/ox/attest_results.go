package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestResultsCmd = &cobra.Command{
	Use:   "results",
	Short: "List local BDD run outcomes",
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}
		runsRoot, _ := cmd.Flags().GetString("runs-root")
		results, err := attest.LoadRunResults(ctx.RepoRoot, runsRoot)
		if err != nil {
			return err
		}
		jsonOut, agentID := wantJSON(cmd)
		return emit(cmd, jsonOut, agentID, results, renderAttestResults(results), "attest results")
	},
}

func renderAttestResults(results []attest.RunResult) string {
	var b strings.Builder
	if len(results) == 0 {
		return "\n  " + ui.RenderMuted("no local BDD run artifacts found") + "\n"
	}
	fmt.Fprintf(&b, "\n%s  %d local run result(s)\n\n", ui.RenderAccent("Attest"), len(results))
	for _, result := range results {
		outcome := result.FinalizeStatus
		if outcome == "" {
			outcome = result.Status
		}
		fmt.Fprintf(&b, "  %s  %s · %s", result.RunID, outcome, result.Source)
		if result.ScenarioTotal > 0 {
			fmt.Fprintf(&b, " · %d scenarios, %d failed", result.ScenarioTotal, result.ScenarioFailed)
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

func init() {
	attestResultsCmd.Flags().String("runs-root", "", "run artifacts directory relative to the repo root (default tests/bdd/runs)")
	attestResultsCmd.Flags().Bool("json", false, "structured JSON output for AI coworkers")
	addCorpusFlag(attestResultsCmd)
	attestCmd.AddCommand(attestResultsCmd)
}
