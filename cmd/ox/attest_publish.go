package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/attestpublication"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var attestCmd = &cobra.Command{
	Use:   "attest",
	Short: "Work with frozen Attest runs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var attestPublishCmd = &cobra.Command{
	Use:   "publish --from <frozen-export> --out <directory>",
	Short: "Publish a frozen Attest run",
	Long: `Package and publish a frozen Attest run directly to SageOx.

The export must have been produced when the run completed. ox reads that export
only; it never reconstructs a report from the current checkout. --out receives
the deterministic ZIP and durable resume journal.`,
	RunE: runAttestPublish,
}

func init() {
	attestPublishCmd.Flags().String("from", "", "frozen producer export directory (required)")
	attestPublishCmd.Flags().String("out", "", "directory for ZIP and resume journal (required)")
	attestPublishCmd.Flags().Bool("open", false, "open the published report when processing finishes")
	attestPublishCmd.Flags().Bool("json", false, "output the credential-free publication result as JSON")
	attestCmd.GroupID = "dev"
	attestCmd.AddCommand(attestPublishCmd)
	rootCmd.AddCommand(attestCmd)
}

func runAttestPublish(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	out, _ := cmd.Flags().GetString("out")
	if from == "" || out == "" {
		return errors.New("--from <frozen-export> and --out <directory> are required")
	}
	absFrom, err := filepath.Abs(from)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(absFrom, absOut); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("--out must be outside the frozen export so publication state cannot become package evidence")
	}
	projectRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("find repository: %w", err)
	}
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return errors.New("this repository has no SageOx repo_id; run ox init first")
	}
	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil {
		return fmt.Errorf("authenticate publication request: %w", err)
	}
	if token == nil || token.AccessToken == "" {
		return errors.New("sign in with ox login before publishing an Attest run")
	}
	publisher := attestpublication.Publisher{Control: &attestpublication.ControlClient{BaseURL: ep, Token: token.AccessToken}}
	result, err := publisher.PublishExport(cmd.Context(), repoID, absFrom, absOut)
	if err != nil {
		return renderAttestPublishError(err)
	}
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		raw, err := json.Marshal(struct {
			RunID         string  `json:"run_id"`
			Status        string  `json:"status"`
			URL           *string `json:"url,omitempty"`
			ArchiveSHA256 string  `json:"archive_sha256"`
			Journal       string  `json:"journal"`
		}{result.Run.RunID, result.Run.Status, result.Run.URL, result.Package.ArchiveSHA256, result.JournalPath})
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Attest publication %s is %s\n", result.Run.RunID, result.Run.Status)
		if result.Run.URL != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Report: %s\n", *result.Run.URL)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Resume journal: %s\n", result.JournalPath)
	}
	open, _ := cmd.Flags().GetBool("open")
	if open && result.Run.URL != nil {
		if err := cli.OpenInBrowser(*result.Run.URL); err != nil && !errors.Is(err, cli.ErrHeadless) {
			return err
		}
	}
	return nil
}

func renderAttestPublishError(err error) error {
	var apiErr *attestpublication.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 409:
			return fmt.Errorf("attest publication conflicts with immutable frozen package: %w", err)
		case 410:
			return fmt.Errorf("attest publication identity was deleted: %w", err)
		case 503:
			return fmt.Errorf("attest service is temporarily unavailable; preserve the resume journal and retry: %w", err)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("local packaging or resume state is unavailable: %w", err)
	}
	return fmt.Errorf("attest S3 transfer or processing failed: %w", err)
}
