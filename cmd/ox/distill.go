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
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/facts"
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

// endOfDay returns 23:59:59 of the given date in the specified timezone.
// If no timezone is provided, UTC is used.
func endOfDay(t time.Time, tz ...*time.Location) time.Time {
	loc := time.UTC
	if len(tz) > 0 && tz[0] != nil {
		loc = tz[0]
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, loc)
}

// endOfMonth returns the last second of the given month in the specified timezone.
// If no timezone is provided, UTC is used.
func endOfMonth(t time.Time, tz ...*time.Location) time.Time {
	loc := time.UTC
	if len(tz) > 0 && tz[0] != nil {
		loc = tz[0]
	}
	// first day of next month, minus one second
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc).Add(-time.Second)
}

// isoWeekRange returns the Monday 00:00:00 and Sunday 23:59:59 of the given ISO week
// in the specified timezone. Pass nil for UTC.
func isoWeekRange(year, week int, tz ...*time.Location) (start, end time.Time) {
	loc := time.UTC
	if len(tz) > 0 && tz[0] != nil {
		loc = tz[0]
	}
	// Jan 4 is always in ISO week 1
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, loc)
	// Find Monday of ISO week 1
	weekday := jan4.Weekday()
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	week1Monday := jan4.AddDate(0, 0, -int(weekday-1))

	start = week1Monday.AddDate(0, 0, (week-1)*7)
	end = start.AddDate(0, 0, 6) // Sunday
	end = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, loc)
	return start, end
}

// groupObservationsByDay groups observations by their RecordedAt date in the given timezone.
// Keys are YYYY-MM-DD strings. Observations within each group maintain their sort order.
// If tz is nil, UTC is used.
func groupObservationsByDay(observations []distillObservation, tz *time.Location) map[string][]distillObservation {
	if tz == nil {
		tz = time.UTC
	}
	groups := make(map[string][]distillObservation)
	for _, obs := range observations {
		if obs.RecordedAt.IsZero() {
			continue
		}
		day := obs.RecordedAt.In(tz).Format("2006-01-02")
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
// tz is used for week boundary calculations; if nil, UTC is used.
func readWeeklyFilesForMonth(weeklyDir string, year, month int, tz *time.Location) ([]string, []string, error) {
	if tz == nil {
		tz = time.UTC
	}
	entries, err := os.ReadDir(weeklyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, tz)
	monthEnd := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, tz).Add(-time.Second)

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

		wStart, wEnd := isoWeekRange(wy, ww, tz)
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
	distillLayer       string
	distillDryRun      bool
	distillSync        bool
	distillVerbose     bool
	distillModel       string
	distillNoPush      bool
	distillConcurrency int
	distillAll         bool
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
	distillCmd.Flags().BoolVar(&distillSync, "sync", false, "sync ledger, team context, and code index before distilling")
	distillCmd.Flags().BoolVar(&distillVerbose, "verbose", false, "log full prompts to stderr")
	distillCmd.Flags().StringVar(&distillModel, "model", "", "override the AI coworker model (e.g., sonnet, opus)")
	distillCmd.Flags().BoolVar(&distillNoPush, "no-push", false, "skip pushing team context commits to remote (useful for testing)")
	distillCmd.Flags().IntVar(&distillConcurrency, "concurrency", 1, "max parallel LLM calls for fact extraction (1-8)")
	distillCmd.Flags().BoolVar(&distillAll, "all", false, "process all history (default: last 7 days)")

	rootCmd.AddCommand(distillCmd)
}

// distillStateV2 tracks weekly/monthly distillation timestamps.
// Extraction tracking (sessions, discussions, github) is now stateless —
// handled via source_hash and query_since/query_until in fact file metadata.
// Daily distill tracking uses sources frontmatter in daily .md files.
type distillStateV2 struct {
	SchemaVersion string `json:"schema_version"`
	TeamID        string `json:"team_id"`
	LastWeekly    string `json:"last_weekly,omitempty"`
	LastMonthly   string `json:"last_monthly,omitempty"`
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
	if !distillVerbose {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "--- prompt (%s) ---\n%s--- end prompt ---\n", label, prompt)
}

// syncBeforeDistill ensures the daemon is running, then syncs the ledger,
// team contexts, and code index. Each step shows progress via a spinner.
// Returns an error if any critical sync step fails.
func syncBeforeDistill() error {
	// ensure daemon is running
	if err := daemon.EnsureDaemon(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// sync ledger
	if err := cli.WithSpinnerNoResult("Syncing ledger...", func() error {
		client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
		return client.SyncWithProgress(func(stage string, percent *int, message string) {
			// progress handled by spinner
		})
	}); err != nil {
		return fmt.Errorf("ledger sync failed: %w", err)
	}

	// sync team contexts
	if err := cli.WithSpinnerNoResult("Syncing team contexts...", func() error {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		return client.TeamSyncWithProgress(func(stage string, percent *int, message string) {
			// progress handled by spinner
		})
	}); err != nil {
		return fmt.Errorf("team context sync failed: %w", err)
	}

	// update code index
	if err := cli.WithSpinnerNoResult("Updating code index...", func() error {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		_, err := client.CodeIndex(daemon.CodeIndexPayload{}, func(stage string, percent *int, message string) {
			// progress handled by spinner
		})
		return err
	}); err != nil {
		// code index failure is non-fatal — distill can proceed without it
		slog.Warn("code index update failed", "error", err)
	}

	return nil
}

// distillRepo holds resolved paths for a single repo in a multi-ledger distill run.
type distillRepo struct {
	ProjectRoot string // path to the project root
	RepoID      string // unique repo identifier from ProjectConfig
	LedgerPath  string // path to the ledger (contains sessions/)
	CodeDBDir   string // path to the CodeDB data directory
}

// resolveDistillRepos returns the list of repos to process for fact extraction.
// If DISTILL_REPOS is set, parses colon-separated project root paths.
// Otherwise, falls back to findProjectRoot() for single-repo behavior.
// All repos must be initialized SageOx projects belonging to the same team.
func resolveDistillRepos(primaryRoot string) ([]distillRepo, error) {
	env := os.Getenv("DISTILL_REPOS")
	if env == "" {
		// single-repo mode: resolve from the primary project root
		repo, err := resolveOneDistillRepo(primaryRoot)
		if err != nil {
			slog.Debug("primary repo not resolvable for extraction", "error", err)
			return nil, nil
		}
		return []distillRepo{repo}, nil
	}

	parts := strings.Split(env, ":")
	var repos []distillRepo
	seen := make(map[string]bool)
	var teamID string
	var teamEndpoint string

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("invalid path %q in DISTILL_REPOS: %w", p, err)
		}
		if seen[abs] {
			continue // deduplicate
		}
		seen[abs] = true

		if !config.IsInitialized(abs) {
			return nil, fmt.Errorf("path %q in DISTILL_REPOS is not a SageOx project", abs)
		}

		// validate team + endpoint consistency
		projCfg, err := config.LoadProjectConfig(abs)
		if err != nil {
			return nil, fmt.Errorf("cannot load project config for %q: %w", abs, err)
		}
		repoEP := endpoint.NormalizeEndpoint(projCfg.Endpoint)
		if teamID == "" {
			teamID = projCfg.TeamID
			teamEndpoint = repoEP
		} else {
			if projCfg.TeamID != teamID {
				return nil, fmt.Errorf("repo %q belongs to team %q, expected %q — all DISTILL_REPOS must be in the same team", abs, projCfg.TeamID, teamID)
			}
			if repoEP != teamEndpoint {
				return nil, fmt.Errorf("repo %q uses endpoint %q, expected %q — all DISTILL_REPOS must use the same endpoint", abs, repoEP, teamEndpoint)
			}
		}

		repo, err := resolveOneDistillRepo(abs)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve repo %q in DISTILL_REPOS: %w", abs, err)
		}
		repos = append(repos, repo)
	}

	if len(repos) == 0 {
		return nil, fmt.Errorf("DISTILL_REPOS contains no valid repos")
	}
	return repos, nil
}

// resolveOneDistillRepo loads project context and resolves paths for a single repo.
func resolveOneDistillRepo(root string) (distillRepo, error) {
	projCtx, err := config.LoadProjectContext(root)
	if err != nil {
		return distillRepo{}, fmt.Errorf("load project context: %w", err)
	}
	repoID := projCtx.RepoID()
	if repoID == "" {
		return distillRepo{}, fmt.Errorf("no repo ID in project context")
	}
	ledgerPath := projCtx.DefaultLedgerPath()
	codeDBDir := resolveCodeDBDir(root)

	return distillRepo{
		ProjectRoot: root,
		RepoID:      repoID,
		LedgerPath:  ledgerPath,
		CodeDBDir:   codeDBDir,
	}, nil
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

	// sync before distilling if requested
	if distillSync {
		if err := syncBeforeDistill(); err != nil {
			return err
		}
	}

	// push team context commits to remote when we're done — even if the
	// pipeline partially fails, earlier stages may have committed facts or
	// distilled summaries that should be synced.
	if !distillDryRun && !distillNoPush {
		ep := endpoint.GetForProject(projectRoot)
		defer func() {
			if err := pushTeamContext(context.Background(), tc.Path, ep); err != nil {
				slog.Warn("failed to push team context after distill", "error", err)
			}
		}()
	}

	// detect AI coworker CLI
	backend, err := agentcli.Detect()
	if err != nil && !distillDryRun {
		return fmt.Errorf("distillation requires an AI coworker CLI: %w", err)
	}

	// set working directory so relative file paths in prompts resolve correctly
	if claude, ok := backend.(*agentcli.Claude); ok {
		claude.WorkDir = tc.Path
		if distillModel != "" {
			claude.Model = distillModel
		}
	}

	// load distill state (v2 format, backward compat with v1)
	state := loadDistillStateV2(projectRoot, tc.Path)

	// load or rebuild the distill index (performance cache for file scanning)
	idx, rebuilt := loadDistillIndex(projectRoot, tc.Path)
	if rebuilt {
		fmt.Fprintf(cmd.OutOrStdout(), "Rebuilt distill index from %d facts, %d dailies\n", len(idx.FactMeta), len(idx.DailySources))
	} else {
		// check for new files from other machines
		if n := updateDistillIndex(idx, tc.Path); n > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Updated index: %d new files\n", n)
		}
	}
	// always save index on exit — even partial runs should cache progress
	if !distillDryRun {
		defer func() {
			if err := saveDistillIndex(projectRoot, idx); err != nil {
				slog.Warn("failed to save distill index", "error", err)
			}
		}()
	}

	// ensure memory directories exist
	if err := ensureMemoryDirs(tc.Path); err != nil {
		return fmt.Errorf("create memory directories: %w", err)
	}

	// seed MEMORY.md if it doesn't exist
	if err := seedMemoryMD(tc.Path); err != nil {
		slog.Warn("failed to seed MEMORY.md", "error", err)
	}

	// migrate old DISTILL.md to memory/guidance/ if needed
	if migrated, err := migrateDistillGuidelines(tc.Path); err != nil {
		slog.Warn("failed to migrate DISTILL.md", "error", err)
	} else if migrated {
		slog.Info("migrated DISTILL.md to memory/guidance/")
	}

	// seed default guidance files if they don't exist
	if err := seedGuidanceFiles(tc.Path); err != nil {
		slog.Warn("failed to seed guidance files", "error", err)
	}

	// load guidance once — snapshot at run start, not re-read per stage
	extractGuidelines := loadGuidance(tc.Path, "EXTRACT.md")
	distillGuidelines := loadGuidance(tc.Path, "DISTILL.md")

	// resolve team timezone for date bucketing
	tz := config.ResolveTimezone(projectRoot)

	now := time.Now().In(tz)

	// determine which layers to run
	plan := determineLayers(state, distillLayer, now, tz)

	if plan.isEmpty() {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to distill")
		return nil
	}

	// resolve repos for multi-ledger extraction
	repos, err := resolveDistillRepos(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve distill repos: %w", err)
	}

	// (per-repo state migration removed — extraction is now stateless via file metadata)

	// extract facts from unprocessed discussions before daily distill
	// (discussions live in team context, not per-ledger — no multi-repo loop needed)
	if plan.Daily {
		if err := extractDiscussionFacts(ctx, cmd, backend, tc, extractGuidelines); err != nil {
			slog.Warn("discussion fact extraction failed", "error", err)
		}
	}

	// extract facts from all repos' ledgers (sessions + GitHub)
	if plan.Daily {
		for _, repo := range repos {
			repoLabel := repo.RepoID
			if len(repos) > 1 {
				fmt.Fprintf(cmd.OutOrStdout(), "Processing repo %s (%s)...\n", repo.RepoID, repo.ProjectRoot)
			}

			if err := extractSessionFacts(cmd, tc, repo.RepoID, repo.LedgerPath); err != nil {
				slog.Warn("session fact extraction failed", "repo", repoLabel, "error", err)
			}

			if err := extractGitHubFacts(ctx, cmd, backend, tc, repo.RepoID, repo.CodeDBDir, extractGuidelines); err != nil {
				slog.Warn("github fact extraction failed", "repo", repoLabel, "error", err)
			}

			// save state after each repo for crash resilience
			if !distillDryRun {
				if err := saveDistillStateV2(projectRoot, state); err != nil {
					slog.Warn("failed to save distill state after repo extraction", "repo", repoLabel, "error", err)
				}
			}
		}
	}

	if plan.Daily {
		if err := distillDaily(ctx, cmd, backend, tc, state, idx, projectRoot, now, distillGuidelines, tz); err != nil {
			return fmt.Errorf("daily distill: %w", err)
		}
	}

	for _, week := range plan.Weeks {
		if err := distillWeekly(ctx, cmd, backend, tc, state, week, distillGuidelines, tz); err != nil {
			return fmt.Errorf("weekly distill (%d-W%02d): %w", week.Year, week.Week, err)
		}
	}

	for _, month := range plan.Months {
		if err := distillMonthly(ctx, cmd, backend, tc, state, month, distillGuidelines, tz); err != nil {
			return fmt.Errorf("monthly distill (%s): %w", month, err)
		}
	}

	// save updated state (skip in dry-run; index is saved via defer)
	if !distillDryRun {
		if err := saveDistillStateV2(projectRoot, state); err != nil {
			slog.Warn("failed to save distill state", "error", err)
		}
	}

	return nil
}

// determineLayers returns a distillPlan describing which layers and periods to distill.
// When multiple week/month boundaries have been crossed since last run, each gets its own entry.
// tz is used for date boundary calculations; if nil, UTC is used.
func determineLayers(state *distillStateV2, explicit string, now time.Time, tz *time.Location) distillPlan {
	if tz == nil {
		tz = time.UTC
	}
	var plan distillPlan

	if explicit == "daily" || explicit == "" {
		plan.Daily = true
	}

	if explicit == "weekly" || explicit == "" {
		lastWeekly := state.lastWeeklyTime()
		if now.Sub(lastWeekly) >= 7*24*time.Hour {
			// enumerate each ISO week from lastWeekly to now
			plan.Weeks = enumerateWeeks(lastWeekly, now, tz)
		}
	}

	if explicit == "monthly" || explicit == "" {
		lastMonthly := state.lastMonthlyTime()
		// Normalize both sides to the team timezone before comparing calendar
		// months, so a timezone change between runs doesn't skip or reprocess months.
		lastInTZ := lastMonthly.In(tz)
		nowInTZ := now.In(tz)
		if lastMonthly.IsZero() || lastInTZ.Year() < nowInTZ.Year() || lastInTZ.Month() < nowInTZ.Month() {
			plan.Months = enumerateMonths(lastMonthly, now, tz)
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
// tz is used for week boundary calculations; if nil, UTC is used.
func enumerateWeeks(lastTime, now time.Time, tz *time.Location) []isoWeek {
	if tz == nil {
		tz = time.UTC
	}
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
		_, weekEnd := isoWeekRange(y, w, tz)
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
// tz is used for month boundary calculations; if nil, UTC is used.
func enumerateMonths(lastTime, now time.Time, tz *time.Location) []string {
	if tz == nil {
		tz = time.UTC
	}
	var months []string

	var cursor time.Time
	if lastTime.IsZero() {
		// If no prior monthly, start from a reasonable lookback (12 months max)
		cursor = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tz).AddDate(-1, 0, 0)
	} else {
		// Start from the next month after last processed
		cursor = time.Date(lastTime.Year(), lastTime.Month()+1, 1, 0, 0, 0, 0, tz)
	}

	for !cursor.After(now) {
		monthEnd := endOfMonth(cursor, tz)
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
func extractDiscussionFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, guidelines string) error {
	pending, err := scanPendingDiscussions(tc.Path)
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
		// compute source hash before extraction for embedding in the fact file
		sourceHash := discussionContentHash(filepath.Join(tc.Path, "discussions", d.DirName))

		factContent, err := extractSingleDiscussionFacts(ctx, cmd, backend, d, guidelines, sourceHash)
		if err != nil {
			// non-fatal per discussion — log and continue
			slog.Warn("skip discussion fact extraction", "dir", d.DirName, "error", err)
			continue
		}

		factFile := filepath.Join("memory", ".discussion-facts", d.DirName+".jsonl")
		if err := writeMemoryFile(tc.Path, factFile, factContent); err != nil {
			slog.Warn("failed to write discussion facts", "dir", d.DirName, "error", err)
			continue
		}

		if err := commitMemoryFile(tc.Path, factFile, fmt.Sprintf("memory: extract facts from %s", d.Title)); err != nil {
			slog.Warn("failed to commit discussion facts", "dir", d.DirName, "error", err)
		}

		// remove legacy .md fact file to prevent duplicate facts in daily distill
		oldMdFile := filepath.Join("memory", ".discussion-facts", d.DirName+".md")
		oldMdPath := filepath.Join(tc.Path, oldMdFile)
		if _, err := os.Stat(oldMdPath); err == nil {
			if err := removeMemoryFile(tc.Path, oldMdFile, fmt.Sprintf("memory: remove legacy %s (migrated to .jsonl)", d.DirName+".md")); err != nil {
				slog.Warn("failed to remove legacy .md fact file", "path", oldMdFile, "error", err)
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Extracted facts from discussion: %s\n", d.Title)
	}

	return nil
}

// extractSingleDiscussionFacts generates facts for one discussion.
// When server-generated summary.json exists, extracts facts directly from
// structured data without an LLM call. Falls back to LLM via JSONL output.
// sourceHash is embedded in the fact file header for stateless change detection.
func extractSingleDiscussionFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, d discussionInput, guidelines, sourceHash string) (string, error) {
	// if server-generated summary.json exists, extract facts directly without LLM
	if d.SummaryJSONDir != "" {
		result, err := extractFactsFromSummaryJSON(d, sourceHash)
		if err == nil {
			slog.Info("extracted facts from summary.json", "discussion", d.DirName)
			return result, nil
		}
		slog.Debug("summary.json not usable, using LLM", "discussion", d.DirName, "err", err)
	}

	prompt := agentcli.DiscussionFactsPrompt(d.Title, d.Summary, d.Transcript, guidelines, d.Annotations)
	logPrompt(cmd, "discussion-facts: "+d.Title, prompt)
	output, err := backend.Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI agent: %w", err)
	}

	// Parse and validate LLM output as JSONL facts
	_, parsedFacts, err := facts.ParseFacts([]byte(output))
	if err != nil {
		return "", fmt.Errorf("parse discussion facts: %w", err)
	}
	if len(parsedFacts) == 0 {
		return "", nil // no valid facts extracted
	}

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceDiscussion,
			RecordedAt:    d.CreatedAt.Format(time.RFC3339),
			SourceHash:    sourceHash,
		},
	}

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}

	var sb strings.Builder
	sb.Write(headerBytes)
	sb.WriteByte('\n')
	for _, f := range parsedFacts {
		line, _ := json.Marshal(f)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func distillDaily(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, idx *distillIndex, projectRoot string, now time.Time, guidelines string, tz *time.Location) error {
	obsDir := filepath.Join(tc.Path, "memory", ".observations")
	since := inferDailyHighWater(tc.Path)

	// default to 7 days back unless --all is set
	if since.IsZero() && !distillAll {
		since = now.AddDate(0, 0, -7)
		slog.Info("no prior daily distill found, defaulting to last 7 days (use --all for full history)")
	}

	observations, _, err := scanPendingObservations(obsDir, since)
	if err != nil {
		return fmt.Errorf("scan observations: %w", err)
	}

	// read discussion facts grouped by date (content-based timestamps)
	factsByDay, err := readPendingDiscussionFacts(tc.Path, since, tz)
	if err != nil {
		slog.Warn("failed to read discussion facts", "error", err)
	}

	// read github facts grouped by date
	ghFactsByDay, err := readPendingGitHubFacts(tc.Path, since, tz)
	if err != nil {
		slog.Warn("failed to read github facts", "error", err)
	}
	// merge github facts into discussion facts map
	if factsByDay == nil {
		factsByDay = make(map[string][]discussionFactEntry)
	}
	for day, facts := range ghFactsByDay {
		factsByDay[day] = append(factsByDay[day], facts...)
	}

	// read session facts grouped by date
	sessionFactsByDay, err := readPendingSessionFacts(tc.Path, since, tz)
	if err != nil {
		slog.Warn("failed to read session facts", "error", err)
	}
	if factsByDay == nil {
		factsByDay = make(map[string][]discussionFactEntry)
	}
	for day, sessionFacts := range sessionFactsByDay {
		factsByDay[day] = append(factsByDay[day], sessionFacts...)
	}

	// filter out facts already distilled (tracked via index, mirrors sources frontmatter)
	alreadyDistilled := idx.distilledSources()
	var skippedFacts int
	for day, dayFacts := range factsByDay {
		var newFacts []discussionFactEntry
		for _, f := range dayFacts {
			if alreadyDistilled[f.RelPath] {
				skippedFacts++
				continue
			}
			newFacts = append(newFacts, f)
		}
		if len(newFacts) == 0 {
			delete(factsByDay, day)
		} else {
			factsByDay[day] = newFacts
		}
	}
	if skippedFacts > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Skipped %d already-distilled facts\n", skippedFacts)
	}

	if len(observations) == 0 && len(factsByDay) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending observations or facts for daily distill")
		return nil
	}

	// group observations by day (in team timezone)
	obsByDay := groupObservationsByDay(observations, tz)

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
			fmt.Fprintf(cmd.OutOrStdout(), "Daily distill: %d observations and %d facts for %s\n",
				obsCount, factCount, day)
		}
		return nil
	}


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
		fmt.Fprintf(cmd.OutOrStdout(), "Distilling %d observations and %d facts into daily summary for %s...\n",
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

		// collect source paths for frontmatter
		var sources []string
		for _, f := range dayFacts {
			sources = append(sources, f.RelPath)
		}

		filePath := filepath.Join("memory", "daily", day+"-"+id.String()+".md")
		content := formatDailyMemory(day, output, len(dayObs), len(dayFacts), sources)

		if err := writeMemoryFile(tc.Path, filePath, content); err != nil {
			return fmt.Errorf("write daily memory: %w", err)
		}

		if err := commitMemoryFile(tc.Path, filePath, fmt.Sprintf("memory: distill daily %s", day)); err != nil {
			slog.Warn("failed to commit daily memory", "error", err)
		}

		// update index with new daily summary
		idx.addDailySummary(filePath, sources)

		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	}

	// (LastDaily no longer tracked — daily high-water inferred from files)

	return nil
}

func distillWeekly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, week isoWeek, guidelines string, tz *time.Location) error {
	dailyDir := filepath.Join(tc.Path, "memory", "daily")
	weekID := fmt.Sprintf("%d-W%02d", week.Year, week.Week)
	start, end := isoWeekRange(week.Year, week.Week, tz)
	startDate := start.Format("2006-01-02")
	endDate := end.Format("2006-01-02")

	slog.Debug("distill weekly", "week", weekID, "start", startDate, "end", endDate, "tz", tz, "daily_dir", dailyDir)

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
	slog.Info("distill weekly: sending to AI coworker", "week", weekID, "daily_count", len(dailySummaries), "daily_files", dailyFiles, "prompt_bytes", len(prompt))
	fmt.Fprintf(cmd.OutOrStdout(), "Synthesizing %d daily summaries into weekly %s...\n", len(dailySummaries), weekID)

	output, err := backend.Run(ctx, prompt)
	if err != nil {
		slog.Error("distill weekly: AI coworker failed", "week", weekID, "error", err, "daily_files", dailyFiles, "prompt_bytes", len(prompt))
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

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filePath)
	return nil
}

func distillMonthly(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, month string, guidelines string, tz *time.Location) error {
	weeklyDir := filepath.Join(tc.Path, "memory", "weekly")

	// parse month string to get year and month
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return fmt.Errorf("parse month %q: %w", month, err)
	}

	weeklySummaries, weeklyFiles, err := readWeeklyFilesForMonth(weeklyDir, t.Year(), int(t.Month()), tz)
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

	state.LastMonthly = endOfMonth(t, tz).Format(time.RFC3339)

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

func formatDailyMemory(date, content string, obsCount, factCount int, sources []string) string {
	var sb strings.Builder
	if len(sources) > 0 {
		sb.WriteString("---\nsources:\n")
		for _, s := range sources {
			fmt.Fprintf(&sb, "  - %s\n", s)
		}
		sb.WriteString("---\n")
	}

	var source string
	switch {
	case obsCount > 0 && factCount > 0:
		source = fmt.Sprintf("%d observations and %d facts", obsCount, factCount)
	case factCount > 0:
		source = fmt.Sprintf("%d facts", factCount)
	default:
		source = fmt.Sprintf("%d observations", obsCount)
	}
	fmt.Fprintf(&sb, "# Daily Memory — %s\n\n%s\n\n---\n*Distilled from %s*\n", date, content, source)
	return sb.String()
}

// parseDailySources extracts source paths from YAML frontmatter in a daily summary.
// Expects format:
//
//	---
//	sources:
//	  - memory/.github-facts/2026-03-30-xxx.jsonl
//	  - memory/.session-facts/2026-03-30/yyy.jsonl
//	---
func parseDailySources(content string) []string {
	// find frontmatter block between --- delimiters
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil
	}
	frontmatter := content[4 : 4+end]

	var sources []string
	inSources := false
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "sources:" {
			inSources = true
			continue
		}
		if inSources && strings.HasPrefix(line, "  - ") {
			sources = append(sources, strings.TrimPrefix(trimmed, "- "))
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inSources = false
		}
	}
	return sources
}

// loadDistillStateV2 loads distill state for weekly/monthly tracking.
// Daily and extraction tracking is now stateless (via file metadata).
func loadDistillStateV2(projectRoot, tcPath string) *distillStateV2 {
	v2Path := filepath.Join(projectRoot, ".sageox", "cache", "distill-state-v2.json")
	if data, err := os.ReadFile(v2Path); err == nil {
		var loaded distillStateV2
		if err := json.Unmarshal(data, &loaded); err == nil {
			return &loaded
		}
	}

	// no state — infer weekly/monthly from existing files
	state := &distillStateV2{
		SchemaVersion: "2",
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
