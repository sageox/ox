package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// lipgloss styles for session list
var (
	sessionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	sessionDateStyle = lipgloss.NewStyle().
				Foreground(cli.ColorInfo)

	sessionDurationStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	sessionTypeStyle = lipgloss.NewStyle().
				Foreground(cli.ColorAccent)

	sessionSummaryStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	sessionEmptyStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim).
				Italic(true)

	sessionHydrationStyle = lipgloss.NewStyle().
				Foreground(cli.ColorWarning)
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sessions",
	Long: `List sessions in the ledger.

Shows date, time, user, and hydration status for each session.
Sessions are sorted by date with newest first.

By default, only shows sessions from the last 7 days for performance.
Use --all to show all sessions regardless of age.

Examples:
  ox session list              # show last 10 from past 7 days
  ox session list --limit 20   # show last 20 from past 7 days
  ox session list --all        # show all sessions (may be slow)`,
	RunE: runSessionList,
}

func init() {
	sessionCmd.AddCommand(sessionListCmd)
	sessionListCmd.Flags().Int("limit", 10, "maximum sessions to show (0 for no limit)")
	sessionListCmd.Flags().Bool("all", false, "show all sessions regardless of age (may be slow)")
}

// sessionListOutput is the JSON output format for session list.
type sessionListOutput struct {
	Sessions       []sessionListEntry `json:"sessions"`
	Total          int                `json:"total"`
	Window         string             `json:"window,omitempty"`
	RepoName       string             `json:"repo_name"`
	RepoID         string             `json:"repo_id"`
	LedgerAvailable bool              `json:"ledger_available"`
}

// sessionListEntry is a single session in JSON output.
type sessionListEntry struct {
	Name      string `json:"name"`
	Date      string `json:"date"`
	Time      string `json:"time"`
	User      string `json:"user,omitempty"`
	Status    string `json:"status"`
	Recording bool   `json:"recording,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

func runSessionList(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	showAll, _ := cmd.Flags().GetBool("all")
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	if showAll {
		limit = 0
	}

	store, projectRoot, err := newSessionStore()
	if err != nil {
		if jsonOutput {
			cwd, _ := os.Getwd()
			return outputJSON(sessionListOutput{
				Sessions:        []sessionListEntry{},
				RepoName:        filepath.Base(cwd),
				RepoID:          "",
				LedgerAvailable: false,
			})
		}
		cwd, _ := os.Getwd()
		fmt.Println()
		fmt.Println(sessionEmptyStyle.Render(fmt.Sprintf("  Not in a SageOx project (cwd: %s).", cwd)))
		fmt.Println()
		cli.PrintHint("Run from a git directory where SageOx has been initialized, or run 'ox init' to set up.")
		return nil
	}

	repoName := filepath.Base(projectRoot)
	repoID := config.GetRepoID(projectRoot)

	var sessions []session.SessionInfo

	// --all: scan all sessions (may be slow with many sessions)
	// default: only scan sessions from last 7 days for performance
	if showAll {
		sessions, err = store.ListAllSessions()
	} else {
		sessions, err = store.ListSessions()
	}
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	// track which sessions are in the ledger (uploaded)
	uploadedSessions := make(map[string]bool)

	// also scan ledger sessions from team members
	ledgerPath, ledgerErr := resolveLedgerPath()
	ledgerAvailable := ledgerErr == nil
	if ledgerAvailable {
		ledgerStore, storeErr := session.NewStore(ledgerPath)
		if storeErr == nil {
			var ledgerSessions []session.SessionInfo
			if showAll {
				ledgerSessions, _ = ledgerStore.ListAllSessions()
			} else {
				ledgerSessions, _ = ledgerStore.ListSessions()
			}

			// build lookup of existing session names
			existing := make(map[string]bool)
			for _, s := range sessions {
				existing[s.SessionName] = true
			}

			for _, ls := range ledgerSessions {
				uploadedSessions[ls.SessionName] = true
				if !existing[ls.SessionName] {
					sessions = append(sessions, ls)
				}
			}

			// re-sort by date (newest first)
			sort.Slice(sessions, func(i, j int) bool {
				return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
			})
		} else {
			slog.Debug("skipping ledger sessions", "err", storeErr)
			ledgerAvailable = false
		}
	} else {
		slog.Debug("ledger not available for session list", "err", ledgerErr)
	}

	// handle empty case
	if len(sessions) == 0 {
		if jsonOutput {
			window := "7d"
			if showAll {
				window = "all"
			}
			return outputJSON(sessionListOutput{
				Sessions:        []sessionListEntry{},
				RepoName:        repoName,
				RepoID:          repoID,
				LedgerAvailable: ledgerAvailable,
				Window:          window,
			})
		}
		fmt.Println()
		repoLabel := fmt.Sprintf("%q", repoName)
		if repoID != "" {
			repoLabel += fmt.Sprintf(" (%s)", repoID)
		}
		fmt.Println(sessionEmptyStyle.Render(fmt.Sprintf("  No sessions found for %s.", repoLabel)))
		fmt.Println()
		if !ledgerAvailable {
			cli.PrintHint("Ledger not available — only local sessions were checked. Run 'ox doctor --fix' to set up ledger sync.")
		} else {
			cli.PrintHint("Start a recording with 'ox session start' to capture your development session.")
		}
		return nil
	}

	// apply limit
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	// get local username for sessions without meta.json
	listEndpoint := endpoint.GetForProject(projectRoot)
	localUser := getAuthenticatedUsername(listEndpoint)

	// JSON output
	if jsonOutput {
		window := "7d"
		if showAll {
			window = "all"
		}
		entries := make([]sessionListEntry, 0, len(sessions))
		for _, t := range sessions {
			uploaded := uploadedSessions[t.SessionName]
			status := "local"
			if t.Recording {
				status = "recording"
			} else if uploaded {
				status = "uploaded"
			}
			user := t.Username
			if user == "" {
				user = localUser
			}
			entries = append(entries, sessionListEntry{
				Name:      t.SessionName,
				Date:      t.CreatedAt.Format("2006-01-02"),
				Time:      t.CreatedAt.Format("15:04"),
				User:      user,
				Status:    status,
				Recording: t.Recording,
				Summary:   t.Summary,
			})
		}
		return outputJSON(sessionListOutput{
			Sessions:        entries,
			Total:           len(entries),
			Window:          window,
			RepoName:        repoName,
			RepoID:          repoID,
			LedgerAvailable: ledgerAvailable,
		})
	}

	// print header
	fmt.Println()
	printSessionTableHeader()

	// print each session
	for _, t := range sessions {
		uploaded := uploadedSessions[t.SessionName]
		printSessionRow(t, uploaded, localUser)
	}

	fmt.Println()

	// summary
	fmt.Printf("%s %d session(s) shown",
		cli.StyleDim.Render("Total:"),
		len(sessions))

	if !showAll {
		fmt.Printf(" %s", cli.StyleDim.Render("(last 7 days; use --all for older)"))
	} else if limit > 0 && len(sessions) >= limit {
		fmt.Printf(" %s", cli.StyleDim.Render("(use --limit 0 to show all)"))
	}
	fmt.Println()

	return nil
}

func printSessionTableHeader() {
	// column headers
	dateCol := fmt.Sprintf("%-12s", "DATE")
	timeCol := fmt.Sprintf("%-8s", "TIME")
	userCol := fmt.Sprintf("%-16s", "USER")
	statusCol := fmt.Sprintf("%-14s", "STATUS")
	nameCol := "SESSION"

	header := sessionHeaderStyle.Render(dateCol + timeCol + userCol + statusCol + nameCol)
	fmt.Println("  " + header)

	// underline
	underline := strings.Repeat("-", 120)
	fmt.Println("  " + cli.StyleDim.Render(underline))
}

func printSessionRow(t session.SessionInfo, uploaded bool, localUser string) {
	// format date
	dateStr := t.CreatedAt.Format("2006-01-02")
	timeStr := t.CreatedAt.Format("15:04")

	// display name: session name if available, else filename
	name := t.SessionName
	if name == "" {
		name = t.Filename
	}

	// status: recording > uploaded > local only
	var statusStr string
	var statusStyle string // "recording", "uploaded", or "local"
	if t.Recording {
		statusStr = "● recording"
		statusStyle = "recording"
	} else if uploaded {
		statusStr = "✓ uploaded"
		statusStyle = "uploaded"
	} else {
		statusStr = "✗ local only"
		statusStyle = "local"
	}

	// user display: prefer meta.json username, fallback to local user
	userStr := t.Username
	if userStr == "" && localUser != "" {
		userStr = localUser
	}
	if userStr == "" {
		userStr = "-"
	}
	// show identity before @ (e.g., "ryan" from "ryan@sageox.ai")
	if idx := strings.Index(userStr, "@"); idx > 0 {
		userStr = userStr[:idx]
	}
	if len(userStr) > 14 {
		userStr = userStr[:11] + "..."
	}

	// build row
	dateCol := fmt.Sprintf("%-12s", dateStr)
	timeCol := fmt.Sprintf("%-8s", timeStr)
	userCol := fmt.Sprintf("%-16s", userStr)
	statusCol := fmt.Sprintf("%-14s", statusStr)

	row := sessionDateStyle.Render(dateCol) +
		sessionDurationStyle.Render(timeCol) +
		sessionSummaryStyle.Render(userCol)

	switch statusStyle {
	case "recording":
		row += sessionTypeStyle.Render(statusCol)
	case "uploaded":
		row += sessionDurationStyle.Render(statusCol)
	default:
		row += sessionHydrationStyle.Render(statusCol)
	}

	row += sessionSummaryStyle.Render(name)

	fmt.Println("  " + row)
}

func formatSessionDuration(d time.Duration) string {
	if d < time.Minute {
		secs := int(d.Seconds())
		if secs <= 0 {
			return "-"
		}
		return fmt.Sprintf("%ds", secs)
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		return fmt.Sprintf("%dm", mins)
	}

	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
