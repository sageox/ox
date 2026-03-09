package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/pkg/agentx"
	"github.com/spf13/cobra"
)

// coworkerListOutput is the JSON output structure for ox coworker list
type coworkerListOutput struct {
	Coworkers []coworkerInfo `json:"coworkers"`
	Source    string         `json:"source"`
	Endpoint  string         `json:"endpoint"`
	Teams     []string       `json:"teams,omitempty"` // team IDs included in this list
}

// coworkerInfo represents a single coworker in the list output
type coworkerInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model,omitempty"`
	Path        string `json:"path"`
	TeamID      string `json:"team_id"`
	TeamName    string `json:"team_name,omitempty"`
}

// coworkerLoadOutput is the JSON output structure for ox coworker load <name>
type coworkerLoadOutput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	Content     string `json:"content"`
	Path        string `json:"path"`
	TeamID      string `json:"team_id"`
	TeamName    string `json:"team_name,omitempty"`
	Loaded      bool   `json:"loaded"`
}

var coworkerCmd = &cobra.Command{
	Use:   "coworker",
	Short: "Manage team coworkers (AI subagents)",
	Long: `Manage team coworkers - expert AI subagents defined in your team context.

Coworkers are specialized agents with domain expertise that can be loaded
into your context when needed. They are defined in your team's coworkers/
directory and can be listed and loaded on demand.

Commands:
  list       List available coworkers
  load       Load a coworker's prompt into context
  add        Add a coworker to the team
  remove     Remove a coworker from the team`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var coworkerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available team coworkers",
	Long: `List all available coworkers from team contexts at the current endpoint.

Coworkers are expert AI subagents defined in coworkers/agents/.
Use 'ox coworker load <name>' to load a coworker's expertise into your context.`,
	RunE: runCoworkerList,
}

var coworkerLoadCmd = &cobra.Command{
	Use:   "load <name>",
	Short: "Load a coworker's prompt into context",
	Long: `Load a coworker's full prompt content into your AI coworker context.

This outputs the coworker's expertise as markdown, which can then be used
for specialized tasks. The load event is also logged to the session for
metrics on coworker usage.

Example:
  ox coworker load code-reviewer
  ox coworker load code-reviewer --model opus`,
	Args: cobra.ExactArgs(1),
	RunE: runCoworkerLoad,
}

var coworkerAddCmd = &cobra.Command{
	Use:   "add <file>",
	Short: "Add a coworker to the team",
	Long: `Add an expert coworker to your team's context.

Validates the file has YAML frontmatter with a description field,
copies it to the team's coworkers/agents/ directory, and commits it.

Agent file format (follows Claude Code's .claude/agents/ frontmatter style):
  ---
  description: "Expert code reviewer"
  model: "opus"
  ---
  # Code Reviewer
  You are an expert code reviewer...

Required frontmatter: description
Optional frontmatter: model (opus, sonnet, haiku)

Example:
  ox coworker add reviewer.md
  ox coworker add ~/agents/security-expert.md`,
	Args: cobra.ExactArgs(1),
	RunE: runCoworkerAdd,
}

var coworkerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a coworker from the team",
	Long: `Remove an expert coworker from your team's context.

Removes the coworker's agent file from coworkers/agents/ and commits
the deletion. Use --force to skip confirmation.

Example:
  ox coworker remove code-reviewer
  ox coworker remove code-reviewer --force`,
	Args: cobra.ExactArgs(1),
	RunE: runCoworkerRemove,
}

func init() {
	// shared --team flag on all subcommands
	for _, cmd := range []*cobra.Command{coworkerListCmd, coworkerLoadCmd, coworkerAddCmd, coworkerRemoveCmd} {
		cmd.Flags().String("team", "", "Team ID to use (defaults to this repo's team)")
	}

	// coworker list flags
	coworkerListCmd.Flags().Bool("json", false, "Output as JSON")

	// coworker load flags
	coworkerLoadCmd.Flags().String("model", "", "Override the coworker's default model (sonnet, opus, haiku)")
	coworkerLoadCmd.Flags().Bool("json", false, "Output as JSON")

	// coworker remove flags
	coworkerRemoveCmd.Flags().Bool("force", false, "Skip confirmation prompt")

	coworkerCmd.AddCommand(coworkerListCmd)
	coworkerCmd.AddCommand(coworkerLoadCmd)
	coworkerCmd.AddCommand(coworkerAddCmd)
	coworkerCmd.AddCommand(coworkerRemoveCmd)
	rootCmd.AddCommand(coworkerCmd)
}

// resolveTeamContext returns the team context for a coworker command.
// If --team is set, looks up that team ID from local config.
// Otherwise falls back to the repo's configured team.
func resolveCoworkerTeam(cmd *cobra.Command, projectRoot string) (*config.TeamContext, error) {
	teamFlag, _ := cmd.Flags().GetString("team")
	if teamFlag != "" {
		// search local config first, then all known teams (global teams directory)
		allTeams := config.FindAllTeamContexts(projectRoot)
		for i, tc := range allTeams {
			if tc.TeamID == teamFlag {
				return &allTeams[i], nil
			}
		}
		return nil, fmt.Errorf("team %q not found; check ox status for available teams", teamFlag)
	}
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return nil, fmt.Errorf("no team context configured; run 'ox init' to set up or use --team")
	}
	return tc, nil
}

func runCoworkerList(cmd *cobra.Command, args []string) error {
	// no agent gate - coworker list is useful for both humans and agents
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// get current endpoint
	currentEndpoint := endpoint.GetForProject(projectRoot)

	tc, err := resolveCoworkerTeam(cmd, projectRoot)
	if err != nil {
		if jsonMode {
			output := coworkerListOutput{
				Coworkers: []coworkerInfo{},
				Source:    "none",
				Endpoint:  currentEndpoint,
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(output)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No team context configured.")
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ox init' to set up team context.")
		return nil
	}

	// discover coworkers for this repo's team
	var allCoworkers []coworkerInfo

	agents, err := claude.DiscoverAgents(tc.Path)
	if err == nil {
		for _, agent := range agents {
			allCoworkers = append(allCoworkers, coworkerInfo{
				Name:        agent.Name,
				Description: agent.Description,
				Model:       agent.Model,
				Path:        agent.Path,
				TeamID:      tc.TeamID,
				TeamName:    tc.TeamName,
			})
		}
	}

	if jsonMode {
		output := coworkerListOutput{
			Coworkers: allCoworkers,
			Source:    "team_context",
			Endpoint:  currentEndpoint,
			Teams:     []string{tc.TeamID},
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// resolve display name for team
	teamLabel := tc.TeamName
	if teamLabel == "" {
		teamLabel = tc.TeamID
	}

	w := cmd.OutOrStdout()

	// text output — empty state
	if len(allCoworkers) == 0 {
		fmt.Fprintf(w, "%s No SageOx coworkers found in %s team.\n",
			cli.Styles.Info.Render("ℹ"), teamLabel)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Add one with: %s\n", cli.StyleCommand.Render("ox coworker add <file>.md"))
		fmt.Fprintf(w, "  See format:   %s\n", cli.StyleCommand.Render("ox coworker add --help"))
		return nil
	}

	// header with underline (matches ox help style)
	header := fmt.Sprintf("Expert Coworkers (%s)", teamLabel)
	fmt.Fprintln(w, cli.StyleGroupHeader.Render(header))
	fmt.Fprintln(w, cli.StyleDim.Render(strings.Repeat("─", len(header))))

	// build rows for dynamic column alignment
	rows := make([][]string, 0, len(allCoworkers))
	for _, cw := range allCoworkers {
		desc := cw.Description
		if desc == "" {
			desc = "(no description)"
		}
		rows = append(rows, []string{cw.Name, desc})
	}
	widths := cli.ColumnWidths(rows, []int{8, 12}, []int{24, 60})

	// data rows — name in brand, description in dim
	for _, row := range rows {
		name := fmt.Sprintf("%-*s", widths[0], row[0])
		fmt.Fprintf(w, "  %s  %s\n",
			cli.StyleCalloutBold.Render(name),
			cli.StyleDim.Render(row[1]),
		)
	}

	// load hint
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s Load with %s\n",
		cli.StyleDim.Render("▸"),
		cli.StyleCommand.Render("ox coworker load <name>"))

	return nil
}

func runCoworkerLoad(cmd *cobra.Command, args []string) error {
	// gate: require agent context
	if errMsg := agentx.RequireAgent("ox coworker load"); errMsg != "" {
		return fmt.Errorf("%s", errMsg)
	}

	name := args[0]
	modelOverride, _ := cmd.Flags().GetString("model")
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	tc, err := resolveCoworkerTeam(cmd, projectRoot)
	if err != nil {
		return err
	}

	agentContent, err := claude.LoadAgent(tc.Path, name)
	if err != nil || agentContent == nil {
		return fmt.Errorf("coworker %q not found in team context", name)
	}
	foundTeam := tc

	// apply model override if provided
	model := agentContent.Model
	if modelOverride != "" {
		model = modelOverride
	}

	// log to session if recording
	logCoworkerLoad(projectRoot, name, model)

	// track coworker load via telemetry
	trackCoworkerLoad(name, model, foundTeam.TeamID)

	if jsonMode {
		output := coworkerLoadOutput{
			Name:        agentContent.Name,
			Description: agentContent.Description,
			Model:       model,
			Content:     agentContent.Content,
			Path:        agentContent.Path,
			TeamID:      foundTeam.TeamID,
			TeamName:    foundTeam.TeamName,
			Loaded:      true,
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// text output - emit full content for agent consumption
	fmt.Fprintln(cmd.OutOrStdout(), agentContent.Content)
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "---")
	modelInfo := model
	if modelInfo == "" {
		modelInfo = "inherit"
	}
	teamInfo := foundTeam.TeamID
	if foundTeam.TeamName != "" {
		teamInfo = foundTeam.TeamName
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Coworker loaded: %s (model: %s, team: %s)\n", name, modelInfo, teamInfo)

	return nil
}

func runCoworkerAdd(cmd *cobra.Command, args []string) error {
	srcPath := args[0]

	// validate the file
	description, model, err := claude.ValidateAgentFile(srcPath)
	if err != nil {
		return fmt.Errorf("invalid coworker file: %w", err)
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	tc, err := resolveCoworkerTeam(cmd, projectRoot)
	if err != nil {
		return err
	}

	// derive coworker name from filename (strip .md extension)
	name := strings.TrimSuffix(filepath.Base(srcPath), ".md")

	// ensure coworkers/agents/ directory exists
	agentsDir := filepath.Join(tc.Path, claude.AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create agents directory: %w", err)
	}

	// copy file to team context
	destPath := filepath.Join(agentsDir, name+".md")
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("coworker %q already exists; remove it first with: ox coworker remove %s", name, name)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("write coworker file: %w", err)
	}

	// git add + commit in team context repo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relPath := filepath.Join(claude.AgentsDir, name+".md")
	if _, err := gitutil.RunGit(ctx, tc.Path, "add", relPath); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	commitMsg := fmt.Sprintf("add coworker: %s", name)
	if _, err := gitutil.RunGit(ctx, tc.Path, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	modelInfo := model
	if modelInfo == "" {
		modelInfo = "inherit"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Added coworker %q (model: %s)\n", name, modelInfo)
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", description)

	return nil
}

func runCoworkerRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	force, _ := cmd.Flags().GetBool("force")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	tc, err := resolveCoworkerTeam(cmd, projectRoot)
	if err != nil {
		return err
	}

	agentPath := filepath.Join(tc.Path, claude.AgentsDir, name+".md")
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		return fmt.Errorf("coworker %q not found", name)
	}

	if !force {
		fmt.Fprintf(cmd.OutOrStdout(), "Remove coworker %q? [y/N] ", name)
		var answer string
		if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil || !strings.EqualFold(answer, "y") {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	// git rm + commit in team context repo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relPath := filepath.Join(claude.AgentsDir, name+".md")
	if _, err := gitutil.RunGit(ctx, tc.Path, "rm", relPath); err != nil {
		return fmt.Errorf("git rm: %w", err)
	}

	commitMsg := fmt.Sprintf("remove coworker: %s", name)
	if _, err := gitutil.RunGit(ctx, tc.Path, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Removed coworker %q\n", name)
	return nil
}

// logCoworkerLoad writes a coworker load entry to the session if recording.
func logCoworkerLoad(projectRoot, name, model string) {
	if !session.IsRecording(projectRoot) {
		return
	}

	state, err := session.LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return
	}

	// create coworker load entry
	entry := session.NewCoworkerLoadEntry(name, model)

	// append to raw.jsonl
	if state.SessionFile != "" {
		appendEntryToSession(state.SessionFile, entry)
	} else if state.SessionPath != "" {
		rawFile := state.SessionPath + "/raw.jsonl"
		appendEntryToSession(rawFile, entry)
	}
}

// appendEntryToSession appends a session entry to a session file.
func appendEntryToSession(filePath string, entry session.SessionEntry) {
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_, _ = f.WriteString("\n")
}

// trackCoworkerLoad sends a telemetry event for coworker loads.
func trackCoworkerLoad(name, model, teamID string) {
	if cliCtx == nil || cliCtx.TelemetryClient == nil {
		return
	}

	metadata := make(map[string]string)
	if teamID != "" {
		metadata["team_id"] = teamID
	}

	cliCtx.TelemetryClient.Track(telemetry.Event{
		Type:          telemetry.EventCoworkerLoad,
		CoworkerName:  name,
		CoworkerModel: model,
		Metadata:      metadata,
		Success:       true,
	})
}
