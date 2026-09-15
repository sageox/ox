package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	Long: `Shows recent AI coworker murmurs across your team and detects
potential file-level collisions where multiple people are working on the same files.

Output is JSON, designed for AI coworker consumption.

Examples:
  ox glance                        # since last checkpoint (or 4h)
  ox glance --since 3d             # last 3 days
  ox glance --since 7d --until 3d  # 7 days ago to 3 days ago
  ox glance --since 2026-03-18 --until 2026-03-22
  ox glance --repo repo_019ff2f5-2079-7be1-b05e-8caad2772e61 --since 7d`,
	RunE: runGlance,
}

func init() {
	rootCmd.AddCommand(glanceCmd)
	glanceCmd.Flags().String("since", "", "start of time window (3d, 7d, 24h, 1w, ISO date)")
	glanceCmd.Flags().String("until", "", "end of time window (same formats as --since; default: now)")
	glanceCmd.Flags().String("repo", "", "read a hosted ledger by canonical repo_<uuid> instead of the current project (see docs/specs/ledger-read-sync.md)")
}

func runGlance(cmd *cobra.Command, _ []string) error {
	repoID, _ := cmd.Flags().GetString("repo")
	// Unlike `ox session list --repo`, this flag has no filesystem-path form.
	// Falling through to the current project would silently answer about a
	// repository the caller did not name.
	if repoID != "" && !hostedLedgerSelected(repoID) {
		return hostedReadFailed(cmd, "invalid_arguments", 2)
	}

	sinceFlag, _ := cmd.Flags().GetString("since")
	untilFlag, _ := cmd.Flags().GetString("until")

	// Resolve --until first: it bounds the window in both modes and neither
	// needs a ledger to parse it. Default to now so JSON serializes a real
	// timestamp.
	until := time.Now()
	if untilFlag != "" {
		parsed, err := glance.ParseTimeFlag(untilFlag)
		if err != nil {
			return fmt.Errorf("invalid --until: %w", err)
		}
		until = parsed
	}
	var since time.Time
	if sinceFlag != "" {
		parsed, err := glance.ParseTimeFlag(sinceFlag)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
		since = parsed
	}

	if repoID != "" {
		return runHostedGlance(cmd, repoID, since, until)
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("ledger not available: %w\n\nRun 'ox doctor --fix' to set up ledger sync", err)
	}
	if since.IsZero() {
		since = glance.GetSince(ledgerPath)
	}

	// Derive repo name from project root
	projectRoot, _ := requireProjectRoot()
	data, err := glanceActivity(ledgerPath, glanceRepoName(projectRoot), since, until)
	if err != nil {
		return err
	}

	if err := outputGlanceJSON(cmd.OutOrStdout(), data); err != nil {
		return err
	}
	// Advance the checkpoint only once the activity has actually been written.
	// Advancing first means a failed write — a closed pipe, a full disk — skips
	// that window forever: the next bare invocation resumes after activity the
	// consumer never received.
	_ = glance.MarkRead(ledgerPath)
	return nil
}

// runHostedGlance reports a hosted ledger's activity under the shared checkout
// lock, for a caller that has no source checkout and selects the ledger by its
// canonical identity instead. See docs/specs/ledger-read-sync.md.
//
// The checkpoint that backs a bare `ox glance` is keyed by ledger path and
// advanced by MarkRead. A hosted read advances nothing, so a caller that omits
// --since gets the same default window a first-ever glance gets.
func runHostedGlance(cmd *cobra.Command, repoID string, since, until time.Time) error {
	if since.IsZero() {
		since = until.Add(-glance.DefaultWindow)
	}
	var data glance.ActivityData
	if err := withHostedLedger(cmd, repoID, func(path string) error {
		harvested, err := glanceActivity(path, repoID, since, until)
		if err != nil {
			slog.Debug("hosted glance", "repo_id", repoID, "err", err)
			return hostedReadFailed(cmd, "unavailable", 1)
		}
		data = harvested
		return nil
	}); err != nil {
		return err
	}
	return outputGlanceJSON(cmd.OutOrStdout(), data)
}

// glanceActivity harvests the murmurs and sessions recorded in [since, until]
// under ledgerPath and folds them into the rendered activity view. repo labels
// the output; it is the checkout's directory name for a project read and the
// canonical repo ID for a hosted one.
func glanceActivity(ledgerPath, repo string, since, until time.Time) (glance.ActivityData, error) {
	result, err := glance.HarvestMurmurs(ledgerPath, since, until)
	if err != nil {
		return glance.ActivityData{}, fmt.Errorf("harvesting murmurs: %w", err)
	}

	sessResult, err := glance.HarvestSessions(ledgerPath, since, until)
	if err != nil {
		return glance.ActivityData{}, fmt.Errorf("harvesting sessions: %w", err)
	}

	if len(result.Murmurs) == 0 && len(sessResult.Sessions) == 0 {
		// Empty arrays (not null) so the schema stays stable
		return glance.ActivityData{
			Since:     since,
			Until:     until,
			Repo:      repo,
			Authors:   []glance.AuthorSummary{},
			Conflicts: []glance.FileOverlap{},
			Overlap:   []glance.OverlapPair{},
		}, nil
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
	return data, nil
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
