package main

// installAmpHooks delegates to the external ox-adapter-amp binary.
func installAmpHooks(user bool) error {
	return installExternalAdapterHooks("amp", user)
}

// uninstallAmpHooks delegates to the external ox-adapter-amp binary.
func uninstallAmpHooks(user bool) error {
	return uninstallExternalAdapterHooks("amp", user)
}

// hasAmpHooks delegates to the external ox-adapter-amp binary.
func hasAmpHooks(user bool) bool {
	return checkExternalAdapterHooks("amp", user)
}

// listAmpHooks returns the installation status of Amp CLI hooks.
func listAmpHooks() map[string]bool {
	return listExternalAdapterHooks("amp")
}
