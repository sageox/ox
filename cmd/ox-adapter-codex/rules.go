package main

// Codex CLI rules support — scaffold (not yet wired into main.go).
//
// State as of May 2026 (research source: developers.openai.com/codex/):
//
//   - Codex DOES have a `~/.codex/rules/` and `<repo>/.codex/rules/` directory,
//     BUT those rules are Starlark-language COMMAND-EXECUTION POLICIES
//     (allow / prompt / forbidden patterns for shell commands the agent
//     wants to run). They are NOT behavioral guidance — they're a sandbox
//     mechanism, closer to git pre-commit policy than to .claude/rules/.
//     Reference: developers.openai.com/codex/rules
//
//   - Behavioral guidance still goes in AGENTS.md (project root + ~/.codex/AGENTS.md
//     for personal). Codex walks up from cwd to find AGENTS.md and reads
//     up to project_doc_max_bytes from each layer. There is NO modular
//     per-rule .md file system for behavioral guidance.
//
//   - Codex's broader `.codex/` config (config.toml, hooks, rules/) is
//     skipped for "untrusted" projects (security model).
//
// Implication for SageOx ox CLI:
//
//   - We CANNOT use the `.codex/rules/` directory for behavioral pointer
//     rules (wrong semantics — those are Starlark exec policies).
//   - The existing AGENTS.md ox-prime-marker injection (handled in main.go
//     / hooks) is the ONLY surface today for delivering SageOx context to
//     Codex. `ox agent prime` XML still applies once the agent runs prime.
//   - This scaffold exists so that IF Codex adds a behavioral rules dir
//     in the future (e.g., .codex/instructions/ with markdown), wiring is
//     trivial: implement the handlers, add CapRulesInstaller to main.go's
//     capability list, and the existing installAdapterRules flow handles
//     the rest.
//
// Why these handlers exist but aren't wired:
//
//   - main.go does NOT reference handleInstallRules/handleCheckRules/
//     handleUninstallRules — `installAdapterRules` short-circuits on the
//     CapRulesInstaller capability, which we deliberately don't advertise.
//   - This keeps the behavior honest: Codex doesn't get rule files written
//     today, and ox doctor doesn't claim rules are installable.
//   - When upstream gains support, flip main.go (one-line capability add
//     plus three handler refs) and replace the stubs below.

import (
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// handleInstallRules — stub. Codex has no modular behavioral rules surface
// in May 2026. AGENTS.md is the existing delivery path (handled elsewhere
// via ox prime markers).
func handleInstallRules(_ adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
	return &adapterprotocol.InstallRulesResponse{
		Installed:    false,
		FilesWritten: nil,
	}, nil
}

// handleCheckRules — stub. Reports "installed" vacuously (no rules expected
// since Codex has no modular behavioral rules system).
func handleCheckRules(_ adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
	return &adapterprotocol.CheckRulesResponse{
		Installed: true,
		Missing:   nil,
		Stale:     nil,
		RulesDir:  "", // no canonical SageOx rules dir for Codex
	}, nil
}

// handleUninstallRules — stub. Nothing to uninstall.
func handleUninstallRules(_ adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error) {
	return &adapterprotocol.UninstallRulesResponse{
		Uninstalled:  false,
		FilesRemoved: nil,
	}, nil
}
