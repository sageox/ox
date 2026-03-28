package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
)

// agentInstancesCmd is a hidden alias for "ox agent list", kept for backward compatibility.
var agentInstancesCmd = &cobra.Command{
	Use:    "instances",
	Short:  "List active AI coworker instances (use 'list' instead)",
	Hidden: true,
	RunE:   runAgentInstances,
}

// agentSessionsCmd is deprecated, use agentListCmd instead
var agentSessionsCmd = &cobra.Command{
	Use:    "sessions",
	Short:  "List active agent instances (deprecated: use 'list')",
	Hidden: true,
	RunE:   runAgentInstances,
}

func init() {
	agentInstancesCmd.Flags().Bool("json", false, "Output as JSON")
	agentSessionsCmd.Flags().Bool("json", false, "Output as JSON")
	agentCmd.AddCommand(agentInstancesCmd)
	agentCmd.AddCommand(agentSessionsCmd)
}

func runAgentInstances(cmd *cobra.Command, _ []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")

	instances, err := daemon.GetAllInstances()
	if err != nil {
		if jsonOutput {
			return outputInstancesJSON(nil, err)
		}
		return fmt.Errorf("failed to get instances: %w", err)
	}

	if jsonOutput {
		return outputInstancesJSON(instances, nil)
	}

	return outputInstancesTable(instances)
}

func outputInstancesJSON(instances []daemon.InstanceInfo, err error) error {
	type output struct {
		Instances []daemon.InstanceInfo `json:"instances"`
		Error     string                `json:"error,omitempty"`
	}

	out := output{Instances: instances}
	if err != nil {
		out.Error = err.Error()
	}
	if out.Instances == nil {
		out.Instances = []daemon.InstanceInfo{}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func outputInstancesTable(instances []daemon.InstanceInfo) error {
	if len(instances) == 0 {
		fmt.Println("No active AI coworker instances.")
		fmt.Println()
		fmt.Println("Instances appear when coworkers run 'ox agent prime' and hooks send heartbeats.")
		fmt.Println("Run 'ox agent prime' in a repository to register this instance.")
		return nil
	}

	// header
	fmt.Printf("%-10s %-36s %-8s %-18s %s\n",
		cli.StyleBold.Render("COWORKER"),
		cli.StyleBold.Render("WORKSPACE"),
		cli.StyleBold.Render("STATUS"),
		cli.StyleBold.Render("LAST HEARTBEAT"),
		cli.StyleBold.Render("LAST WHISPER"))
	fmt.Println(strings.Repeat("-", 90))

	// rows
	for _, inst := range instances {
		workspace := shortenPath(inst.WorkspacePath)
		lastHB := formatTimeAgoShort(inst.LastHeartbeat)

		var lastWhisper string
		if inst.LastWhisper.IsZero() {
			lastWhisper = "-"
		} else {
			lastWhisper = formatTimeAgoShort(inst.LastWhisper)
		}

		statusStyle := cli.StyleSuccess
		if inst.Status == daemon.StatusIdle {
			statusStyle = cli.StyleDim
		}

		fmt.Printf("%-10s %-36s %s %-18s %s\n",
			inst.AgentID,
			workspace,
			statusStyle.Render(fmt.Sprintf("%-8s", inst.Status)),
			cli.StyleDim.Render(lastHB),
			cli.StyleDim.Render(lastWhisper))
	}

	fmt.Println()
	fmt.Printf("%d active instance(s)\n", len(instances))

	return nil
}

// shortenPath shortens a path for display by replacing home dir with ~
// and truncating long paths
func shortenPath(path string) string {
	if path == "" {
		return "-"
	}

	// replace home dir with ~
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		path = "~" + path[len(home):]
	}

	// truncate if too long
	maxLen := 34
	if len(path) > maxLen {
		parts := strings.Split(path, string(filepath.Separator))
		if len(parts) > 2 {
			path = ".../" + filepath.Join(parts[len(parts)-2], parts[len(parts)-1])
		}
		if len(path) > maxLen {
			path = "..." + path[len(path)-maxLen+3:]
		}
	}

	return path
}

// formatTimeAgoShort formats a time as a short relative time
func formatTimeAgoShort(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	diff := time.Since(t)

	switch {
	case diff < time.Second:
		return "now"
	case diff < time.Minute:
		return fmt.Sprintf("%ds ago", int(diff.Seconds()))
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	}
}
