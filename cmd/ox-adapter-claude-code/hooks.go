package main

import (
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// Hook installation for Claude Code is handled by the built-in
// InstallProjectClaudeHooks (cmd/ox/hooks_claude.go), which writes the
// correct array-of-HookEntry form that current Claude Code expects:
//
//	"hooks": { "PreToolUse": [ { "matcher": "", "hooks": [ ... ] } ] }
//
// This adapter previously wrote the same events as bare strings, e.g.
// `hooks["PostToolUse"] = "..."`, which Claude Code's settings parser
// rejects with "Expected array, but received string" — and on rejection
// it skips the *entire* settings.json, breaking every hook in the repo.
//
// To avoid clobbering the built-in writer, the adapter's hook handlers
// are intentional no-ops. The capability is left declared so the daemon
// protocol surface is unchanged, but every handler reports nothing was
// installed/changed and never touches settings.json.
//
// See ox-kkdi for context.

func handleInstallHooks(_ adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	return &adapterprotocol.InstallHooksResponse{
		Installed: false,
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	// the built-in installer owns this file; report not-installed from the
	// adapter's perspective so callers don't misattribute state to us.
	return &adapterprotocol.CheckHooksResponse{
		Installed: false,
		Scope:     p.Scope,
	}, nil
}

func handleUninstallHooks(_ adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled: true,
	}, nil
}
