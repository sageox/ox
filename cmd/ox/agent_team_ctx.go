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
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/vtt"
	"github.com/sageox/ox/pkg/discussion"
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
in your context, you don't need to re-run this command.

To learn how to add to team context (rules, docs, discussions),
run 'ox guide team-context' or 'ox guide team-rules'.`,
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
// Uses the unified team discovery which merges daemon, local config, and filesystem sources.
func resolveTeamContext(projectRoot, query string) *config.TeamContext {
	t := resolveTeamByQuery(projectRoot, query)
	if t == nil {
		return nil
	}
	return t.toConfigTeamContext()
}

// listRecentDiscussions scans discussion directories and outputs the most
// recent with title and visual content tags.
// Returns true if any discussions were found.
func listRecentDiscussions(out io.Writer, discussionsDir string) bool {
	entries, err := os.ReadDir(discussionsDir)
	if err != nil {
		return false
	}

	var discussions []DiscussionIndexEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(discussionsDir, entry.Name())

		de := DiscussionIndexEntry{
			DirName: entry.Name(),
		}

		// read title from metadata.json
		if meta, err := loadDiscussionMetadata(dirPath); err == nil {
			de.Title = meta.Title
		}

		// extract unique speaker names from transcript.vtt
		if data, err := os.ReadFile(filepath.Join(dirPath, "transcript.vtt")); err == nil {
			if cues, err := vtt.Parse(data); err == nil {
				de.Participants = vtt.UniqueSpeakers(cues)
			}
		}

		// detect visual content from keyframes.json
		if kf, err := discussion.LoadKeyframes(dirPath); err == nil && kf != nil {
			de.VisualTypes = discussion.AllVisualTypes(kf)
		}

		discussions = append(discussions, de)
	}

	if len(discussions) == 0 {
		return false
	}

	// sort reverse-alphabetically by dir name (date-prefixed = newest first)
	sort.Slice(discussions, func(i, j int) bool {
		return discussions[i].DirName > discussions[j].DirName
	})

	limit := recentDiscussionLimit
	if len(discussions) < limit {
		limit = len(discussions)
	}

	fmt.Fprintln(out, "## Recent Discussions")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Recent discussions (%d shown, read files in each dir for detail):\n", limit)
	fmt.Fprintln(out)
	for _, d := range discussions[:limit] {
		label := d.DirName
		if d.Title != "" {
			label = d.Title
		}
		dirPath := filepath.Join(discussionsDir, d.DirName)

		// build suffix parts: participants and visual types
		var suffixes []string
		if len(d.Participants) > 0 {
			suffixes = append(suffixes, strings.Join(d.Participants, ", "))
		}
		if len(d.VisualTypes) > 0 {
			suffixes = append(suffixes, strings.Join(d.VisualTypes, ", "))
		}

		if len(suffixes) > 0 {
			fmt.Fprintf(out, "- %s (%s) — %s\n", label, strings.Join(suffixes, "; "), dirPath)
		} else {
			fmt.Fprintf(out, "- %s — %s\n", label, dirPath)
		}
	}

	if len(discussions) > limit {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "For older discussions, list dirs in: %s\n", discussionsDir)
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
