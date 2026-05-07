// plugin_embed.go bundles the canonical ox-bridge Amp plugin into the
// adapter binary. The plugin is installed user-globally — Amp loads it
// for every project automatically, so it doesn't pollute individual repos.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed plugin/ox-bridge.ts
var oxBridgePluginSrc []byte

// userBridgePluginPath returns the canonical user-scope path for the
// bridge plugin (~/.config/amp/plugins/ox-bridge.ts). Amp's plugin
// loader scans this directory automatically — see
// https://ampcode.com/manual#plugins.
//
// Returns an error rather than silently producing a cwd-relative path
// when the home directory cannot be resolved, so install-hooks won't
// drop the plugin in the wrong location. Tries os.UserHomeDir first
// (honors $HOME on unix, $USERPROFILE on windows), falls back to $HOME
// directly for environments where UserHomeDir is unhappy.
func userBridgePluginPath() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "amp", "plugins", "ox-bridge.ts"), nil
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "amp", "plugins", "ox-bridge.ts"), nil
	}
	return "", fmt.Errorf("cannot determine user home directory")
}
