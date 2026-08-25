package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/decision"
	"github.com/spf13/cobra"
)

// decisionCmd is a pure command group (no RunE): bare `ox decision` prints
// help. v1 ships ONE verb — enrich. Decision Records are plain committed
// markdown, so browsing is the filesystem and search is `ox code search`
// (codedb already indexes them); enrich is the only operation that needs
// structure ox can compute.
var decisionCmd = &cobra.Command{
	Use:   "decision",
	Short: "Work with Decision Records (ADRs, DDRs) — enrich with team context",
	Long: `Work with this repo's Decision Records (DRs — ADRs are one type).

  enrich   compute team context for creating or updating a DR (JSON by default)

DRs are committed markdown files (default discovery: docs/adr, docs/decisions,
adr, docs/architecture/decisions; override via the committed .sageox config
'decision.paths'). They are already full-text searchable via 'ox code search'.

Agents: run 'ox decision enrich --topic "<subject>"' BEFORE drafting a new DR,
and 'ox decision enrich --file <dr.md>' before editing an existing one. Zero
LLM/network cost; every citation it emits resolves.`,
}

// decisionEnrichCmd is the agent entry: JSON by default, --text for humans.
var decisionEnrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich a Decision Record (or DR topic) with team context (JSON by default)",
	Long: `Enrich a Decision Record with deterministic team context: related-decision
candidates, corpus conventions (numbering, template, statuses), unresolved-ref
and credit-cap checks, code drift, prior sessions, and ready-to-paste
citations. ox computes everything locally — no LLM or network call — and never
edits the DR; the agent authors every word.

Input modes (precedence order):
  --topic "<subject>"   consult BEFORE drafting a new DR
  --file <dr.md>        an existing DR (adds drift + ref verification)
  stdin                 a draft to verify before presenting

Output is JSON by default (the AI coworker path). Use --text for a human summary.
If any configured source cannot be read, enrich emits a degraded result and
exits non-zero so callers cannot mistake partial retrieval for a verified miss.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		topic, _ := cmd.Flags().GetString("topic")
		file, _ := cmd.Flags().GetString("file")
		text, _ := cmd.Flags().GetBool("text")
		explain, _ := cmd.Flags().GetBool("explain")

		in, err := decision.ResolveInput(topic, file, cmd.InOrStdin())
		if err != nil {
			return err
		}
		if in.Topic == "" && strings.TrimSpace(in.Raw) == "" {
			fmt.Fprintln(cmd.OutOrStdout(),
				`No input. Pass --topic "<subject>" before drafting, --file <dr.md> for an existing DR, or pipe a draft on stdin.`)
			return nil
		}

		// gitRoot is best-effort: detectors are fail-open, so an empty root
		// simply yields fewer signals rather than an error.
		gitRoot := findGitRoot()
		result := decision.Enrich(context.Background(), in, gitRoot, decision.WithExplain(explain))

		if text {
			err = writeDecisionHuman(cmd, result)
		} else {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			err = enc.Encode(result)
		}
		if err != nil {
			return err
		}
		if result.Signals.Degraded {
			return cli.ErrSilent
		}
		return nil
	},
}

// writeDecisionHuman renders the --text summary: signals, top annotations,
// context, guidance. Plain single-column output, 80-col friendly.
func writeDecisionHuman(cmd *cobra.Command, r decision.Result) error {
	out := cmd.OutOrStdout()

	title := r.Decision.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(out, "Decision: %s\n", title)
	if r.Decision.ID != "" {
		fmt.Fprintf(out, "ID:       %s (%s)\n", r.Decision.ID, orDash(r.Decision.Status))
	} else if r.Decision.SuggestedID != "" {
		fmt.Fprintf(out, "Suggested ID: %s\n", r.Decision.SuggestedID)
	}
	fmt.Fprintf(out, "Signals:  related=%d sessions=%d murmurs=%d diagnostics=%d unresolved_refs=%d\n",
		r.Signals.Related, r.Signals.PriorSessions, r.Signals.Murmurs, r.Signals.Diagnostics, r.Signals.UnresolvedRefs)
	if r.Signals.Degraded {
		fmt.Fprintln(out, "          degraded=true (a source could not be read — absence is NOT verified)")
	}

	if len(r.Annotations) > 0 {
		fmt.Fprintln(out, "\nAnnotations:")
		for _, a := range r.Annotations {
			ref := a.Ref
			if ref != "" {
				ref = " [" + ref + "]"
			}
			fmt.Fprintf(out, "  - %s%s: %s\n", a.Type, ref, a.Why)
		}
	}
	if len(r.Context) > 0 {
		fmt.Fprintln(out, "\nContext:")
		for _, c := range r.Context {
			who := c.Author
			if who != "" {
				who = " · " + who
			}
			when := c.When
			if when != "" {
				when = " · " + when
			}
			fmt.Fprintf(out, "  - [%s] %s%s%s\n", c.Kind, c.Title, who, when)
		}
	}
	if len(r.Dropped) > 0 {
		fmt.Fprintln(out, "\nDropped candidates:")
		for _, d := range r.Dropped {
			label := d.Ref
			if label == "" {
				label = d.RefPath
			}
			fmt.Fprintf(out, "  - %s — %s (%.3f)\n", label, d.Title, d.Score)
		}
	}
	if r.Guidance != "" {
		fmt.Fprintf(out, "\nGuidance: %s\n", r.Guidance)
	}
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func init() {
	decisionEnrichCmd.Flags().String("topic", "", "consult mode: the DR subject, before drafting")
	decisionEnrichCmd.Flags().String("file", "", "an existing DR file to enrich (adds drift + ref checks)")
	decisionEnrichCmd.Flags().Bool("text", false, "human summary instead of JSON")
	decisionEnrichCmd.Flags().Bool("explain", false, "also list candidates omitted by result caps or the relevance floor")

	decisionCmd.AddCommand(decisionEnrichCmd)
	decisionCmd.GroupID = "dev"
	rootCmd.AddCommand(decisionCmd)
}
