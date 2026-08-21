package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationTopicsCmd is `ox conversation topics <id>` (L2): the
// distillation overview — projected episode status plus topic rows. Atom
// bodies are deliberately one rung down (`ox conversation topic`).
var conversationTopicsCmd = &cobra.Command{
	Use:           "topics <id>",
	Short:         "List a conversation's distillation topics",
	Long:          "The distillation overview of one conversation: projected episode status (draft and finalized are served identically) plus one row per topic — id, title, summary, projected-current atom count, and citation URIs. Atom bodies are one rung down: ox conversation topic.",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE:          runConversationTopics,
}

var conversationTopicsFlagSet conversationFormatFlags

func init() {
	registerConversationFormatFlags(conversationTopicsCmd, &conversationTopicsFlagSet)
}

func runConversationTopics(cmd *cobra.Command, args []string) error {
	flags := conversationTopicsFlagSet
	format, fmtErr := resolveConversationFormat(flags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	if len(args) != 1 {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode,
			"topics takes exactly one <id> (cnv_<uuidv7>, rec_<uuidv7>, or a sageox:// citation URI)")
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.Topics(args[0])
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationTopicsText)
}

// renderConversationTopicsText prints the episode header dim, then one
// aligned line per topic: id, right-aligned atom count, bold title, dim
// summary. Counts are padded from the data before styling.
func renderConversationTopicsText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.TopicsData)
	if !ok || data == nil {
		return
	}
	header := "episode=" + data.Episode.Status
	if data.Episode.ExtractedAt != "" {
		header += " extracted=" + data.Episode.ExtractedAt
	}
	if data.Episode.SkippedReason != "" {
		header += " reason=" + data.Episode.SkippedReason
	}
	header += fmt.Sprintf(" atoms=%d superseded=%d", data.AtomsTotal, data.AtomsSuperseded)
	fmt.Fprintln(w, cli.StyleDim.Render(header))

	if len(data.Topics) == 0 {
		fmt.Fprintln(w, "(no topics)")
		return
	}
	countWidth := 0
	for _, tp := range data.Topics {
		if l := len(strconv.Itoa(tp.AtomCount)); l > countWidth {
			countWidth = l
		}
	}
	for _, tp := range data.Topics {
		count := fmt.Sprintf("%*d", countWidth, tp.AtomCount)
		fmt.Fprintf(w, "%s  %s  %s  %s\n",
			tp.ID, cli.StyleDim.Render(count+" atoms"), cli.StyleBold.Render(tp.Title), cli.StyleDim.Render(tp.Summary))
	}
}
