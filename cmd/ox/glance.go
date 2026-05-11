package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/glance"
	"github.com/spf13/cobra"
)

var glanceCmd = &cobra.Command{
	Use:   "glance",
	Short: "See what your team's AI coworkers are working on",
	Long: `Shows recent AI coworker murmurs across your team and detects
potential file-level collisions where multiple people are working on the same files.

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
}

func runGlance(cmd *cobra.Command, _ []string) error {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("ledger not available: %w\n\nRun 'ox doctor --fix' to set up ledger sync", err)
	}

	sinceFlag, _ := cmd.Flags().GetString("since")
	untilFlag, _ := cmd.Flags().GetString("until")

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

	// Derive repo name from project root
	projectRoot, _ := requireProjectRoot()
	repo := glanceRepoName(projectRoot)

	// Harvest murmurs and sessions from ledger
	result, err := glance.HarvestMurmurs(ledgerPath, since, until)
	if err != nil {
		return fmt.Errorf("harvesting murmurs: %w", err)
	}

	sessResult, err := glance.HarvestSessions(ledgerPath, since, until)
	if err != nil {
		return fmt.Errorf("harvesting sessions: %w", err)
	}

	if len(result.Murmurs) == 0 && len(sessResult.Sessions) == 0 {
		// Output valid JSON with empty arrays (not null) for stable schema
		return outputGlanceJSON(cmd.OutOrStdout(), glance.ActivityData{
			Since:     since,
			Until:     until,
			Repo:      repo,
			Authors:   []glance.AuthorSummary{},
			Conflicts: []glance.FileOverlap{},
			Overlap:   []glance.OverlapPair{},
		})
	}

	// Merge session file touches into conflict detection alongside murmurs
	sessionRecords := glance.SessionFilesToMurmurRecords(sessResult.Sessions)
	allRecords := append(result.Murmurs, sessionRecords...)

	// Analyze
	authors := glance.GroupByAuthor(result.Murmurs, sessResult.Sessions)
	conflicts := glance.DetectConflicts(allRecords)

	var wipCount, fcCount int
	for _, m := range result.Murmurs {
		switch m.Topic {
		case "wip":
			wipCount++
		case "file-changes":
			fcCount++
		}
	}

	data := glance.ActivityData{
		Since:     since,
		Until:     until,
		Repo:      repo,
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Patterns:  glance.DetectPatterns(allRecords),
		Velocity:  glance.ConflictVelocity(allRecords, since, until, 24*time.Hour, 24*time.Hour),
		Stats: glance.Stats{
			TotalMurmurs:    len(result.Murmurs),
			TotalSessions:   len(sessResult.Sessions),
			TotalAuthors:    len(authors),
			TotalConflicts:  len(conflicts.Overlaps),
			WIPCount:        wipCount,
			FileChangeCount: fcCount,
		},
	}

	data.Enrich()

	// Advance checkpoint so next invocation without --since starts from now
	_ = glance.MarkRead(ledgerPath)

	return outputGlanceJSON(cmd.OutOrStdout(), data)
}

func outputGlanceJSON(w io.Writer, data glance.ActivityData) error {
	enc := json.NewEncoder(w)
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
