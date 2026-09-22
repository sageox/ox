package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/query"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var codePRsCmd = &cobra.Command{
	Use:   "prs",
	Short: "List pull requests ranked for triage (stalled, age, activity)",
	Long: `List indexed pull requests ranked for triage.

By default surfaces the most-stalled open PRs first (least-recent activity at
the top — those most likely to need attention). All ranking signals are
deterministic and computed from indexed GitHub data (no LLM ranking, no
external API).

Sort modes:
  stalled  (default) — most idle PR first, where stalled-days = days since the
                       most recent of updated_at or last comment.
  age      — oldest PR by created_at first.
  activity — most comments first.

Each result row carries: age_days, stalled_days, comments, reviewers (distinct
authors of review comments), discussants (distinct authors of discussion
comments), commits, labels, URL.`,
	Example: `  # default — most-stalled open PRs first, JSON for agents
  ox code prs

  # oldest open PRs first, limit 10
  ox code prs --sort age --limit 10

  # most-discussed closed PRs (e.g., for retrospectives)
  ox code prs --state closed --sort activity`,
	RunE: runCodePRs,
}

func init() {
	codePRsCmd.Flags().Int("limit", 20, "max number of PRs to return")
	codePRsCmd.Flags().String("sort", "stalled", "ranking: stalled | age | activity")
	codePRsCmd.Flags().String("state", "open", "PR state: open | closed | merged | all")
	codePRsCmd.Flags().Bool("pretty", false, "pretty-print JSON output")
	codeCmd.AddCommand(codePRsCmd)
}

func runCodePRs(cmd *cobra.Command, _ []string) error {
	root, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("not in a git repository")
	}

	dataDir := resolveCodeDBDir(root)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		if agentID, _ := detectAgentContext(); agentID != "" {
			return emitIndexNotReadyJSON(cmd, indexStatusNotIndexed,
				"No code index found for this repo.",
				"Run 'ox code index' to build the index, then rerun 'ox code prs'. Until then, use 'gh pr list' or the GitHub web UI.")
		}
		return fmt.Errorf("%s\n%s",
			cli.StyleError.Render("No code index found"),
			"Run "+cli.StyleCommand.Render("ox code index")+" to create one")
	}

	// SQL-only: triage reads from pull_requests + pr_comments + pr_commits, no
	// bleve. Matches the `code activity` rationale — avoids being blocked by a
	// corrupt/locked bleve sub-index.
	db, err := codedb.OpenSQLOnly(dataDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	limit, _ := cmd.Flags().GetInt("limit")
	sort, _ := cmd.Flags().GetString("sort")
	state, _ := cmd.Flags().GetString("state")

	if !validTriageSort(sort) {
		return fmt.Errorf("invalid --sort %q: must be one of stalled | age | activity", sort)
	}
	if !validTriageState(state) {
		return fmt.Errorf("invalid --state %q: must be one of open | closed | merged | all", state)
	}

	// Normalize once so validation, the query, and the echoed response all agree.
	sort = strings.ToLower(sort)
	state = strings.ToLower(state)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	results, err := query.TriagePRs(ctx, db.Store(), query.TriageOpts{
		Limit: limit,
		Sort:  sort,
		State: state,
	})
	if err != nil {
		return fmt.Errorf("triage PRs: %w", err)
	}

	resp := triagePRResponse{
		Results: results,
		Total:   len(results),
		Sort:    sort,
		State:   state,
	}
	if len(results) == 0 {
		resp.Guidance = "No PRs matched. Indexed PR data? Try `ox index github` if this repo has GitHub data."
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	if pretty, _ := cmd.Flags().GetBool("pretty"); pretty {
		var any json.RawMessage
		if err := json.Unmarshal(out, &any); err == nil {
			if indented, err := json.MarshalIndent(any, "", "  "); err == nil {
				out = indented
			}
		}
	}
	out = append(out, '\n')
	return cli.WriteJSONBytes(cmd.OutOrStdout(), out)
}

type triagePRResponse struct {
	Results  []query.TriagedPR `json:"results"`
	Total    int               `json:"total"`
	Sort     string            `json:"sort"`
	State    string            `json:"state"`
	Guidance string            `json:"guidance,omitempty"`
}

func validTriageSort(s string) bool {
	switch strings.ToLower(s) {
	case "stalled", "age", "activity":
		return true
	}
	return false
}

func validTriageState(s string) bool {
	switch strings.ToLower(s) {
	case "open", "closed", "merged", "all":
		return true
	}
	return false
}
