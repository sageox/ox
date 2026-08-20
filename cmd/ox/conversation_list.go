package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationListCmd is `ox conversation list` (L0 of the disclosure
// ladder): browse the active team's conversations from INDEX.json alone.
var conversationListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List the active team's recorded conversations",
	Long:          "Browse the active team's conversations from the local team-context index — newest first, capped at --limit. Each row carries the ids, title, date, participants, and counts an agent needs to decide whether to descend a rung.",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.NoArgs,
	RunE:          runConversationList,
}

// conversationListFlags is the list flag surface. Shared between the parent
// command (bare `ox conversation` runs list) and the list child.
type conversationListFlags struct {
	conversationFormatFlags
	Limit int
	Since string
}

var conversationListFlagSet conversationListFlags

func init() {
	registerConversationListFlags(conversationListCmd, &conversationListFlagSet)
}

// registerConversationListFlags binds the list flags in one place so the
// parent command and tests reuse the exact production surface.
func registerConversationListFlags(cmd *cobra.Command, f *conversationListFlags) {
	registerConversationFormatFlags(cmd, &f.conversationFormatFlags)
	cmd.Flags().IntVar(&f.Limit, "limit", read.DefaultListLimit, "cap the number of conversations returned")
	cmd.Flags().StringVar(&f.Since, "since", "", "only conversations recorded on or after this instant (RFC3339 or YYYY-MM-DD)")
}

// runConversationList is the RunE for `ox conversation list` and for the
// bare `ox conversation` parent.
func runConversationList(cmd *cobra.Command, _ []string) error {
	flags := conversationListFlagSet
	format, fmtErr := resolveConversationFormat(flags.conversationFormatFlags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	since, sinceErr := parseConversationSince(flags.Since)
	if sinceErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, sinceErr.Error())
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.List(read.ListOptions{Limit: flags.Limit, Since: since})
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationListText)
}

// parseConversationSince parses the --since flag: RFC3339 or a bare
// YYYY-MM-DD date (interpreted as UTC midnight). Empty means no filter.
func parseConversationSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("--since must be RFC3339 or YYYY-MM-DD, got %q", raw)
}

// renderConversationListText prints one aligned line per conversation:
// date, conversation id, title (padded raw, then styled), and a dim counts
// column. Alignment is computed from the data before any styling so ANSI
// escapes never skew the columns.
func renderConversationListText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.ListData)
	if !ok || data == nil {
		return
	}
	if len(data.Conversations) == 0 {
		fmt.Fprintln(w, "(no conversations)")
	}
	titleWidth := 0
	for _, c := range data.Conversations {
		if len(c.Title) > titleWidth {
			titleWidth = len(c.Title)
		}
	}
	for _, c := range data.Conversations {
		date := "unknown   "
		if len(c.RecordedAt) >= 10 {
			date = c.RecordedAt[:10]
		}
		extras := fmt.Sprintf("decisions=%d actions=%d", c.DecisionCount, c.ActionItemCount)
		if c.HasDistillation {
			extras += " distilled"
		}
		if len(c.Topics) > 0 {
			extras += " topics=" + strings.Join(c.Topics, ",")
		}
		paddedTitle := fmt.Sprintf("%-*s", titleWidth, c.Title)
		fmt.Fprintf(w, "%s  %s  %s  %s\n",
			date, c.ConversationID, cli.StyleBold.Render(paddedTitle), cli.StyleDim.Render(extras))
	}
	summary := fmt.Sprintf("%d shown · %d indexed", len(data.Conversations), data.TotalIndexed)
	if data.Truncated {
		summary += " · truncated by --limit"
	}
	fmt.Fprintln(w, cli.StyleDim.Render(summary))
}
