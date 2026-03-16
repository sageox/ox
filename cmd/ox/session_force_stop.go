package main

import (
	"fmt"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionForceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a recording session",
	Long: `Stop an active recording session by agent ID.

Use 'ox session status' to see active recordings and their agent IDs.

Examples:
  ox session stop --agent-id OxzAbc    # stop a specific session
  ox session stop                       # lists active recordings if no agent ID given`,
	RunE: runSessionForceStop,
}

func init() {
	sessionCmd.AddCommand(sessionForceStopCmd)
	sessionForceStopCmd.Flags().String("agent-id", "", "agent ID of the session to stop")
}

func runSessionForceStop(cmd *cobra.Command, args []string) error {
	agentID, _ := cmd.Flags().GetString("agent-id")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	if agentID == "" {
		return listActiveRecordings(projectRoot)
	}

	// validate recording exists
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active recording found for agent %q\nRun 'ox session status' to see active recordings", agentID)
	}

	// runAgentSessionStop only uses inst.AgentID
	inst := &agentinstance.Instance{AgentID: agentID}
	return runAgentSessionStop(inst)
}

func listActiveRecordings(projectRoot string) error {
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to list recordings: %w", err)
	}

	if len(states) == 0 {
		fmt.Println("No active recordings found.")
		fmt.Println()
		cli.PrintHint("Start a recording with: ox agent <id> session start")
		return nil
	}

	fmt.Printf("Active recordings (%d):\n\n", len(states))
	for _, s := range states {
		duration := s.Duration().Round(1e9) // round to seconds
		fmt.Printf("  Agent: %s  Duration: %s  Adapter: %s\n", s.AgentID, duration, s.AdapterName)
	}
	fmt.Println()
	cli.PrintHint("Stop a recording with: ox session stop --agent-id <agent-id>")
	return nil
}
