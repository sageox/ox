package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
)

// lipgloss styles for murmur list
var (
	murmurHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	murmurTimeStyle = lipgloss.NewStyle().
			Foreground(cli.ColorInfo)

	murmurAgentStyle = lipgloss.NewStyle().
				Foreground(cli.ColorAccent)

	murmurTopicStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	murmurContentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#CCCCCC"))

	murmurDimStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	murmurCriticalStyle = lipgloss.NewStyle().
				Foreground(cli.ColorError).
				Bold(true)
)

var murmurListCmd = &cobra.Command{
	Use:   "list [topic]",
	Short: "List recent murmurs from coworkers",
	Long: `List murmurs published by AI coworkers and teammates on this repo.

Shows timestamp, coworker, topic, and content for each murmur.
Useful for seeing what your team is working on right now.

Examples:
  ox murmur list                    # last 10 murmurs (default 12h window)
  ox murmur list wip                # filter by topic
  ox murmur list --last=20          # last 20 murmurs
  ox murmur list --since=2h         # murmurs from the last 2 hours
  ox murmur list --topic=conflict   # filter by topic (flag form)
  ox murmur list --json             # JSON output for scripting`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMurmurList,
}

func init() {
	murmurCmd.AddCommand(murmurListCmd)
	murmurListCmd.Flags().Int("last", 10, "number of murmurs to show (0 for no limit)")
	murmurListCmd.Flags().String("since", "", "time window: e.g. 30m, 2h, 1d (default: 12h)")
	murmurListCmd.Flags().String("topic", "", "filter by topic slug")
	murmurListCmd.Flags().String("scope", "", "filter by scope: ledger or team")
	murmurListCmd.Flags().String("agent-id", "", "filter by coworker ID")
}

// murmurListOutput is the JSON output format.
type murmurListOutput struct {
	Murmurs     []murmurListEntry    `json:"murmurs"`
	Total       int                  `json:"total"`
	Window      string               `json:"window"`
	Scope       string               `json:"scope,omitempty"`
	Diagnostics *murmurListDiagnostic `json:"diagnostics,omitempty"`
}

// murmurListDiagnostic surfaces sync/auth health when no murmurs are found.
type murmurListDiagnostic struct {
	Authenticated bool   `json:"authenticated"`
	LedgerExists  bool   `json:"ledger_exists"`
	UnpushedCount int    `json:"unpushed_count"`
	Hint          string `json:"hint,omitempty"`
}

// murmurListEntry is a single murmur in JSON output.
type murmurListEntry struct {
	ID          string `json:"id"`
	Timestamp   string `json:"timestamp"`
	AgentID     string `json:"agent_id,omitempty"`
	PrincipalID string `json:"principal_id,omitempty"`
	Topic       string `json:"topic"`
	Importance  string `json:"importance"`
	Content     string `json:"content"`
	Scope       string `json:"scope,omitempty"`
}

func runMurmurList(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	last, _ := cmd.Flags().GetInt("last")
	sinceStr, _ := cmd.Flags().GetString("since")
	topicFilter, _ := cmd.Flags().GetString("topic")
	scopeFilter, _ := cmd.Flags().GetString("scope")
	agentFilter, _ := cmd.Flags().GetString("agent-id")

	// positional arg overrides --topic when flag not explicitly set
	if len(args) > 0 && !cmd.Flags().Changed("topic") {
		topicFilter = args[0]
	}
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	// parse --since into hours for ReadMurmursInWindow (capped at 24h)
	windowHours := ledger.DefaultMurmurWindowHours // 12h default
	windowLabel := "12h"
	if sinceStr != "" {
		d, parseErr := parseDuration(sinceStr)
		if parseErr != nil {
			return fmt.Errorf("invalid --since value %q: %w\nExamples: 30m, 2h, 1d", sinceStr, parseErr)
		}
		hours := int(d.Hours())
		if hours < 1 {
			hours = 1 // minimum 1 hour granularity for directory scanning
		}
		if hours > ledger.MaxMurmurWindowHours {
			hours = ledger.MaxMurmurWindowHours
			windowLabel = "24h"
		} else {
			windowLabel = sinceStr
		}
		windowHours = hours
	}

	// collect murmurs from ledger and optionally team context
	var allMurmurs []ledger.MurmurFile

	// ledger murmurs
	if scopeFilter == "" || scopeFilter == "ledger" {
		ledgerPath := getLedgerPath()
		if ledgerPath != "" {
			murmurs, readErr := ledger.ReadMurmursInWindow(ledgerPath, windowHours)
			if readErr == nil {
				allMurmurs = append(allMurmurs, murmurs...)
			}
		}
	}

	// team context murmurs
	if scopeFilter == "" || scopeFilter == "team" {
		tc := config.FindRepoTeamContext(projectRoot)
		if tc != nil {
			murmurs, readErr := ledger.ReadMurmursInWindow(tc.Path, windowHours)
			if readErr == nil {
				allMurmurs = append(allMurmurs, murmurs...)
			}
		}
	}

	// apply --since filter precisely (directory scanning is hourly granularity)
	if sinceStr != "" {
		d, _ := parseDuration(sinceStr)
		cutoff := time.Now().UTC().Add(-d)
		filtered := allMurmurs[:0]
		for _, m := range allMurmurs {
			if !m.Timestamp.Before(cutoff) {
				filtered = append(filtered, m)
			}
		}
		allMurmurs = filtered
	}

	// apply filters
	if topicFilter != "" {
		filtered := allMurmurs[:0]
		for _, m := range allMurmurs {
			if m.Topic == topicFilter {
				filtered = append(filtered, m)
			}
		}
		allMurmurs = filtered
	}
	if agentFilter != "" {
		filtered := allMurmurs[:0]
		for _, m := range allMurmurs {
			if m.AgentID == agentFilter {
				filtered = append(filtered, m)
			}
		}
		allMurmurs = filtered
	}

	// sort newest first
	sort.Slice(allMurmurs, func(i, j int) bool {
		return allMurmurs[i].Timestamp.After(allMurmurs[j].Timestamp)
	})

	total := len(allMurmurs)

	// apply --last limit
	if last > 0 && len(allMurmurs) > last {
		allMurmurs = allMurmurs[:last]
	}

	// collect diagnostics when empty (check sync/auth health)
	var diag *murmurListDiagnostic
	if total == 0 {
		diag = collectMurmurDiagnostics(projectRoot)
	}

	// JSON output
	if jsonOutput {
		entries := make([]murmurListEntry, 0, len(allMurmurs))
		for _, m := range allMurmurs {
			entries = append(entries, murmurListEntry{
				ID:          m.ID,
				Timestamp:   m.Timestamp.Format(time.RFC3339),
				AgentID:     m.AgentID,
				PrincipalID: m.PrincipalID,
				Topic:       m.Topic,
				Importance:  m.Importance,
				Content:     m.Content,
				Scope:       m.Scope,
			})
		}
		return outputJSON(murmurListOutput{
			Murmurs:     entries,
			Total:       total,
			Window:      windowLabel,
			Scope:       scopeFilter,
			Diagnostics: diag,
		})
	}

	// empty case
	if len(allMurmurs) == 0 {
		fmt.Println()
		fmt.Println(murmurDimStyle.Render("  No murmurs found in the last " + windowLabel + "."))
		if diag != nil {
			printMurmurDiagnostics(diag)
		}
		fmt.Println()
		cli.PrintHint("Murmurs are short-lived coordination signals from AI coworkers.")
		cli.PrintHint("Publish one with: ox murmur --topic=wip \"what you're doing\"")
		return nil
	}

	// table output
	fmt.Println()
	printMurmurTableHeader()

	for _, m := range allMurmurs {
		printMurmurRow(m)
	}

	fmt.Println()
	shown := len(allMurmurs)
	summary := fmt.Sprintf("%d murmur(s) shown", shown)
	if total > shown {
		summary += fmt.Sprintf(" of %d total", total)
	}
	summary += fmt.Sprintf(" (window: %s)", windowLabel)
	fmt.Printf("%s %s\n", cli.StyleDim.Render("Total:"), cli.StyleDim.Render(summary))

	return nil
}

func printMurmurTableHeader() {
	timeCol := fmt.Sprintf("%-14s", "TIME")
	userCol := fmt.Sprintf("%-12s", "USER")
	agentCol := fmt.Sprintf("%-10s", "COWORKER")
	topicCol := fmt.Sprintf("%-18s", "TOPIC")
	contentCol := "CONTENT"

	header := murmurHeaderStyle.Render(timeCol + userCol + agentCol + topicCol + contentCol)
	fmt.Println("  " + header)
	fmt.Println("  " + cli.StyleDim.Render(strings.Repeat("-", 120)))
}

func printMurmurRow(m ledger.MurmurFile) {
	// time: show relative if within 24h, otherwise date+time
	var timeStr string
	age := time.Since(m.Timestamp)
	switch {
	case age < time.Minute:
		timeStr = "just now"
	case age < time.Hour:
		timeStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		timeStr = fmt.Sprintf("%dh%dm ago", int(age.Hours()), int(age.Minutes())%60)
	default:
		timeStr = m.Timestamp.Local().Format("Jan 02 15:04")
	}

	// user: extract short name from principal ID (email → local part)
	userStr := m.PrincipalID
	if userStr == "" {
		userStr = "-"
	}
	if idx := strings.IndexByte(userStr, '@'); idx > 0 {
		userStr = userStr[:idx]
	}
	if len(userStr) > 10 {
		userStr = userStr[:10]
	}

	// agent: truncate if needed
	agentStr := m.AgentID
	if agentStr == "" {
		agentStr = "-"
	}
	if len(agentStr) > 8 {
		agentStr = agentStr[:8]
	}

	// topic
	topicStr := m.Topic
	if len(topicStr) > 16 {
		topicStr = topicStr[:13] + "..."
	}

	// content: first line, no hard truncation — let terminal wrap
	content := m.Content
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		content = content[:idx]
	}

	timeCol := fmt.Sprintf("%-14s", timeStr)
	userCol := fmt.Sprintf("%-12s", userStr)
	agentCol := fmt.Sprintf("%-10s", agentStr)
	topicCol := fmt.Sprintf("%-18s", topicStr)

	row := murmurTimeStyle.Render(timeCol) +
		murmurDimStyle.Render(userCol) +
		murmurAgentStyle.Render(agentCol) +
		murmurTopicStyle.Render(topicCol) +
		murmurContentStyle.Render(content)

	fmt.Println("  " + row)
}

// parseDuration extends time.ParseDuration with support for "d" (days).
func parseDuration(s string) (time.Duration, error) {
	// handle "d" suffix for days
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(numStr, "%d", &days); err != nil {
			return 0, fmt.Errorf("invalid day count %q", numStr)
		}
		if days < 1 {
			return 0, fmt.Errorf("days must be >= 1")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// collectMurmurDiagnostics checks auth, ledger, and push health to explain
// why murmurs may be empty. Only called when zero murmurs are found.
func collectMurmurDiagnostics(projectRoot string) *murmurListDiagnostic {
	diag := &murmurListDiagnostic{}

	// check auth
	ep := endpoint.GetForProject(projectRoot)
	authenticated, _ := auth.IsAuthCredentialValidForEndpoint(ep)
	diag.Authenticated = authenticated

	// check ledger
	ledgerPath := getLedgerPath()
	diag.LedgerExists = ledgerPath != ""

	// check for unpushed murmur commits
	if ledgerPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := gitutil.RunGit(ctx, ledgerPath, "log", "--oneline", "origin/main..HEAD", "--", "data/murmurs/")
		if err == nil && strings.TrimSpace(out) != "" {
			diag.UnpushedCount = len(strings.Split(strings.TrimSpace(out), "\n"))
		}
	}

	// build hint
	switch {
	case !diag.Authenticated:
		diag.Hint = "Not logged in — murmur sync is disabled. Run: ox login"
	case !diag.LedgerExists:
		diag.Hint = "No ledger found — run: ox init"
	case diag.UnpushedCount > 0:
		diag.Hint = fmt.Sprintf("%d unpushed murmur(s) — sync may be failing. Run: ox doctor", diag.UnpushedCount)
	}

	// only return diagnostics if there's something to report
	if diag.Hint == "" {
		return nil
	}
	return diag
}

// murmurWarnStyle renders inline diagnostic warnings on stdout (not stderr).
var murmurWarnStyle = lipgloss.NewStyle().Foreground(cli.ColorWarning)

// printMurmurDiagnostics renders diagnostic warnings for the human-readable empty state.
func printMurmurDiagnostics(diag *murmurListDiagnostic) {
	if diag == nil || diag.Hint == "" {
		return
	}
	fmt.Printf("\n  %s %s\n", murmurWarnStyle.Render("⚠"), murmurWarnStyle.Render(diag.Hint))
}
