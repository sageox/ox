package main

import (
	"fmt"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/ui"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionIncomplete,
		Name:        "incomplete sessions",
		Category:    "Sessions",
		FixLevel:    FixLevelCheckOnly, // requires agent context to fix
		Description: "Reports session finalization, recovery, and uploads pending on this machine",
		Run:         func(fix bool) checkResult { return checkSessionIncomplete(fix) },
	})
}

// checkSessionIncomplete reports work using the daemon's finalization rules.
// When running outside agent context, provides human-friendly guidance.
// When running inside agent context, provides commands to finalize.
func checkSessionIncomplete(_ bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("incomplete sessions", "no ledger found", "")
	}

	output := buildAgentDoctorOutput("", findGitRoot())
	result := PassedCheck("incomplete sessions", "no sessions need local finalization")
	if len(output.IncompleteSessions) > 0 {
		if agentx.IsAgentContext() {
			result = buildIncompleteSessionAgentResult(output.IncompleteSessions)
		} else {
			result = buildIncompleteSessionHumanResult(output.IncompleteSessions)
		}
	}
	for _, group := range []struct {
		label string
		names []string
	}{
		{"Awaiting upload", output.PendingUploads},
		{"Awaiting automatic finalization", output.PendingFinalization},
		{"Awaiting recovery", output.RecoverySessions},
		{"Automatic finalization blocked", output.BlockedSessions},
		{"Publication refused", output.RefusedSessions},
	} {
		if len(group.names) > 0 {
			result.warning = true
			result.detail += fmt.Sprintf("\n%s: %s", group.label, strings.Join(group.names, ", "))
			if len(output.IncompleteSessions) == 0 {
				result.message = "session work pending"
			}
		}
	}
	if output.CommitNeeded || output.PushNeeded {
		result.warning = true
		result.detail += "\nLedger changes await commit or push; run ox agent <id> doctor for next steps"
		if len(output.IncompleteSessions) == 0 {
			result.message = "session work pending"
		}
	}
	if output.SessionScanError != "" {
		result.passed = false
		result.message = "session inspection failed"
		result.detail += "\n" + output.SessionScanError
	}
	return result
}

// buildIncompleteSessionHumanResult creates human-friendly guidance for incomplete sessions.
// Marks the result as requiring agent intervention since summaries need LLM generation.
func buildIncompleteSessionHumanResult(incomplete []IncompleteSessionInfo) checkResult {
	count := len(incomplete)
	msg := fmt.Sprintf("%d found", count)

	// build detail with session list (CTA is rendered by the agent-required box)
	var sb strings.Builder
	sb.WriteString("Sessions needing finalization:\n")

	showCount := count
	if showCount > 5 {
		showCount = 5
	}

	for i := 0; i < showCount; i++ {
		info := incomplete[i]
		fmt.Fprintf(&sb, "  - %s (missing: %s)\n", info.SessionID, strings.Join(info.Missing, ", "))
	}

	if count > 5 {
		fmt.Fprintf(&sb, "  ... and %d more", count-5)
	}

	// mark as agent-required since summaries need LLM generation
	return WarningCheck("incomplete sessions", msg, sb.String()).WithRequiresAgent()
}

// buildIncompleteSessionAgentResult creates agent-oriented commands for incomplete sessions
func buildIncompleteSessionAgentResult(incomplete []IncompleteSessionInfo) checkResult {
	count := len(incomplete)
	msg := fmt.Sprintf("%d found", count)

	// build pre-styled detail with prominent fix commands
	var sb strings.Builder
	sb.WriteString(ui.MutedStyle.Render("Sessions needing finalization:") + "\n")

	showCount := count
	if showCount > 5 {
		showCount = 5
	}

	for i := 0; i < showCount; i++ {
		info := incomplete[i]
		sb.WriteString(ui.MutedStyle.Render(fmt.Sprintf("  - %s (missing: %s)", info.SessionID, strings.Join(info.Missing, ", "))) + "\n")
	}

	if count > 5 {
		sb.WriteString(ui.MutedStyle.Render(fmt.Sprintf("  ... and %d more", count-5)) + "\n")
	}

	sb.WriteString("\n")
	sb.WriteString(ui.AccentStyle.Render("→") + " " +
		ui.AccentStyle.Bold(true).Render("ox agent <id> doctor") +
		ui.MutedStyle.Render("  regenerate missing artifacts") + "\n")
	sb.WriteString(ui.AccentStyle.Render("→") + " " +
		ui.AccentStyle.Bold(true).Render("ox session export <name>") +
		ui.MutedStyle.Render("  generate session.html"))

	// detailRaw: pre-styled output, WithRequiresAgent: sets .needs-doctor-agent marker
	result := WarningCheck("incomplete sessions", msg, sb.String()).WithRequiresAgent()
	result.detailRaw = true
	return result
}
