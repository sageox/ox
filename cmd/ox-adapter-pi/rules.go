package main

// Pi (mariozechner/badlogic) rules support — scaffold (not wired into main.go).
//
// State as of May 2026 (research source:
// github.com/badlogic/pi-mono, github.com/can1357/oh-my-pi):
//
//   - Pi loads AGENTS.md (and CLAUDE.md) from `~/.pi/agent/`, parent
//     directories, and the current directory at startup. Single-file
//     conventions; no modular per-rule scheme.
//   - System prompt customization via SYSTEM.md and APPEND_SYSTEM.md.
//     Settings via `~/.pi/agent/settings.json` and `.pi/settings.json`.
//   - Pi does have a SKILL.md format with frontmatter and discovery
//     rules — that's a SKILL system, distinct from per-file behavioral
//     rules in the .claude/rules/ sense.
//   - There is NO `~/.pi/rules/` or `.pi/rules/` directory for modular
//     behavioral rules.
//
// Implication for SageOx ox CLI:
//
//   - The existing AGENTS.md ox-prime-marker injection (handled elsewhere
//     in this adapter) is the only delivery surface today.
//   - `ox agent prime` XML reaches Pi once it runs prime — agent-agnostic.
//   - Pi's skill system could in principle host a SageOx skill, but
//     that's a separate scope and a different lifecycle (skills, not
//     rules).
//
// Not wired:
//
//   - main.go does NOT reference these handlers and does NOT advertise
//     CapRulesInstaller. Honest about lack of support.
//   - When/if Pi ships a first-class modular rules directory, flip wiring.

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
