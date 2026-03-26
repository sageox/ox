package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
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
func extractGitHubFacts(ctx context.Context, cmd *cobra.Command, backend agentcli.Backend, tc *config.TeamContext, state *distillStateV2, projectRoot string) error {
	// resolve CodeDB
	dataDir := resolveCodeDBDir(projectRoot)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		slog.Debug("no CodeDB for github fact extraction, skipping")
		return nil
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	// compute time window from distill state
	now := time.Now().UTC()
	since := githubFactsSince(state, tc.Path)

	result, err := query.AssembleActivity(ctx, db.Store(), since, now)
	if err != nil {
		return fmt.Errorf("assemble activity: %w", err)
	}

	// skip LLM call if no activity
	if result.Metadata.PRCount == 0 && result.Metadata.IssueCount == 0 && result.Metadata.CommitCount == 0 {
		slog.Debug("no github activity in window, skipping extraction",
			"since", since.Format(time.RFC3339), "until", now.Format(time.RFC3339))
		if !distillDryRun {
			state.LastGitHubFacts = now.Format(time.RFC3339)
		}
		return nil
	}

	// partition activity by day
	byDay := result.ByDay()
	days := query.SortedDays(byDay)

	if distillDryRun {
		for _, day := range days {
			bucket := byDay[day]
			fmt.Fprintf(cmd.OutOrStdout(), "GitHub activity: %d PRs, %d issues, %d commits for extraction (%s)\n",
				bucket.Metadata.PRCount, bucket.Metadata.IssueCount, bucket.Metadata.CommitCount, day)
		}
		return nil
	}

	factsDir := filepath.Join(tc.Path, "memory", ".github-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return fmt.Errorf("create github-facts dir: %w", err)
	}

	for _, day := range days {
		bucket := byDay[day]

		// skip days with no meaningful clusters
		if bucket.Metadata.PRCount == 0 && bucket.Metadata.IssueCount == 0 && bucket.Metadata.CommitCount == 0 {
			continue
		}

		data, err := bucket.MarshalEventClusters()
		if err != nil {
			return fmt.Errorf("marshal event clusters (%s): %w", day, err)
		}

		if len(data) > 100_000 {
			slog.Warn("github activity batch is large, may exceed LLM context", "day", day, "bytes", len(data))
		}

		interval := "1 day"
		prompt := buildGitHubExtractorPrompt(string(data), interval)
		logPrompt(cmd, "github-facts:"+day, prompt)

		fmt.Fprintf(cmd.OutOrStdout(), "Extracting facts from %d PRs, %d issues, %d commits for %s...\n",
			bucket.Metadata.PRCount, bucket.Metadata.IssueCount, bucket.Metadata.CommitCount, day)

		output, err := backend.Run(ctx, prompt)
		if err != nil {
			return fmt.Errorf("AI coworker (%s): %w", day, err)
		}

		output = strings.TrimSpace(output)
		if output == "" || output == "[]" {
			slog.Debug("github extractor returned no facts", "day", day)
			continue
		}

		// generate UUID7 filename for collision avoidance
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate fact file ID: %w", err)
		}

		factFile := filepath.Join("memory", ".github-facts", day+"-"+id.String()+".jsonl")

		// Parse and validate LLM output as JSONL facts
		_, parsedFacts, err := facts.ParseFacts([]byte(output))
		if err != nil {
			slog.Warn("github extractor returned unparseable output, skipping", "day", day, "error", err)
			continue
		}
		if len(parsedFacts) == 0 {
			slog.Debug("github extractor returned no valid facts after parsing", "day", day)
			continue
		}

		header := facts.FileHeader{
			Meta: facts.FileMeta{
				SchemaVersion: facts.SchemaVersion,
				SourceType:    facts.SourceGitHub,
				RecordedAt:    day + "T00:00:00Z",
			},
		}

		if err := facts.WriteFacts(filepath.Join(tc.Path, factFile), header, parsedFacts); err != nil {
			return fmt.Errorf("write github facts: %w", err)
		}

		if err := commitMemoryFile(tc.Path, factFile, fmt.Sprintf("memory: extract github facts for %s", day)); err != nil {
			slog.Warn("failed to commit github facts", "error", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", factFile)
	}

	state.LastGitHubFacts = now.Format(time.RFC3339)
	return nil
}

// githubFactsSince returns the start of the time window for GitHub fact extraction.
// If no state is available, it infers the high-water mark from existing files.
func githubFactsSince(state *distillStateV2, tcPath string) time.Time {
	if state.LastGitHubFacts != "" {
		if t, err := time.Parse(time.RFC3339, state.LastGitHubFacts); err == nil {
			return t
		}
	}
	// infer high-water from existing fact files (fresh clone recovery)
	if hw := inferGitHubFactsHighWater(tcPath); !hw.IsZero() {
		return hw
	}
	// default: 7 days ago
	return time.Now().UTC().AddDate(0, 0, -7)
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

// buildGitHubExtractorPrompt constructs the full prompt for the GitHub fact extractor.
func buildGitHubExtractorPrompt(clustersJSON, interval string) string {
	var sb strings.Builder
	sb.WriteString(githubExtractorSystemPrompt)
	sb.WriteString("\n\n---\n\n")
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
func readPendingGitHubFacts(tcPath string, since time.Time) (map[string][]discussionFactEntry, error) {
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
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

		date := parseFactDate(content, entry.Name())
		if date == "" {
			continue
		}

		if !since.IsZero() {
			factDate, err := time.Parse("2006-01-02", date)
			if err == nil && factDate.Before(since.Truncate(24*time.Hour)) {
				continue
			}
		}

		result[date] = append(result[date], discussionFactEntry{
			Content: content,
			RelPath: filepath.Join("memory", ".github-facts", entry.Name()),
			Date:    date,
		})
	}

	return result, nil
}
