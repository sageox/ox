package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// lipgloss styles for session show
var (
	showTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary)

	showLabelStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim).
			Width(14)

	showValueStyle = lipgloss.NewStyle()

	showHighlightStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	showEntryTypeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorInfo)

	showEntryTimestampStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	showEntryContentStyle = lipgloss.NewStyle()

	showToolStyle = lipgloss.NewStyle().
			Foreground(cli.ColorAccent)

	showSeparatorStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	showSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)
)

// sessionShowData represents a read session
type sessionShowData struct {
	Info     session.SessionInfo
	Metadata *sessionMetadata
	Entries  []map[string]any
	Footer   map[string]any
}

// sessionMetadata contains session header metadata
type sessionMetadata struct {
	Version   string
	AgentID   string
	AgentType string
	Username  string
	RepoID    string
	CreatedAt time.Time
}

var sessionShowCmd = &cobra.Command{
	Use:        "show [session-name]",
	Short:      "View a session (use 'ox session view' instead)",
	Hidden:     true,
	Deprecated: "use 'ox session view --json' instead",
	RunE:       runSessionShowLegacy,
}

func init() {
	sessionCmd.AddCommand(sessionShowCmd)
	sessionShowCmd.Flags().StringP("input", "i", "", "input JSONL file path (bypasses managed store)")
	sessionShowCmd.Flags().Bool("latest", false, "show most recent session")
	sessionShowCmd.Flags().Bool("raw", false, "show raw JSON format")
	sessionShowCmd.Flags().Bool("metadata", false, "show only metadata (no entries)")
	sessionShowCmd.Flags().Int("limit", 0, "limit number of entries shown (0 = all)")
}

func runSessionShowLegacy(cmd *cobra.Command, args []string) error {
	inputPath, _ := cmd.Flags().GetString("input")
	showRaw, _ := cmd.Flags().GetBool("raw")
	metadataOnly, _ := cmd.Flags().GetBool("metadata")
	entryLimit, _ := cmd.Flags().GetInt("limit")
	// latest flag parsed but not yet used until session store is wired up
	_, _ = cmd.Flags().GetBool("latest")

	var t *sessionShowData

	if inputPath != "" {
		// read from arbitrary file path
		st, err := session.ReadSessionFromPath(inputPath)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return fmt.Errorf("file not found: %s", inputPath)
			}
			return fmt.Errorf("read session: %w", err)
		}
		t = convertStoredSession(st)
	} else if len(args) > 0 {
		store, _, storeErr := newSessionStore()
		if storeErr != nil {
			return storeErr
		}
		name := args[0]
		if !strings.HasSuffix(name, ".jsonl") {
			name += ".jsonl"
		}
		st, readErr := store.ReadSession(name)
		if readErr != nil {
			if errors.Is(readErr, session.ErrSessionNotFound) {
				// try ledger before giving up
				st = tryReadFromLedger(name)
				if st == nil {
					return fmt.Errorf("session %q not found\nRun 'ox session list' to see available sessions", args[0])
				}
			} else {
				return fmt.Errorf("read session %q: %w", args[0], readErr)
			}
		}
		t = convertStoredSession(st)
	} else {
		// no input - show hint
		out := cmd.OutOrStdout()
		fmt.Fprintln(out)
		fmt.Fprintln(out, sessionEmptyStyle.Render("  No sessions found."))
		fmt.Fprintln(out)
		cli.PrintHint("Start a recording with 'ox agent <id> session start' to capture your development session.")
		return nil
	}

	out := cmd.OutOrStdout()
	// output based on format
	if showRaw {
		return showRawSession(out, t, entryLimit)
	}

	return showFormattedSession(out, t, metadataOnly, entryLimit)
}

// viewAsJSON renders a session as raw JSON output.
func viewAsJSON(w io.Writer, storedSession *session.StoredSession, metadataOnly bool, limit int) error {
	t := convertStoredSession(storedSession)
	if metadataOnly {
		metaOnly := &sessionShowData{
			Info:     t.Info,
			Metadata: t.Metadata,
			Footer:   t.Footer,
		}
		return showRawSession(w, metaOnly, 0)
	}
	return showRawSession(w, t, limit)
}

// convertStoredSession converts a session.StoredSession to sessionShowData.
func convertStoredSession(st *session.StoredSession) *sessionShowData {
	t := &sessionShowData{
		Info:    st.Info,
		Entries: st.Entries,
		Footer:  st.Footer,
	}

	if st.Meta != nil {
		t.Metadata = &sessionMetadata{
			Version:   st.Meta.Version,
			AgentID:   st.Meta.AgentID,
			AgentType: st.Meta.AgentType,
			Username:  st.Meta.Username,
			RepoID:    st.Meta.RepoID,
			CreatedAt: st.Meta.CreatedAt,
		}
	}

	return t
}

func showRawSession(w io.Writer, t *sessionShowData, limit int) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")

	// if showing with limit, only include limited entries
	if limit > 0 && len(t.Entries) > limit {
		t.Entries = t.Entries[:limit]
	}

	return encoder.Encode(t)
}

func showFormattedSession(w io.Writer, t *sessionShowData, metadataOnly bool, limit int) error {
	fmt.Fprintln(w)

	// header
	fmt.Fprintln(w, showTitleStyle.Render("Session"))
	fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 60)))
	fmt.Fprintln(w)

	// file info
	printShowField(w, "Filename", t.Info.Filename)
	printShowField(w, "Type", t.Info.Type)
	printShowField(w, "Size", formatSize(t.Info.Size))
	printShowField(w, "Created", t.Info.CreatedAt.Format("2006-01-02 15:04:05"))
	printShowField(w, "Modified", t.Info.ModTime.Format("2006-01-02 15:04:05"))

	// metadata section
	if t.Metadata != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, showSectionStyle.Render("Metadata"))
		fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 40)))

		if t.Metadata.Version != "" {
			printShowField(w, "Version", t.Metadata.Version)
		}
		if t.Metadata.AgentID != "" {
			printShowField(w, "Agent ID", t.Metadata.AgentID)
		}
		if t.Metadata.AgentType != "" {
			printShowField(w, "Agent Type", t.Metadata.AgentType)
		}
		if t.Metadata.Username != "" {
			printShowField(w, "Username", t.Metadata.Username)
		}
		if t.Metadata.RepoID != "" {
			printShowField(w, "Repo ID", t.Metadata.RepoID)
		}
		if !t.Metadata.CreatedAt.IsZero() {
			printShowField(w, "Started", t.Metadata.CreatedAt.Format("2006-01-02 15:04:05"))
		}
	}

	// footer info
	if t.Footer != nil {
		if closedAt, ok := t.Footer["closed_at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, closedAt); err == nil {
				printShowField(w, "Closed", parsed.Format("2006-01-02 15:04:05"))
			}
		}
		if entryCount, ok := t.Footer["entry_count"].(float64); ok {
			printShowField(w, "Entries", fmt.Sprintf("%d", int(entryCount)))
		}
	}

	// stop here if metadata only
	if metadataOnly {
		fmt.Fprintln(w)
		return nil
	}

	// entries section
	if len(t.Entries) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, showSectionStyle.Render("Entries"))
		fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 40)))

		entries := t.Entries
		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
			fmt.Fprintln(w, cli.StyleDim.Render(fmt.Sprintf("  (showing first %d of %d entries)", limit, len(t.Entries))))
			fmt.Fprintln(w)
		}

		for i, entry := range entries {
			printSessionEntry(w, i+1, entry)
		}

		if limit > 0 && len(t.Entries) > limit {
			fmt.Fprintln(w)
			fmt.Fprintln(w, cli.StyleDim.Render(fmt.Sprintf("  ... %d more entries (use --limit 0 to show all)", len(t.Entries)-limit)))
		}
	} else {
		fmt.Fprintln(w)
		fmt.Fprintln(w, cli.StyleDim.Render("  No entries recorded."))
	}

	fmt.Fprintln(w)
	return nil
}

func printShowField(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %s %s\n",
		showLabelStyle.Render(label+":"),
		showValueStyle.Render(value))
}

func printSessionEntry(w io.Writer, seq int, entry map[string]any) {
	// get entry type
	entryType, _ := entry["type"].(string)
	if entryType == "" {
		entryType = "unknown"
	}

	// get timestamp
	var timestamp string
	if ts, ok := entry["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			timestamp = parsed.Format("15:04:05")
		} else {
			timestamp = ts
		}
	}

	// sequence/type header
	typeLabel := showEntryTypeStyle.Render(fmt.Sprintf("[%d] %s", seq, entryType))
	if timestamp != "" {
		typeLabel += " " + showEntryTimestampStyle.Render(timestamp)
	}
	fmt.Fprintln(w, "  "+typeLabel)

	// entry data based on type
	switch entryType {
	case "message":
		printMessageEntry(w, entry)
	case "tool_call":
		printToolCallEntry(w, entry)
	case "tool_result":
		printToolResultEntry(w, entry)
	default:
		printGenericEntry(w, entry)
	}

	fmt.Fprintln(w)
}

func printMessageEntry(w io.Writer, entry map[string]any) {
	data, ok := entry["data"].(map[string]any)
	if !ok {
		return
	}

	role, _ := data["role"].(string)
	content, _ := data["content"].(string)

	if role != "" {
		fmt.Fprintf(w, "    %s: ", showHighlightStyle.Render(role))
	} else {
		fmt.Fprint(w, "    ")
	}

	// truncate long content
	if len(content) > 200 {
		content = content[:197] + "..."
	}

	// indent multiline content
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if i == 0 {
			fmt.Fprintln(w, showEntryContentStyle.Render(line))
		} else if i < 5 {
			fmt.Fprintln(w, "    "+showEntryContentStyle.Render(line))
		} else if i == 5 {
			fmt.Fprintln(w, "    "+cli.StyleDim.Render("... (truncated)"))
			break
		}
	}
}

func printToolCallEntry(w io.Writer, entry map[string]any) {
	data, ok := entry["data"].(map[string]any)
	if !ok {
		return
	}

	toolName, _ := data["tool_name"].(string)
	if toolName != "" {
		fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Tool:"), showToolStyle.Render(toolName))
	}

	// show input preview
	if input, ok := data["input"].(string); ok && input != "" {
		preview := input
		if len(preview) > 100 {
			preview = preview[:97] + "..."
		}
		fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Input:"), cli.StyleDim.Render(preview))
	}
}

func printToolResultEntry(w io.Writer, entry map[string]any) {
	data, ok := entry["data"].(map[string]any)
	if !ok {
		return
	}

	toolName, _ := data["tool_name"].(string)
	if toolName != "" {
		fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Tool:"), showToolStyle.Render(toolName))
	}

	// show success/error status
	if success, ok := data["success"].(bool); ok {
		if success {
			fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Status:"), cli.StyleSuccess.Render("success"))
		} else {
			fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Status:"), cli.StyleError.Render("failed"))
		}
	}

	// show output preview
	if output, ok := data["output"].(string); ok && output != "" {
		preview := output
		if len(preview) > 100 {
			preview = preview[:97] + "..."
		}
		fmt.Fprintf(w, "    %s %s\n", cli.StyleDim.Render("Output:"), cli.StyleDim.Render(preview))
	}
}

func printGenericEntry(w io.Writer, entry map[string]any) {
	// show data as compact JSON
	if data, ok := entry["data"]; ok {
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			return
		}

		jsonStr := string(jsonBytes)
		if len(jsonStr) > 150 {
			jsonStr = jsonStr[:147] + "..."
		}

		fmt.Fprintf(w, "    %s\n", cli.StyleDim.Render(jsonStr))
	}
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
