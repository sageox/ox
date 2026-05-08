package main

// Aider rules support — scaffold (not yet wired into main.go).
//
// State as of May 2026 (research source: aider.chat/docs/usage/conventions.html,
// github.com/Aider-AI/conventions):
//
//   - Aider uses CONVENTIONS.md (or any file referenced via the --read flag).
//     Loaded into every chat as standing instructions. Single-file model.
//   - Configurable via .aider.conf.yml `read: CONVENTIONS.md` (or list).
//     Multiple files via the list form, but each is loaded whole — no
//     modular per-rule scheme.
//   - Discussion in 2026 around adopting AGENTS.md as the cross-tool
//     standard (Aider issue #4363) but no modular rules/ directory.
//
// Implication for SageOx ox CLI:
//
//   - The existing CONVENTIONS.md / .aider.conf.yml integration (handled
//     elsewhere in this adapter) is the only delivery surface today.
//   - In principle ox could drop a SageOx-namespace file in the repo and
//     append a `read:` entry to .aider.conf.yml, but that touches user
//     config and is invasive for marginal benefit. Deferring until Aider
//     gains a modular rules surface or a SageOx user explicitly asks.
//   - `ox agent prime` XML still reaches Aider once it runs prime —
//     agent-agnostic.
//
// Not wired:
//
//   - main.go does NOT reference these handlers and does NOT advertise
//     CapRulesInstaller. Honest about lack of support.

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
