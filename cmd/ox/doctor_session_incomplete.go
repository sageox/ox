package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/pkg/agentx"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionIncomplete,
		Name:        "incomplete sessions",
		Category:    "Sessions",
		FixLevel:    FixLevelCheckOnly, // requires agent context to fix
		Description: "Detects sessions missing summaries or HTML viewers",
		Run:         func(fix bool) checkResult { return checkSessionIncomplete(fix) },
	})
}

// checkSessionIncomplete detects sessions that are missing summary.md or session.html.
// When running outside agent context, provides human-friendly guidance.
// When running inside agent context, provides commands to finalize.
func checkSessionIncomplete(_ bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("incomplete sessions", "no ledger found", "")
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return SkippedCheck("incomplete sessions", "no sessions directory", "")
	}

	// reuse the existing findIncompleteSessions from agent_doctor.go
	// (it's in the same package, so we can call it directly)
	incomplete := findIncompleteSessions(sessionsDir, "")
	if len(incomplete) == 0 {
		return PassedCheck("incomplete sessions", "all sessions complete")
	}

	// check if we're in agent context
	inAgentContext := agentx.IsAgentContext()

	if inAgentContext {
		// agent context: provide commands to finalize
		return buildIncompleteSessionAgentResult(incomplete)
	}

	// human context: provide helpful guidance with agent-required flag
	return buildIncompleteSessionHumanResult(incomplete)
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
		sb.WriteString(fmt.Sprintf("  - %s (missing: %s)\n", info.SessionID, strings.Join(info.Missing, ", ")))
	}

	if count > 5 {
		sb.WriteString(fmt.Sprintf("  ... and %d more", count-5))
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

	result := WarningCheck("incomplete sessions", msg, sb.String())
	result.detailRaw = true
	return result
}
