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

const bridgePluginFile = "ox-bridge.ts"

// userBridgePluginPath returns where Amp loads the bridge plugin from: the
// system plugin dir, $XDG_CONFIG_HOME/amp/plugins/ox-bridge.ts when
// XDG_CONFIG_HOME is set, else ~/.config/amp/plugins/ox-bridge.ts on every
// platform (%USERPROFILE%\.config\amp\plugins\ on Windows). See "System
// plugins" in https://ampcode.com/docs/customize/plugins.
//
// This is the only place that decides the path; install-hooks writes here
// and diagnose checks here, so the two cannot disagree.
//
// A relative XDG_CONFIG_HOME is ignored, as the XDG Base Directory spec
// requires. os.UserConfigDir is deliberately not used: it answers
// ~/Library/Application Support on macOS and %AppData% on Windows, neither of
// which Amp reads.
func userBridgePluginPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "amp", "plugins", bridgePluginFile), nil
	}
	return homeBridgePluginPath()
}

// homeBridgePluginPath returns ~/.config/amp/plugins/ox-bridge.ts regardless
// of XDG_CONFIG_HOME. Before ox honored XDG_CONFIG_HOME it always installed
// here, so diagnose uses it to find a copy Amp never loads.
//
// Returns an error rather than silently producing a cwd-relative path
// when the home directory cannot be resolved, so install-hooks won't
// drop the plugin in the wrong location. Tries os.UserHomeDir first
// (honors $HOME on unix, $USERPROFILE on windows), falls back to $HOME
// directly for environments where UserHomeDir is unhappy.
func homeBridgePluginPath() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "amp", "plugins", bridgePluginFile), nil
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "amp", "plugins", bridgePluginFile), nil
	}
	return "", fmt.Errorf("cannot determine user home directory")
}
