package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestPublishCmd = &cobra.Command{
	Use:   "publish --to <dir>",
	Short: "Write the attest layout to a directory for hosting",
	Long: `Write this repo's attest bundle in the published layout.

Produces attest/v1/... — the status report, every attestation record, and an
index entry — laid out so a reader can consume it without our code.

Deliberately writes to a DIRECTORY, not to a bucket. ox ships to customers, and
adding a cloud SDK to it for an internal experiment would buy a large dependency
and an AWS-shaped assumption in exchange for a step that is already one line:

  ox attest publish --to .attest-publish
  # upload payloads, then manifest, then index (never use --delete)

That is the same pattern the dev portal already uses to serve its design
catalog, and it keeps the layout portable to any object store.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dest, _ := cmd.Flags().GetString("to")
		if dest == "" {
			return errors.New("--to <dir> is required")
		}
		if strings.HasPrefix(dest, "s3://") {
			return fmt.Errorf("--to takes a directory, not a bucket URL\n" +
				"  write locally then sync:\n" +
				"    ox attest publish --to .attest-publish\n" +
				"    # use an uploader that sends payloads, manifest, then index; never --delete")
		}

		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}
		report := attest.BuildReport(ctx.Corpus, ctx.Plans, ctx.Records)
		runs, err := attest.ReferencedRunResults(ctx.RepoRoot, ctx.Records)
		if err != nil {
			return err
		}

		repoKey, _ := cmd.Flags().GetString("repo-key")
		if repoKey == "" {
			repoKey = defaultRepoKey(ctx.RepoRoot)
		}

		res, err := attest.WriteBundle(dest, repoKey, report, ctx.Records, runs, time.Now())
		if err != nil {
			return err
		}

		jsonOut, agentID := wantJSON(cmd)
		return emit(jsonOut, agentID, res, renderPublish(res, report, repoKey, dest), "attest publish")
	},
}

func renderPublish(res *attest.PublishResult, r *attest.Report, repoKey, dest string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s\n", ui.RenderPass("published"), res.Root)
	fmt.Fprintf(&b, "  %s\n", ui.RenderMuted(fmt.Sprintf(
		"repo-key %s · %d files · %d capabilities · %d attestations",
		repoKey, res.Files, r.Capabilities, r.Records)))
	fmt.Fprintf(&b, "  %s\n", ui.RenderMuted("manifest written last — its absence is how a reader spots a torn publish"))

	if r.Records == 0 {
		fmt.Fprintf(&b, "\n  %s\n", ui.RenderWarn(
			"no attestation records — this bundle publishes the ladder, but no proofs"))
	}
	fmt.Fprintf(&b, "\n  %s\n    %s\n\n", ui.RenderMuted("host it with"),
		fmt.Sprintf("upload %s/ with payloads first, then manifest, then index (never --delete)", strings.TrimSuffix(dest, "/")))
	return b.String()
}

func init() {
	attestPublishCmd.Flags().String("to", "", "destination directory (required)")
	attestPublishCmd.Flags().String("repo-key", "", "opaque repo key (default: the repo directory name)")
	attestPublishCmd.Flags().Bool("json", false, "structured JSON output for agents")
	addCorpusFlag(attestPublishCmd)
	attestCmd.AddCommand(attestPublishCmd)
}
