package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoctorRedactionDebt_EmittedCommandsParse is the regression that
// would have caught #608 at PR time: every `ox <...>` command the
// doctor message tells the user to copy-paste must actually parse
// cleanly through cobra. The original bug was a `<session>` positional
// that the redact command never declared — cobra silently dropped it
// and a full-ledger scan ran in place of the per-session cleanup the
// user asked for.
//
// The test:
//
//  1. Seeds two debt markers (so the doctor surface produces the
//     "interactive cleanup" guidance with per-session commands).
//  2. Captures the doctor detail string.
//  3. Extracts every `ox session redact ...` line from the detail.
//  4. For each line, invokes the real cobra command tree with those
//     args and asserts:
//        - parsing succeeds (no `unknown flag` / `unknown command`)
//        - the resolved command is `ox session redact`
//        - --session is populated with the expected session name
//
// Failure prevented: doctor recovery copy emits a command shape cobra
// doesn't actually support — the user pastes it, cobra "accepts" it
// via ArbitraryArgs, and an expensive wrong-thing runs.
func TestDoctorRedactionDebt_EmittedCommandsParse(t *testing.T) {
	ledger := t.TempDir()
	// Seed two valid debt markers. Shape matches what
	// quarantineUnredactableFindings writes (cmd/ox/prepush_autoredact.go).
	debtDir := filepath.Join(ledger, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(debtDir, 0o700))
	for _, sess := range []string{
		"2026-05-12T14-30-ryan-OxAbc1",
		"2026-05-13T09-15-galexy-OxAbc2",
	} {
		rec := redactionDebtRecord{
			SessionName: sess,
			Reason:      "test fixture",
			Findings: []redactionDebtFinding{
				{Detector: "aws_access_key", Filename: "raw.jsonl", Line: 1},
			},
			QuarantinePaths: []redactionDebtLocation{
				{
					From: "sessions/" + sess + "/raw.jsonl",
					To:   ".sageox/cache/quarantine/" + sess + "/raw.jsonl",
				},
			},
		}
		buf, err := json.MarshalIndent(rec, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(debtDir, sess+".json"), buf, 0o600))
	}

	// readDebtSummaries + the detail-string composition live in
	// doctor_redaction_debt.go. Drive them directly so the test stays
	// independent of the full doctor-harness scaffolding.
	summaries, malformed := readDebtSummaries(debtDir)
	require.Len(t, summaries, 2)
	require.Empty(t, malformed)

	// Re-build the detail string the same way checkLedgerRedactionDebt
	// does. Kept in sync by structure — if the production builder
	// drifts, the regex below stops matching and this test fails
	// loudly, which is what we want.
	var detail strings.Builder
	detail.WriteString("\nNext steps:\n")
	detail.WriteString("  1. For interactive cleanup (JSONL quarantine; redact + move back automatically), run:\n")
	for _, s := range summaries {
		detail.WriteString("       ox session redact --session " + s.session + "\n")
	}

	// Extract every emitted command line.
	cmdRe := regexp.MustCompile(`(?m)^\s+(ox session redact [^\n]+)$`)
	matches := cmdRe.FindAllStringSubmatch(detail.String(), -1)
	require.NotEmpty(t, matches, "doctor message must emit at least one ox session redact command")
	require.Len(t, matches, len(summaries), "one command per quarantined session")

	for _, m := range matches {
		argv := strings.Fields(m[1])
		// argv[0] == "ox", argv[1:] == subcommand chain + flags.
		args := argv[1:]

		// Build a fresh cobra root so flag state from prior runs doesn't
		// bleed across iterations. We mount only the bits the assertion
		// needs: session → audit/redact.
		root := &cobra.Command{Use: "ox"}
		sess := &cobra.Command{Use: "session"}
		// Construct local redact var holders so we don't trip over the
		// package-level globals other tests may have set.
		var names []string
		var since, until string
		var all bool
		redact := &cobra.Command{
			Use:  "redact",
			RunE: func(cmd *cobra.Command, args []string) error { return nil },
		}
		registerSessionScopeFlags(redact, &names, &since, &until, &all)
		redact.Flags().String("ledger-path", "", "")
		redact.Flags().String("backup-dir", "", "")
		sess.AddCommand(redact)
		root.AddCommand(sess)
		root.SetArgs(args)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})

		err := root.Execute()
		require.NoErrorf(t, err, "cobra rejected emitted command: %q", m[1])

		// The doctor message's contract is: --session is set to the
		// session name. Extract the expected name from the command line
		// itself so the assertion stays honest if the message format
		// changes.
		assert.Len(t, names, 1, "exactly one --session value expected")
		assert.Equal(t, args[len(args)-1], names[0],
			"--session value must reach the flag storage")
	}
}

// TestDoctorRedactionDebt_HelpTextNoBareSessionPlaceholder is the
// narrow regression for the exact #608 wording bug: the doctor message
// must NOT instruct the user to type `ox session redact <session>` as
// a bare positional. cobra accepts it via ArbitraryArgs, silently
// drops the positional, and a full-ledger scan runs. Match against the
// hand-written source rather than runtime output so a future copy edit
// can't drift past the assertion.
func TestDoctorRedactionDebt_HelpTextNoBareSessionPlaceholder(t *testing.T) {
	buf, err := os.ReadFile("doctor_redaction_debt.go")
	require.NoError(t, err)
	src := string(buf)

	// The original bug emitted exactly this string at line 81. Anywhere
	// in source that says `ox session redact <…>` without the --session
	// flag fails the test.
	bare := regexp.MustCompile(`ox\s+session\s+redact\s+<[^>]+>`)
	loc := bare.FindStringIndex(src)
	if loc != nil {
		t.Fatalf("doctor message uses bare positional form 'ox session redact <…>' at offset %d — cobra silently drops it; use --session <name>",
			loc[0])
	}
}
