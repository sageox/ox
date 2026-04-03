package main

// installOpenCodeHooks delegates to the external ox-adapter-opencode binary.
func installOpenCodeHooks(user bool) error {
	return installExternalAdapterHooks("opencode", user)
}

// uninstallOpenCodeHooks delegates to the external ox-adapter-opencode binary.
func uninstallOpenCodeHooks(user bool) error {
	return uninstallExternalAdapterHooks("opencode", user)
}

// hasOpenCodeHooks delegates to the external ox-adapter-opencode binary.
func hasOpenCodeHooks(user bool) bool {
	return checkExternalAdapterHooks("opencode", user)
}

// listOpenCodeHooks returns the installation status of OpenCode plugins.
func listOpenCodeHooks() map[string]bool {
	return listExternalAdapterHooks("opencode")
}

// InstallProjectOpenCodeHooks installs the ox prime plugin to a specific project directory.
// Delegates to the external adapter with the given gitRoot as repo root.
func InstallProjectOpenCodeHooks(gitRoot string) error {
	ea, err := resolveExternalAdapter("opencode")
	if err != nil {
		return err
	}

	result, err := ea.InstallHooks(gitRoot, "project")
	if err != nil {
		return err
	}

	if !result.Installed {
		return nil // adapter reported no-op (already installed)
	}

	return nil
}

// HasProjectOpenCodeHooks checks if ox prime plugin exists in a specific project directory.
// Delegates to the external adapter with the given gitRoot as repo root.
func HasProjectOpenCodeHooks(gitRoot string) bool {
	ea, err := resolveExternalAdapter("opencode")
	if err != nil {
		return false
	}

	result, err := ea.CheckHooks(gitRoot, "project")
	if err != nil {
		return false
	}

	return result.Installed
}
