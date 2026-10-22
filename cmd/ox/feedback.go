package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

var feedbackTokenForEndpoint = auth.EnsureValidTokenForEndpointContext

func newFeedbackCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "feedback --file <path|->",
		Short:   "Send private product feedback to SageOx Support",
		GroupID: "diagnostics",
		Long: `If ox confused you, failed you, or cost you tokens, say so here.
Send a report from a file or stdin. The first line is its title (8–200
characters). Reports must fit within 4 KiB and 4,000 characters.

Only your report, optional reply email, ox version, and OS/architecture are
included. No logs, environment variables, or session content are collected
automatically.
Possible secrets are refused locally. Sending also requires current secret
checks; --dry-run works offline and prints the exact JSON body without sending.

Replies use your signed-in account's notification address. Without login,
provide --email. This sends private correspondence to SageOx Support; it does
not create a public GitHub issue. Explicit feedback works with telemetry off.

Examples:
  ox feedback --type bug --file report.txt --dry-run
  ox feedback --type bug --file report.txt --json
  cat report.txt | ox feedback --file - --email person@example.com`,
		Args: cobra.NoArgs,
		RunE: runFeedback,
	}
	cmd.Flags().String("file", "", "Report file, or - to read stdin (required)")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().String("type", "feedback", "Report category: bug, feature-request, or feedback")
	cmd.Flags().String("email", "", "Submit without login using this reply address")
	cmd.Flags().Bool("dry-run", false, "Print the locally checked JSON body without sending")
	return cmd
}

func init() {
	rootCmd.AddCommand(newFeedbackCommand())
}

// Previews must not start telemetry or flush previously queued command events.
func isFeedbackPreview(cmd *cobra.Command) bool {
	if cmd.CommandPath() != "ox feedback" {
		return false
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	return dryRun
}

func runFeedback(cmd *cobra.Command, _ []string) error {
	source, _ := cmd.Flags().GetString("file")
	category, _ := cmd.Flags().GetString("type")
	email, _ := cmd.Flags().GetString("email")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if err := validateFeedbackType(category); err != nil {
		return errkind.WithDetail(errkind.Usage, "invalid feedback type", err)
	}
	if err := validateFeedbackEmail(email); err != nil {
		return errkind.WithDetail(errkind.Usage, "invalid feedback email", err)
	}
	reader := cmd.InOrStdin()
	if source != "-" {
		file, err := os.Open(source)
		if err != nil {
			return errors.New("cannot read feedback file; check --file and retry")
		}
		defer file.Close()
		reader = file
	}
	report, err := readFeedbackReport(reader)
	if err != nil {
		return errkind.WithDetail(errkind.Usage, "invalid feedback report", err)
	}
	// Reject credentials before consulting authentication or network services.
	if err := validateFeedbackLocalPayload([]byte(report)); err != nil {
		return err
	}
	request := api.SupportCaseRequest{
		Description: report,
		Email:       email,
		Context: api.SupportContext{
			Version:        1,
			Surface:        "ox-cli",
			Intent:         "feedback",
			Category:       category,
			ProductVersion: version.Version,
			Platform:       runtime.GOOS + " " + runtime.GOARCH,
		},
	}
	payload, err := cli.MarshalJSONIndent(request)
	if err != nil {
		return errors.New("cannot encode feedback")
	}
	if err := validateFeedbackLocalPayload(payload); err != nil {
		return err
	}
	if dryRun {
		// These are the same bytes passed to SubmitSupportCase, with no envelope.
		_, err := cmd.OutOrStdout().Write(payload)
		return err
	}
	projectRoot, _ := findProjectRoot()
	ep := endpoint.GetForProject(projectRoot)
	var token *auth.StoredToken
	if email == "" {
		token, err = feedbackTokenForEndpoint(cmd.Context(), ep, 300)
		if err != nil {
			return errors.New("feedback not submitted: authentication could not be refreshed; run ox login or supply --email and retry")
		}
	}
	if (token == nil || token.AccessToken == "") && email == "" {
		return errkind.Errorf(errkind.NotLoggedIn, "feedback needs a reply address: use --email or run ox login")
	}
	if err := validateFeedbackPayload(cmd.Context(), payload); err != nil {
		return err
	}
	client := api.NewRepoClientWithEndpoint(ep)
	if token != nil {
		client.WithAuthToken(token.AccessToken)
	}
	id, err := client.SubmitSupportCase(cmd.Context(), payload)
	if err != nil {
		return fmt.Errorf("feedback submission not confirmed: %w; keep your report before retrying (a retry may send a duplicate)", err)
	}
	if jsonOutput {
		return cli.PrintJSONTo(cmd.OutOrStdout(), struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Guidance string `json:"guidance"`
		}{id, "sent", "SageOx Support accepted the report. Keep this receipt for follow-up."})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted by SageOx Support · receipt %s\n", id)
	return err
}
