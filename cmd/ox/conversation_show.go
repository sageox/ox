package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/internal/weblink"
	"github.com/spf13/cobra"
)

// conversationShowCmd is `ox conversation show <id>` (L1): metadata plus the
// human summary, nothing else — decisions and atoms live one rung down.
var conversationShowCmd = &cobra.Command{
	Use:           "show <id>",
	Short:         "Show one conversation's metadata and human summary",
	Long:          "Metadata plus the human summary of one conversation. A conversation without a summary yet is data, not an error: the summary block reports available=false with a typed reason. Accepts cnv_<uuidv7>, rec_<uuidv7>, a sageox:// citation URI, or a pasted sageox.ai recording link (…/c/rec_…, …/recordings/rec_…).",
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
			"show takes exactly one <id> (cnv_<uuidv7>, rec_<uuidv7>, a sageox:// citation URI, or a sageox.ai recording link)")
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	idArg, shareErr := resolveConversationIDArg(conversationContext(cmd), args[0])
	if shareErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(shareErr), nil)
	}
	env := reader.Show(idArg)
	traceConversationEnvelope(env)
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

// traceConversationEnvelope records the conversation(s) a successful show or
// search put in front of the AI coworker as surfaced items in its session
// trace. Best-effort and silent: no agent ID or no live recording, no trace.
func traceConversationEnvelope(env *read.Envelope) {
	if env == nil || env.Error != nil {
		return
	}
	traceConversationHits(func(urlFor func(id string) string) []providedHit {
		return conversationProvidedHits(env, urlFor)
	})
}

// traceConversationRead records the one conversation a successful transcript,
// topics, topic or walkthrough read put in front of the AI coworker, so a
// session that read it can be credited with using it. Ref and title only —
// never the transcript, topic or summary text the read printed (ADR-027). The
// title comes from the walkthrough payload, else the reader's title lookup.
func traceConversationRead(env *read.Envelope, reader *read.Reader, idArg string) {
	if env == nil || env.Error != nil || !env.Success {
		return
	}
	traceConversationHits(func(urlFor func(id string) string) []providedHit {
		id, err := read.ParseID(idArg)
		if err != nil {
			return nil
		}
		var title string
		if d, ok := env.Data.(*read.WalkthroughData); ok && d != nil {
			title = d.Title
		} else if reader != nil {
			title, _ = reader.ConversationTitle(idArg)
		}
		return []providedHit{conversationHit(id.ConversationID, id.RecordingID, title, urlFor)}
	})
}

// traceConversationHits writes the hits build returns into the AI coworker's
// live session trace, linking each to its /c/ page. build runs only when there
// is an agent to credit.
func traceConversationHits(build func(urlFor func(id string) string) []providedHit) {
	agentID := traceAgentIDFromEnv()
	if agentID == "" {
		return
	}
	projectRoot, err := findProjectRoot()
	if err != nil {
		return
	}
	cfg, _ := config.LoadProjectConfig(projectRoot)
	ep := prResolveEndpoint(cfg)
	traceProvidedHits(projectRoot, agentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval,
		build(func(id string) string { return discussionURL(ep, id) }))
}

// conversationProvidedHits lists the conversations an envelope surfaced (see
// conversationHit). Never the summary, hits, or transcript text.
func conversationProvidedHits(env *read.Envelope, urlFor func(id string) string) []providedHit {
	switch d := env.Data.(type) {
	case *read.ShowData:
		if d != nil {
			return []providedHit{conversationHit(d.ConversationID, d.RecordingID, d.Title, urlFor)}
		}
	case *read.SearchData:
		if d == nil {
			return nil
		}
		var hits []providedHit
		for _, r := range d.Results {
			if len(hits) >= maxProvidedHitsPerCall {
				break
			}
			hits = append(hits, conversationHit(r.ConversationID, r.RecordingID, r.Title, urlFor))
		}
		return hits
	}
	return nil
}

// conversationHit is one surfaced conversation: ref = its canonical cnv_ id
// (the recording id when there is no conversation id and it does not parse),
// ref_type "discussion", its rec_ twin as an alias so a use recorded under
// either name attaches, the title, and the /c/ link.
func conversationHit(cnvID, recID, title string, urlFor func(id string) string) providedHit {
	ref := cnvID
	if ref == "" {
		ref = recID
	}
	if id, err := read.ParseID(ref); err == nil {
		ref, recID = id.ConversationID, id.RecordingID
	}
	var aliases []string
	if recID != "" && recID != ref {
		aliases = []string{recID}
	}
	return providedHit{SurfacedRef: contextuse.SurfacedRef{Ref: ref, RefType: "discussion", Title: title, URL: urlFor(ref), Aliases: aliases}}
}

// discussionURL is a discussion's universal /c/ link — the route `ox pr
// header` uses — for a cnv_ or rec_ id; "" for anything else.
func discussionURL(ep, id string) string {
	switch weblink.IDTypePrefix(id) {
	case "cnv_", "rec_":
		return weblink.ArtifactURL(ep, id, "/c/", weblink.IDTypePrefix(id))
	}
	return ""
}
