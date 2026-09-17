package main

const codexHookTrustHint = "In Codex, run /hooks to review and trust the SageOx hooks. Send a prompt, then run ox doctor --check to verify hook execution, the capture daemon, and captured entries. Hook installation alone does not confirm recording."

// installCodexHooks delegates to the external ox-adapter-codex binary.
func installCodexHooks(user bool) error {
	return installExternalAdapterHooks("codex", user)
}

// uninstallCodexHooks delegates to the external ox-adapter-codex binary.
func uninstallCodexHooks(user bool) error {
	return uninstallExternalAdapterHooks("codex", user)
}

// hasCodexHooks delegates to the external ox-adapter-codex binary.
func hasCodexHooks(user bool) bool {
	return checkExternalAdapterHooks("codex", user)
}

// listCodexHooks returns the installation status of Codex CLI hooks.
func listCodexHooks() map[string]bool {
	return listExternalAdapterHooks("codex")
}
