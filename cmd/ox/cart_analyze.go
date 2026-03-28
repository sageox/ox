package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sageox/ox/internal/carts"
	"github.com/sageox/ox/internal/glance"
	"github.com/spf13/cobra"
)

var cartAnalyzeCmd = &cobra.Command{
	Use:   "cart-analyze",
	Short: "Dump cart + session + murmur data for AI analysis",
	Long: `Reads carts (planned work), sessions (work evidence), and murmurs
(coordination signals) for a time range and outputs structured JSON.

Designed for consumption by Claude Code or Claude Cowork for visualization.`,
	RunE: runCartAnalyze,
}

func init() {
	cartAnalyzeCmd.Flags().String("since", "24h", "Start of time window (e.g. 24h, 7d, 2026-03-20)")
	cartAnalyzeCmd.Flags().String("until", "", "End of time window (default: now)")
	rootCmd.AddCommand(cartAnalyzeCmd)
}

func runCartAnalyze(cmd *cobra.Command, args []string) error {
	store, _, err := openCartsStore(cmd)
	if err != nil {
		return err
	}
	defer store.Close()

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("resolve ledger: %w", err)
	}

	sinceStr, _ := cmd.Flags().GetString("since")
	untilStr, _ := cmd.Flags().GetString("until")

	since, err := glance.ParseTimeFlag(sinceStr)
	if err != nil {
		return fmt.Errorf("parse --since: %w", err)
	}

	until := time.Now().UTC()
	if untilStr != "" {
		until, err = glance.ParseTimeFlag(untilStr)
		if err != nil {
			return fmt.Errorf("parse --until: %w", err)
		}
	}

	// Load carts from DoltDB
	issues, err := store.List(cmd.Context(), carts.IssueFilter{
		CreatedSince: &since,
		CreatedUntil: &until,
	})
	if err != nil {
		return fmt.Errorf("list carts: %w", err)
	}

	// Load dependencies for each cart
	deps := make(map[string][]*carts.Dependency)
	for _, issue := range issues {
		d, err := store.GetDependencies(cmd.Context(), issue.ID)
		if err == nil && len(d) > 0 {
			deps[issue.ID] = d
		}
	}

	output, err := carts.Analyze(carts.AnalyzeInput{
		Carts:        issues,
		Dependencies: deps,
		LedgerPath:   ledgerPath,
		Since:        since,
		Until:        until,
	})
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}

	fmt.Println(string(data))
	return nil
}
