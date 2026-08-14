package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Mint an attestation from a red/green run pair",
	Long: `Write the durable record of a red-first proof.

This is the artifact that cannot be rederived. It captures a failure produced by
a break that no longer exists in the tree, so a field missing at mint time can
only be recovered by re-applying that break at the original commit — often
against an environment that no longer builds. Mint it complete or lose it.

The record lands beside the feature it attests, in this repo, and is committed in
the same change that earns the stamp.

  ox attest record \
    --capability team-management/team-visibility#a-non-member-is-denied... \
    --break "team gate inverted — checkTeamAccess returns true for every caller" \
    --red-run run_01991f3c-8a2e --green-run run_01991f47-b105 \
    --red-verbatim-file /tmp/red.txt --step 4 \
    --step-text "Then Marcus sees Access Required" \
    --landed-on-claim-step \
    --surface app/team/settings/layout.tsx --surface internal/handlers/team/access.go`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}

		capQuery, _ := cmd.Flags().GetString("capability")
		if capQuery == "" {
			return errors.New("--capability is required")
		}
		cap, err := resolveCapability(ctx, capQuery)
		if err != nil {
			return err
		}

		verbatim, err := readVerbatim(cmd)
		if err != nil {
			return err
		}

		landed, _ := cmd.Flags().GetBool("landed-on-claim-step")
		breakDesc, _ := cmd.Flags().GetString("break")
		redRun, _ := cmd.Flags().GetString("red-run")
		greenRun, _ := cmd.Flags().GetString("green-run")
		stepIndex, _ := cmd.Flags().GetInt("step")
		stepText, _ := cmd.Flags().GetString("step-text")
		surfaces, _ := cmd.Flags().GetStringArray("surface")
		verdict, _ := cmd.Flags().GetString("verdict")
		repoKey, _ := cmd.Flags().GetString("repo-key")

		if breakDesc == "" {
			return errors.New("--break is required: a record with no break describes no proof")
		}
		if verdict == "" {
			// Derive rather than default: a red that landed away from the step
			// naming the claim is ambiguous BY CONSTRUCTION, and making the
			// author remember that is how "4 clean + 1 ambiguous" becomes "5".
			verdict = attest.ProofClean
			if !landed {
				verdict = attest.ProofAmbiguous
			}
			if verbatim == "" {
				verdict = attest.ProofInconclusive
			}
		}

		commit, err := attest.HeadCommit(ctx.RepoRoot)
		if err != nil {
			return err
		}
		if repoKey == "" {
			repoKey = defaultRepoKey(ctx.RepoRoot)
		}

		rec := &attest.Attestation{
			Version:       attest.AttestationVersion,
			AttestationID: attest.GenerateAttestationID(),
			CapabilityID:  cap.ID,
			Claim:         cap.Title(),
			RepoKey:       repoKey,
			Subject:       attest.TreeRef{Scheme: attest.SchemeGitCommit, Value: commit},
			MintedAt:      time.Now().UTC().Format(time.RFC3339),
			Proof: attest.Proof{
				Verdict: verdict,
				Break: attest.Break{
					Description:    breakDesc,
					DiffDigest:     diffDigest(cmd),
					TargetSurfaces: surfaces,
				},
				ObservedRed: attest.ObservedRed{
					Verbatim:          verbatim,
					StepIndex:         stepIndex,
					StepText:          stepText,
					LandedOnClaimStep: landed,
				},
				RedRunID:   redRun,
				GreenRunID: greenRun,
			},
			SpecFingerprint: currentSpecFingerprint(ctx, *cap),
			ObservedSurface: attest.ObservedSurface{
				Granularity: "route",
				Surfaces:    toSurfaces(surfaces),
			},
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			if err := rec.Validate(); err != nil {
				return err
			}
			jsonOut, agentID := wantJSON(cmd)
			return emit(jsonOut, agentID, rec, renderRecordPreview(rec, ""), "attest record")
		}

		path, err := attest.WriteRecord(ctx.CorpusRoot, rec)
		if err != nil {
			return err
		}
		jsonOut, agentID := wantJSON(cmd)
		return emit(jsonOut, agentID,
			map[string]any{"path": path, "attestation": rec},
			renderRecordPreview(rec, path), "attest record")
	},
}

// readVerbatim takes the failure text from a file or a flag.
//
// A file is the preferred form and the flag exists only for one-liners: shell
// quoting mangles multi-line assertion output, and this field must be the run's
// exact bytes. Repairing, re-wrapping, or tidying it would be editing evidence.
func readVerbatim(cmd *cobra.Command) (string, error) {
	path, _ := cmd.Flags().GetString("red-verbatim-file")
	inline, _ := cmd.Flags().GetString("red-verbatim")
	if path != "" && inline != "" {
		return "", errors.New("pass --red-verbatim-file or --red-verbatim, not both")
	}
	if path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path is the point
		if err != nil {
			return "", fmt.Errorf("read --red-verbatim-file: %w", err)
		}
		return strings.TrimRight(string(raw), "\n"), nil
	}
	return inline, nil
}

// diffDigest hashes the applied patch so the record can prove a break existed.
// Without it "we broke it and it went red" is an unfalsifiable assertion.
func diffDigest(cmd *cobra.Command) string {
	path, _ := cmd.Flags().GetString("break-diff-file")
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path is the point
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func toSurfaces(ids []string) []attest.Surface {
	if len(ids) == 0 {
		return nil
	}
	out := make([]attest.Surface, 0, len(ids))
	for _, id := range ids {
		out = append(out, attest.Surface{SurfaceID: id})
	}
	return out
}

// defaultRepoKey derives an opaque, project-scoped key free of SageOx
// identifiers, per the constraint that keeps the attest layout portable.
//
// Uses the MAIN repo root, not the current directory: in a git worktree the
// checkout is named for the branch or task ("dili-v2"), so keying on it would
// scatter one repo's published bundles across a new prefix per worktree and
// make cross-branch comparison impossible. Falls back to the working root when
// the main root cannot be resolved.
func defaultRepoKey(repoRoot string) string {
	if main, err := repotools.FindMainRepoRoot(repotools.VCSGit); err == nil && main != "" {
		repoRoot = main
	}
	parts := strings.Split(strings.TrimRight(repoRoot, "/"), "/")
	return parts[len(parts)-1]
}

func renderRecordPreview(rec *attest.Attestation, path string) string {
	var b strings.Builder
	fmt.Fprintln(&b)
	if path == "" {
		fmt.Fprintf(&b, "  %s\n", ui.RenderMuted("dry run — nothing written"))
	} else {
		fmt.Fprintf(&b, "  %s %s\n", ui.RenderPass("wrote"), path)
	}
	fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(rec.CapabilityID))
	fmt.Fprintf(&b, "  claim   %s\n", rec.Claim)
	fmt.Fprintf(&b, "  break   %s\n", ui.RenderWarn(rec.Proof.Break.Description))

	switch rec.Proof.Verdict {
	case attest.ProofClean:
		fmt.Fprintf(&b, "  verdict %s\n", ui.RenderPass("clean — the red landed on the step naming the claim"))
	case attest.ProofAmbiguous:
		fmt.Fprintf(&b, "  verdict %s\n", ui.RenderWarn("ambiguous — the red landed elsewhere; this does NOT prove the claim"))
	default:
		fmt.Fprintf(&b, "  verdict %s\n", ui.RenderFail("inconclusive — the break produced no failure at all"))
	}
	fmt.Fprintf(&b, "  runs    red %s · green %s\n", rec.Proof.RedRunID, rec.Proof.GreenRunID)
	fmt.Fprintf(&b, "  subject %s %s\n", rec.Subject.Scheme, shortRef(rec.Subject.Value))
	if len(rec.ObservedSurface.Surfaces) == 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(
			"no --surface given: freshness can never rule out product drift for this record"))
	}
	fmt.Fprintln(&b)
	return b.String()
}

func init() {
	attestRecordCmd.Flags().String("capability", "", "capability id or an unambiguous substring (required)")
	attestRecordCmd.Flags().String("break", "", "what was broken, in the language of the claim (required)")
	attestRecordCmd.Flags().String("break-diff-file", "", "the applied patch, hashed to prove a break existed")
	attestRecordCmd.Flags().String("red-run", "", "run id of the run that went red")
	attestRecordCmd.Flags().String("green-run", "", "run id of the run that passed with the break removed")
	attestRecordCmd.Flags().String("red-verbatim-file", "", "file holding the failure text, exactly as emitted")
	attestRecordCmd.Flags().String("red-verbatim", "", "failure text inline (prefer --red-verbatim-file)")
	attestRecordCmd.Flags().Int("step", 0, "1-indexed step the failure landed on")
	attestRecordCmd.Flags().String("step-text", "", "text of the step the failure landed on")
	attestRecordCmd.Flags().Bool("landed-on-claim-step", false, "the red landed on the step naming the behavior")
	attestRecordCmd.Flags().StringArray("surface", nil, "a file or route the run exercised (repeatable)")
	attestRecordCmd.Flags().String("verdict", "", "override the derived verdict (clean|ambiguous|inconclusive)")
	attestRecordCmd.Flags().String("repo-key", "", "opaque repo key (default: the repo directory name)")
	attestRecordCmd.Flags().Bool("dry-run", false, "validate and print without writing")
	attestRecordCmd.Flags().Bool("json", false, "structured JSON output for agents")
	addCorpusFlag(attestRecordCmd)
	attestCmd.AddCommand(attestRecordCmd)
}
