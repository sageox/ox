package main

// Amp (Sourcegraph) rules support — scaffold (not yet wired into main.go).
//
// State as of May 2026 (research source: sourcegraph.com/amp,
// github.com/sourcegraph/amp-examples-and-guides):
//
//   - Amp uses AGENT.md (and AGENTS.md plural form, per the cross-tool
//     standard) at the project root. Single-file conventions; Amp reads
//     the file at the start of every Thread.
//   - Amp supports MULTIPLE AGENTS.md files for large codebases — one
//     general-purpose at root plus per-subdirectory files. It also
//     supports `@file.md` references inside AGENTS.md to pull in
//     additional documentation.
//   - There is NO dedicated modular rules/ directory (no
//     `.amp/rules/<topic>.md` per-rule scheme).
//
// Implication for SageOx ox CLI:
//
//   - The existing AGENTS.md ox-prime-marker injection (handled elsewhere
//     in this adapter) is the only surface today.
//   - `ox agent prime` XML reaches Amp once Amp runs prime — agent-agnostic.
//   - This scaffold exists so that IF Amp adds a modular `.amp/rules/`
//     directory in the future, wiring is trivial: implement the handlers,
//     advertise CapRulesInstaller in main.go.
//
// Not wired:
//
//   - main.go does NOT reference these handlers and does NOT advertise
//     CapRulesInstaller. The handlers compile but are never invoked.
//   - Honest: ox doctor does not claim rules are installable on Amp today.

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
