package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentcli"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

// contentHash returns a short hex hash of the input for change detection.
// Used to skip LLM calls when the input hasn't changed since last distill.
func contentHash(inputs ...string) string {
	h := sha256.New()
	for _, s := range inputs {
		h.Write([]byte(s))
		h.Write([]byte{0}) // separator
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var (
	distillLayer  string
	distillDryRun bool
)

var distillCmd = &cobra.Command{
	Use:   "distill",
	Short: "Distill team observations into memory summaries",
	Long: `Distill accumulated observations into structured memory files.

Collects pending observations from the team context, invokes the local
AI coworker CLI (claude) for LLM-powered summarization, and writes the
results back as memory files in the team context repo.

Memory files are organized by temporal layers:
  memory/daily/YYYY-MM-DD.md   — daily summaries from raw observations
  memory/weekly/YYYY-WXX.md    — weekly synthesis from dailies
  memory/monthly/YYYY-MM.md    — monthly synthesis from weeklies`,
	RunE: runDistill,
}

func init() {
	distillCmd.Flags().StringVar(&distillLayer, "layer", "", "distill only a specific layer (daily, weekly, monthly)")
	distillCmd.Flags().BoolVar(&distillDryRun, "dry-run", false, "show what would be distilled without invoking the AI coworker")

	if auth.IsMemoryEnabled() {
		rootCmd.AddCommand(distillCmd)
	}
}

// distillStateV2 tracks per-layer distillation timestamps.
type distillStateV2 struct {
	SchemaVersion string `json:"schema_version"`
	TeamID        string `json:"team_id"`
	LastDaily     string `json:"last_daily,omitempty"`
	LastWeekly    string `json:"last_weekly,omitempty"`
	LastMonthly   string `json:"last_monthly,omitempty"`
	DailyCount    int    `json:"daily_count"`
	// content hash of last distilled input — skip LLM call if unchanged
	LastDailyHash   string `json:"last_daily_hash,omitempty"`
	LastWeeklyHash  string `json:"last_weekly_hash,omitempty"`
	LastMonthlyHash string `json:"last_monthly_hash,omitempty"`
	// ProcessedDiscussions tracks which discussion dirs have been processed.
	// Key: directory name, Value: content hash at time of processing.
	// Map-based (not timestamp cursor) because discussions arrive out of order via daemon sync.
	ProcessedDiscussions map[string]string `json:"processed_discussions,omitempty"`
	// v1 compat fields (read for migration, not written)
	LastDistilled    string `json:"last_distilled,omitempty"`
	ObservationCount int    `json:"observation_count,omitempty"`
}

// lastDailyTime returns the effective last daily distill time,
// falling back to v1's LastDistilled for backward compatibility.
func (s *distillStateV2) lastDailyTime() time.Time {
	if s.LastDaily != "" {
		if t, err := time.Parse(time.RFC3339, s.LastDaily); err == nil {
			return t
		}
	}
	if s.LastDistilled != "" {
		if t, err := time.Parse(time.RFC3339, s.LastDistilled); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *distillStateV2) lastWeeklyTime() time.Time {
	if s.LastWeekly != "" {
		if t, err := time.Parse(time.RFC3339, s.LastWeekly); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *distillStateV2) lastMonthlyTime() time.Time {
	if s.LastMonthly != "" {
		if t, err := time.Parse(time.RFC3339, s.LastMonthly); err == nil {
			return t
		}
	}
	return time.Time{}
}

// logPrompt logs the full prompt to stderr when --verbose is set.
func logPrompt(cmd *cobra.Command, label, prompt string) {
	verbose, _ := cmd.Flags().GetBool("verbose")
	if !verbose {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "--- prompt (%s) ---\n%s--- end prompt ---\n", label, prompt)
}

func runDistill(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured — run 'ox init' first")
	}

	// detect AI coworker CLI
	backend, err := agentcli.Detect()
	if err != nil && !distillDryRun {
		return fmt.Errorf("distillation requires an AI coworker CLI: %w", err)
	}

	// set working directory so relative file paths in prompts resolve correctly
	if claude, ok := backend.(*agentcli.Claude); ok {
		claude.WorkDir = tc.Path
	}

	// load distill state (v2 format, backward compat with v1)
	state := loadDistillStateV2(projectRoot)

	// ensure memory directories exist
	if err := ensureMemoryDirs(tc.Path); err != nil {
		return fmt.Errorf("create memory directories: %w", err)
	}

	// seed MEMORY.md if it doesn't exist
	if err := seedMemoryMD(tc.Path); err != nil {
		slog.Warn("failed to seed MEMORY.md", "error", err)
	}

	// load team distillation guidelines (DISTILL.md) — optional
	guidelines := loadDistillGuidelines(tc.Path)

	now := time.Now().UTC()

	// determine which layers to run
	layers := determineLayers(state, distillLayer, now)

	if len(layers) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to distill")
		return nil
	}

	// extract facts from unprocessed discussions before daily distill
	if slices.Contains(layers, "daily") {
		if err := extractDiscussionFacts(ctx, cmd, backend, tc, state, guidelines); err != nil {
			slog.Warn("discussion fact extraction failed", "error", err)
		} else if err := saveDistillStateV2(projectRoot, state); err != nil {
			slog.Warn("failed to save distill state after discussion extraction", "error", err)
		}
	}

	for _, layer := range layers {
		switch layer {
		case "daily":
			if err := distillDaily(ctx, cmd, backend, tc, state, projectRoot, now, guidelines); err != nil {
				return fmt.Errorf("daily distill: %w", err)
			}
		case "weekly":
			if err := distillWeekly(ctx, cmd, backend, tc, state, now, guidelines); err != nil {
				return fmt.Errorf("weekly distill: %w", err)
			}
		case "monthly":
			if err := distillMonthly(ctx, cmd, backend, tc, state, now, guidelines); err != nil {
				return fmt.Errorf("monthly distill: %w", err)
			}
		}
	}

	// save updated state
	if err := saveDistillStateV2(projectRoot, state); err != nil {
		slog.Warn("failed to save distill state", "error", err)
	}

	return nil
}

// determineLayers returns which distillation layers should run.
func determineLayers(state *distillStateV2, explicit string, now time.Time) []string {
	if explicit != "" {
		return []string{explicit}
	}

	var layers []string

	// daily: always check for new observations
	layers = append(layers, "daily")

	// weekly: if 7+ days since last weekly
	if now.Sub(state.lastWeeklyTime()) >= 7*24*time.Hour {
		layers = append(layers, "weekly")
	}

	// monthly: if 30+ days since last monthly
	if now.Sub(state.lastMonthlyTime()) >= 30*24*time.Hour {
		layers = append(layers, "monthly")
	}

	return layers
}

// extractDiscussionFacts scans for unprocessed discussions and writes fact files.
// Each discussion gets a fact file in memory/.discussion-facts/{dirName}.md.
// Uses LLM to extract structured facts, or writes stub if in dry-run mode.
func extractDiscussionFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, guidelines string) error {
	if state.ProcessedDiscussions == nil {
		state.ProcessedDiscussions = make(map[string]string)
	}

	pending, err := scanPendingDiscussions(tc.Path, state.ProcessedDiscussions)
	if err != nil {
		return fmt.Errorf("scan discussions: %w", err)
	}

	if len(pending) == 0 {
		return nil
	}

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending discussions: %d\n", len(pending))
		for _, d := range pending {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", d.DirName, d.Title)
		}
		return nil
	}

	factsDir := filepath.Join(tc.Path, "memory", ".discussion-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return fmt.Errorf("create discussion-facts dir: %w", err)
	}

	for _, d := range pending {
		factContent, err := extractSingleDiscussionFacts(ctx, cmd, backend, d, guidelines)
		if err != nil {
			// non-fatal per discussion — log and continue
			slog.Warn("skip discussion fact extraction", "dir", d.DirName, "error", err)
			continue
		}

		factFile := filepath.Join("memory", ".discussion-facts", d.DirName+".md")
		if err := writeMemoryFile(tc.Path, factFile, factContent); err != nil {
			slog.Warn("failed to write discussion facts", "dir", d.DirName, "error", err)
			continue
		}

		if err := commitMemoryFile(tc.Path, factFile, fmt.Sprintf("memory: extract facts from %s", d.Title)); err != nil {
			slog.Warn("failed to commit discussion facts", "dir", d.DirName, "error", err)
		}

		// track as processed with content hash
		hash := discussionContentHash(filepath.Join(tc.Path, "discussions", d.DirName))
		state.ProcessedDiscussions[d.DirName] = hash

		fmt.Fprintf(cmd.OutOrStdout(), "Extracted facts from discussion: %s\n", d.Title)
	}

	return nil
}

// extractSingleDiscussionFacts generates facts for one discussion via LLM.
func extractSingleDiscussionFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, d discussionInput, guidelines string) (string, error) {
	prompt := agentcli.DiscussionFactsPrompt(d.Title, d.Summary, d.Transcript, guidelines)
	logPrompt(cmd, "discussion-facts: "+d.Title, prompt)
	output, err := backend.Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI agent: %w", err)
	}

	// format with metadata header
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Facts: %s\n\n", d.Title)
	sb.WriteString(output)
	if !strings.HasSuffix(output, "\n") {
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "\n---\n*Extracted from discussion: %s (created %s)*\n", d.DirName, d.CreatedAt.Format("2006-01-02"))
	return sb.String(), nil
}

func distillDaily(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, projectRoot string, now time.Time, guidelines string) error {
	obsDir := filepath.Join(tc.Path, "memory", ".observations")
	since := state.lastDailyTime()

	observations, _, err := scanPendingObservations(obsDir, since)
	if err != nil {
		return fmt.Errorf("scan observations: %w", err)
	}

	// read discussion facts created since last daily
	factContents, factPaths, err := readPendingDiscussionFacts(tc.Path, since)
	if err != nil {
		slog.Warn("failed to read discussion facts", "error", err)
	}

	if len(observations) == 0 && len(factContents) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending observations or discussion facts for daily distill")
		return nil
	}

	// extract observation content strings
	contents := make([]string, len(observations))
	for i, obs := range observations {
		contents[i] = obs.Content
	}

	// combined hash incorporates both sources — new facts trigger re-distill
	hashInputs := append(contents, factContents...)
	hash := contentHash(hashInputs...)
	if hash == state.LastDailyHash {
		fmt.Fprintln(cmd.OutOrStdout(), "Daily input unchanged since last distill, skipping")
		return nil
	}

	date := now.Format("2006-01-02")

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Daily distill: %d observations and %d discussion facts for %s (hash: %s)\n",
			len(observations), len(factContents), date, hash[:8])
		return nil
	}

	prompt := agentcli.DailyPrompt(contents, date, guidelines, factPaths...)
	logPrompt(cmd, "daily", prompt)
	fmt.Fprintf(cmd.OutOrStdout(), "Distilling %d observations and %d discussion facts into daily summary for %s...\n",
		len(observations), len(factContents), date)

	output, err := backend.Run(ctx, prompt)
	if err != nil {
		return fmt.Errorf("AI coworker: %w", err)
	}

	// write daily memory file
	filePath := filepath.Join("memory", "daily", date+".md")
	content := formatDailyMemory(date, output, len(observations), len(factContents))

	if err := writeMemoryFile(tc.Path, filePath, content); err != nil {
		return fmt.Errorf("write daily memory: %w", err)
	}

	if err := commitMemoryFile(tc.Path, filePath, fmt.Sprintf("memory: distill daily %s", date)); err != nil {
		slog.Warn("failed to commit daily memory", "error", err)
	}

	state.LastDaily = now.Format(time.RFC3339)
	state.LastDailyHash = hash
	state.DailyCount += len(observations)

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	return nil
}

func distillWeekly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, now time.Time, guidelines string) error {
	dailyDir := filepath.Join(tc.Path, "memory", "daily")
	dailySummaries, dailyFiles, err := readRecentMemoryFiles(dailyDir, 7)
	if err != nil || len(dailySummaries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No daily summaries available for weekly distill")
		return nil
	}

	// check if input changed
	hash := contentHash(dailySummaries...)
	if hash == state.LastWeeklyHash {
		fmt.Fprintln(cmd.OutOrStdout(), "Weekly input unchanged since last distill, skipping")
		return nil
	}

	year, week := now.ISOWeek()
	weekID := fmt.Sprintf("%d-W%02d", year, week)

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Weekly distill: %d daily summaries for %s (hash: %s)\n", len(dailySummaries), weekID, hash[:8])
		return nil
	}

	prompt := agentcli.WeeklyPrompt(dailySummaries, weekID, guidelines)
	logPrompt(cmd, "weekly", prompt)
	fmt.Fprintf(cmd.OutOrStdout(), "Synthesizing %d daily summaries into weekly %s...\n", len(dailySummaries), weekID)

	output, err := backend.Run(ctx, prompt)
	if err != nil {
		return fmt.Errorf("AI coworker: %w", err)
	}

	filePath := filepath.Join("memory", "weekly", weekID+".md")
	content := fmt.Sprintf("# Weekly Memory — %s\n\n%s\n\n---\n*Synthesized from %d daily summaries (%s)*\n",
		weekID, output, len(dailySummaries), strings.Join(dailyFiles, ", "))

	if err := writeMemoryFile(tc.Path, filePath, content); err != nil {
		return fmt.Errorf("write weekly memory: %w", err)
	}

	if err := commitMemoryFile(tc.Path, filePath, fmt.Sprintf("memory: distill weekly %s", weekID)); err != nil {
		slog.Warn("failed to commit weekly memory", "error", err)
	}

	state.LastWeekly = now.Format(time.RFC3339)
	state.LastWeeklyHash = hash

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	return nil
}

func distillMonthly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, now time.Time, guidelines string) error {
	weeklyDir := filepath.Join(tc.Path, "memory", "weekly")
	weeklySummaries, weeklyFiles, err := readRecentMemoryFiles(weeklyDir, 5)
	if err != nil || len(weeklySummaries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No weekly summaries available for monthly distill")
		return nil
	}

	// check if input changed
	hash := contentHash(weeklySummaries...)
	if hash == state.LastMonthlyHash {
		fmt.Fprintln(cmd.OutOrStdout(), "Monthly input unchanged since last distill, skipping")
		return nil
	}

	month := now.Format("2006-01")

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Monthly distill: %d weekly summaries for %s (hash: %s)\n", len(weeklySummaries), month, hash[:8])
		return nil
	}

	prompt := agentcli.MonthlyPrompt(weeklySummaries, month, guidelines)
	logPrompt(cmd, "monthly", prompt)
	fmt.Fprintf(cmd.OutOrStdout(), "Synthesizing %d weekly summaries into monthly %s...\n", len(weeklySummaries), month)

	output, err := backend.Run(ctx, prompt)
	if err != nil {
		return fmt.Errorf("AI coworker: %w", err)
	}

	filePath := filepath.Join("memory", "monthly", month+".md")
	content := fmt.Sprintf("# Monthly Memory — %s\n\n%s\n\n---\n*Synthesized from %d weekly summaries (%s)*\n",
		month, output, len(weeklySummaries), strings.Join(weeklyFiles, ", "))

	if err := writeMemoryFile(tc.Path, filePath, content); err != nil {
		return fmt.Errorf("write monthly memory: %w", err)
	}

	if err := commitMemoryFile(tc.Path, filePath, fmt.Sprintf("memory: distill monthly %s", month)); err != nil {
		slog.Warn("failed to commit monthly memory", "error", err)
	}

	state.LastMonthly = now.Format(time.RFC3339)
	state.LastMonthlyHash = hash

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	return nil
}

// readRecentMemoryFiles reads up to maxFiles most recent .md files from a directory.
// Returns file contents and filenames, sorted most recent first.
func readRecentMemoryFiles(dir string, maxFiles int) ([]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	// collect .md files
	var mdFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, e.Name())
		}
	}

	// sort reverse chronological (filenames are date-based)
	sort.Sort(sort.Reverse(sort.StringSlice(mdFiles)))

	if len(mdFiles) > maxFiles {
		mdFiles = mdFiles[:maxFiles]
	}

	var contents []string
	var names []string
	for _, name := range mdFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		contents = append(contents, string(data))
		names = append(names, name)
	}

	return contents, names, nil
}

func formatDailyMemory(date, content string, obsCount, discussionCount int) string {
	var source string
	switch {
	case obsCount > 0 && discussionCount > 0:
		source = fmt.Sprintf("%d observations and %d discussions", obsCount, discussionCount)
	case discussionCount > 0:
		source = fmt.Sprintf("%d discussions", discussionCount)
	default:
		source = fmt.Sprintf("%d observations", obsCount)
	}
	return fmt.Sprintf("# Daily Memory — %s\n\n%s\n\n---\n*Distilled from %s*\n", date, content, source)
}

// loadDistillStateV2 loads distill state, migrating from v1 if needed.
func loadDistillStateV2(projectRoot string) *distillStateV2 {
	// try loading v2 state first
	v2Path := filepath.Join(projectRoot, ".sageox", "distill-state-v2.json")
	if data, err := os.ReadFile(v2Path); err == nil {
		var loaded distillStateV2
		if err := json.Unmarshal(data, &loaded); err == nil {
			return &loaded
		}
	}

	// fall back to v1 state for migration
	v1State, _ := loadDistillState(projectRoot)

	state := &distillStateV2{
		SchemaVersion: "2",
	}

	if v1State != nil {
		state.TeamID = v1State.TeamID
		state.LastDistilled = v1State.LastDistilled
		state.ObservationCount = v1State.ObservationCount
	}

	return state
}

// saveDistillStateV2 persists the v2 distill state.
func saveDistillStateV2(projectRoot string, state *distillStateV2) error {
	state.SchemaVersion = "2"
	path := filepath.Join(projectRoot, ".sageox", "distill-state-v2.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
