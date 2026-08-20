package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationTranscriptCmd is `ox conversation transcript <id>` (L4): a VTT
// slice by cue range or time window, always served from the current
// transcript with an honest pinning status.
var conversationTranscriptCmd = &cobra.Command{
	Use:   "transcript <id>",
	Short: "Read a transcript slice by cue range or time window",
	Long: `A transcript slice of one conversation, selected by an inclusive 1-based
cue range (--cues N-M), a media-clock time window (--from/--to), or the
selectors carried by a sageox:// citation URI passed as <id>. With no
selector, the first 100 cues are served and the window reports truncated.

The requested range is always served from the current transcript; the
envelope reports revision_requested/revision_current and a pinning status
(pinned | unpinned | revision_mismatch) so citation-followers see drift
honestly instead of silent renumbering.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE:          runConversationTranscript,
}

// conversationTranscriptFlags is the transcript flag surface.
type conversationTranscriptFlags struct {
	conversationFormatFlags
	Cues string
	From string
	To   string
	Full bool
}

var conversationTranscriptFlagSet conversationTranscriptFlags

func init() {
	registerConversationTranscriptFlags(conversationTranscriptCmd, &conversationTranscriptFlagSet)
}

func registerConversationTranscriptFlags(cmd *cobra.Command, f *conversationTranscriptFlags) {
	registerConversationFormatFlags(cmd, &f.conversationFormatFlags)
	cmd.Flags().StringVar(&f.Cues, "cues", "", "inclusive 1-based cue range, N-M (or a single cue N)")
	cmd.Flags().StringVar(&f.From, "from", "", "window start on the media clock (hh:mm:ss[.mmm] or a duration like 3m12s)")
	cmd.Flags().StringVar(&f.To, "to", "", "window end on the media clock (same forms as --from)")
	cmd.Flags().BoolVar(&f.Full, "full", false, "serve the entire transcript — intended for humans; agents should request windows (--cues or --from/--to)")
}

func runConversationTranscript(cmd *cobra.Command, args []string) error {
	flags := conversationTranscriptFlagSet
	format, fmtErr := resolveConversationFormat(flags.conversationFormatFlags)
	if fmtErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode, fmtErr.Error())
	}
	if len(args) != 1 {
		return conversationUsageExit(cmd.OutOrStdout(), format, conversationUsageErrorCode,
			"transcript takes exactly one <id> (cnv_<uuidv7>, rec_<uuidv7>, or a sageox:// citation URI)")
	}
	opts, selErr := resolveTranscriptSelectors(flags)
	if selErr != nil {
		return conversationUsageExit(cmd.OutOrStdout(), format, read.ErrCodeInvalidSelector, selErr.Error())
	}

	reader, openErr := openConversationReader()
	if openErr != nil {
		return finishConversationEnvelope(cmd.OutOrStdout(), format, read.ErrorEnvelope(openErr), nil)
	}
	env := reader.Transcript(args[0], opts)
	return finishConversationEnvelope(cmd.OutOrStdout(), format, env, renderConversationTranscriptText)
}

// resolveTranscriptSelectors validates the selector flag combination and
// parses the values into reader options. Structural range checks (reversed
// range, cue 0, cues+window exclusivity) live in the read layer; this only
// rejects what the reader never sees — unparseable spellings, a half-open
// --from/--to pair, and --full combined with a selector.
func resolveTranscriptSelectors(flags conversationTranscriptFlags) (read.TranscriptOptions, error) {
	var opts read.TranscriptOptions
	hasCues := flags.Cues != ""
	hasFrom := flags.From != ""
	hasTo := flags.To != ""

	if flags.Full && (hasCues || hasFrom || hasTo) {
		return opts, fmt.Errorf("--full serves everything; combine it with neither --cues nor --from/--to")
	}
	if hasFrom != hasTo {
		return opts, fmt.Errorf("--from and --to go together; supply both ends of the window")
	}
	opts.Full = flags.Full

	if hasCues {
		first, last, err := parseCueRange(flags.Cues)
		if err != nil {
			return opts, err
		}
		if first < 1 || last < 1 {
			// Rejected here rather than in the read layer: cue 0 is that
			// layer's "unset" sentinel, so it would silently fall through to
			// the default window instead of erroring.
			return opts, fmt.Errorf("cue ordinals are 1-based; 0 is not addressable")
		}
		opts.CueFirst, opts.CueLast = first, last
	}
	if hasFrom {
		from, err := parseMediaOffset(flags.From)
		if err != nil {
			return opts, fmt.Errorf("--from: %w", err)
		}
		to, err := parseMediaOffset(flags.To)
		if err != nil {
			return opts, fmt.Errorf("--to: %w", err)
		}
		opts.FromOffset, opts.ToOffset = from, to
		opts.HasWindow = true
	}
	return opts, nil
}

// parseCueRange parses "N-M" (or a single "N", meaning N-N) into a 1-based
// inclusive pair. Only the spelling is checked here; ordinal validity is the
// read layer's call.
func parseCueRange(raw string) (int, int, error) {
	first, last, isRange := strings.Cut(raw, "-")
	f, err := strconv.Atoi(first)
	if err != nil {
		return 0, 0, fmt.Errorf("--cues must be N or N-M (1-based), got %q", raw)
	}
	if !isRange {
		return f, f, nil
	}
	l, err := strconv.Atoi(last)
	if err != nil {
		return 0, 0, fmt.Errorf("--cues must be N or N-M (1-based), got %q", raw)
	}
	return f, l, nil
}

// parseMediaOffset parses a media-clock offset in either the WebVTT spelling
// ([hh:]mm:ss[.mmm]) or as a Go duration (3m12s). Negative offsets are
// rejected — the media clock starts at zero.
func parseMediaOffset(raw string) (time.Duration, error) {
	if !strings.Contains(raw, ":") {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("%q is not a timestamp (hh:mm:ss[.mmm]) or duration (3m12s)", raw)
		}
		if d < 0 {
			return 0, fmt.Errorf("%q is negative; the media clock starts at zero", raw)
		}
		return d, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("%q is not a timestamp; use [hh:]mm:ss[.mmm]", raw)
	}
	var total time.Duration
	for i, p := range parts {
		if p == "" {
			return 0, fmt.Errorf("%q is not a timestamp; use [hh:]mm:ss[.mmm]", raw)
		}
		if i == len(parts)-1 {
			secs, err := strconv.ParseFloat(p, 64)
			if err != nil || secs < 0 || secs >= 60 {
				return 0, fmt.Errorf("%q has an invalid seconds field", raw)
			}
			total += time.Duration(secs * float64(time.Second))
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (i == len(parts)-2 && n >= 60 && len(parts) == 3) {
			return 0, fmt.Errorf("%q has an invalid field; use [hh:]mm:ss[.mmm]", raw)
		}
		if i == 0 && len(parts) == 3 {
			total += time.Duration(n) * time.Hour
		} else {
			total += time.Duration(n) * time.Minute
		}
	}
	return total, nil
}

// renderConversationTranscriptText prints the pinning header dim, then one
// line per cue: a dim cue locator, an accent speaker id, and the text.
// Cue-number alignment is computed from the served window before styling.
func renderConversationTranscriptText(w io.Writer, env *read.Envelope) {
	data, ok := env.Data.(*read.TranscriptData)
	if !ok || data == nil {
		return
	}
	header := "pinning=" + data.Pinning
	if data.RevisionCurrent > 0 {
		header += fmt.Sprintf(" revision=%d", data.RevisionCurrent)
	}
	if data.RevisionRequested > 0 {
		header += fmt.Sprintf(" requested=%d", data.RevisionRequested)
	}
	fmt.Fprintln(w, cli.StyleDim.Render(header))

	if len(data.Cues) == 0 {
		fmt.Fprintln(w, "(no cues in the requested window)")
		return
	}
	nWidth := len(strconv.Itoa(data.Cues[len(data.Cues)-1].N))
	for _, c := range data.Cues {
		locator := fmt.Sprintf("[%*d] %s", nWidth, c.N, c.Start)
		line := cli.StyleDim.Render(locator)
		if c.Speaker != "" {
			line += "  " + cli.StyleAccent.Render(c.Speaker)
		}
		fmt.Fprintf(w, "%s  %s\n", line, c.Text)
	}
	if data.Window.Truncated {
		fmt.Fprintln(w, cli.StyleDim.Render("(default window; more cues exist — request --cues N-M or --full)"))
	}
	if data.Window.Clamped {
		fmt.Fprintln(w, cli.StyleDim.Render("(requested range clamped to the available cues)"))
	}
}
