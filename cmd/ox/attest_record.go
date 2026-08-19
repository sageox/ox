package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

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
		surfaces, _, err = normalizeAttestSurfaces(ctx.RepoRoot, surfaces)
		if err != nil {
			return err
		}
		breakDigest, err := diffDigest(cmd)
		if err != nil {
			return err
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
		if err := validateAttestProofInputs(verdict, redRun, greenRun, stepIndex, stepText); err != nil {
			return err
		}

		commit, err := attest.HeadCommit(ctx.RepoRoot)
		if err != nil {
			return err
		}
		if repoKey == "" {
			repoKey = defaultRepoKey(ctx.RepoRoot)
		}
		if err := validateRepoKey(repoKey); err != nil {
			return err
		}
		if err := requireCleanAttestTree(ctx.RepoRoot); err != nil {
			return err
		}
		specFingerprint, err := liveSpecFingerprint(ctx, *cap)
		if err != nil {
			return fmt.Errorf("fingerprint current specification: %w", err)
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
					DiffDigest:     breakDigest,
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
			SpecFingerprint: specFingerprint,
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
			return emit(cmd, jsonOut, agentID, rec, renderRecordPreview(rec, ""), "attest record")
		}

		path, err := attest.WriteRecord(ctx.CorpusRoot, rec)
		if err != nil {
			return err
		}
		jsonOut, agentID := wantJSON(cmd)
		return emit(cmd, jsonOut, agentID,
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
		return string(raw), nil
	}
	return inline, nil
}

// diffDigest hashes the applied patch so the record can prove a break existed.
// Without it "we broke it and it went red" is an unfalsifiable assertion.
func diffDigest(cmd *cobra.Command) (string, error) {
	path, _ := cmd.Flags().GetString("break-diff-file")
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path is the point
	if err != nil {
		return "", fmt.Errorf("read --break-diff-file: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validateAttestRunIDs(redRun, greenRun string) error {
	if redRun == "" {
		return errors.New("--red-run is required: a red-first proof must identify the failing run")
	}
	if strings.TrimSpace(redRun) != redRun {
		return errors.New("--red-run cannot have leading or trailing whitespace")
	}
	if greenRun == "" {
		return errors.New("--green-run is required: a proof must identify the passing run")
	}
	if strings.TrimSpace(greenRun) != greenRun {
		return errors.New("--green-run cannot have leading or trailing whitespace")
	}
	if redRun == greenRun {
		return errors.New("--red-run and --green-run must identify different runs")
	}
	return nil
}

func validateAttestProofInputs(verdict, redRun, greenRun string, stepIndex int, stepText string) error {
	switch verdict {
	case attest.ProofClean, attest.ProofAmbiguous, attest.ProofInconclusive:
	default:
		return fmt.Errorf("--verdict %q is not one of clean, ambiguous, inconclusive", verdict)
	}
	if err := validateAttestRunIDs(redRun, greenRun); err != nil {
		return err
	}
	if verdict == attest.ProofInconclusive {
		return nil
	}
	if stepIndex < 1 {
		return errors.New("--step must be a positive 1-indexed step for a red verdict")
	}
	if strings.TrimSpace(stepText) == "" {
		return errors.New("--step-text is required for a red verdict")
	}
	return nil
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
	return repoKeyFromRoot(repoRoot)
}

func repoKeyFromRoot(repoRoot string) string {
	return filepath.Base(filepath.Clean(repoRoot))
}

// validateRepoKey keeps the opaque key to one portable path segment. The
// publisher uses it in object paths, so separators and traversal components
// are never valid identifiers.
func validateRepoKey(repoKey string) error {
	if repoKey == "" {
		return errors.New("invalid --repo-key: use one non-empty path segment")
	}
	if strings.TrimSpace(repoKey) != repoKey {
		return fmt.Errorf("invalid --repo-key %q: leading or trailing whitespace is not allowed", repoKey)
	}
	if repoKey == "." || repoKey == ".." || filepath.IsAbs(repoKey) {
		return fmt.Errorf("invalid --repo-key %q: use one relative path segment", repoKey)
	}
	if strings.ContainsAny(repoKey, `/\\<>:"|?*`) {
		return fmt.Errorf("invalid --repo-key %q: path separators and filesystem-reserved characters are not allowed", repoKey)
	}
	if strings.TrimRight(repoKey, " .") != repoKey {
		return fmt.Errorf("invalid --repo-key %q: a key cannot end in a space or dot", repoKey)
	}
	for _, r := range repoKey {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid --repo-key %q: control characters are not allowed", repoKey)
		}
	}
	return nil
}

// normalizeAttestSurfaces canonicalizes file surfaces to repo-relative slash
// paths, which is the form emitted by git. Route identifiers stay untouched.
// Prefixes are available when an otherwise ambiguous identifier needs an
// explicit classification: file:<path> or route:<id>.
func normalizeAttestSurfaces(repoRoot string, ids []string) ([]string, []string, error) {
	normalized := make([]string, 0, len(ids))
	files := make([]string, 0, len(ids))
	for _, id := range ids {
		value, isFile, err := normalizeAttestSurface(repoRoot, id)
		if err != nil {
			return nil, nil, err
		}
		normalized = append(normalized, value)
		if isFile {
			files = append(files, value)
		}
	}
	return normalized, files, nil
}

func normalizeAttestSurface(repoRoot, id string) (string, bool, error) {
	if id == "" {
		return "", false, errors.New("--surface cannot be empty")
	}
	if strings.HasPrefix(id, "route:") {
		routeID := strings.TrimPrefix(id, "route:")
		if routeID == "" {
			return "", false, errors.New("--surface route: identifier cannot be empty")
		}
		return routeID, false, nil
	}

	explicitFile := strings.HasPrefix(id, "file:")
	if explicitFile {
		id = strings.TrimPrefix(id, "file:")
		if id == "" {
			return "", false, errors.New("--surface file: path cannot be empty")
		}
	}

	pathID := filepath.FromSlash(strings.ReplaceAll(id, `\`, "/"))
	candidate := pathID
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(repoRoot, candidate)
	}
	_, statErr := os.Stat(candidate)
	looksLikeFile := explicitFile || strings.HasPrefix(id, "./") || strings.HasPrefix(id, "../") || statErr == nil ||
		(!filepath.IsAbs(pathID) && !strings.Contains(id, "://") && filepath.Ext(pathID) != "")
	if !looksLikeFile {
		return id, false, nil
	}

	rel, err := filepath.Rel(repoRoot, filepath.Clean(candidate))
	if err != nil {
		return "", false, fmt.Errorf("normalize --surface %q: %w", id, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, fmt.Errorf("--surface file %q is outside the repository", id)
	}
	return filepath.ToSlash(rel), true, nil
}

// requireCleanAttestTree prevents binding evidence from a dirty green run to
// HEAD. Checking only declared surfaces is insufficient: surfaces are optional,
// and an omitted dirty product file would otherwise produce a record that says
// the clean commit was tested when the working tree was what actually ran.
func requireCleanAttestTree(repoRoot string) error {
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain=v1", "--untracked-files=all") //nolint:gosec // fixed git arguments; repository root is caller-resolved
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check attestation tree against HEAD: %w", err)
	}
	if len(out) != 0 {
		return errors.New("cannot record an attestation from a dirty working tree\n" +
			"  commit or remove every tracked and untracked change, rerun the green proof on that commit, then record it")
	}
	return nil
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
			"no --surface given: freshness remains unknown because product drift cannot be ruled out"))
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
	attestRecordCmd.Flags().StringArray("surface", nil, "a file or route the run exercised; prefix ambiguous values with file: or route: (repeatable)")
	attestRecordCmd.Flags().String("verdict", "", "override the derived verdict (clean|ambiguous|inconclusive)")
	attestRecordCmd.Flags().String("repo-key", "", "opaque repo key (default: the repo directory name)")
	attestRecordCmd.Flags().Bool("dry-run", false, "validate and print without writing")
	attestRecordCmd.Flags().Bool("json", false, "structured JSON output for AI coworkers")
	addCorpusFlag(attestRecordCmd)
	attestCmd.AddCommand(attestRecordCmd)
}
