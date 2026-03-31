package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/query"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var codeActivityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Assemble GitHub activity clusters for the fact extractor",
	Long: `Query CodeDB for recent GitHub activity and output event clusters
as a flat JSON array suitable for the fact extractor pipeline.

Each element in the array has a "type" field: "pull_request", "issue", or "commit".
PR clusters include reviews (grouped by reviewer), discussion comments, and commits.`,
	RunE: runCodeActivity,
}

func init() {
	codeActivityCmd.Flags().String("since", "7d", "time window: duration (7d, 24h) or date (2026-03-15)")
	codeActivityCmd.Flags().Bool("pretty", false, "pretty-print JSON output with indentation")

	codeCmd.AddCommand(codeActivityCmd)
}

func runCodeActivity(cmd *cobra.Command, _ []string) error {
	root, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("not in a git repository")
	}

	dataDir := resolveCodeDBDir(root)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return fmt.Errorf("%s\n%s",
			cli.StyleError.Render("No code index found"),
			"Run "+cli.StyleCommand.Render("ox code index")+" to create one")
	}

	sinceStr, _ := cmd.Flags().GetString("since")
	since, err := parseSinceFlag(sinceStr)
	if err != nil {
		return fmt.Errorf("invalid --since value %q: %w", sinceStr, err)
	}
	until := time.Now().UTC()

	if isCodeDBIndexing(false) {
		return fmt.Errorf("code index is currently being built — activity queries unavailable until indexing completes. Run 'ox code status' to check progress")
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	result, err := query.AssembleActivity(ctx, db.Store(), since, until)
	if err != nil {
		return fmt.Errorf("assemble activity: %w", err)
	}

	data, err := result.MarshalEventClusters()
	if err != nil {
		return fmt.Errorf("marshal event clusters: %w", err)
	}

	prettyJSON, _ := cmd.Flags().GetBool("pretty")
	if prettyJSON {
		var pretty json.RawMessage
		if err := json.Unmarshal(data, &pretty); err == nil {
			indented, err := json.MarshalIndent(pretty, "", "  ")
			if err == nil {
				data = indented
			}
		}
	}

	fmt.Fprintln(os.Stdout, string(data))
	return nil
}

// parseSinceFlag parses a --since flag value as either a duration (7d, 24h)
// or an absolute date (2026-03-15, 2026-03-15T10:00:00Z).
func parseSinceFlag(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty value")
	}

	// Try duration with "d" suffix (e.g., "7d", "30d")
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err == nil && days > 0 {
			return time.Now().UTC().AddDate(0, 0, -days), nil
		}
	}

	// Try standard Go duration (e.g., "24h", "168h")
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("negative duration not allowed: %s", s)
		}
		return time.Now().UTC().Add(-d), nil
	}

	// Try date formats
	for _, layout := range []string{
		"2006-01-02",
		time.RFC3339,
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("expected duration (7d, 24h) or date (2026-03-15)")
}
