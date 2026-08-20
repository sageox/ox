package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationShowCmd is `ox conversation show <id>` (L1): metadata plus the
// human summary, nothing else — decisions and atoms live one rung down.
var conversationShowCmd = &cobra.Command{
	Use:           "show <id>",
	Short:         "Show one conversation's metadata and human summary",
	Long:          "Metadata plus the human summary of one conversation. A conversation without a summary yet is data, not an error: the summary block reports available=false with a typed reason. Accepts cnv_<uuidv7>, rec_<uuidv7>, or a sageox:// citation URI.",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE:          runConversationShow,
}

var conversationShowFlagSet conversationFormatFlags

func init() {
	registerConversationFormatFlags(conversationShowCmd, &conversationShowFlagSet)
}

func runConversationShow(cmd *cobra.Command, args []string) error {
	flags := conversationShowFlagSet
	format, fmtErr := resolveConversationFormat(flags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	if len(args) != 1 {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode,
			"show takes exactly one <id> (cnv_<uuidv7>, rec_<uuidv7>, or a sageox:// citation URI)")
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.Show(args[0])
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationShowText)
}

// renderConversationShowText prints the L1 view: bold title, dim metadata
// lines, then the summary body (or its typed absence reason).
func renderConversationShowText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.ShowData)
	if !ok || data == nil {
		return
	}
	fmt.Fprintln(w, cli.StyleBold.Render(data.Title))
	fmt.Fprintln(w, cli.StyleDim.Render("id: "+data.ConversationID))
	if data.RecordedAt != "" {
		fmt.Fprintln(w, cli.StyleDim.Render("recorded: "+data.RecordedAt))
	}
	if len(data.Participants) > 0 {
		fmt.Fprintln(w, cli.StyleDim.Render("participants: "+strings.Join(data.Participants, ", ")))
	}
	fmt.Fprintln(w)
	if data.Summary.Available {
		fmt.Fprintln(w, data.Summary.HumanSummary)
	} else {
		fmt.Fprintln(w, cli.StyleDim.Render("(no summary: "+data.Summary.Reason+")"))
	}
}
