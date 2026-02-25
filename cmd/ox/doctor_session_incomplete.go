package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	// build detail message with session list
	var sb strings.Builder
	sb.WriteString("Sessions needing finalization:\n")

	// limit to first 5 for readability
	showCount := count
	if showCount > 5 {
		showCount = 5
	}

	for i := 0; i < showCount; i++ {
		info := incomplete[i]
		sb.WriteString(fmt.Sprintf("  - %s (missing: %s)\n", info.SessionID, strings.Join(info.Missing, ", ")))
	}

	if count > 5 {
		sb.WriteString(fmt.Sprintf("  ... and %d more\n", count-5))
	}

	sb.WriteString("\nRun `ox agent doctor` inside your AI coding session to fix.")

	// mark as agent-required since summaries need LLM generation
	return WarningCheck("incomplete sessions", msg, sb.String()).WithRequiresAgent()
}

// buildIncompleteSessionAgentResult creates agent-oriented commands for incomplete sessions
func buildIncompleteSessionAgentResult(incomplete []IncompleteSessionInfo) checkResult {
	count := len(incomplete)
	msg := fmt.Sprintf("%d found", count)

	// build detail message with commands
	var sb strings.Builder
	sb.WriteString("Sessions needing finalization:\n")

	// limit to first 5 for readability
	showCount := count
	if showCount > 5 {
		showCount = 5
	}

	for i := 0; i < showCount; i++ {
		info := incomplete[i]
		sb.WriteString(fmt.Sprintf("  - %s (missing: %s)\n", info.SessionID, strings.Join(info.Missing, ", ")))
	}

	if count > 5 {
		sb.WriteString(fmt.Sprintf("  ... and %d more\n", count-5))
	}

	// provide agent commands
	sb.WriteString("\nTo finalize sessions, run:\n")
	sb.WriteString("  ox agent <id> doctor            # regenerate missing artifacts\n")
	sb.WriteString("  ox session export <session-name> # generate session.html")

	// when in agent context, don't mark as requires-agent (since we're already in agent context)
	return WarningCheck("incomplete sessions", msg, sb.String())
}
