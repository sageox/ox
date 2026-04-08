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
	"github.com/sageox/ox/internal/identity"
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
	Sessions        []sessionListEntry `json:"sessions"`
	Total           int                `json:"total"`
	Window          string             `json:"window,omitempty"`
	RepoName        string             `json:"repo_name"`
	RepoID          string             `json:"repo_id"`
	LedgerAvailable bool               `json:"ledger_available"`
}

// sessionListEntry is a single session in JSON output.
type sessionListEntry struct {
	Name       string `json:"name"`
	Date       string `json:"date"`
	Time       string `json:"time"`
	User       string `json:"user,omitempty"`
	Status     string `json:"status"`
	Recording  bool   `json:"recording,omitempty"`
	Summary    string `json:"summary,omitempty"`
	EntryCount int    `json:"entry_count,omitempty"`
	IsSubagent bool   `json:"is_subagent,omitempty"`
	Origin     string `json:"origin,omitempty"`
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

			// mark ledger sessions as uploaded using merge key
			for _, ls := range ledgerSessions {
				uploadedSessions[sessionMergeKey(ls)] = true
			}

			// merge local sessions with ledger sessions (local wins on duplicates)
			sessions = mergeSessionSources(sessions, ledgerSessions)

			// scan ledger cache for in-progress or unuploaded sessions
			// recordings are initially written to {ledger}/.sageox/cache/sessions/ before upload
			ledgerCachePath := filepath.Join(ledgerPath, ".sageox", "cache")
			cacheStore, cacheErr := session.NewStore(ledgerCachePath)
			if cacheErr == nil {
				var cacheSessions []session.SessionInfo
				if showAll {
					cacheSessions, _ = cacheStore.ListAllSessions()
				} else {
					cacheSessions, _ = cacheStore.ListSessions()
				}

				// merge cache sessions (lowest priority)
				sessions = mergeSessionSources(sessions, cacheSessions)
			}
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
	localUser := identity.AttributionUsername(listEndpoint, config.GetDisplayName())

	// JSON output
	if jsonOutput {
		window := "7d"
		if showAll {
			window = "all"
		}
		entries := make([]sessionListEntry, 0, len(sessions))
		for _, t := range sessions {
			uploaded := uploadedSessions[sessionMergeKey(t)]
			status := string(session.ClassifySession(t, uploaded))
			user := t.Username
			if user == "" {
				user = localUser
			}
			entries = append(entries, sessionListEntry{
				Name:       t.SessionName,
				Date:       t.CreatedAt.Format("2006-01-02"),
				Time:       t.CreatedAt.Format("15:04"),
				User:       user,
				Status:     status,
				Recording:  t.Recording,
				Summary:    t.Summary,
				EntryCount: t.EntryCount,
				IsSubagent: t.IsSubagent,
				Origin:     t.Origin,
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
		uploaded := uploadedSessions[sessionMergeKey(t)]
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
	turnsCol := fmt.Sprintf("%-8s", "TURNS")
	statusCol := fmt.Sprintf("%-14s", "STATUS")
	nameCol := "SESSION"

	header := sessionHeaderStyle.Render(dateCol + timeCol + userCol + turnsCol + statusCol + nameCol)
	fmt.Println("  " + header)

	// underline
	underline := strings.Repeat("-", 128)
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

	// subagent indicator
	if t.IsSubagent {
		name = "↳ " + name
	}

	// status via canonical classifier
	sessionStatus := session.ClassifySession(t, uploaded)
	var statusStr string
	var statusStyle string
	switch sessionStatus {
	case session.StatusRecording:
		statusStr = "● recording"
		statusStyle = "recording"
	case session.StatusPaused:
		statusStr = "⏸ paused"
		statusStyle = "local"
	case session.StatusGhost:
		statusStr = "⊘ ghost"
		statusStyle = "ghost"
	case session.StatusOrphan:
		statusStr = "⊘ orphan"
		statusStyle = "orphan"
	case session.StatusUploaded:
		statusStr = "✓ uploaded"
		statusStyle = "uploaded"
	case session.StatusCanceled:
		statusStr = "✗ canceled"
		statusStyle = "ghost" // dim — discarded
	default:
		statusStr = "✗ local only"
		statusStyle = "local"
	}

	// turns column
	turnsStr := "-"
	if t.EntryCount > 0 {
		turnsStr = fmt.Sprintf("%d", t.EntryCount)
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
	turnsCol := fmt.Sprintf("%-8s", turnsStr)
	statusCol := fmt.Sprintf("%-14s", statusStr)

	row := sessionDateStyle.Render(dateCol) +
		sessionDurationStyle.Render(timeCol) +
		sessionSummaryStyle.Render(userCol)

	// dim turns when zero
	if t.EntryCount == 0 {
		row += sessionEmptyStyle.Render(turnsCol)
	} else {
		row += sessionDurationStyle.Render(turnsCol)
	}

	switch statusStyle {
	case "recording":
		row += sessionTypeStyle.Render(statusCol)
	case "ghost":
		row += sessionEmptyStyle.Render(statusCol) // dim italic — useless, auto-cleanable
	case "orphan":
		row += sessionHydrationStyle.Render(statusCol) // warning color — has data, needs recovery
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

// sessionMergeKey returns the dedup key for a session.
// Uses SessionName when available, falls back to FilePath or Filename
// to avoid collapsing distinct legacy sessions with empty SessionName.
func sessionMergeKey(s session.SessionInfo) string {
	if s.SessionName != "" {
		return s.SessionName
	}
	if s.FilePath != "" {
		return s.FilePath
	}
	return s.Filename
}

// mergeSessionSources deduplicates sessions by merge key.
// Primary sessions take precedence over additional sessions.
// Returns merged sessions sorted newest-first by CreatedAt.
func mergeSessionSources(primary, additional []session.SessionInfo) []session.SessionInfo {
	existing := make(map[string]bool, len(primary))
	result := make([]session.SessionInfo, 0, len(primary)+len(additional))
	for _, s := range primary {
		existing[sessionMergeKey(s)] = true
		result = append(result, s)
	}
	for _, s := range additional {
		if k := sessionMergeKey(s); !existing[k] {
			existing[k] = true
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}
