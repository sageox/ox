package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// conversationCmd is the `ox conversation` command group (alias `conv`):
// local-first, read-only access to the active team's recorded discussions —
// summaries, transcript slices, and distillation topics — straight from the
// team-context checkout on disk. With no subcommand, it behaves like
// `ox conversation list` for discoverability (mirroring `ox distill history`).
//
// Every subcommand is a thin shell over internal/conversation/read: the read
// package owns team resolution, INDEX.json lookup, path guarding, disclosure
// windows, and envelope assembly. Nothing in cmd/ decides policy.
var conversationCmd = &cobra.Command{
	Use:     "conversation",
	Aliases: []string{"conv"},
	Short:   "Read recorded team conversations from the local Team Context",
	Long: `Read-only commands for browsing recorded team conversations locally:
summaries, transcript slices, and distillation topics, served from the
team-context checkout the daemon keeps synced. Works fully logged out.

Commands disclose progressively: list -> show -> topics -> topic -> transcript.
Each JSON envelope's guidance field names the next step, and token_estimate
reports what reading the payload costs. With no subcommand, behaves like
` + "`ox conversation list`" + `.

Accepted ids: cnv_<uuidv7>, rec_<uuidv7>, or a full sageox:// citation URI
copied from a distillation atom.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.NoArgs,
	RunE:          runConversationList,
}

func init() {
	// The parent defaults to list via RunE, so it binds the same flags list
	// binds — the shared conversationListFlagSet means one Go variable
	// carries the parsed state regardless of which command cobra dispatched
	// (single-invocation process, so shared state is safe; tests reset it).
	registerConversationListFlags(conversationCmd, &conversationListFlagSet)
	conversationCmd.AddCommand(conversationListCmd)
	conversationCmd.AddCommand(conversationShowCmd)
	conversationCmd.AddCommand(conversationTranscriptCmd)
	conversationCmd.AddCommand(conversationTopicsCmd)
	conversationCmd.AddCommand(conversationTopicCmd)
	rootCmd.AddCommand(conversationCmd)
}

// conversationFormatFlags is the output-mode surface every conversation
// command shares: --format json|text defaulting to json (the distill-history
// pattern — machine-first), with --text as a plain shorthand.
type conversationFormatFlags struct {
	Format string
	Text   bool
}

// registerConversationFormatFlags binds the shared output-mode flags.
func registerConversationFormatFlags(cmd *cobra.Command, f *conversationFormatFlags) {
	cmd.Flags().StringVar(&f.Format, "format", "json", "output format: json|text")
	cmd.Flags().BoolVar(&f.Text, "text", false, "shorthand for --format text")
}

// resolveConversationFormat validates the output-mode flags and returns the
// effective format. --text wins over --format (it is a shorthand, and the
// most recent human intent).
func resolveConversationFormat(f conversationFormatFlags) (string, error) {
	if f.Format != "json" && f.Format != "text" {
		// Malformed --format: emit the envelope as json so machine parsers
		// still get structured output on the bad-flag path.
		return "json", fmt.Errorf("--format must be json or text, got %q", f.Format)
	}
	if f.Text {
		return "text", nil
	}
	return f.Format, nil
}

// openConversationReader resolves the repo's active team context into a
// Reader. A package-level variable so command-level tests can point it at a
// staged discussions root; the default goes through the canonical helpers
// (findProjectRoot -> config.FindRepoTeamContext inside read.Open).
var openConversationReader = func() (*read.Reader, *read.Error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return nil, &read.Error{
			Code:    read.ErrCodeNoTeamContext,
			Message: fmt.Sprintf("not inside an ox project: %v", err),
		}
	}
	return read.Open(projectRoot)
}

// conversationTextRenderer renders one command's success payload for humans.
// The shared writer handles the error, warning, and guidance framing.
type conversationTextRenderer func(w io.Writer, env *read.Envelope)

// writeConversationEnvelope renders env to w in the requested format. JSON
// is the read package's envelope, marshaled compact with one trailing
// newline. Text delegates the data payload to the per-command renderer and
// frames it with warnings (styled, before the data) and the dim guidance
// hint (after) per the CLI design system.
func writeConversationEnvelope(w io.Writer, format string, env *read.Envelope, render conversationTextRenderer) {
	if format == "text" {
		for _, warning := range env.Warnings {
			fmt.Fprintln(w, cli.StyleWarning.Render("warning: "+warning))
		}
		if env.Error != nil {
			fmt.Fprintf(w, "%s %s: %s\n", cli.StyleError.Render("Error:"), env.Error.Code, env.Error.Message)
			if env.Guidance != "" {
				fmt.Fprintln(w, cli.StyleDim.Render("→ "+env.Guidance))
			}
			return
		}
		if render != nil {
			render(w, env)
		}
		if env.Guidance != "" {
			fmt.Fprintln(w, cli.StyleDim.Render("→ "+env.Guidance))
		}
		return
	}
	b, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(w, `{"success":false,"error":{"code":"envelope_marshal_failed","message":%q,"retryable":false}}`+"\n", err.Error())
		return
	}
	_, _ = w.Write(b)            // best-effort terminal output
	_, _ = w.Write([]byte{'\n'}) // best-effort terminal output
}

// conversationExitCode maps a typed read error to a process exit code:
// usage-shaped codes (a bad id or a structurally invalid selector — the
// caller got the invocation wrong) exit 2, everything else is runtime (1).
func conversationExitCode(e *read.Error) int {
	if e == nil {
		return 0
	}
	switch e.Code {
	case read.ErrCodeInvalidID, read.ErrCodeInvalidSelector:
		return 2
	default:
		return 1
	}
}

// conversationExitError wraps a read error into the typed exit-code error
// main.go already honors (distillHistoryExitError is the one exit-carrying
// error executeWithFrictionRecovery unwraps; reusing it keeps the
// conversation family out of friction recovery and the default error
// printer — the envelope is already on stdout when this returns).
func conversationExitError(e *read.Error) error {
	return &distillHistoryExitError{
		ExitCode: conversationExitCode(e),
		Envelope: distillHistoryEnvelope{
			Success: false,
			Error: &distillHistoryEnvelopeError{
				Code:      e.Code,
				Message:   e.Message,
				Retryable: e.Retryable,
			},
		},
	}
}

// finishConversationEnvelope writes a reader-produced envelope and returns
// the matching exit error (nil on success). Every command's RunE tail path.
func finishConversationEnvelope(w io.Writer, format string, env *read.Envelope, render conversationTextRenderer) error {
	writeConversationEnvelope(w, format, env, render)
	if env.Error != nil {
		return conversationExitError(env.Error)
	}
	return nil
}

// conversationUsageExit writes a usage-error envelope (exit 2) for flag
// failures the command layer itself detects, before any reader call. The
// code is usage_error for malformed flags and invalid_selector for
// structurally bad window selectors, matching the read package's own split.
func conversationUsageExit(w io.Writer, format, code, msg string) error {
	e := &read.Error{Code: code, Message: msg}
	writeConversationEnvelope(w, format, read.ErrorEnvelope(e), nil)
	return &distillHistoryExitError{
		ExitCode: 2,
		Envelope: distillHistoryEnvelope{
			Success: false,
			Error:   &distillHistoryEnvelopeError{Code: code, Message: msg},
		},
	}
}

// conversationUsageErrorCode is the envelope code for malformed flag values
// detected at the command layer (mirrors the distill-history usage code).
const conversationUsageErrorCode = "usage_error"
