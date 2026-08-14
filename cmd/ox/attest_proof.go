package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestProofCmd = &cobra.Command{
	Use:   "proof <capability>",
	Short: "The claim, the break applied, and the verbatim failure it produced",
	Long: `Show the evidence behind one capability.

Every test report answers "did it pass?". This answers "how do you know?" — by
showing the break somebody applied to make the claim false, and the exact failure
that break produced. The capability argument may be a full id or any unambiguous
substring of one.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}

		match, err := resolveCapability(ctx, args[0])
		if err != nil {
			return err
		}
		assessment := attest.Assess(*match, ctx.Plans, ctx.Records)

		// Freshness needs the working tree, which is precisely why this command
		// can only live in a CLI. Skipped when there is no record to age.
		var fresh *attest.Freshness
		if assessment.Record != nil {
			f := attest.CheckFreshness(ctx.RepoRoot, assessment.Record, currentSpecFingerprint(ctx, *match))
			fresh = &f
		}

		payload := struct {
			Assessment attest.Assessment `json:"assessment"`
			Freshness  *attest.Freshness `json:"freshness,omitempty"`
		}{assessment, fresh}

		jsonOut, agentID := wantJSON(cmd)
		return emit(jsonOut, agentID, payload, renderProof(assessment, fresh), "attest proof")
	},
}

// resolveCapability accepts a full id or an unambiguous substring.
//
// An ambiguous query is an ERROR listing the candidates, never a silent pick of
// the first match: showing the wrong capability's proof is worse than showing
// none, because it reads as authoritative.
func resolveCapability(ctx *attestContext, query string) (*attest.Capability, error) {
	var exact *attest.Capability
	var partial []*attest.Capability
	for i := range ctx.Corpus.Capabilities {
		c := &ctx.Corpus.Capabilities[i]
		if c.ID == query {
			exact = c
			break
		}
		if strings.Contains(strings.ToLower(c.ID), strings.ToLower(query)) {
			partial = append(partial, c)
		}
	}
	if exact != nil {
		return exact, nil
	}
	switch len(partial) {
	case 0:
		return nil, fmt.Errorf("no capability matches %q\n  hint: `ox attest status --weakest 0` lists every capability id", query)
	case 1:
		return partial[0], nil
	default:
		ids := make([]string, 0, len(partial))
		for _, c := range partial {
			ids = append(ids, "  "+c.ID)
		}
		sort.Strings(ids)
		if len(ids) > 10 {
			ids = append(ids[:10], fmt.Sprintf("  ... and %d more", len(partial)-10))
		}
		return nil, fmt.Errorf("%q matches %d capabilities:\n%s", query, len(partial), strings.Join(ids, "\n"))
	}
}

// currentSpecFingerprint returns the compiled plan's fingerprint for a
// capability's feature as it stands NOW, for comparison against the value the
// record pinned at mint time.
func currentSpecFingerprint(ctx *attestContext, cap attest.Capability) string {
	plan, ok := ctx.Plans.For(cap.Path)
	if !ok {
		return ""
	}
	return attest.FingerprintDigest(plan.Fingerprint)
}

func renderProof(a attest.Assessment, f *attest.Freshness) string {
	var b strings.Builder
	cap := a.Capability

	fmt.Fprintf(&b, "\n%s  %s\n", renderVerdict(a.Verdict, verdictGlyph(a.Verdict)+" "+string(a.Verdict)), cap.Title())
	fmt.Fprintf(&b, "  %s\n\n", ui.RenderMuted(fmt.Sprintf("%s · %s:%d", cap.ID, cap.Path, cap.Line)))

	if a.Record == nil {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn("no attestation record — nothing here proves this claim can fail"))
		fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(fmt.Sprintf(
			"%d/%d scenarios dispatch · %d stamped", a.Dispatching, len(cap.Scenarios), a.Stamped)))
		if a.Stamped > 0 {
			fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(
				"a stamp is a claim; mint the record with `ox attest record`"))
		}
		fmt.Fprintln(&b)
		return b.String()
	}

	rec := a.Record
	fmt.Fprintf(&b, "  %s\n  %s\n\n", ui.RenderMuted("THE CLAIM"), rec.Claim)

	fmt.Fprintf(&b, "  %s\n  %s\n\n", ui.RenderMuted("THE BREAK APPLIED"), ui.RenderWarn(rec.Proof.Break.Description))

	fmt.Fprintf(&b, "  %s\n", ui.RenderMuted("WHAT FAILED, VERBATIM"))
	for _, line := range strings.Split(strings.TrimRight(rec.Proof.ObservedRed.Verbatim, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", ui.RenderFail(line))
	}
	if rec.Proof.ObservedRed.StepText != "" {
		fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(fmt.Sprintf(
			"step %d — %s", rec.Proof.ObservedRed.StepIndex, rec.Proof.ObservedRed.StepText)))
	}
	// The single most important line on this screen: whether the red landed
	// where the claim lives. Without it, a red anywhere reads as a proof.
	if rec.Proof.ObservedRed.LandedOnClaimStep {
		fmt.Fprintf(&b, "  %s\n\n", ui.RenderPass("landed on the step that names the behavior · clean proof"))
	} else {
		fmt.Fprintf(&b, "  %s\n\n", ui.RenderWarn("landed away from the step that names the behavior · NOT a clean proof"))
	}

	fmt.Fprintf(&b, "  %s\n  red %s · green %s · %s %s\n",
		ui.RenderMuted("RUNS"), rec.Proof.RedRunID, rec.Proof.GreenRunID,
		rec.Subject.Scheme, shortRef(rec.Subject.Value))

	if f != nil {
		fmt.Fprintf(&b, "\n  %s\n  ", ui.RenderMuted("FRESHNESS"))
		if f.Current {
			fmt.Fprintf(&b, "%s\n", ui.RenderPass(f.Summary()))
		} else {
			fmt.Fprintf(&b, "%s\n", ui.RenderWarn(f.Summary()))
		}
		for _, s := range f.ProductDrift {
			fmt.Fprintf(&b, "    %s\n", ui.RenderMuted("moved: "+s))
		}
	}

	if len(rec.Evidence) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", ui.RenderMuted("EVIDENCE"))
		unredacted := 0
		for _, e := range rec.Evidence {
			fmt.Fprintf(&b, "    %s\n", ui.RenderMuted(fmt.Sprintf("%s · %s/%s", e.Kind, e.StoreID, e.Key)))
			if !e.Redacted {
				unredacted++
			}
		}
		// Visible copy, never a quiet flag: the redactor is text-only, so a
		// capture of a signed-in surface still shows the user menu.
		if unredacted > 0 {
			fmt.Fprintf(&b, "    %s\n", ui.RenderWarn(fmt.Sprintf(
				"%d artifact(s) are NOT redacted — treat as internal, not public-safe", unredacted)))
		}
	}

	fmt.Fprintln(&b)
	return b.String()
}

func shortRef(v string) string {
	if len(v) > 8 {
		return v[:8]
	}
	return v
}

func init() {
	attestProofCmd.Flags().Bool("json", false, "structured JSON output for agents")
	addCorpusFlag(attestProofCmd)
	attestCmd.AddCommand(attestProofCmd)
}
