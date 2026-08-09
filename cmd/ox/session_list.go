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
	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
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

Use --repo to list another repo's sessions by filesystem path instead of
the current directory. The path must be the repo root (.sageox/config.json
must exist directly there — subdirectories are not walked up). Local
session data reflects the --repo target, but ledger-merged sessions still
come from the CURRENT directory's configured ledger — this only matters
if the two repos use different team ledgers.

Examples:
  ox session list              # show last 10 from past 7 days
  ox session list --limit 20   # show last 20 from past 7 days
  ox session list --all        # show all sessions (may be slow)
  ox session list --repo /path/to/other/repo  # list a different repo's sessions`,
	RunE: runSessionList,
}

func init() {
	sessionCmd.AddCommand(sessionListCmd)
	sessionListCmd.Flags().Int("limit", 10, "maximum sessions to show (0 for no limit)")
	sessionListCmd.Flags().Bool("all", false, "show all sessions regardless of age (may be slow)")
	sessionListCmd.Flags().String("repo", "", "list sessions for a different repo by filesystem path (must be the repo root; default: current repo)")
}

// sessionListOutput is the JSON output format for session list.
type sessionListOutput struct {
	Sessions        []sessionListEntry `json:"sessions"`
	Total           int                `json:"total"`
	Window          string             `json:"window,omitempty"`
	RepoName        string             `json:"repo_name"`
	RepoID          string             `json:"repo_id"`
	LedgerAvailable bool               `json:"ledger_available"`
	Guidance        string             `json:"guidance,omitempty"`
}

// sessionListEntry is a single session in JSON output.
type sessionListEntry struct {
	Name            string `json:"name"`
	Date            string `json:"date"`
	Time            string `json:"time"`
	User            string `json:"user,omitempty"`
	Status          string `json:"status"`
	Recording       bool   `json:"recording,omitempty"`
	Title           string `json:"title,omitempty"`
	Summary         string `json:"summary,omitempty"`
	EntryCount      int    `json:"entry_count,omitempty"`
	IsSubagent      bool   `json:"is_subagent,omitempty"`
	Origin          string `json:"origin,omitempty"`
	HydrationStatus string `json:"hydration_status,omitempty"`
	StopReason      string `json:"stop_reason,omitempty"`
	HasRawData      bool   `json:"has_raw_data,omitempty"`
}

// sessionListAgentGuidance is the hint returned in JSON output when the caller
// is an agent. Keep terse — it ships to every agent invocation.
const sessionListAgentGuidance = "Pick sessions by title/summary; run 'ox session view <name>' to dig in. If hydration_status='dehydrated', run 'ox session download <name>' first."

func runSessionList(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	showAll, _ := cmd.Flags().GetBool("all")
	repoPath, _ := cmd.Flags().GetString("repo")
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	// Agents (AGENT_ENV=claude-code / codex / etc.) consume this output as
	// context. Default to JSON since it carries title, summary, entry_count,
	// hydration_status, and stop_reason — everything needed to decide which
	// session to dig into. Users can still force text with --json=false.
	jsonFlagSet := cmd.Root().PersistentFlags().Changed("json")
	inAgent := agentx.IsAgentContext()
	if inAgent && !jsonFlagSet {
		jsonOutput = true
	}

	if showAll {
		limit = 0
	}

	var store *session.Store
	var projectRoot string
	var err error
	if repoPath != "" {
		// --repo names a specific repo explicitly, so a resolution failure
		// hard-errors here instead of falling into the soft "not in a
		// project" branch below. That branch's output describes the
		// CURRENT directory as empty of sessions, which would misdescribe
		// a failure that is actually about the --repo path.
		store, projectRoot, err = newSessionStoreForRepoPath(repoPath)
		if err != nil {
			return err
		}
	} else {
		store, projectRoot, err = newSessionStore()
		if err != nil {
			if jsonOutput {
				cwd, _ := os.Getwd()
				out := sessionListOutput{
					Sessions:        []sessionListEntry{},
					RepoName:        filepath.Base(cwd),
					RepoID:          "",
					LedgerAvailable: false,
				}
				if inAgent {
					out.Guidance = sessionListAgentGuidance
				}
				return outputJSON(cmd.OutOrStdout(), out)
			}
			cwd, _ := os.Getwd()
			fmt.Println()
			fmt.Println(sessionEmptyStyle.Render(fmt.Sprintf("  Not in a SageOx project (cwd: %s).", cwd)))
			fmt.Println()
			cli.PrintHint("Run from a git directory where SageOx has been initialized, or run 'ox init' to set up.")
			return nil
		}
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
	//
	// SCOPE (v1): resolveLedgerPath() resolves via the CALLING process's
	// cwd/git root, not --repo's path. When --repo is set, the local-cache
	// sessions above correctly reflect the --repo target, but these
	// ledger-merged rows still reflect the CURRENT directory's configured
	// ledger. Invisible when both repos share one team ledger (the common
	// case); wrong if they use different ledgers. Resolving the ledger
	// relative to --repo too is a larger change, deferred for now.
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

			// mark ledger sessions as uploaded using merge key.
			// Drafts are excluded: a meta.json-only placeholder means the
			// session is still recording, not that its turn data landed.
			for _, ls := range ledgerSessions {
				if ls.Draft {
					continue
				}
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
			out := sessionListOutput{
				Sessions:        []sessionListEntry{},
				RepoName:        repoName,
				RepoID:          repoID,
				LedgerAvailable: ledgerAvailable,
				Window:          window,
			}
			if inAgent {
				out.Guidance = sessionListAgentGuidance
			}
			return outputJSON(cmd.OutOrStdout(), out)
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
			// legacy sessions leave SessionName empty — fall back to the filename so
			// agents always have a usable identifier for the 'ox session view <name>'
			// follow-up step described in the guidance hint.
			name := t.SessionName
			if name == "" {
				name = t.Filename
			}
			entries = append(entries, sessionListEntry{
				Name:      name,
				Date:      t.CreatedAt.Format("2006-01-02"),
				Time:      t.CreatedAt.Format("15:04"),
				User:      user,
				Status:    status,
				Recording: t.Recording,
				// Filter leaky strings out of JSON output too — agents
				// (claude-code, codex, etc.) consume this as context and
				// would otherwise see "Summary failed content validation:..."
				// as if it were the session's real title. Same invariant the
				// human-readable sessionBlurb enforces.
				Title:           sanitizedNonLeakySessionText(t.Title),
				Summary:         sanitizedNonLeakySessionText(t.Summary),
				EntryCount:      t.EntryCount,
				IsSubagent:      t.IsSubagent,
				Origin:          t.Origin,
				HydrationStatus: string(t.HydrationStatus),
				StopReason:      t.StopReason,
				HasRawData:      t.HasRawData,
			})
		}
		out := sessionListOutput{
			Sessions:        entries,
			Total:           len(entries),
			Window:          window,
			RepoName:        repoName,
			RepoID:          repoID,
			LedgerAvailable: ledgerAvailable,
		}
		if inAgent {
			out.Guidance = sessionListAgentGuidance
		}
		return outputJSON(cmd.OutOrStdout(), out)
	}

	// agents consume this output as context — drop decorative chrome that wastes tokens
	terse := agentx.IsAgentContext()

	// print header
	fmt.Println()
	printSessionTableHeader(terse)

	// print each session
	for _, t := range sessions {
		uploaded := uploadedSessions[sessionMergeKey(t)]
		printSessionRow(t, uploaded, localUser)
	}

	fmt.Println()

	if terse {
		return nil
	}

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

func printSessionTableHeader(terse bool) {
	// column headers
	dateCol := fmt.Sprintf("%-12s", "DATE")
	timeCol := fmt.Sprintf("%-8s", "TIME")
	userCol := fmt.Sprintf("%-16s", "USER")
	turnsCol := fmt.Sprintf("%-8s", "TURNS")
	statusCol := fmt.Sprintf("%-14s", "STATUS")
	nameCol := "SESSION"

	header := sessionHeaderStyle.Render(dateCol + timeCol + userCol + turnsCol + statusCol + nameCol)
	fmt.Println("  " + header)

	if terse {
		return
	}

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
	case session.StatusDraft:
		// A placeholder is published, but no turn data has landed. Rendered
		// distinctly from "uploaded" so a live session is never mistaken for
		// a finished one.
		statusStr = "◌ draft"
		statusStyle = "local"
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

	// adapter-detected terminal stop reasons (rate limit, quota, generic
	// terminal_error) surface here as a trailing annotation so a session
	// list reader can tell at a glance why the agent paused. User-initiated
	// stops (stopped/canceled/recovered) return "" from FormatStopReason
	// and do not add the suffix.
	if !t.Recording {
		if stopLabel := session.FormatStopReason(t); stopLabel != "" {
			row += "  " + sessionHydrationStyle.Render("["+stopLabel+"]")
		}
	}

	fmt.Println("  " + row)

	// emit a second line with the session's title (or summary snippet) so agents
	// and humans can tell sessions apart without having to open each one.
	if blurb := sessionBlurb(t); blurb != "" {
		fmt.Println("    " + sessionEmptyStyle.Render(blurb))
	}
}

// sessionBlurb returns a short, single-line description of the session for display
// under the row. Prefers meta.Title, falls back to the first real sentence of
// meta.Summary. Returns empty when neither is useful.
func sessionBlurb(s session.SessionInfo) string {
	const maxLen = 100
	// Title/Summary come verbatim from meta.json written by other coworkers —
	// strip ANSI and control bytes before rendering so a shared ledger can't
	// spoof the terminal.
	//
	// Belt-and-suspenders against the ox-qqka leak: producer-side fixes
	// (session_finalize.go) and the writer guard (lfs.ValidateUserVisible)
	// prevent new leaks, but EXISTING ledgers may still carry validator-
	// error stubs in either meta.title OR meta.summary until ox-l4mj
	// runs. Render nothing rather than the diagnostic in either case.
	if t := sanitizeSessionText(strings.TrimSpace(s.Title)); t != "" && !lfs.IsLeakySummaryString(t) {
		return truncateSingleLine(t, maxLen)
	}
	sum := sanitizeSessionText(strings.TrimSpace(s.Summary))
	if sum == "" || lfs.IsLeakySummaryString(sum) {
		return ""
	}
	// skip markdown structure (headings, bullets, tables, metadata lines) and
	// find the first real prose sentence so the blurb is actually informative.
	for line := range strings.SplitSeq(sum, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownStructure(line) {
			continue
		}
		if i := strings.Index(line, ". "); i > 0 {
			line = line[:i+1]
		}
		return truncateSingleLine(line, maxLen)
	}
	return ""
}

// sanitizedNonLeakySessionText returns the sanitized text unless it
// matches a known leak shape (validator-error sentinel string), in
// which case it returns "". Use this anywhere a value from meta.title
// or meta.summary is going to be displayed or returned to a consumer
// that doesn't already filter — JSON output, agent prompts, blurbs.
//
// This is the renderer-side belt-and-suspenders against the ox-qqka
// leak: producer-side fixes prevent NEW leaks; this prevents existing
// pre-fix ledgers from surfacing the diagnostic until ox-l4mj has run.
func sanitizedNonLeakySessionText(s string) string {
	clean := sanitizeSessionText(strings.TrimSpace(s))
	if clean == "" || lfs.IsLeakySummaryString(clean) {
		return ""
	}
	return clean
}

// sanitizeSessionText strips ANSI CSI/OSC escape sequences and other C0/C1
// control bytes from text originating in a shared ledger's meta.json. Preserves
// tabs and newlines (callers flatten newlines separately when rendering
// single-line).
func sanitizeSessionText(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b && i+1 < len(runes) {
			// consume the introducer byte first so its value is not treated as
			// a sequence terminator (e.g. '[' is itself in the CSI final-byte
			// range @-~, which would otherwise end the CSI on byte one).
			i++
			switch runes[i] {
			case '[': // CSI — params then final byte in 0x40..0x7e
				for i+1 < len(runes) {
					nr := runes[i+1]
					i++
					if nr >= '@' && nr <= '~' {
						break
					}
				}
			case ']': // OSC — payload then BEL or ST (ESC \)
				for i+1 < len(runes) {
					i++
					if runes[i] == 0x07 {
						break
					}
					if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
						i++
						break
					}
				}
			default:
				// two-byte escape (e.g. ESC =, ESC 7) — introducer already consumed
			}
			continue
		}
		// drop C0 controls except \t and \n; drop DEL and C1 (0x80-0x9f).
		if r < 0x20 && r != '\t' && r != '\n' {
			continue
		}
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isMarkdownStructure reports whether a line is markdown syntax rather than
// narrative prose (headings, bullets, tables, blockquotes, code fences, or
// bold metadata like "**Key:** value").
func isMarkdownStructure(line string) bool {
	if line == "" {
		return true
	}
	switch line[0] {
	case '#', '-', '*', '|', '>', '`':
		return true
	}
	return strings.HasPrefix(line, "**")
}

func truncateSingleLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
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
//
// One exception to primary-wins: a DRAFT placeholder loses to any non-draft
// row for the same session. The ledger's git-tracked sessions/ is merged as
// primary, ahead of the ledger cache where live recordings actually live — so
// without this, a draft published at turn 2 shadows its own live recording and
// every active session renders as finished, with no title and no turn count.
// A draft carries strictly less information than the row it would displace.
func mergeSessionSources(primary, additional []session.SessionInfo) []session.SessionInfo {
	index := make(map[string]int, len(primary))
	result := make([]session.SessionInfo, 0, len(primary)+len(additional))
	for _, s := range primary {
		k := sessionMergeKey(s)
		if _, dup := index[k]; dup {
			continue
		}
		index[k] = len(result)
		result = append(result, s)
	}
	for _, s := range additional {
		k := sessionMergeKey(s)
		at, seen := index[k]
		if !seen {
			index[k] = len(result)
			result = append(result, s)
			continue
		}
		if result[at].Draft && !s.Draft {
			result[at] = s
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}
