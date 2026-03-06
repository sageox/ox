package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

const recentDiscussionLimit = 15

var agentTeamCtxCmd = &cobra.Command{
	Use:   "team-ctx [slug]",
	Short: "Output team context for AI agent planning",
	Long: `Output team discussions and distilled context for AI agent planning.

Without arguments: outputs the primary team's context (this repo's team).
With a team slug: outputs that specific team's context.

Lists the 15 most recent discussion files (read them for full detail),
then outputs the distilled summary from agent-context/distilled-discussions.md.

Output includes a content hash (team-ctx:<hash>) - if this marker is already
in your context, you don't need to re-run this command.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAgentTeamCtx,
}

func runAgentTeamCtx(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	var tc *config.TeamContext

	if len(args) > 0 {
		tc = resolveTeamContext(projectRoot, args[0])
		if tc == nil {
			return fmt.Errorf("team context not found: %q (use ox agent prime to see available teams)", args[0])
		}
	} else {
		tc = config.FindRepoTeamContext(projectRoot)
		if tc == nil {
			return fmt.Errorf("no team context configured for this project")
		}
	}

	cw := agentinstance.NewCountingWriter(cmd.OutOrStdout())
	out := io.Writer(cw)

	// list recent discussion files
	discussionsDir := filepath.Join(tc.Path, "discussions")
	hasDiscussions := listRecentDiscussions(out, discussionsDir)

	// output distilled summary
	agentContextPath := filepath.Join(tc.Path, "agent-context", "distilled-discussions.md")
	hasDistilled := outputDistilledContext(out, agentContextPath)

	if !hasDiscussions && !hasDistilled {
		return fmt.Errorf("no team context available: no discussions or distilled context found in %s", tc.Path)
	}

	// team-ctx is a direct cobra subcommand (not via runWithAgentID),
	// so send context heartbeat directly if agent ID is available
	if bytes := cw.BytesWritten(); bytes > 0 {
		if agentID := os.Getenv("SAGEOX_AGENT_ID"); agentID != "" {
			sendContextHeartbeat(agentID, bytes, "team-ctx")
		}
	}
	return nil
}

// resolveTeamContext finds a team context by slug, team ID, or name.
// Resolution order: exact slug match -> exact team ID match -> case-insensitive name match.
func resolveTeamContext(projectRoot, query string) *config.TeamContext {
	allTeams := config.FindAllTeamContexts(projectRoot)
	if len(allTeams) == 0 {
		return nil
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))

	// pass 1: exact slug match
	for i, tc := range allTeams {
		slug := tc.Slug
		if slug == "" {
			slug = api.DeriveSlug(tc.TeamName)
		}
		if slug == queryLower {
			return &allTeams[i]
		}
	}

	// pass 2: exact team ID match
	for i, tc := range allTeams {
		if tc.TeamID == query {
			return &allTeams[i]
		}
	}

	// pass 3: case-insensitive name match
	for i, tc := range allTeams {
		if strings.EqualFold(tc.TeamName, query) {
			return &allTeams[i]
		}
	}

	return nil
}

// listRecentDiscussions scans the discussions/ directory and outputs
// the 15 most recent files (by name, which are date-prefixed).
// Returns true if any discussions were found.
func listRecentDiscussions(out io.Writer, discussionsDir string) bool {
	entries, err := os.ReadDir(discussionsDir)
	if err != nil {
		return false
	}

	// collect all files recursively from discussions/
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			// scan subdirectory for files
			subDir := filepath.Join(discussionsDir, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if !sub.IsDir() {
					files = append(files, filepath.Join(subDir, sub.Name()))
				}
			}
		} else {
			files = append(files, filepath.Join(discussionsDir, entry.Name()))
		}
	}

	if len(files) == 0 {
		return false
	}

	// sort reverse-alphabetically (date-prefixed names = newest first)
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	limit := recentDiscussionLimit
	if len(files) < limit {
		limit = len(files)
	}

	fmt.Fprintln(out, "## Recent Discussions")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Read these files for full discussion details (%d most recent):\n", limit)
	fmt.Fprintln(out)
	for _, f := range files[:limit] {
		fmt.Fprintf(out, "- %s\n", f)
	}

	if len(files) > limit {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "For older discussions, list files in: %s\n", discussionsDir)
	}
	fmt.Fprintln(out)

	return true
}

// outputDistilledContext reads and outputs the distilled discussions file.
// Returns true if the file was found and output.
func outputDistilledContext(out io.Writer, path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	hash := sha256.Sum256(content)
	hashStr := fmt.Sprintf("%x", hash[:4])

	fmt.Fprintf(out, "<!-- team-ctx:%s -->\n", hashStr)
	fmt.Fprintln(out, "## Distilled Team Context")
	fmt.Fprintln(out)
	fmt.Fprint(out, string(content))

	return true
}
