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

// conversationSearchCmd is `ox conversation search`: find a conversation by
// what was said, who was there, and when — across the team's whole recorded
// history on disk, not just the recent window list covers.
var conversationSearchCmd = &cobra.Command{
	Use:   "search [keywords]",
	Short: "Find recorded conversations by keyword, person, and date",
	Long: `Search the active team's recorded conversations on disk by keyword,
participant, speaker, and date — for questions like "what did I talk to Ajit
about three weeks ago on search?":

  ox conversation search search files --participant Ajit --since 2026-09-01 --until 2026-09-14

Keywords are matched against each conversation's title, topics, chapters,
decisions, action items, summary, and transcript (word prefixes: "search"
also finds "searching"). Filler words ("what did we talk about") are dropped.
Every keyword must match; when none match them all, conversations matching
some of them are returned with a warning.

Each hit carries a sageox:// citation that ox conversation transcript opens
at the matched cues. Recordings of the same meeting from two devices fold
into one result. For semantic search across all team context (docs,
sessions, plans), use ox query.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runConversationSearch,
}

type conversationSearchFlags struct {
	conversationFormatFlags
	Participants []string
	Speaker      string
	Since        string
	Until        string
	Limit        int
}

var conversationSearchFlagSet conversationSearchFlags

func init() {
	registerConversationSearchFlags(conversationSearchCmd, &conversationSearchFlagSet)
	conversationCmd.AddCommand(conversationSearchCmd)
}

func registerConversationSearchFlags(cmd *cobra.Command, f *conversationSearchFlags) {
	registerConversationFormatFlags(cmd, &f.conversationFormatFlags)
	cmd.Flags().StringArrayVar(&f.Participants, "participant", nil, "only conversations this person was in (name or part of it; repeat to require several)")
	cmd.Flags().StringVar(&f.Speaker, "speaker", "", "match keywords only in what this person said")
	cmd.Flags().StringVar(&f.Since, "since", "", "only conversations recorded on or after this instant (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&f.Until, "until", "", "only conversations recorded before this instant; a YYYY-MM-DD date includes that whole day")
	cmd.Flags().IntVar(&f.Limit, "limit", read.DefaultSearchLimit, fmt.Sprintf("cap the number of results (max %d)", read.MaxSearchLimit))
}

func runConversationSearch(cmd *cobra.Command, args []string) error {
	flags := conversationSearchFlagSet
	format, fmtErr := resolveConversationFormat(flags.conversationFormatFlags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	since, sinceErr := parseConversationSince(flags.Since)
	if sinceErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, sinceErr.Error())
	}
	until, untilErr := parseConversationUntil(flags.Until)
	if untilErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, untilErr.Error())
	}
	if !since.IsZero() && !until.IsZero() && !until.After(since) {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, "--until must be after --since")
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.Search(read.SearchOptions{
		Query:        strings.Join(args, " "),
		Participants: flags.Participants,
		Speaker:      flags.Speaker,
		Since:        since,
		Until:        until,
		Limit:        flags.Limit,
	})
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationSearchText)
}

// parseConversationUntil parses --until. A bare YYYY-MM-DD means "through
// the end of that day" — the way a person says it — so it becomes the next
// UTC midnight, an exclusive bound.
func parseConversationUntil(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().AddDate(0, 0, 1), nil
	}
	return time.Time{}, fmt.Errorf("--until must be RFC3339 or YYYY-MM-DD, got %q", raw)
}

// renderConversationSearchText prints each result as a header line (date,
// id, title, who) followed by its hits, each with the citation to open it.
// Team-context strings are untrusted and sanitized before reaching the
// terminal.
func renderConversationSearchText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.SearchData)
	if !ok || data == nil {
		return
	}
	clean := cli.SanitizeTerminalText
	if len(data.Results) == 0 {
		fmt.Fprintln(w, "(no matching conversations)")
	}
	for i, r := range data.Results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		date := "unknown   "
		if len(r.RecordedAt) >= 10 {
			date = r.RecordedAt[:10]
		}
		fmt.Fprintf(w, "%s  %s  %s\n", date, r.ConversationID, cli.StyleBold.Render(clean(r.Title)))
		who := r.Speakers
		if len(who) == 0 {
			who = r.Participants
		}
		meta := []string{}
		if len(who) > 0 {
			meta = append(meta, clean(strings.Join(who, ", ")))
		}
		if len(r.MatchedIn) > 0 {
			meta = append(meta, "matched in "+strings.Join(r.MatchedIn, ", "))
		}
		if len(r.AlsoRecordedAs) > 0 {
			meta = append(meta, fmt.Sprintf("also recorded as %s", strings.Join(r.AlsoRecordedAs, ", ")))
		}
		if len(meta) > 0 {
			fmt.Fprintln(w, "  "+cli.StyleDim.Render(strings.Join(meta, " · ")))
		}
		for _, h := range r.Hits {
			label := h.Kind
			if h.Start != "" {
				label = h.Start
				if len(label) > 8 {
					label = label[:8] // HH:MM:SS
				}
			}
			if h.Speaker != "" {
				label += " " + clean(h.Speaker)
			}
			fmt.Fprintf(w, "  [%s] %s\n", label, clean(h.Text))
			fmt.Fprintln(w, "    "+cli.StyleDim.Render(h.Citation))
		}
	}
	summary := fmt.Sprintf("%d shown · %d searched · %d summarized on disk", len(data.Results), data.Searched, data.Summarized)
	if len(data.Terms) > 0 {
		summary += " · terms: " + strings.Join(data.Terms, " ")
	}
	if data.Truncated {
		summary += " · truncated by --limit"
	}
	fmt.Fprintln(w, cli.StyleDim.Render(summary))
}
