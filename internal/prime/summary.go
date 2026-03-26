package prime

import (
	"fmt"
	"strings"
)

// ShortenPath truncates a path for display, keeping the last 3 components.
func ShortenPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 3 {
		return path
	}
	return ".../" + strings.Join(parts[len(parts)-3:], "/")
}

// BuildHumanSummary creates a human-readable summary for --review mode.
func BuildHumanSummary(output Output) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "- **Agent ID:** %s\n", output.AgentID)
	if output.SessionID != "" {
		fmt.Fprintf(&sb, "- **Session ID:** %s\n", output.SessionID)
	}
	if output.AgentType != "" {
		supportStatus := "supported"
		if !output.AgentSupported {
			supportStatus = "not officially supported"
		}
		fmt.Fprintf(&sb, "- **Agent Type:** %s (%s)\n", output.AgentType, supportStatus)
	}
	fmt.Fprintf(&sb, "- **Status:** %s\n", output.Status)

	if output.SupportNotice != "" {
		fmt.Fprintf(&sb, "- **Support Notice:** %s\n", output.SupportNotice)
	}

	if output.Message != "" {
		fmt.Fprintf(&sb, "- **Message:** %s\n", output.Message)
	}

	if output.TokenEstimate > 0 {
		fmt.Fprintf(&sb, "- **Token Estimate:** %d\n", output.TokenEstimate)
	}

	if output.Guidance != nil {
		fmt.Fprintf(&sb, "- **Guidance:** %d intent-to-command mappings\n", len(output.Guidance.Commands))
	}

	if output.ProjectGuidance != nil {
		if output.ProjectGuidance.Skipped {
			fmt.Fprintf(&sb, "- **Project Guidance:** AGENTS.md skipped (%s)\n", output.ProjectGuidance.SkipReason)
		} else {
			fmt.Fprintf(&sb, "- **Project Guidance:** AGENTS.md found (%d bytes, ~%d tokens)\n",
				output.ProjectGuidance.Size, output.ProjectGuidance.Tokens)
		}
	}

	if output.CapturePrior != nil {
		sb.WriteString("- **Capture Prior:** Instructions available for retroactive history capture\n")
	}

	if output.Session != nil {
		if output.Session.Recording {
			fmt.Fprintf(&sb, "- **Session:** Recording (mode: %s)\n", output.Session.Mode)
		} else if output.Session.LedgerNeeded {
			sb.WriteString("- **Session:** Not recording (ledger not provisioned)\n")
		}
	}

	if output.TeamContext != nil {
		fmt.Fprintf(&sb, "- **Team Context:** %s\n", output.TeamContext.TeamID)
		if ci := output.TeamContext.CoworkerInstructions; ci != nil && ci.HasAgentsMD {
			fmt.Fprintf(&sb, "- **Team AGENTS.md:** %s\n", ShortenPath(ci.AgentsMDPath))
		}
	}

	if output.OtherTeams != nil {
		fmt.Fprintf(&sb, "- **Other Teams:** %d additional team contexts available\n", len(output.OtherTeams.Teams))
	}

	if output.PrimeCallCount > 0 {
		fmt.Fprintf(&sb, "- **Prime Call Count:** %d\n", output.PrimeCallCount)
	}

	if output.PrimeExcessiveNotice != "" {
		fmt.Fprintf(&sb, "- **Warning:** %s\n", output.PrimeExcessiveNotice)
	}

	if output.Ledger != nil {
		if output.Ledger.Exists {
			fmt.Fprintf(&sb, "- **Ledger:** %s\n", ShortenPath(output.Ledger.Path))
		} else {
			sb.WriteString("- **Ledger:** Not provisioned\n")
		}
	}

	if output.NeedsDoctorAgent {
		fmt.Fprintf(&sb, "- **Doctor Attention Needed:** %s\n", output.DoctorHint)
	}

	if output.HooksInstalled {
		fmt.Fprintf(&sb, "- **Hooks Installed:** %s\n", output.HooksRestartNotice)
	}

	if output.UpdateAvailable {
		fmt.Fprintf(&sb, "- **Update Available:** %s\n", output.UpdateHint)
	}

	return sb.String()
}
