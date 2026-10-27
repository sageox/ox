// Package usagepane embeds the ox brief pane: a Claude Code mod that shows
// what SageOx gave this session (`ox brief --json`) in a scrollable pane beside
// the chat, opened with /ox-brief. Installing it is the generic Claude mod
// installer's job; this package only names the mod and hands over its files.
package usagepane

import (
	"embed"
	"io/fs"

	"github.com/sageox/ox/internal/claudemod"
)

// The mod's identity in Claude Code: plugin `ox-cli-brief-pane` from the local
// folder marketplace `ox-cli-brief`, which is the mod folder itself. The
// `ox-cli-` prefix marks it as owned by the ox binary (ADR-031): ox rewrites it
// freely. Coworkers see "ox brief" and type /ox-brief.
const (
	PluginName      = "ox-cli-brief-pane"
	MarketplaceName = "ox-cli-brief"
	PluginRef       = PluginName + "@" + MarketplaceName

	// LegacyPluginName and LegacyMarketplaceName are the mod's names before
	// `ox usage` became `ox brief`. ox removes that plugin wherever it finds
	// it (ox doctor --fix, ox init, ox integrate uninstall).
	LegacyPluginName      = "ox-cli-usage-pane"
	LegacyMarketplaceName = "ox-cli-usage"

	// ModRoot is the folder inside ModFS that holds the mod; its
	// .claude-plugin/plugin.json carries a {{VERSION}} placeholder the
	// installer fills with the ox version.
	ModRoot = "mod"
)

//go:embed all:mod
var modFS embed.FS

// Dir is where ox keeps the brief pane mod for Claude Code. Claude reads a
// local folder marketplace in place, so rewriting this folder updates the mod.
func Dir() string {
	return claudemod.Dir("mods", PluginName)
}

// LegacyDir is where an ox from before the rename kept the pane mod
// (LegacyPluginName). Only the cleanup of that old plugin reads it.
func LegacyDir() string {
	return claudemod.Dir("mods", LegacyPluginName)
}

// ModFS returns the embedded mod files, rooted above ModRoot.
func ModFS() fs.FS {
	return modFS
}
