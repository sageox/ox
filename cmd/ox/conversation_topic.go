package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationTopicCmd is `ox conversation topic <id> <tp_id>` (L3): one
// topic with all of its atoms. Topics are addressed by the exact tp_<uuidv7>
// copied from `ox conversation topics` output — no title matching.
var conversationTopicCmd = &cobra.Command{
	Use:           "topic <id> <tp_id>",
	Short:         "Show one distillation topic's atoms",
	Long:          "One topic of a conversation's distillation, with all of its atoms — kind, signal, text, quote, source citation URIs, and confidence. Topics are addressed by the exact tp_<uuidv7> copied from ox conversation topics. The default view is projected-current; --include-superseded adds tombstones so succession chains are auditable.",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE:          runConversationTopic,
}

// conversationTopicFlags is the topic flag surface.
type conversationTopicFlags struct {
	conversationFormatFlags
	IncludeSuperseded bool
}

var conversationTopicFlagSet conversationTopicFlags

func init() {
	registerConversationTopicFlags(conversationTopicCmd, &conversationTopicFlagSet)
}

func registerConversationTopicFlags(cmd *cobra.Command, f *conversationTopicFlags) {
	registerConversationFormatFlags(cmd, &f.conversationFormatFlags)
	cmd.Flags().BoolVar(&f.IncludeSuperseded, "include-superseded", false, "also serve superseded atoms as tombstones (valid_from/valid_to/superseded_by)")
}

func runConversationTopic(cmd *cobra.Command, args []string) error {
	flags := conversationTopicFlagSet
	format, fmtErr := resolveConversationFormat(flags.conversationFormatFlags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	if len(args) != 2 {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode,
			"topic takes exactly <id> <tp_id>; copy the tp_ id from ox conversation topics")
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.Topic(args[0], args[1], flags.IncludeSuperseded)
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationTopicText)
}

// renderConversationTopicText prints the topic header, then one block per
// atom: a dim kind/signal/confidence locator line, the atom text, and dim
// quote and citation lines when present. Tombstones carry a warning-styled
// superseded marker.
func renderConversationTopicText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.TopicData)
	if !ok || data == nil {
		return
	}
	fmt.Fprintln(w, cli.StyleBold.Render(data.Topic.Title))
	fmt.Fprintln(w, cli.StyleDim.Render(data.Topic.ID+" · "+data.Topic.Summary))
	fmt.Fprintln(w, cli.StyleDim.Render(fmt.Sprintf("atoms=%d superseded=%d", data.AtomsTotal, data.AtomsSuperseded)))

	if len(data.Atoms) == 0 {
		fmt.Fprintln(w, "(no atoms)")
		return
	}
	for _, a := range data.Atoms {
		fmt.Fprintln(w)
		locator := fmt.Sprintf("%s · %s · %s · confidence %.2f", a.ID, a.Kind, a.Signal, a.Confidence)
		fmt.Fprintln(w, cli.StyleDim.Render(locator))
		if a.ValidTo != "" {
			marker := "superseded " + a.ValidTo
			if a.SupersededBy != "" {
				marker += " by " + a.SupersededBy
			}
			fmt.Fprintln(w, cli.StyleWarning.Render(marker))
		}
		fmt.Fprintln(w, a.Text)
		if a.Quote != nil {
			fmt.Fprintln(w, cli.StyleDim.Render(fmt.Sprintf("cue %d: %q", a.Quote.CueRef, a.Quote.Text)))
		}
		if a.Source != nil && len(a.Source.URIs) > 0 {
			fmt.Fprintln(w, cli.StyleDim.Render("cite: "+strings.Join(a.Source.URIs, " ")))
		}
	}
}
