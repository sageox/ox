// plugin_embed.go bundles the canonical ox-bridge Amp plugin into the
// adapter binary. The plugin is installed user-globally — Amp loads it
// for every project automatically, so it doesn't pollute individual repos.
package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed plugin/ox-bridge.ts
var oxBridgePluginSrc []byte

// userBridgePluginPath returns the canonical user-scope path for the
// bridge plugin. Amp's plugin loader scans this directory automatically
// (see https://ampcode.com/manual#plugins).
func userBridgePluginPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "amp", "plugins", "ox-bridge.ts")
}
