package main

// Gemini CLI rules support — scaffold (not yet wired into main.go).
//
// State as of May 2026 (research source: geminicli.com/docs,
// google-gemini.github.io/gemini-cli/docs):
//
//   - Gemini CLI uses GEMINI.md as the canonical context file. Discovered
//     hierarchically: scans cwd and ancestors up to a trusted root, plus
//     subdirectories below cwd. User-level config lives in ~/.gemini/.
//   - Workspace config dir is `<repo>/.gemini/` containing settings.json,
//     GEMINI.md, custom slash commands, project extensions, workspace
//     policies, and session data. Plus user-level `~/.gemini/`.
//   - Modular composition is via `@file.md` IMPORT SYNTAX inside GEMINI.md,
//     not via a dedicated rules directory. The CLI also scans subdirs for
//     additional GEMINI.md files (and respects .gitignore / .geminiignore).
//   - The context filename can be configured (single string or array).
//   - There is NO dedicated `.gemini/rules/<topic>.md` per-file scheme.
//
// Implication for SageOx ox CLI:
//
//   - One viable approach IF SageOx wants to leverage Gemini's import
//     syntax: drop a file at `<repo>/.gemini/sageox/use-team-context.md`
//     and rely on the user's GEMINI.md adding `@.gemini/sageox/use-team-context.md`.
//     But that requires user action (editing GEMINI.md) — invasive.
//   - The existing GEMINI.md ox-prime-marker injection (handled elsewhere)
//     is the lowest-friction surface today.
//   - `ox agent prime` XML reaches Gemini once it runs prime —
//     agent-agnostic.
//
// Not wired:
//
//   - main.go does NOT reference these handlers and does NOT advertise
//     CapRulesInstaller.
//   - When/if Gemini ships a first-class rules directory, flip wiring.

import (
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleInstallRules(_ adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
	return &adapterprotocol.InstallRulesResponse{
		Installed:    false,
		FilesWritten: nil,
	}, nil
}

func handleCheckRules(_ adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
	return &adapterprotocol.CheckRulesResponse{
		Installed: true,
		Missing:   nil,
		Stale:     nil,
		RulesDir:  "",
	}, nil
}

func handleUninstallRules(_ adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error) {
	return &adapterprotocol.UninstallRulesResponse{
		Uninstalled:  false,
		FilesRemoved: nil,
	}, nil
}
