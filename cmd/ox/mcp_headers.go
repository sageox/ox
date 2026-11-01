package main

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:    "mcp",
	Short:  "Local helpers for MCP clients",
	Hidden: true,
	// Do not initialize root's auth, telemetry, exporters, or daemon heartbeat.
	PersistentPreRunE: applyConfigFlag,
	PersistentPostRun: func(*cobra.Command, []string) {},
}

var mcpHeadersCmd = &cobra.Command{
	Use:    "headers",
	Short:  "Print local repository headers for an MCP client",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		projectRoot := config.FindProjectRoot()
		if projectRoot == "" || !config.IsInitialized(projectRoot) {
			return nil
		}

		// Reuse the request-header validator; corrupt local values are dropped.
		useragent.SetRepoID(config.GetRepoID(projectRoot))
		headers := make(map[string]string)
		if id := useragent.RepoID(); id != "" {
			headers[useragent.HeaderRepoID] = id
		}
		// MCP consumes these bytes as a protocol, even when terminal color is forced.
		data, err := cli.MarshalJSONIndent(headers)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	},
}

func init() {
	mcpCmd.AddCommand(mcpHeadersCmd)
	rootCmd.AddCommand(mcpCmd)
}

// Identify local helpers before shared startup, including rejected invocations.
// Cobra's lookup keeps a flag value such as --config=mcp from selecting this path.
func localMCPRequested(args []string) bool {
	// Detect packed helper invocations without running the normalizer, which
	// announces corrections on stdout. Cobra will reject the original spelling.
	if split, ok := wordSplitArgs(args); ok {
		args = split
	}
	cmd, _, _ := rootCmd.Find(args)
	for ; cmd != nil; cmd = cmd.Parent() {
		if cmd == mcpCmd {
			return true
		}
	}
	return false
}
