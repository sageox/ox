// Package usagepane embeds the ox usage pane: a Claude Code mod that shows
// what ox did this session (`ox usage --json`) in a scrollable pane beside the
// chat, opened with /ox-usage. Installing it is the generic Claude mod
// installer's job; this package only names the mod and hands over its files.
package usagepane

import (
	"embed"
	"io/fs"

	"github.com/sageox/ox/internal/claudemod"
)

// The mod's identity in Claude Code: plugin `ox-cli-usage-pane` from the local
// folder marketplace `ox-cli-usage`, which is the mod folder itself. The
// `ox-cli-` prefix marks it as owned by the ox binary (ADR-031): ox rewrites it
// freely. Coworkers see "ox usage" and type /ox-usage.
const (
	PluginName      = "ox-cli-usage-pane"
	MarketplaceName = "ox-cli-usage"
	PluginRef       = PluginName + "@" + MarketplaceName

	// ModRoot is the folder inside ModFS that holds the mod; its
	// .claude-plugin/plugin.json carries a {{VERSION}} placeholder the
	// installer fills with the ox version.
	ModRoot = "mod"
)

//go:embed all:mod
var modFS embed.FS

// Dir is where ox keeps the usage pane mod for Claude Code. Claude reads a
// local folder marketplace in place, so rewriting this folder updates the mod.
func Dir() string {
	return claudemod.Dir("mods", PluginName)
}

// ModFS returns the embedded mod files, rooted above ModRoot.
func ModFS() fs.FS {
	return modFS
}
