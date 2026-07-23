package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/glance"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/recap"
	"github.com/spf13/cobra"
)

var recapCmd = &cobra.Command{
	Use:   "recap",
	Short: "Show the concrete value SageOx has delivered to your work",
	Long: `Answer "What value am I getting from SageOx?" with receipts, not vibes.

recap mines your ledger for the specific team-context knowledge that reached
your coding sessions — the decisions, conventions, and prior work SageOx put in
front of you so you didn't re-derive them — and points at each by name.

When called by an AI coworker it emits a JSON evidence bundle plus guidance to
narrate a personalized, prose answer. In a bare terminal it prints an honest
summary. If value is still ramping, it prescribes the next steps that start
generating it. Scoped to the current project's ledger; personal by default.`,
	RunE: runRecap,
}

func runRecap(cmd *cobra.Command, _ []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	since, err := resolveRecapSince(cmd)
	if err != nil {
		return err
	}
	now := time.Now()

	id := resolveRecapIdentity(cmd, projectRoot)

	teamPath, teamName := "", ""
	if tc := config.FindRepoTeamContext(projectRoot); tc != nil {
		teamPath, teamName = tc.Path, tc.TeamName
	}

	in := recap.BuildInput{
		LedgerPath:  ledgerPath,
		TeamPath:    teamPath,
		TeamName:    teamName,
		ProjectRoot: projectRoot,
		RepoName:    filepath.Base(projectRoot),
		Identity:    id,
		Since:       since,
		Until:       now,
		Now:         now,
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	agentID, _ := detectAgentContext()
	if agentID != "" && !cmd.Flags().Changed("json") {
		jsonOut = true
	}

	var out *recap.Output
	if jsonOut || !cli.IsInteractive() {
		out = recap.Build(in)
	} else {
		var spinErr error
		out, spinErr = cli.WithSpinner("Reading your ledger…", func() (*recap.Output, error) {
			return recap.Build(in), nil
		})
		// The spinner itself can fail (e.g. bubbletea program error) and hand
		// back a nil Output; recap.Build never errors, so just build directly
		// rather than risk a nil-deref in RenderHuman.
		if spinErr != nil || out == nil {
			out = recap.Build(in)
		}
	}

	var outputBytes int
	if jsonOut {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
		outputBytes = buf.Len()
		if _, err := buf.WriteTo(os.Stdout); err != nil {
			return err
		}
	} else {
		rendered := recap.RenderHuman(out, terminalWidth())
		outputBytes = len(rendered)
		fmt.Print(rendered)
	}

	if agentID != "" {
		slog.Debug("recap context cost", "agent_id", agentID, "bytes", outputBytes)
		trackContextBytes(int64(outputBytes))
	}
	return nil
}

// resolveRecapSince parses the --since flag into an absolute lower bound.
func resolveRecapSince(cmd *cobra.Command) (time.Time, error) {
	raw, _ := cmd.Flags().GetString("since")
	since, err := glance.ParseTimeFlag(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q: %w", raw, err)
	}
	return since, nil
}

// resolveRecapIdentity resolves whose value to report. Defaults to the current
// user (stable id, then privacy-safe display name, then username slug); --user
// overrides by display name or slug.
func resolveRecapIdentity(cmd *cobra.Command, projectRoot string) recap.Identity {
	if u, _ := cmd.Flags().GetString("user"); u != "" {
		return recap.Identity{DisplayName: u, Slug: u}
	}
	ep := endpoint.GetForProject(projectRoot)
	display := config.GetDisplayName()
	return recap.Identity{
		UserID:      auth.GetUserID(ep),
		DisplayName: identity.AttributionDisplayName(ep, display),
		Slug:        identity.AttributionUsername(ep, display),
	}
}

// terminalWidth returns the target render width, honoring COLUMNS (set by tests
// and by users who want a fixed width) and defaulting to 80 — which also keeps
// prose at a comfortable reading measure on very wide terminals.
func terminalWidth() int {
	if c := strings.TrimSpace(os.Getenv("COLUMNS")); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n >= 40 {
			return n
		}
	}
	return 80
}

func init() {
	recapCmd.Flags().Bool("json", false, "structured JSON evidence bundle for AI coworkers")
	recapCmd.Flags().String("since", "30d", "reporting window (e.g. 7d, 30d, 24h, or an ISO date)")
	recapCmd.Flags().String("user", "", "report for a specific coworker (display name or slug); default is you")
}
