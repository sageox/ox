package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
)

var murmurStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show murmur delivery status and per-coworker rate limit state",
	Long: `Show the last murmur from each active coworker and whether they are
currently rate-limited or ready to publish.

Reads the current and previous hour from the ledger — no murmurs are sent.

Examples:
  ox murmur status            # table view
  ox murmur status --json     # JSON for scripting`,
	RunE: runMurmurStatus,
}

func init() {
	murmurCmd.AddCommand(murmurStatusCmd)
}

// murmurAgentStatus holds the per-agent state for JSON output.
type murmurAgentStatus struct {
	AgentID      string `json:"agent_id"`
	PrincipalID  string `json:"principal_id,omitempty"`
	LastMurmur   string `json:"last_murmur"`          // RFC3339, or "" if none
	NextEligible string `json:"next_eligible"`         // RFC3339, or "ready"
	Ready        bool   `json:"ready"`
	LastTopic    string `json:"last_topic,omitempty"`
	LastContent  string `json:"last_content,omitempty"`
}

type murmurStatusOutput struct {
	YourAgentID string              `json:"your_agent_id,omitempty"`
	Agents      []murmurAgentStatus `json:"agents"`
	Source      string              `json:"source"`
}

func runMurmurStatus(cmd *cobra.Command, _ []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	yourAgentID := os.Getenv("SAGEOX_AGENT_ID")

	// collect murmurs from ledger (2h window covers current + previous hour boundary)
	var allMurmurs []ledger.MurmurFile
	source := "ledger"

	ledgerPath := getLedgerPath()
	if ledgerPath != "" {
		murmurs, _ := ledger.ReadMurmursInWindow(ledgerPath, 2)
		allMurmurs = append(allMurmurs, murmurs...)
	}

	// also check team context
	tc := config.FindRepoTeamContext(projectRoot)
	if tc != nil {
		murmurs, _ := ledger.ReadMurmursInWindow(tc.Path, 2)
		allMurmurs = append(allMurmurs, murmurs...)
		source = "ledger+team"
	}

	// group by agent_id: keep only the most recent murmur per agent
	type agentEntry struct {
		latest      ledger.MurmurFile
		principalID string
	}
	byAgent := make(map[string]*agentEntry)
	for _, m := range allMurmurs {
		id := m.AgentID
		if id == "" {
			id = "(unknown)"
		}
		e, ok := byAgent[id]
		if !ok || m.Timestamp.After(e.latest.Timestamp) {
			byAgent[id] = &agentEntry{latest: m, principalID: m.PrincipalID}
		}
	}

	// build sorted list: your agent first, then alphabetical
	type row struct {
		agentID     string
		principalID string
		last        time.Time
		lastTopic   string
		lastContent string
	}
	var rows []row
	for id, e := range byAgent {
		rows = append(rows, row{
			agentID:     id,
			principalID: e.principalID,
			last:        e.latest.Timestamp,
			lastTopic:   e.latest.Topic,
			lastContent: e.latest.Content,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].agentID == yourAgentID {
			return true
		}
		if rows[j].agentID == yourAgentID {
			return false
		}
		return rows[i].agentID < rows[j].agentID
	})

	now := time.Now()

	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	if jsonOutput {
		agents := make([]murmurAgentStatus, 0, len(rows))
		for _, r := range rows {
			nextEligible := r.last.Add(murmurRateInterval)
			ready := now.After(nextEligible)
			s := murmurAgentStatus{
				AgentID:     r.agentID,
				PrincipalID: r.principalID,
				Ready:       ready,
				LastTopic:   r.lastTopic,
				LastContent: r.lastContent,
			}
			if !r.last.IsZero() {
				s.LastMurmur = r.last.UTC().Format(time.RFC3339)
			}
			if ready {
				s.NextEligible = "ready"
			} else {
				s.NextEligible = nextEligible.UTC().Format(time.RFC3339)
			}
			agents = append(agents, s)
		}
		return outputJSON(murmurStatusOutput{
			YourAgentID: yourAgentID,
			Agents:      agents,
			Source:      source,
		})
	}

	// --- table output ---
	fmt.Println()

	if len(rows) == 0 {
		fmt.Println(murmurDimStyle.Render("  No murmur activity in the last 2 hours."))
		fmt.Println()
		cli.PrintHint("Publish with: ox murmur --topic=wip \"what you're working on\"")
		return nil
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(cli.ColorPrimary)
	agentCol  := fmt.Sprintf("%-12s", "COWORKER")
	userCol   := fmt.Sprintf("%-12s", "USER")
	lastCol   := fmt.Sprintf("%-14s", "LAST MURMUR")
	statusCol := fmt.Sprintf("%-16s", "STATUS")
	topicCol  := "LAST TOPIC"
	fmt.Println("  " + headerStyle.Render(agentCol+userCol+lastCol+statusCol+topicCol))
	fmt.Println("  " + cli.StyleDim.Render(strings.Repeat("-", 90)))

	for _, r := range rows {
		nextEligible := r.last.Add(murmurRateInterval)
		ready := now.After(nextEligible)

		// time ago
		age := now.Sub(r.last)
		var lastStr string
		switch {
		case r.last.IsZero():
			lastStr = "never"
		case age < time.Minute:
			lastStr = "just now"
		case age < time.Hour:
			lastStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
		default:
			lastStr = fmt.Sprintf("%dh%dm ago", int(age.Hours()), int(age.Minutes())%60)
		}

		// status
		var statusStr string
		var statusStyle lipgloss.Style
		if ready {
			statusStr = "ready"
			statusStyle = lipgloss.NewStyle().Foreground(cli.ColorSuccess)
		} else {
			remaining := time.Until(nextEligible).Truncate(time.Millisecond)
			statusStr = fmt.Sprintf("ready in %s", remaining)
			statusStyle = lipgloss.NewStyle().Foreground(cli.ColorWarning)
		}

		// user: email → local part
		userStr := r.principalID
		if userStr == "" {
			userStr = "-"
		}
		if idx := strings.IndexByte(userStr, '@'); idx > 0 {
			userStr = userStr[:idx]
		}
		if len(userStr) > 10 {
			userStr = userStr[:10]
		}

		// topic
		topicStr := r.lastTopic
		if len(topicStr) > 16 {
			topicStr = topicStr[:13] + "..."
		}

		isYours := r.agentID == yourAgentID
		agentStr := r.agentID
		if len(agentStr) > 10 {
			agentStr = agentStr[:10]
		}

		agentStyle := murmurAgentStyle
		if isYours {
			agentStyle = lipgloss.NewStyle().Foreground(cli.ColorAccent).Bold(true)
			agentStr += " ★"
		}

		row := agentStyle.Render(fmt.Sprintf("%-12s", agentStr)) +
			murmurDimStyle.Render(fmt.Sprintf("%-12s", userStr)) +
			murmurTimeStyle.Render(fmt.Sprintf("%-14s", lastStr)) +
			statusStyle.Render(fmt.Sprintf("%-16s", statusStr)) +
			murmurTopicStyle.Render(topicStr)

		fmt.Println("  " + row)
	}

	fmt.Println()
	hint := fmt.Sprintf("Rate limit: 1 murmur per %s per coworker  ·  Source: %s", murmurRateInterval, source)
	fmt.Println("  " + cli.StyleDim.Render(hint))
	if yourAgentID == "" {
		fmt.Println()
		cli.PrintHint("Set SAGEOX_AGENT_ID to highlight your agent with ★")
	}
	fmt.Println()
	return nil
}
