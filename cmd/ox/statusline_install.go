package main

import (
	"fmt"
	"os"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// The ox status line is a per-coworker preference (`ox config set statusline
// off`), so it is installed in the gitignored .claude/settings.local.json,
// never in the shared, committed .claude/settings.json. Claude Code reserves
// the status-line row whenever a statusLine key is present, even one whose
// command prints nothing, and a local `"statusLine": null` does not unset a
// shared one. The only way "off" can make the row disappear is for the key
// to be absent from the coworker's own files.

// syncProjectStatusLine brings a project's Claude Code settings in line with
// the coworker's statusline setting: on installs ox's statusLine in
// settings.local.json (or upgrades a stale one), off removes it. An ox
// statusLine in the shared settings.json, where earlier builds put it, is
// moved out so it stops forcing the row on every coworker. A foreign
// statusLine in either file is never touched: settings.local.json outranks
// settings.json in Claude Code, so a local ox row would hide a status line the
// project itself ships, and ox is not installed over one. It reports whether
// any file changed.
func syncProjectStatusLine(gitRoot string) (bool, error) {
	changed := false

	shared, sharedMap, err := readSharedClaudeSettings(gitRoot)
	if err != nil {
		return false, err
	}
	if claude.RemoveOxStatusLine(sharedMap) {
		if err := writeSharedClaudeSettings(gitRoot, shared, sharedMap); err != nil {
			return false, err
		}
		changed = true
	}

	localPath := getLocalClaudeSettingsPath(gitRoot)
	local, localMap, err := readSettingsFileRaw(localPath)
	if err != nil {
		return changed, err
	}
	var localChanged bool
	switch {
	case statuslineEnabled() && claude.ClassifyStatusLine(sharedMap) == claude.StatusLineForeign:
		// The project ships its own status line; installing ours locally
		// would override it for this coworker.
	case statuslineEnabled():
		localChanged, err = claude.EnsureOxStatusLine(localMap)
		if err != nil {
			return changed, fmt.Errorf("set status line: %w", err)
		}
	default:
		localChanged = claude.RemoveOxStatusLine(localMap)
	}
	if !localChanged {
		return changed, nil
	}
	if len(localMap) == 0 && len(local.Hooks) == 0 {
		if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
			return changed, err
		}
		return true, nil
	}
	if err := writeSettingsFileRaw(localPath, local, localMap, settingsPerm); err != nil {
		return changed, err
	}
	return true, nil
}

// applyStatuslineSetting is run after `ox config set|unset statusline` so the
// change shows in the current project without waiting for `ox doctor`. It is
// best-effort: outside a project with ox's Claude Code hooks there is nothing
// to update.
func applyStatuslineSetting() {
	gitRoot := findGitRoot()
	if gitRoot == "" || !HasProjectClaudeHooks(gitRoot) {
		return
	}
	if _, err := syncProjectStatusLine(gitRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", getLocalClaudeSettingsPath(gitRoot), err)
	}
}
