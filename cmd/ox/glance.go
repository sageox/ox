package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/glance"
	"github.com/spf13/cobra"
)

var glanceCmd = &cobra.Command{
	Use:   "glance",
	Short: "See what your team's AI coworkers are working on",
	Long: `Shows recent AI coworker sessions across your team and detects
file-level conflicts where multiple people are touching the same files.

Output is JSON, designed for AI coworker consumption.

Examples:
  ox glance                        # since last checkpoint (or 4h)
  ox glance --since 3d             # last 3 days
  ox glance --since 7d --until 3d  # 7 days ago to 3 days ago
  ox glance --since 2026-03-18 --until 2026-03-22`,
	RunE: runGlance,
}

func init() {
	rootCmd.AddCommand(glanceCmd)
	glanceCmd.Flags().String("since", "", "start of time window (3d, 7d, 24h, 1w, ISO date)")
	glanceCmd.Flags().String("until", "", "end of time window (same formats as --since; default: now)")
	glanceCmd.Flags().Bool("include-subagents", false, "include subagent sessions")
}

func runGlance(cmd *cobra.Command, _ []string) error {
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
		since, err = glance.ParseTimeFlag(sinceFlag)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
	} else {
		since = glance.GetSince(ledgerPath)
	}

	// Resolve --until (default to now so JSON serializes a real timestamp)
	until := time.Now()
	if untilFlag != "" {
		until, err = glance.ParseTimeFlag(untilFlag)
		if err != nil {
			return fmt.Errorf("invalid --until: %w", err)
		}
	}

	// Resolve project root for hydration and mailmap
	projectRoot, _ := requireProjectRoot()

	// Derive repo name from project root, not cwd
	repo := glanceRepoName(projectRoot)

	// Load .mailmap for username deduplication
	var mailmap map[string]string
	if projectRoot != "" {
		mailmap = glance.LoadMailmap(filepath.Join(projectRoot, ".mailmap"))
	}

	// Harvest sessions from ledger, hydrating dehydrated ones on the fly
	result, err := glance.HarvestSessions(ledgerPath, since, until, glance.HarvestOptions{
		IncludeSubagents: includeSubagents,
		Mailmap:          mailmap,
		HydrateFunc: func(sessionsDir, sessionName string) error {
			if projectRoot == "" {
				return fmt.Errorf("no project root")
			}
			err := hydrateFromLedger(projectRoot, sessionsDir, sessionName, true)
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
		// Output valid JSON with empty arrays (not null) for stable schema
		return outputGlanceJSON(glance.ActivityData{
			Since:     since,
			Until:     until,
			Repo:      repo,
			Authors:   []glance.AuthorSummary{},
			Conflicts: []glance.FileOverlap{},
			Overlap:   []glance.OverlapPair{},
			Stats:     glance.Stats{SkippedDehydrated: result.SkippedDehydrated},
		})
	}

	// Analyze
	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	data := glance.ActivityData{
		Since:     since,
		Until:     until,
		Repo:      repo,
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Patterns:  glance.DetectPatterns(result.Sessions),
		Velocity:  glance.ConflictVelocity(result.Sessions, since, until, 24*time.Hour, 24*time.Hour),
		Stats: glance.Stats{
			TotalSessions:     len(result.Sessions),
			TotalAuthors:      len(authors),
			TotalConflicts:    len(conflicts.Overlaps),
			SkippedDehydrated: result.SkippedDehydrated,
		},
	}

	data.Enrich()

	// Advance checkpoint so next invocation without --since starts from now
	_ = glance.MarkRead(ledgerPath)

	return outputGlanceJSON(data)
}

func outputGlanceJSON(data glance.ActivityData) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// glanceRepoName returns the repo name from the project root, falling back to cwd basename.
func glanceRepoName(projectRoot string) string {
	if projectRoot != "" {
		return filepath.Base(projectRoot)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}
