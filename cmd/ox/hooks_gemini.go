package main

// installGeminiHooks delegates to the external ox-adapter-gemini binary.
func installGeminiHooks(user bool) error {
	return installExternalAdapterHooks("gemini", user)
}

// uninstallGeminiHooks delegates to the external ox-adapter-gemini binary.
func uninstallGeminiHooks(user bool) error {
	return uninstallExternalAdapterHooks("gemini", user)
}

// hasGeminiHooks delegates to the external ox-adapter-gemini binary.
func hasGeminiHooks(user bool) bool {
	return checkExternalAdapterHooks("gemini", user)
}

// listGeminiHooks returns the installation status of Gemini CLI hooks.
func listGeminiHooks() map[string]bool {
	return listExternalAdapterHooks("gemini")
}
