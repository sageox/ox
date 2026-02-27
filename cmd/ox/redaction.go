package main

import (
	"fmt"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/signing"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// redaction command styles
var (
	redactBold   = lipgloss.NewStyle().Bold(true).Foreground(ui.ColorAccent)
	redactGreen  = lipgloss.NewStyle().Foreground(ui.ColorPass)
	redactYellow = lipgloss.NewStyle().Foreground(ui.ColorWarn)
	redactRed    = lipgloss.NewStyle().Foreground(ui.ColorFail)
)

func init() {
	// redaction is nested under session since it only applies to sessions.
	// if we add other policies beyond redaction (data retention, sync policies, etc.),
	// a new "ox policy" parent command could make sense.
	sessionCmd.AddCommand(redactionCmd)
	redactionCmd.AddCommand(redactionPolicyCmd)
	redactionCmd.AddCommand(redactionVerifyCmd)

	// flags for policy command
	redactionPolicyCmd.Flags().Bool("json", false, "output as JSON")
}

var redactionCmd = &cobra.Command{
	Use:   "redaction",
	Short: "Manage secret redaction policy",
	Long: `Inspect and verify the secret redaction policy.

Ox automatically redacts secrets from sessions and captured data.
Use these commands to see what gets redacted and verify the policy
hasn't been tampered with.

For a comprehensive view including custom rules from REDACT.md files,
use: ox agent redact

Commands:
  policy    Show the complete redaction pattern list
  verify    Verify the signature of the embedded patterns`,
}

var redactionPolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Show the redaction policy",
	Long: `Display all secret patterns that ox will automatically redact.

The patterns cover common credential types:
  - Cloud provider keys (AWS, GCP, Azure)
  - API tokens (GitHub, GitLab, Slack, Stripe)
  - Database connection strings
  - Private keys and JWTs
  - Generic secrets (api_key=, password=, etc.)

Use --json for machine-readable output suitable for security audits.`,
	RunE: runRedactionPolicy,
}

var redactionVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify redaction policy signature",
	Long: `Verify that the embedded redaction patterns haven't been tampered with.

During release builds, the redaction manifest is cryptographically signed
with Ed25519. This command regenerates the manifest from the current
patterns and verifies it against the embedded signature.

Exit codes:
  0  Signature valid (or not configured for dev builds)
  1  Signature invalid or verification error`,
	RunE: runRedactionVerify,
}

func runRedactionPolicy(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	manifest := session.GenerateManifest()

	if jsonOutput {
		data, err := manifest.PrettyJSON()
		if err != nil {
			return fmt.Errorf("failed to generate JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// human-readable output
	fmt.Println(redactBold.Render("Redaction Policy"))
	fmt.Println()

	hash, _ := manifest.HashHex()
	fmt.Printf("  Schema version: %s\n", manifest.SchemaVersion)
	fmt.Printf("  Pattern count:  %d\n", manifest.PatternCount())
	fmt.Printf("  Manifest hash:  %s\n", hash)
	fmt.Println()

	fmt.Println(redactBold.Render("Patterns:"))
	fmt.Println()

	for _, p := range manifest.Patterns {
		fmt.Printf("  %s\n", redactBold.Render(p.Name))
		fmt.Printf("    Regex:  %s\n", p.Regex)
		fmt.Printf("    Redact: %s\n", p.Redact)
		fmt.Println()
	}

	// show verification status
	result := session.VerifyRedactionSignature()
	fmt.Println(redactBold.Render("Signature Status:"))
	switch result.Status {
	case signing.StatusNotConfigured:
		fmt.Println("  " + redactYellow.Render("Not configured (development build)"))
	case signing.StatusValid:
		fmt.Println("  " + redactGreen.Render("Valid"))
	case signing.StatusInvalid:
		fmt.Println("  " + redactRed.Render("INVALID - patterns may have been tampered with"))
	case signing.StatusError, signing.StatusMissing:
		fmt.Printf("  %s: %v\n", redactRed.Render("Error"), result.Error)
	}

	return nil
}

func runRedactionVerify(cmd *cobra.Command, args []string) error {
	result := session.VerifyRedactionSignature()

	switch result.Status {
	case signing.StatusNotConfigured:
		fmt.Println(redactBold.Render("Signature Verification"))
		fmt.Println()
		fmt.Println("  Status: " + redactYellow.Render("Not configured"))
		fmt.Println()
		fmt.Println("  This is a development build without embedded signatures.")
		fmt.Println("  Release builds include cryptographic signatures to detect tampering.")
		fmt.Println()
		fmt.Printf("  Manifest hash: %s\n", result.Hash)
		return nil

	case signing.StatusValid:
		fmt.Println(redactBold.Render("Signature Verification"))
		fmt.Println()
		fmt.Println("  Status: " + redactGreen.Render("VALID"))
		fmt.Println()
		fmt.Println("  The redaction patterns match the signed release manifest.")
		fmt.Println("  No tampering detected.")
		fmt.Println()
		fmt.Printf("  Manifest hash: %s\n", result.Hash)
		return nil

	case signing.StatusInvalid:
		fmt.Println(redactBold.Render("Signature Verification"))
		fmt.Println()
		fmt.Println("  Status: " + redactRed.Render("INVALID"))
		fmt.Println()
		fmt.Println("  " + redactRed.Render("WARNING: The redaction patterns do not match the signed manifest!"))
		fmt.Println("  This binary may have been tampered with.")
		fmt.Println()
		fmt.Println("  Recommended actions:")
		fmt.Println("    1. Download ox from the official release")
		fmt.Println("    2. Verify the checksum matches")
		fmt.Println("    3. Report this issue if you downloaded from official sources")
		fmt.Println()
		fmt.Printf("  Manifest hash: %s\n", result.Hash)
		return fmt.Errorf("signature verification failed")

	case signing.StatusError, signing.StatusMissing:
		fmt.Println(redactBold.Render("Signature Verification"))
		fmt.Println()
		fmt.Println("  Status: " + redactRed.Render("ERROR"))
		fmt.Println()
		fmt.Printf("  %v\n", result.Error)
		return result.Error

	default:
		return fmt.Errorf("unknown verification status: %v", result.Status)
	}
}
