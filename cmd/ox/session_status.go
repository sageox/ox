package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check recording status",
	Long: `Check the current session recording status.

Shows all active recordings in this project. With multiple worktrees
or agents, there may be several recordings in progress simultaneously.

Use --current to filter to the calling agent's recording only
(requires SAGEOX_AGENT_ID environment variable).

Examples:
  ox session status              # show all active recordings
  ox session status --json       # JSON output
  ox session status --current    # only this agent's recording`,
	RunE: runSessionStatus,
}

// sessionStatusOutput is the JSON output format for session status.
type sessionStatusOutput struct {
	Recording     bool                    `json:"recording"`
	Guidance      string                  `json:"guidance,omitempty"`
	Count         int                     `json:"count,omitempty"`
	Sessions      []sessionRecordingEntry `json:"sessions,omitempty"`
	Title         string                  `json:"title,omitempty"`
	DurationSecs  int                     `json:"duration_seconds,omitempty"`
	Duration      string                  `json:"duration,omitempty"`
	Agent         string                  `json:"agent,omitempty"`
	AgentID       string                  `json:"agent_id,omitempty"`
	SessionFile   string                  `json:"session_file,omitempty"`
	StartedAt     string                  `json:"started_at,omitempty"`
	WorkspacePath string                  `json:"workspace_path,omitempty"`
	Branch        string                  `json:"branch,omitempty"`
}

// sessionRecordingEntry represents one active recording in the multi-session output.
type sessionRecordingEntry struct {
	AgentID       string `json:"agent_id"`
	Title         string `json:"title,omitempty"`
	Agent         string `json:"agent,omitempty"`
	DurationSecs  int    `json:"duration_seconds"`
	Duration      string `json:"duration"`
	StartedAt     string `json:"started_at"`
	SessionFile   string `json:"session_file,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	Branch        string `json:"branch,omitempty"`
}

func init() {
	sessionCmd.AddCommand(sessionStatusCmd)
	sessionStatusCmd.Flags().Bool("current", false, "only show this agent's recording (uses SAGEOX_AGENT_ID)")
}

func runSessionStatus(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	currentOnly, _ := cmd.Flags().GetBool("current")

	projectRoot, err := requireProjectRoot()
	if err != nil {
		if jsonOutput {
			return outputJSON(sessionStatusOutput{Recording: false})
		}
		return err
	}

	// load all recording states
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		if jsonOutput {
			return outputJSON(sessionStatusOutput{Recording: false})
		}
		return fmt.Errorf("failed to check recording state: %w", err)
	}

	// filter to current agent if --current
	if currentOnly {
		agentID := os.Getenv("SAGEOX_AGENT_ID")
		if agentID == "" {
			if jsonOutput {
				return outputJSON(sessionStatusOutput{Recording: false})
			}
			return fmt.Errorf("--current requires SAGEOX_AGENT_ID environment variable (set by 'ox agent prime')")
		}
		var filtered []*session.RecordingState
		for _, s := range states {
			if s.AgentID == agentID {
				filtered = append(filtered, s)
			}
		}
		states = filtered
	}

	// no recordings
	if len(states) == 0 {
		if jsonOutput {
			return outputJSON(sessionStatusOutput{
				Recording: false,
				Guidance:  "Run 'ox agent <id> session start' to begin recording",
			})
		}
		fmt.Println(cli.StyleDim.Render("Not recording"))
		fmt.Println()
		fmt.Println("Run 'ox agent <id> session start' to begin recording")
		return nil
	}

	// single recording — backward-compatible output
	if len(states) == 1 {
		state := states[0]
		duration := state.Duration()
		durationStr := formatDurationHuman(duration)

		if jsonOutput {
			output := sessionStatusOutput{
				Recording:     true,
				Guidance:      "Run 'ox agent <id> session stop' to save the recording",
				Count:         1,
				Title:         state.Title,
				DurationSecs:  int(duration.Seconds()),
				Duration:      durationStr,
				Agent:         state.AdapterName,
				AgentID:       state.AgentID,
				SessionFile:   state.SessionFile,
				StartedAt:     state.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
				WorkspacePath: state.WorkspacePath,
				Branch:        state.Branch,
			}
			return outputJSON(output)
		}

		fmt.Println(cli.StyleSuccess.Render("Recording in progress"))
		fmt.Println()
		if state.Title != "" {
			fmt.Printf("  Title:    %s\n", state.Title)
		}
		fmt.Printf("  Duration: %s\n", durationStr)
		fmt.Printf("  Agent:    %s\n", state.AdapterName)
		fmt.Printf("  Started:  %s\n", state.StartedAt.Format("15:04:05"))
		if state.AgentID != "" {
			fmt.Printf("  Agent ID: %s\n", state.AgentID)
		}
		if state.WorkspacePath != "" {
			fmt.Printf("  Workspace: %s\n", state.WorkspacePath)
		}
		if state.Branch != "" {
			fmt.Printf("  Branch:   %s\n", state.Branch)
		}
		fmt.Println()
		fmt.Println(cli.StyleDim.Render("Run 'ox agent <id> session stop' to save the recording"))
		return nil
	}

	// multiple recordings
	if jsonOutput {
		var entries []sessionRecordingEntry
		for _, s := range states {
			d := s.Duration()
			entries = append(entries, sessionRecordingEntry{
				AgentID:       s.AgentID,
				Title:         s.Title,
				Agent:         s.AdapterName,
				DurationSecs:  int(d.Seconds()),
				Duration:      formatDurationHuman(d),
				StartedAt:     s.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
				SessionFile:   s.SessionFile,
				WorkspacePath: s.WorkspacePath,
				Branch:        s.Branch,
			})
		}
		return outputJSON(sessionStatusOutput{
			Recording: true,
			Guidance:  "Use --current to filter to this agent's recording",
			Count:     len(states),
			Sessions:  entries,
		})
	}

	// text output for multiple recordings
	fmt.Println(cli.StyleSuccess.Render(fmt.Sprintf("%d recordings in progress", len(states))))
	fmt.Println()

	for i, state := range states {
		if i > 0 {
			fmt.Println()
		}
		durationStr := formatDurationHuman(state.Duration())

		label := state.AgentID
		if label == "" {
			label = state.AdapterName
		}
		fmt.Printf("  %s %s\n", cli.StyleBold.Render(label), cli.StyleDim.Render("("+durationStr+")"))

		if state.Title != "" {
			fmt.Printf("    Title:   %s\n", state.Title)
		}
		fmt.Printf("    Agent:   %s\n", state.AdapterName)
		fmt.Printf("    Started: %s\n", state.StartedAt.Format("15:04:05"))
		if state.WorkspacePath != "" {
			fmt.Printf("    Workspace: %s\n", state.WorkspacePath)
		}
		if state.Branch != "" {
			fmt.Printf("    Branch:  %s\n", state.Branch)
		}
	}

	fmt.Println()
	fmt.Println(cli.StyleDim.Render("Use --current to filter to this agent's recording"))

	return nil
}

// outputJSON writes JSON to stdout.
func outputJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
