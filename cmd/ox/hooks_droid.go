package main

// installDroidHooks delegates to the external ox-adapter-droid binary.
func installDroidHooks(user bool) error {
	return installExternalAdapterHooks("droid", user)
}

// uninstallDroidHooks delegates to the external ox-adapter-droid binary.
func uninstallDroidHooks(user bool) error {
	return uninstallExternalAdapterHooks("droid", user)
}

// hasDroidHooks delegates to the external ox-adapter-droid binary.
func hasDroidHooks(user bool) bool {
	return checkExternalAdapterHooks("droid", user)
}

// listDroidHooks returns the installation status of Factory Droid hooks.
func listDroidHooks() map[string]bool {
	return listExternalAdapterHooks("droid")
}
