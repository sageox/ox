package main

// OpenCode rules support — scaffold (not yet wired into main.go).
//
// State as of May 2026 (research source: opencode.ai/docs/rules/,
// opencode.ai/docs/config/):
//
//   - OpenCode uses AGENTS.md as the project rules file. Lives at the
//     project root (with CLAUDE.md as a fallback for Claude Code
//     compatibility). Single-file model.
//   - Global rules: `~/.config/opencode/AGENTS.md` (with `~/.claude/CLAUDE.md`
//     as fallback).
//   - Project config dir `.opencode/` and global `~/.config/opencode/`
//     have plural subdirectories: agents/, commands/, modes/, plugins/,
//     skills/, tools/, themes/. Note: NONE of these is a `rules/`
//     directory in the modular per-file sense — they're typed bundles.
//   - opencode.json supports glob patterns to combine instruction files
//     ("packages/*/AGENTS.md") but they're consolidated into a single
//     ruleset, not modular per-rule entries.
//   - Per OpenCode docs (opencode.ai/docs/rules/): no dedicated per-file
//     rules system, no `rules/` subdirectory.
//
// Implication for SageOx ox CLI:
//
//   - The existing AGENTS.md / CLAUDE.md ox-prime-marker injection
//     (handled elsewhere) is the only surface today.
//   - `ox agent prime` XML reaches OpenCode once it runs prime —
//     agent-agnostic.
//   - In principle ox could drop SageOx skills under `.opencode/skills/`,
//     but that's a different mechanism (skills, not rules) and worth
//     scoping separately.
//
// Not wired:
//
//   - main.go does NOT reference these handlers and does NOT advertise
//     CapRulesInstaller.
//   - When/if OpenCode ships a first-class modular rules directory, flip
//     wiring.

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
