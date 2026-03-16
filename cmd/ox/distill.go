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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/agentcli"
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

// dailyDateRe matches the YYYY-MM-DD prefix of daily memory filenames.
// Handles both old naming (2026-03-10.md) and new naming (2026-03-10-{uuid7}.md).
var dailyDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)

// weeklyRe matches YYYY-WXX weekly filenames.
var weeklyRe = regexp.MustCompile(`^(\d{4})-W(\d{2})`)

// monthlyRe matches YYYY-MM monthly filenames.
var monthlyRe = regexp.MustCompile(`^(\d{4}-\d{2})\.md$`)

// distillPlan describes what layers and periods to distill.
type distillPlan struct {
	Daily  bool
	Weeks  []isoWeek
	Months []string // YYYY-MM
}

// isoWeek identifies a specific ISO week.
type isoWeek struct {
	Year int
	Week int
}

// inferDailyHighWater scans memory/daily/ for the latest YYYY-MM-DD prefix.
// Returns end-of-day UTC for that date, or zero time if no files.
func inferDailyHighWater(tcPath string) time.Time {
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		return time.Time{}
	}

	var latestDate string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := dailyDateRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if m[1] > latestDate {
			latestDate = m[1]
		}
	}

	if latestDate == "" {
		return time.Time{}
	}

	// Use start-of-day so observations from the latest date are re-distilled
	// rather than silently skipped. UUID7 filenames prevent overwrites.
	t, err := time.Parse("2006-01-02", latestDate)
	if err != nil {
		return time.Time{}
	}
	return t
}

// inferWeeklyHighWater scans memory/weekly/ for latest YYYY-WXX file.
// Returns end-of-that-week (Sunday 23:59:59 UTC).
func inferWeeklyHighWater(tcPath string) time.Time {
	weeklyDir := filepath.Join(tcPath, "memory", "weekly")
	entries, err := os.ReadDir(weeklyDir)
	if err != nil {
		return time.Time{}
	}

	var latestYear, latestWeek int
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := weeklyRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		var y, w int
		fmt.Sscanf(m[1], "%d", &y)
		fmt.Sscanf(m[2], "%d", &w)
		if !found || y > latestYear || (y == latestYear && w > latestWeek) {
			latestYear, latestWeek = y, w
			found = true
		}
	}

	if !found {
		return time.Time{}
	}

	_, end := isoWeekRange(latestYear, latestWeek)
	return end
}

// inferMonthlyHighWater scans memory/monthly/ for latest YYYY-MM file.
// Returns end-of-that-month.
func inferMonthlyHighWater(tcPath string) time.Time {
	monthlyDir := filepath.Join(tcPath, "memory", "monthly")
	entries, err := os.ReadDir(monthlyDir)
	if err != nil {
		return time.Time{}
	}

	var latestMonth string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := monthlyRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if m[1] > latestMonth {
			latestMonth = m[1]
		}
	}

	if latestMonth == "" {
		return time.Time{}
	}

	t, err := time.Parse("2006-01", latestMonth)
	if err != nil {
		return time.Time{}
	}
	return endOfMonth(t)
}

// endOfDay returns 23:59:59 UTC of the given date.
func endOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
}

// endOfMonth returns the last second of the given month in UTC.
func endOfMonth(t time.Time) time.Time {
	// first day of next month, minus one second
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
}

// isoWeekRange returns the Monday 00:00:00 and Sunday 23:59:59 UTC of the given ISO week.
func isoWeekRange(year, week int) (start, end time.Time) {
	// Jan 4 is always in ISO week 1
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	// Find Monday of ISO week 1
	weekday := jan4.Weekday()
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	week1Monday := jan4.AddDate(0, 0, -int(weekday-1))

	start = week1Monday.AddDate(0, 0, (week-1)*7)
	end = start.AddDate(0, 0, 6) // Sunday
	end = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, time.UTC)
	return start, end
}

// groupObservationsByDay groups observations by their RecordedAt date.
// Keys are YYYY-MM-DD strings. Observations within each group maintain their sort order.
func groupObservationsByDay(observations []distillObservation) map[string][]distillObservation {
	groups := make(map[string][]distillObservation)
	for _, obs := range observations {
		if obs.RecordedAt.IsZero() {
			continue
		}
		day := obs.RecordedAt.Format("2006-01-02")
		groups[day] = append(groups[day], obs)
	}
	return groups
}

// readDailyFilesForDateRange reads all daily .md files whose YYYY-MM-DD prefix
// falls within [startDate, endDate] inclusive. Returns contents and filenames.
func readDailyFilesForDateRange(dailyDir, startDate, endDate string) ([]string, []string, error) {
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	type dailyFile struct {
		name string
		date string
	}
	var matched []dailyFile

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := dailyDateRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		date := m[1]
		if date >= startDate && date <= endDate {
			matched = append(matched, dailyFile{name: e.Name(), date: date})
		}
	}

	// sort chronologically (then by full filename for stability within same day)
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].date != matched[j].date {
			return matched[i].date < matched[j].date
		}
		return matched[i].name < matched[j].name
	})

	var contents, names []string
	for _, f := range matched {
		data, err := os.ReadFile(filepath.Join(dailyDir, f.name))
		if err != nil {
			continue
		}
		contents = append(contents, string(data))
		names = append(names, f.name)
	}
	return contents, names, nil
}

// readWeeklyFilesForMonth reads weekly .md files whose ISO week overlaps with
// the given year-month. Returns contents and filenames.
func readWeeklyFilesForMonth(weeklyDir string, year, month int) ([]string, []string, error) {
	entries, err := os.ReadDir(weeklyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)

	type weeklyFile struct {
		name string
		year int
		week int
	}
	var matched []weeklyFile

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := weeklyRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		var wy, ww int
		fmt.Sscanf(m[1], "%d", &wy)
		fmt.Sscanf(m[2], "%d", &ww)

		wStart, wEnd := isoWeekRange(wy, ww)
		// week overlaps month if week starts before month ends AND week ends after month starts
		if wStart.Before(monthEnd.Add(time.Second)) && wEnd.After(monthStart.Add(-time.Second)) {
			matched = append(matched, weeklyFile{name: e.Name(), year: wy, week: ww})
		}
	}

	// sort chronologically
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].year != matched[j].year {
			return matched[i].year < matched[j].year
		}
		return matched[i].week < matched[j].week
	})

	var contents, names []string
	for _, f := range matched {
		data, err := os.ReadFile(filepath.Join(weeklyDir, f.name))
		if err != nil {
			continue
		}
		contents = append(contents, string(data))
		names = append(names, f.name)
	}
	return contents, names, nil
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
  memory/daily/YYYY-MM-DD-{uuid7}.md — daily summaries from raw observations
  memory/weekly/YYYY-WXX.md          — weekly synthesis from dailies
  memory/monthly/YYYY-MM.md          — monthly synthesis from weeklies`,
	RunE: runDistill,
}

func init() {
	distillCmd.Flags().StringVar(&distillLayer, "layer", "", "distill only a specific layer (daily, weekly, monthly)")
	distillCmd.Flags().BoolVar(&distillDryRun, "dry-run", false, "show what would be distilled without invoking the AI coworker")

	rootCmd.AddCommand(distillCmd)
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
	state := loadDistillStateV2(projectRoot, tc.Path)

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
	plan := determineLayers(state, distillLayer, now)

	if plan.isEmpty() {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to distill")
		return nil
	}

	// extract facts from unprocessed discussions before daily distill
	if plan.Daily {
		if err := extractDiscussionFacts(ctx, cmd, backend, tc, state, guidelines); err != nil {
			slog.Warn("discussion fact extraction failed", "error", err)
		} else if !distillDryRun {
			if err := saveDistillStateV2(projectRoot, state); err != nil {
				slog.Warn("failed to save distill state after discussion extraction", "error", err)
			}
		}
	}

	if plan.Daily {
		if err := distillDaily(ctx, cmd, backend, tc, state, projectRoot, now, guidelines); err != nil {
			return fmt.Errorf("daily distill: %w", err)
		}
	}

	for _, week := range plan.Weeks {
		if err := distillWeekly(ctx, cmd, backend, tc, state, week, guidelines); err != nil {
			return fmt.Errorf("weekly distill (%d-W%02d): %w", week.Year, week.Week, err)
		}
	}

	for _, month := range plan.Months {
		if err := distillMonthly(ctx, cmd, backend, tc, state, month, guidelines); err != nil {
			return fmt.Errorf("monthly distill (%s): %w", month, err)
		}
	}

	// save updated state (skip in dry-run to avoid side effects)
	if !distillDryRun {
		if err := saveDistillStateV2(projectRoot, state); err != nil {
			slog.Warn("failed to save distill state", "error", err)
		}
	}

	return nil
}

// determineLayers returns a distillPlan describing which layers and periods to distill.
// When multiple week/month boundaries have been crossed since last run, each gets its own entry.
func determineLayers(state *distillStateV2, explicit string, now time.Time) distillPlan {
	var plan distillPlan

	if explicit == "daily" || explicit == "" {
		plan.Daily = true
	}

	if explicit == "weekly" || explicit == "" {
		lastWeekly := state.lastWeeklyTime()
		if now.Sub(lastWeekly) >= 7*24*time.Hour {
			// enumerate each ISO week from lastWeekly to now
			plan.Weeks = enumerateWeeks(lastWeekly, now)
		}
	}

	if explicit == "monthly" || explicit == "" {
		lastMonthly := state.lastMonthlyTime()
		// Use calendar month comparison instead of duration to avoid
		// missing short months (e.g., Feb has 28 days, not 30).
		if lastMonthly.IsZero() || lastMonthly.Year() < now.Year() || lastMonthly.Month() < now.Month() {
			plan.Months = enumerateMonths(lastMonthly, now)
		}
	}

	return plan
}

// isEmpty returns true if there's nothing to distill.
func (p distillPlan) isEmpty() bool {
	return !p.Daily && len(p.Weeks) == 0 && len(p.Months) == 0
}

// enumerateWeeks returns each completed ISO week between lastTime and now.
// A week is "completed" if its Sunday has passed relative to now.
func enumerateWeeks(lastTime, now time.Time) []isoWeek {
	var weeks []isoWeek

	// Start from the week after lastTime
	cursor := lastTime
	if cursor.IsZero() {
		// If no prior weekly, start from a reasonable lookback (13 weeks max)
		cursor = now.AddDate(0, 0, -91)
	}

	for {
		// Move cursor to the start of the next week
		y, w := cursor.ISOWeek()
		_, weekEnd := isoWeekRange(y, w)
		// Include this week only if its Sunday has passed (completed) and falls after our cursor
		if weekEnd.After(cursor) && !weekEnd.After(now) {
			weeks = append(weeks, isoWeek{Year: y, Week: w})
		}
		// advance to next week
		cursor = weekEnd.Add(time.Second)
		if cursor.After(now) {
			break
		}
	}

	return weeks
}

// enumerateMonths returns each completed month between lastTime and now.
// A month is "completed" if its last day has passed relative to now.
// Starts from the month after lastTime to avoid re-processing.
func enumerateMonths(lastTime, now time.Time) []string {
	var months []string

	var cursor time.Time
	if lastTime.IsZero() {
		// If no prior monthly, start from a reasonable lookback (12 months max)
		cursor = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(-1, 0, 0)
	} else {
		// Start from the next month after last processed
		cursor = time.Date(lastTime.Year(), lastTime.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}

	for !cursor.After(now) {
		monthEnd := endOfMonth(cursor)
		if monthEnd.After(now) {
			break
		}
		months = append(months, cursor.Format("2006-01"))
		cursor = cursor.AddDate(0, 1, 0)
	}

	return months
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

	// read discussion facts grouped by date (content-based timestamps)
	factsByDay, err := readPendingDiscussionFacts(tc.Path, since)
	if err != nil {
		slog.Warn("failed to read discussion facts", "error", err)
	}

	if len(observations) == 0 && len(factsByDay) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending observations or discussion facts for daily distill")
		return nil
	}

	// group observations by day
	obsByDay := groupObservationsByDay(observations)

	// union all day keys
	daySet := make(map[string]bool)
	for day := range obsByDay {
		daySet[day] = true
	}
	for day := range factsByDay {
		daySet[day] = true
	}

	// sort days chronologically
	days := make([]string, 0, len(daySet))
	for day := range daySet {
		days = append(days, day)
	}
	sort.Strings(days)

	if distillDryRun {
		for _, day := range days {
			obsCount := len(obsByDay[day])
			factCount := len(factsByDay[day])
			fmt.Fprintf(cmd.OutOrStdout(), "Daily distill: %d observations and %d discussion facts for %s\n",
				obsCount, factCount, day)
		}
		return nil
	}

	var latestDay string
	for _, day := range days {
		dayObs := obsByDay[day]
		dayFacts := factsByDay[day]

		// extract observation content strings
		contents := make([]string, len(dayObs))
		for i, obs := range dayObs {
			contents[i] = obs.Content
		}

		// extract fact paths for the prompt
		var factPaths []string
		for _, f := range dayFacts {
			factPaths = append(factPaths, f.RelPath)
		}

		prompt := agentcli.DailyPrompt(contents, day, guidelines, factPaths...)
		logPrompt(cmd, "daily:"+day, prompt)
		fmt.Fprintf(cmd.OutOrStdout(), "Distilling %d observations and %d discussion facts into daily summary for %s...\n",
			len(dayObs), len(dayFacts), day)

		output, err := backend.Run(ctx, prompt)
		if err != nil {
			return fmt.Errorf("AI coworker (%s): %w", day, err)
		}

		// generate UUID7 filename for collision avoidance
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate daily file ID: %w", err)
		}

		filePath := filepath.Join("memory", "daily", day+"-"+id.String()+".md")
		content := formatDailyMemory(day, output, len(dayObs), len(dayFacts))

		if err := writeMemoryFile(tc.Path, filePath, content); err != nil {
			return fmt.Errorf("write daily memory: %w", err)
		}

		if err := commitMemoryFile(tc.Path, filePath, fmt.Sprintf("memory: distill daily %s", day)); err != nil {
			slog.Warn("failed to commit daily memory", "error", err)
		}

		state.DailyCount += len(dayObs) + len(dayFacts)
		latestDay = day
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	}

	if latestDay != "" {
		// Use actual processing time so intra-day re-runs pick up new observations
		state.LastDaily = now.Format(time.RFC3339)
		state.LastDailyHash = "" // hash no longer used for per-day bucketing
	}

	return nil
}

func distillWeekly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, week isoWeek, guidelines string) error {
	dailyDir := filepath.Join(tc.Path, "memory", "daily")
	weekID := fmt.Sprintf("%d-W%02d", week.Year, week.Week)
	start, end := isoWeekRange(week.Year, week.Week)
	startDate := start.Format("2006-01-02")
	endDate := end.Format("2006-01-02")

	dailySummaries, dailyFiles, err := readDailyFilesForDateRange(dailyDir, startDate, endDate)
	if err != nil {
		return fmt.Errorf("read daily files for %s: %w", weekID, err)
	}
	if len(dailySummaries) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No daily summaries for weekly %s, skipping\n", weekID)
		return nil
	}

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Weekly distill: %d daily summaries for %s\n", len(dailySummaries), weekID)
		return nil
	}

	prompt := agentcli.WeeklyPrompt(dailySummaries, weekID, guidelines)
	logPrompt(cmd, "weekly:"+weekID, prompt)
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

	state.LastWeekly = end.Format(time.RFC3339)
	state.LastWeeklyHash = "" // hash no longer used

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	return nil
}

func distillMonthly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, month string, guidelines string) error {
	weeklyDir := filepath.Join(tc.Path, "memory", "weekly")

	// parse month string to get year and month
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return fmt.Errorf("parse month %q: %w", month, err)
	}

	weeklySummaries, weeklyFiles, err := readWeeklyFilesForMonth(weeklyDir, t.Year(), int(t.Month()))
	if err != nil {
		return fmt.Errorf("read weekly files for %s: %w", month, err)
	}
	if len(weeklySummaries) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No weekly summaries for monthly %s, skipping\n", month)
		return nil
	}

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Monthly distill: %d weekly summaries for %s\n", len(weeklySummaries), month)
		return nil
	}

	prompt := agentcli.MonthlyPrompt(weeklySummaries, month, guidelines)
	logPrompt(cmd, "monthly:"+month, prompt)
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

	state.LastMonthly = endOfMonth(t).Format(time.RFC3339)
	state.LastMonthlyHash = "" // hash no longer used

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
// tcPath is used to infer high-water marks from existing files when no state exists.
func loadDistillStateV2(projectRoot, tcPath string) *distillStateV2 {
	// try loading v2 state from cache directory
	v2Path := filepath.Join(projectRoot, ".sageox", "cache", "distill-state-v2.json")
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
		return state
	}

	// no state at all — infer from existing memory files to avoid reprocessing
	if t := inferDailyHighWater(tcPath); !t.IsZero() {
		state.LastDaily = t.Format(time.RFC3339)
	}
	if t := inferWeeklyHighWater(tcPath); !t.IsZero() {
		state.LastWeekly = t.Format(time.RFC3339)
	}
	if t := inferMonthlyHighWater(tcPath); !t.IsZero() {
		state.LastMonthly = t.Format(time.RFC3339)
	}

	return state
}

// saveDistillStateV2 persists the v2 distill state.
func saveDistillStateV2(projectRoot string, state *distillStateV2) error {
	state.SchemaVersion = "2"
	cacheDir := filepath.Join(projectRoot, ".sageox", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	path := filepath.Join(cacheDir, "distill-state-v2.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
