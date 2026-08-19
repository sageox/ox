package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "What does my working diff invalidate",
	Long: `Report which attestations this working tree invalidates.

Compares every record against the WORKING TREE — uncommitted edits included —
so the answer covers what you are about to push, not what you last pushed. That
is why this command exists in the CLI and nowhere else: a server has no working
tree, so the same question asked over an API could only ever be answered about
the last pushed commit, which is a weaker and different claim.

Advisory. Always exits 0: this reports, it never blocks.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}

		result := attestCheckResult{
			RecordCount:    ctx.Records.Count,
			InvalidRecords: ctx.Records.Invalid,
		}
		knownCapabilities := make(map[string]struct{}, len(ctx.Corpus.Capabilities))
		for i := range ctx.Corpus.Capabilities {
			cap := ctx.Corpus.Capabilities[i]
			knownCapabilities[cap.ID] = struct{}{}
			a := attest.Assess(cap, ctx.Plans, ctx.Records)

			if a.Record == nil {
				// A stamped capability with no record has nothing to invalidate
				// — but it IS in the blast radius, and saying so is the point:
				// it is a claim this diff may have broken with no way to tell.
				if a.Stamped > 0 {
					result.StampedNoRecord = append(result.StampedNoRecord, cap.ID)
				}
				continue
			}

			f := attest.CheckFreshness(ctx.RepoRoot, a.Record, currentSpecFingerprint(ctx, cap))
			entry := attestCheckEntry{
				CapabilityID: cap.ID,
				Claim:        a.Record.Claim,
				Freshness:    f,
			}
			switch {
			case f.Unknown:
				result.Unknown = append(result.Unknown, entry)
			case f.Current:
				result.Unaffected = append(result.Unaffected, entry)
			default:
				result.Invalidated = append(result.Invalidated, entry)
			}
		}
		for _, record := range ctx.Records.All() {
			if _, ok := knownCapabilities[record.CapabilityID]; ok {
				continue
			}
			result.Orphaned = append(result.Orphaned, attestCheckOrphan{
				CapabilityID: record.CapabilityID,
				Claim:        record.Claim,
			})
		}
		sort.Strings(result.StampedNoRecord)

		jsonOut, agentID := wantJSON(cmd)
		return emit(cmd, jsonOut, agentID, result, renderAttestCheck(result), "attest check")
	},
}

type attestCheckEntry struct {
	CapabilityID string           `json:"capability_id"`
	Claim        string           `json:"claim"`
	Freshness    attest.Freshness `json:"freshness"`
}

type attestCheckOrphan struct {
	CapabilityID string `json:"capability_id"`
	Claim        string `json:"claim"`
}

type attestCheckResult struct {
	RecordCount    int                 `json:"record_count"`
	InvalidRecords map[string]string   `json:"invalid_records,omitempty"`
	Orphaned       []attestCheckOrphan `json:"orphaned"`
	Invalidated    []attestCheckEntry  `json:"invalidated"`
	Unaffected     []attestCheckEntry  `json:"unaffected"`
	Unknown        []attestCheckEntry  `json:"unknown"`
	// StampedNoRecord are capabilities somebody claimed green with no record —
	// this diff may have broken them and nothing here can tell.
	StampedNoRecord []string `json:"stamped_no_record"`
}

func renderAttestCheck(r attestCheckResult) string {
	var b strings.Builder
	fmt.Fprintln(&b)

	if r.RecordCount == 0 && len(r.InvalidRecords) == 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderMuted("no attestation records in this repo — nothing to invalidate"))
		if len(r.StampedNoRecord) > 0 {
			fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf(
				"%d stamped capabilities have no record, so this diff's effect on them is unknowable",
				len(r.StampedNoRecord))))
		}
		fmt.Fprintln(&b)
		return b.String()
	}

	if len(r.InvalidRecords) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf("? %d attestation record(s) could not be read", len(r.InvalidRecords))))
		paths := make([]string, 0, len(r.InvalidRecords))
		for path := range r.InvalidRecords {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(&b, "    %s\n", ui.RenderMuted(filepath.Base(path)+" · "+r.InvalidRecords[path]))
		}
		fmt.Fprintln(&b)
	}

	if len(r.Orphaned) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf("? %d attestation record(s) no longer match the capability corpus", len(r.Orphaned))))
		for _, orphan := range r.Orphaned {
			fmt.Fprintf(&b, "    %s\n", orphan.Claim)
			fmt.Fprintf(&b, "      %s\n", ui.RenderMuted(orphan.CapabilityID+" · capability was removed or renamed"))
		}
		fmt.Fprintln(&b)
	}

	if len(r.Invalidated) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf("◑ %d attestation(s) go stale", len(r.Invalidated))))
		for _, e := range r.Invalidated {
			fmt.Fprintf(&b, "    %s\n", e.Claim)
			fmt.Fprintf(&b, "      %s\n", ui.RenderMuted(e.CapabilityID+" · "+e.Freshness.Summary()))
			for _, s := range e.Freshness.ProductDrift {
				fmt.Fprintf(&b, "      %s\n", ui.RenderMuted("moved: "+s))
			}
		}
		fmt.Fprintln(&b)
	}

	if len(r.Unaffected) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderPass(fmt.Sprintf("✓ %d attestation(s) unaffected", len(r.Unaffected))))
	}

	// Unknown is never folded into "unaffected". A freshness question we could
	// not answer must not read as a clean bill of health.
	if len(r.Unknown) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf("? %d attestation(s) could not be checked", len(r.Unknown))))
		for _, e := range r.Unknown {
			fmt.Fprintf(&b, "    %s\n", ui.RenderMuted(e.CapabilityID+" · "+e.Freshness.Reason))
		}
	}

	if len(r.StampedNoRecord) > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(fmt.Sprintf(
			"◌ %d stamped capability(ies) have no record to invalidate", len(r.StampedNoRecord))))
	}

	if len(r.Invalidated) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", ui.RenderMuted("re-prove with: ox attest record --capability <id> ..."))
	}
	fmt.Fprintln(&b)
	return b.String()
}

func init() {
	attestCheckCmd.Flags().Bool("json", false, "structured JSON output for AI coworkers")
	addCorpusFlag(attestCheckCmd)
	attestCmd.AddCommand(attestCheckCmd)
}
