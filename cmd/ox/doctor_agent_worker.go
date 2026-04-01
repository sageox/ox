package main

import (
	"fmt"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
)

// authHintForAgent returns the appropriate authentication hint for an agent CLI.
func authHintForAgent(agent string) string {
	if agent == "gemini" {
		return "Set GEMINI_API_KEY or GOOGLE_API_KEY to enable automatic session finalization"
	}
	return fmt.Sprintf("Run '%s login' to enable automatic session finalization", agent)
}

// checkAgentWorkerBinary validates that the effective agent CLI is installed and authenticated.
// Resolves auto-detection: if unconfigured, reports what would be auto-detected.
func checkAgentWorkerBinary() checkResult {
	userCfg, err := config.LoadUserConfig()
	if err != nil {
		return SkippedCheck("agent worker binary", "config unavailable", "")
	}

	raw := userCfg.GetAgentWorkerAgent()

	// explicitly disabled
	if raw == "none" {
		return SkippedCheck("agent worker binary", "disabled",
			"Enable with: ox config set agent_worker claude")
	}

	resolved := agentwork.ResolveAgent(raw)

	// unconfigured and nothing usable auto-detected
	if resolved == "none" {
		// check if anything is installed but not authenticated
		for _, agent := range []string{"claude", "codex", "gemini"} {
			u := agentwork.CheckAgentUsability(agent)
			if u.Installed && !u.Authenticated {
				hint := authHintForAgent(agent)
				return WarningCheck("agent worker binary",
					fmt.Sprintf("%s installed but not authenticated", agent),
					hint)
			}
		}
		return InfoCheck("agent worker binary", "no agent CLI found",
			"Install claude, codex, or gemini to enable automatic session finalization")
	}

	// verify usability of the resolved agent
	u := agentwork.CheckAgentUsability(resolved)
	if !u.Installed {
		return WarningCheck("agent worker binary",
			fmt.Sprintf("%s not found", resolved),
			fmt.Sprintf("agent_worker is set to %q but the %s CLI is not in PATH", resolved, resolved))
	}

	if !u.Authenticated {
		return WarningCheck("agent worker binary",
			fmt.Sprintf("%s not authenticated", resolved),
			fmt.Sprintf("%s — the daemon can't use %s without authentication", authHintForAgent(resolved), resolved))
	}

	source := "configured"
	if raw == "" {
		source = "auto-detected"
	}
	return PassedCheck("agent worker binary",
		fmt.Sprintf("%s (%s, %s)", resolved, source, u.AuthDetail))
}
