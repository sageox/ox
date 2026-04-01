package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sageox/ox/internal/agentcli"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/query"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/facts"
	"github.com/spf13/cobra"
)

// githubExtractorSystemPrompt is the system prompt for the GitHub fact extractor.
const githubExtractorSystemPrompt = `You are a signal extractor for an alignment feed system. Your job is to analyze a batch of GitHub event clusters and produce structured raw facts that capture meaningful events — decisions made, features shipped, blockers encountered, and direction changes.

You receive pre-assembled event clusters where related GitHub objects (PRs, issues, commits, reviews) are already grouped together. The relationships are explicit — you do not need to infer them.

## What to extract

For each cluster, determine if it contains a meaningful event worth surfacing to the team. A meaningful event is one where knowing about it would change how a teammate thinks or acts. Extract a raw fact for each meaningful event.

Look for these categories of signal:

DECISIONS IN REVIEWS
- A reviewer requests a substantive change and the author accepts or pushes back
- A design alternative is discussed and one approach is chosen over another
- A reviewer raises a concern that leads to a scope change or follow-up issue
- Ignore: cosmetic feedback (naming, formatting, style nits), rubber-stamp approvals with no substantive comment

SCOPE AND IMPACT FROM THE PR
- A new public API endpoint, interface, or contract was introduced or changed
- Shared configuration, database schema, or infrastructure was modified
- A dependency was added, removed, or significantly upgraded
- A module boundary was changed in a way that affects other teams or areas
- Ignore: internal refactors that don't change any external interface, test-only changes with no behavioral impact

CONSTRAINTS AND CONTEXT FROM ISSUES
- The issue describes a user-facing problem, performance requirement, or security concern
- The issue captures business context or priority reasoning that the PR omits
- The issue discussion narrowed scope or rejected alternatives before the PR was opened
- Ignore: issue templates with no additional context, issues that are just task placeholders

STATUS SIGNALS
- A PR was merged — this is a ship event
- A PR was closed without merging — this may signal an abandoned approach or direction change
- A PR has unresolved review comments or has been open significantly longer than the team norm — potential blocker
- An issue was opened that describes an urgent bug, regression, or incident

IMPLICIT DECISIONS
- A PR changes a significant approach without any review pushback — the team implicitly agreed
- A pattern is established for the first time (first use of a new library, first implementation of a new convention) — this sets precedent

## What NOT to extract

- Routine progress that doesn't affect anyone else (minor refactors, test additions, documentation typos)
- Bot-generated PRs (dependency bumps, automated formatting) unless they introduce a breaking change
- Duplicate signals — if the same decision appears in the issue discussion AND the review thread, produce one fact, not two. Synthesize across both sources for a richer result.
- Metadata-only events (label changes, assignment changes, milestone updates) unless they signal a meaningful priority shift

## How to handle cross-object synthesis

When a cluster contains multiple objects describing the same event (an issue with discussion, a PR implementing it, a review refining it), produce ONE fact that synthesizes across all of them:
- The headline should describe the outcome, not the artifact ("Adopted token bucket rate limiting" not "PR #152 merged")
- The summary should combine implementation detail from the PR with context from the issue
- The rationale should draw from wherever the WHY lives — often the issue description or review discussion, not the PR description
- The source_ref should point to the PR as the primary artifact, with the issue referenced in the summary if relevant

## Output format

For each meaningful event in the batch, produce a raw fact object:

{
  "headline": "One sentence — what happened, framed as an outcome, not a GitHub event",
  "summary": "Two to three sentences — the key details. What was built or changed, what approach was taken, what scope was affected. Be specific about modules, endpoints, and interfaces touched.",
  "rationale": "Why this choice was made. What alternatives were considered and rejected. What constraint or tradeoff drove the decision. If no rationale is evident in the source material, state that explicitly: 'No rationale captured in source material.'",
  "who": "The primary author. If a reviewer significantly shaped the outcome, include them: 'Sarah (reviewed by Jake)'",
  "source_type": "github",
  "source_ref": "The URL of the primary GitHub object (usually the PR)",
  "timestamp": "ISO 8601 timestamp of the most recent meaningful event (merge time for shipped PRs, latest review comment for in-progress PRs, creation time for new issues)",
  "category": "ship|decision|blocker|direction_change — the type of signal (optional)"
}

Output JSONL: one JSON object per line, NOT a JSON array. If a batch contains no meaningful events, return nothing. Do not fabricate facts to fill space.

If you are uncertain whether something is meaningful, include it with a note in the summary: "[Uncertain significance] ..." — the downstream distiller will make the final call.`

// extractGitHubFacts queries CodeDB for GitHub activity, partitions by day,
// calls the LLM extractor per day, and writes facts to memory/.github-facts/{date}-{uuid7}.jsonl.
//
// repoID identifies the repo for per-repo state tracking.
// dataDir is the path to the CodeDB data directory.
func extractGitHubFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, repoID, dataDir, guidelines string) error {
	if dataDir == "" {
		slog.Debug("no CodeDB dir, skipping github fact extraction", "repo", repoID)
		return nil
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		slog.Debug("no CodeDB for github fact extraction, skipping", "repo", repoID)
		return nil
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	// compute time window from fact file metadata (stateless)
	now := time.Now().UTC()
	since := inferGitHubQueryHighWater(tc.Path)

	result, err := query.AssembleActivity(ctx, db.Store(), since, now)
	if err != nil {
		return fmt.Errorf("assemble activity: %w", err)
	}

	// skip LLM call if no activity
	if result.Metadata.PRCount == 0 && result.Metadata.IssueCount == 0 && result.Metadata.CommitCount == 0 {
		slog.Debug("no github activity in window, skipping extraction",
			"since", since.Format(time.RFC3339), "until", now.Format(time.RFC3339))
		return nil
	}

	// partition activity by day
	byDay := result.ByDay()
	days := query.SortedDays(byDay)

	if distillDryRun {
		for _, day := range days {
			bucket := byDay[day]
			// count items that already have fact files (skippable)
			var skippedPRs, skippedIssues, skippedCommits int
			for _, pr := range bucket.PRClusters {
				data, _ := json.Marshal(pr)
				hash := contentHash(string(data))
				factPath := filepath.Join(tc.Path, "memory", ".github-facts", fmt.Sprintf("%s-pr-%d.jsonl", day, pr.Number))
				if readFactFileSourceHash(factPath) == hash {
					skippedPRs++
				}
			}
			for _, issue := range bucket.StandaloneIssues {
				data, _ := json.Marshal(issue)
				hash := contentHash(string(data))
				factPath := filepath.Join(tc.Path, "memory", ".github-facts", fmt.Sprintf("%s-issue-%d.jsonl", day, issue.Number))
				if readFactFileSourceHash(factPath) == hash {
					skippedIssues++
				}
			}
			if len(bucket.StandaloneCommits) > 0 {
				data, _ := json.Marshal(bucket.StandaloneCommits)
				hash := contentHash(string(data))
				commitsPath := filepath.Join(tc.Path, "memory", ".github-facts", day+"-commits.jsonl")
				if readFactFileSourceHash(commitsPath) == hash {
					skippedCommits = len(bucket.StandaloneCommits)
				}
			}
			newPRs := bucket.Metadata.PRCount - skippedPRs
			newIssues := bucket.Metadata.IssueCount - skippedIssues
			newCommits := bucket.Metadata.CommitCount - skippedCommits
			if newPRs > 0 || newIssues > 0 || newCommits > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "GitHub activity: %d PRs, %d issues, %d commits for extraction (%s)\n",
					newPRs, newIssues, newCommits, day)
			}
			if skippedPRs > 0 || skippedIssues > 0 || skippedCommits > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "  (skipping %d PRs, %d issues, %d commits already extracted)\n",
					skippedPRs, skippedIssues, skippedCommits)
			}
		}
		return nil
	}

	factsDir := filepath.Join(tc.Path, "memory", ".github-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return fmt.Errorf("create github-facts dir: %w", err)
	}

	// collect work items, then fan out with bounded concurrency.
	// each item writes to its own deterministic file — no shared state.
	type workItem struct {
		day      string
		itemType string // "pr", "issue", "commits"
		number   int    // PR/issue number, 0 for commits
		item     any    // PRCluster, StandaloneIssue, or []StandaloneCommit
	}

	var items []workItem
	for _, day := range days {
		bucket := byDay[day]
		for _, pr := range bucket.PRClusters {
			data, _ := json.Marshal(pr)
			hash := contentHash(string(data))
			factPath := filepath.Join(tc.Path, "memory", ".github-facts", fmt.Sprintf("%s-pr-%d.jsonl", day, pr.Number))
			if readFactFileSourceHash(factPath) == hash {
				continue
			}
			items = append(items, workItem{day, "pr", pr.Number, pr})
		}
		for _, issue := range bucket.StandaloneIssues {
			data, _ := json.Marshal(issue)
			hash := contentHash(string(data))
			factPath := filepath.Join(tc.Path, "memory", ".github-facts", fmt.Sprintf("%s-issue-%d.jsonl", day, issue.Number))
			if readFactFileSourceHash(factPath) == hash {
				continue
			}
			items = append(items, workItem{day, "issue", issue.Number, issue})
		}
		if len(bucket.StandaloneCommits) > 0 {
			data, _ := json.Marshal(bucket.StandaloneCommits)
			hash := contentHash(string(data))
			commitsPath := filepath.Join(tc.Path, "memory", ".github-facts", day+"-commits.jsonl")
			if readFactFileSourceHash(commitsPath) == hash {
				continue
			}
			items = append(items, workItem{day, "commits", 0, bucket.StandaloneCommits})
		}
	}

	if len(items) == 0 {
		slog.Debug("all github items already extracted")
		return nil
	}

	concurrency := distillConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}

	if concurrency > 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "Extracting %d GitHub items with concurrency %d...\n", len(items), concurrency)
	}

	var mu sync.Mutex // protects stdout and git operations
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	for _, wi := range items {
		wi := wi // capture loop var
		g.Go(func() error {
			var err error
			switch wi.itemType {
			case "pr", "issue":
				err = extractSingleGitHubItem(gctx, cmd, &mu, backend, tc.Path, wi.day, wi.itemType, wi.number, wi.item, since, guidelines)
			case "commits":
				commits := wi.item.([]query.StandaloneCommit)
				err = extractGitHubCommitBatch(gctx, cmd, &mu, backend, tc.Path, wi.day, commits, since, guidelines)
			}
			if err != nil {
				slog.Warn("github extraction failed", "day", wi.day, "type", wi.itemType, "number", wi.number, "error", err)
			}
			return nil // don't abort other items on failure
		})
	}

	_ = g.Wait()
	return nil
}

// extractSingleGitHubItem extracts facts from a single PR or issue.
// Uses a deterministic filename based on day + item type + number for dedup.
func extractSingleGitHubItem(ctx context.Context, cmd *cobra.Command, mu *sync.Mutex, backend agentcli.Backend, tcPath, day, itemType string, number int, item any, since time.Time, guidelines string) error {
	// deterministic filename: 2026-03-28-pr-152.jsonl
	factFile := filepath.Join("memory", ".github-facts", fmt.Sprintf("%s-%s-%d.jsonl", day, itemType, number))
	fullPath := filepath.Join(tcPath, factFile)

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshal %s #%d: %w", itemType, number, err)
	}

	prompt := buildGitHubExtractorPrompt(string(data), "1 day", guidelines)
	logPrompt(cmd, fmt.Sprintf("github-%s:%d", itemType, number), prompt)

	mu.Lock()
	fmt.Fprintf(cmd.OutOrStdout(), "Extracting facts from %s #%d for %s (%d bytes)...\n",
		itemType, number, day, len(data))
	mu.Unlock()

	llmStart := time.Now()
	output, err := backend.Run(ctx, prompt)
	elapsed := time.Since(llmStart).Round(time.Millisecond)
	mu.Lock()
	fmt.Fprintf(cmd.OutOrStdout(), "  %s #%d took %s\n", itemType, number, elapsed)
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("AI coworker (%s #%d): %w", itemType, number, err)
	}

	output = strings.TrimSpace(output)
	sourceHash := contentHash(string(data))

	if output == "" || output == "[]" {
		// write empty marker (serialize git ops)
		mu.Lock()
		header := facts.FileHeader{
			Meta: facts.FileMeta{
				SchemaVersion: facts.SchemaVersion,
				SourceType:    facts.SourceGitHub,
				RecordedAt:    day + "T00:00:00Z",
				SourceHash:    sourceHash,
				QuerySince:    since.Format(time.RFC3339),
			},
		}
		if err := facts.WriteFacts(fullPath, header, nil); err != nil {
			mu.Unlock()
			return fmt.Errorf("write empty marker: %w", err)
		}
		if err := commitMemoryFile(tcPath, factFile, fmt.Sprintf("memory: no facts from %s #%d on %s", itemType, number, day)); err != nil {
			slog.Warn("failed to commit empty marker, rolling back", "error", err)
			os.Remove(fullPath)
		}
		mu.Unlock()
		return nil
	}

	_, parsedFacts, err := facts.ParseFacts([]byte(output))
	if err != nil {
		slog.Warn("github extractor returned unparseable output", "item", fmt.Sprintf("%s-%d", itemType, number), "error", err)
		return nil
	}
	if len(parsedFacts) == 0 {
		return nil
	}

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    day + "T00:00:00Z",
			SourceHash:    sourceHash,
			QuerySince:    since.Format(time.RFC3339),
		},
	}

	// serialize file writes and git commits (git isn't concurrent-safe)
	mu.Lock()
	defer mu.Unlock()

	if err := facts.WriteFacts(fullPath, header, parsedFacts); err != nil {
		return fmt.Errorf("write github facts: %w", err)
	}

	if err := commitMemoryFile(tcPath, factFile, fmt.Sprintf("memory: extract facts from %s #%d on %s", itemType, number, day)); err != nil {
		slog.Warn("failed to commit github facts, rolling back", "error", err)
		os.Remove(fullPath)
		return fmt.Errorf("commit github facts: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%d facts)\n", factFile, len(parsedFacts))
	return nil
}

// extractGitHubCommitBatch extracts facts from a batch of standalone commits for a day.
func extractGitHubCommitBatch(ctx context.Context, cmd *cobra.Command, mu *sync.Mutex, backend agentcli.Backend, tcPath, day string, commits []query.StandaloneCommit, since time.Time, guidelines string) error {
	factFile := filepath.Join("memory", ".github-facts", day+"-commits.jsonl")
	fullPath := filepath.Join(tcPath, factFile)

	data, err := json.Marshal(commits)
	if err != nil {
		return fmt.Errorf("marshal commits: %w", err)
	}

	sourceHash := contentHash(string(data))

	prompt := buildGitHubExtractorPrompt(string(data), "1 day", guidelines)
	logPrompt(cmd, "github-commits:"+day, prompt)

	mu.Lock()
	fmt.Fprintf(cmd.OutOrStdout(), "Extracting facts from %d standalone commits for %s (%d bytes)...\n", len(commits), day, len(data))
	mu.Unlock()

	llmStart := time.Now()
	output, err := backend.Run(ctx, prompt)
	elapsed := time.Since(llmStart).Round(time.Millisecond)
	mu.Lock()
	fmt.Fprintf(cmd.OutOrStdout(), "  commits batch took %s\n", elapsed)
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("AI coworker (commits %s): %w", day, err)
	}

	output = strings.TrimSpace(output)
	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    day + "T00:00:00Z",
			SourceHash:    sourceHash,
			QuerySince:    since.Format(time.RFC3339),
		},
	}

	// serialize file writes and git commits
	mu.Lock()
	defer mu.Unlock()

	if output == "" || output == "[]" {
		if err := facts.WriteFacts(fullPath, header, nil); err != nil {
			return fmt.Errorf("write empty marker: %w", err)
		}
		if err := commitMemoryFile(tcPath, factFile, fmt.Sprintf("memory: no facts from commits on %s", day)); err != nil {
			slog.Warn("failed to commit empty marker, rolling back", "error", err)
			os.Remove(fullPath)
		}
		return nil
	}

	_, parsedFacts, err := facts.ParseFacts([]byte(output))
	if err != nil {
		slog.Warn("github extractor returned unparseable output", "day", day, "error", err)
		return nil
	}

	if err := facts.WriteFacts(fullPath, header, parsedFacts); err != nil {
		return fmt.Errorf("write github facts: %w", err)
	}

	if err := commitMemoryFile(tcPath, factFile, fmt.Sprintf("memory: extract facts from %d commits on %s", len(commits), day)); err != nil {
		slog.Warn("failed to commit github facts, rolling back", "error", err)
		os.Remove(fullPath)
		return fmt.Errorf("commit github facts: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%d facts)\n", factFile, len(parsedFacts))
	return nil
}

// inferGitHubFactsHighWater scans memory/.github-facts/ for the latest YYYY-MM-DD
// prefix in filenames. Returns start-of-day for that date so activity from that day
// is re-extracted (UUID7 filenames prevent overwrites). Returns zero time if no files.
func inferGitHubFactsHighWater(tcPath string) time.Time {
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		return time.Time{}
	}

	var latestDate string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".md")) {
			continue
		}
		m := dailyDateRe.FindStringSubmatch(name)
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

	t, err := time.Parse("2006-01-02", latestDate)
	if err != nil {
		return time.Time{}
	}
	return t
}

// inferGitHubQueryHighWater returns the start of the query window for GitHub extraction.
// Uses the latest date prefix from existing fact filenames. Falls back to 7 days ago.
// source_hash in each file handles dedup for items that changed within the same day.
func inferGitHubQueryHighWater(tcPath string) time.Time {
	// use filename-based inference (YYYY-MM-DD prefix)
	if hw := inferGitHubFactsHighWater(tcPath); !hw.IsZero() {
		return hw
	}
	return time.Now().UTC().AddDate(0, 0, -7)
}

// buildGitHubExtractorPrompt constructs the full prompt for the GitHub fact extractor.
func buildGitHubExtractorPrompt(clustersJSON, interval, guidelines string) string {
	var sb strings.Builder
	sb.WriteString(githubExtractorSystemPrompt)
	sb.WriteString("\n\n")
	agentcli.WriteGuidelines(&sb, guidelines, "extract-github")
	sb.WriteString("---\n\n")
	fmt.Fprintf(&sb, "Here is a batch of GitHub event clusters from the last %s. ", interval)
	sb.WriteString("Each cluster contains pre-assembled related objects with their full comment histories.\n\n")
	sb.WriteString("Analyze each cluster and extract raw facts for any meaningful events. Remember:\n")
	sb.WriteString("- One fact per meaningful event, not one fact per GitHub object\n")
	sb.WriteString("- Synthesize across nested objects for the richest possible fact\n")
	sb.WriteString("- Focus on decisions, ships, blockers, and direction changes\n")
	sb.WriteString("- Skip routine progress and cosmetic changes\n\n")
	sb.WriteString("<batch>\n")
	sb.WriteString(clustersJSON)
	sb.WriteString("\n</batch>\n\n")
	sb.WriteString("Return JSONL (one JSON object per line, NOT a JSON array). If no meaningful events exist in this batch, return nothing.")
	return sb.String()
}

// readPendingGitHubFacts reads fact files from memory/.github-facts/
// that were created since the given timestamp. Same structure as readPendingDiscussionFacts.
// If tz is non-nil, RFC3339 timestamps are converted to that timezone for date grouping.
func readPendingGitHubFacts(tcPath string, since time.Time, tz ...*time.Location) (map[string][]discussionFactEntry, error) {
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")

	// compute cutoff date in the same timezone used by parseFactDate
	var cutoffDate string
	if !since.IsZero() {
		cutoff := since
		if len(tz) > 0 && tz[0] != nil {
			cutoff = since.In(tz[0])
		}
		cutoffDate = cutoff.Format("2006-01-02")
	}

	entries, err := os.ReadDir(factsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read github-facts dir: %w", err)
	}

	result := make(map[string][]discussionFactEntry)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".md")) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(factsDir, entry.Name()))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		date := parseFactDate(content, entry.Name(), tz...)
		if date == "" {
			continue
		}

		// filter by since (using same timezone as parseFactDate)
		if cutoffDate != "" && date < cutoffDate {
			continue
		}

		result[date] = append(result[date], discussionFactEntry{
			Content: content,
			RelPath: filepath.Join("memory", ".github-facts", entry.Name()),
			Date:    date,
		})
	}

	return result, nil
}
