package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/whatsup"
	"github.com/spf13/cobra"
)

var whatsupCmd = &cobra.Command{
	Use:   "whatsup",
	Short: "See what your team's AI coworkers are working on",
	Long: `Shows recent AI coworker sessions across your team and detects
file-level conflicts where multiple people are touching the same files.

Output is JSON, designed for AI coworker consumption.

Examples:
  ox whatsup                        # since last checkpoint (or 4h)
  ox whatsup --since 3d             # last 3 days
  ox whatsup --since 7d --until 3d  # 7 days ago to 3 days ago
  ox whatsup --since 2026-03-18 --until 2026-03-22`,
	RunE: runWhatsup,
}

func init() {
	rootCmd.AddCommand(whatsupCmd)
	whatsupCmd.Flags().String("since", "", "start of time window (3d, 7d, 24h, 1w, ISO date)")
	whatsupCmd.Flags().String("until", "", "end of time window (same formats as --since; default: now)")
	whatsupCmd.Flags().Bool("include-subagents", false, "include subagent sessions")
}

func runWhatsup(cmd *cobra.Command, _ []string) error {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("ledger not available: %w\n\nRun 'ox doctor --fix' to set up ledger sync", err)
	}

	sinceFlag, _ := cmd.Flags().GetString("since")
	untilFlag, _ := cmd.Flags().GetString("until")
	includeSubagents, _ := cmd.Flags().GetBool("include-subagents")

	// Resolve --since
	var since time.Time
	if sinceFlag != "" {
		since, err = whatsup.ParseTimeFlag(sinceFlag)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
	} else {
		since = whatsup.GetSince(ledgerPath)
	}

	// Resolve --until (zero time means "now")
	var until time.Time
	if untilFlag != "" {
		until, err = whatsup.ParseTimeFlag(untilFlag)
		if err != nil {
			return fmt.Errorf("invalid --until: %w", err)
		}
	}

	// Resolve project root for hydration and mailmap
	projectRoot, _ := requireProjectRoot()

	// Load .mailmap for username deduplication
	var mailmap map[string]string
	if projectRoot != "" {
		mailmap = whatsup.LoadMailmap(filepath.Join(projectRoot, ".mailmap"))
	}

	// Harvest sessions from ledger, hydrating dehydrated ones on the fly
	result, err := whatsup.HarvestSessions(ledgerPath, since, until, whatsup.HarvestOptions{
		IncludeSubagents: includeSubagents,
		Mailmap:          mailmap,
		HydrateFunc: func(sessionsDir, sessionName string) error {
			if projectRoot == "" {
				return fmt.Errorf("no project root")
			}
			// Suppress stdout from hydrateFromLedger to keep JSON output clean
			origStdout := os.Stdout
			os.Stdout, _ = os.Open(os.DevNull)
			err := hydrateFromLedger(projectRoot, sessionsDir, sessionName)
			os.Stdout = origStdout
			if err != nil {
				slog.Debug("hydration failed", "session", sessionName, "error", err)
			}
			return err
		},
	})
	if err != nil {
		return fmt.Errorf("harvesting sessions: %w", err)
	}

	if len(result.Sessions) == 0 {
		// Still output valid JSON with empty data
		return outputWhatsupJSON(whatsup.ActivityData{
			Since: since,
			Until: until,
			Repo:  repoName(),
			Stats: whatsup.Stats{SkippedDehydrated: result.SkippedDehydrated},
		})
	}

	// Analyze
	authors := whatsup.GroupByAuthor(result.Sessions)
	conflicts := whatsup.DetectConflicts(result.Sessions)

	data := whatsup.ActivityData{
		Since:     since,
		Until:     until,
		Repo:      repoName(),
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Stats: whatsup.Stats{
			TotalSessions:     len(result.Sessions),
			TotalAuthors:      len(authors),
			TotalConflicts:    len(conflicts.Overlaps),
			SkippedDehydrated: result.SkippedDehydrated,
		},
	}

	data.Enrich()
	return outputWhatsupJSON(data)
}

func outputWhatsupJSON(data whatsup.ActivityData) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// repoName returns the basename of the current working directory.
func repoName() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}
