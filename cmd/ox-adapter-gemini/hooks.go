// hooks.go installs ox's session-recording hooks into Gemini CLI settings.
//
// Schema source: the installed Gemini CLI's own bundled docs —
// docs/hooks/index.md and docs/hooks/reference.md in
// @google/gemini-cli 0.36.0. Gemini's hooks block is:
//
//	"hooks": {
//	  "<Event>": [
//	    { "matcher": "<regex|exact>",            // optional
//	      "sequential": false,                   // optional
//	      "hooks": [ { "type": "command",
//	                   "command": "...",
//	                   "name": "...",            // optional
//	                   "timeout": 10000 } ] }    // optional, ms
//	  ]
//	}
//
// Two things this adapter previously got wrong, both user-visible:
//
//  1. It wrote a bare *string* as the event value. Gemini validates settings
//     before it does anything else and refuses to start:
//     "Invalid configuration ... Error in: hooks.PostToolUse — Expected array,
//     received string". That is a bricked CLI, not a silent no-op.
//
//  2. It used Claude Code's event names (PreToolUse / PostToolUse / Stop).
//     Gemini has no such events, so even in the correct array shape they never
//     fire. Gemini's real events are SessionStart, SessionEnd, BeforeAgent,
//     AfterAgent, BeforeModel, AfterModel, BeforeToolSelection, BeforeTool,
//     AfterTool, PreCompress and Notification.
//
// The four installed below are exactly the four that ox's own dispatcher maps
// to lifecycle phases for AGENT_ENV=gemini (see localEventPhases in
// cmd/ox/agent_hook.go), so the names line up on both sides of the boundary.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// hookEvents are Gemini's real event names for the lifecycle phases ox records.
var hookEvents = []string{
	"SessionStart", // -> phase start
	"BeforeAgent",  // -> phase prompt
	"AfterTool",    // -> phase after-tool
	"SessionEnd",   // -> phase end
}

// legacyHookEvents are the Claude Code names earlier ox versions wrote into
// Gemini's settings. They are removed on install and uninstall: at best they
// are dead keys, and in the string form ox used to write they stop the gemini
// CLI from starting at all.
var legacyHookEvents = []string{"PreToolUse", "PostToolUse", "Stop"}

// oxHookMarker identifies a hook entry as ox-owned.
const oxHookMarker = "ox agent hook"

// hookTimeoutMS bounds each hook so a hung ox call cannot wedge the agent loop.
const hookTimeoutMS = 10000

// hookCommand returns the shell command to install for a given event.
//
// stderr is discarded rather than folded into stdout. Gemini parses a hook's
// stdout as JSON and documents that any non-JSON byte there breaks parsing —
// the old command ended in "2>&1", which piped every ox log line straight into
// the channel gemini was trying to parse. stdout is left alone because ox emits
// a systemMessage JSON object on the start phase.
func hookCommand(event string) string {
	return fmt.Sprintf(
		`if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook %s 2>/dev/null || true; fi`,
		event,
	)
}

func hookEntryName(event string) string { return "ox-" + event }

// newHookGroup builds one hook definition in Gemini's documented shape.
func newHookGroup(event string) map[string]any {
	group := map[string]any{
		"hooks": []any{
			map[string]any{
				"name":    hookEntryName(event),
				"type":    "command",
				"command": hookCommand(event),
				"timeout": hookTimeoutMS,
			},
		},
	}
	// tool events filter by tool name; the empty matcher is gemini's
	// documented "match everything"
	if event == "AfterTool" {
		group["matcher"] = ""
	}
	return group
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	settingsPath, err := resolveSettingsPath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	settings, err := loadSettings(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			settings = make(map[string]any)
		} else {
			return nil, fmt.Errorf("failed to read settings: %w", err)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	if err := purgeLegacyHooks(hooks); err != nil {
		return nil, err
	}

	for _, event := range hookEvents {
		groups, err := hookGroups(hooks, event)
		if err != nil {
			return nil, err
		}
		hooks[event] = upsertOxGroup(groups, event)
	}

	settings["hooks"] = hooks

	if err := writeSettings(settingsPath, settings); err != nil {
		return nil, fmt.Errorf("failed to write settings: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{settingsPath},
		Hooks:        hookEvents,
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	settingsPath, err := resolveSettingsPath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope}, nil
	}

	notInstalled := &adapterprotocol.CheckHooksResponse{
		Installed: false,
		Scope:     p.Scope,
		HookFiles: []string{settingsPath},
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return notInstalled, nil
	}

	// an ox-owned legacy key means this settings file was written by an older
	// ox and still needs migrating, even if the new events are all present
	for _, event := range legacyHookEvents {
		if v, present := hooks[event]; present && containsOxHook(v) {
			return notInstalled, nil
		}
	}

	for _, event := range hookEvents {
		groups, ok := hooks[event].([]any)
		if !ok || findOxGroup(groups) < 0 {
			return notInstalled, nil
		}
	}

	return &adapterprotocol.CheckHooksResponse{
		Installed: true,
		Scope:     p.Scope,
		HookFiles: []string{settingsPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	settingsPath, err := resolveSettingsPath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	modified := false
	for _, event := range legacyHookEvents {
		v, present := hooks[event]
		if present && containsOxHook(v) {
			delete(hooks, event)
			modified = true
		}
	}

	for _, event := range hookEvents {
		groups, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			if isOxGroup(g) {
				modified = true
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}

	if modified {
		if len(hooks) == 0 {
			delete(settings, "hooks")
		} else {
			settings["hooks"] = hooks
		}
		if err := writeSettings(settingsPath, settings); err != nil {
			return nil, fmt.Errorf("failed to write settings: %w", err)
		}
	}

	resp := &adapterprotocol.UninstallHooksResponse{Uninstalled: true}
	if modified {
		resp.FilesModified = []string{settingsPath}
	}
	return resp, nil
}

// purgeLegacyHooks removes ox-owned Claude-named keys. A legacy key that is not
// ox-owned is left alone and reported: overwriting a value ox did not write is
// not this installer's call to make.
func purgeLegacyHooks(hooks map[string]any) error {
	for _, event := range legacyHookEvents {
		v, present := hooks[event]
		if !present {
			continue
		}
		if containsOxHook(v) {
			delete(hooks, event)
			continue
		}
		if _, isArray := v.([]any); !isArray {
			return fmt.Errorf(
				"gemini settings has a non-array value at hooks.%s that ox did not write; "+
					"gemini refuses to start with it. Remove or fix hooks.%s by hand, then re-run install",
				event, event)
		}
	}
	return nil
}

// hookGroups returns the existing hook definitions for an event, rejecting a
// foreign value of the wrong type rather than silently discarding it.
func hookGroups(hooks map[string]any, event string) ([]any, error) {
	v, present := hooks[event]
	if !present || v == nil {
		return nil, nil
	}
	if groups, ok := v.([]any); ok {
		return groups, nil
	}
	if containsOxHook(v) {
		// ox's own old malformed string value — drop it
		return nil, nil
	}
	return nil, fmt.Errorf(
		"gemini settings has a non-array value at hooks.%s that ox did not write; "+
			"refusing to overwrite it", event)
}

// upsertOxGroup replaces an existing ox group in place (so the command text
// refreshes on upgrade) or appends a new one.
func upsertOxGroup(groups []any, event string) []any {
	if i := findOxGroup(groups); i >= 0 {
		groups[i] = newHookGroup(event)
		return groups
	}
	return append(groups, newHookGroup(event))
}

func findOxGroup(groups []any) int {
	for i, g := range groups {
		if isOxGroup(g) {
			return i
		}
	}
	return -1
}

func isOxGroup(group any) bool {
	m, ok := group.(map[string]any)
	if !ok {
		return false
	}
	entries, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := em["command"].(string); ok && strings.Contains(cmd, oxHookMarker) {
			return true
		}
		if name, ok := em["name"].(string); ok && strings.HasPrefix(name, "ox-") {
			return true
		}
	}
	return false
}

// containsOxHook reports whether any ox-installed command hides in a value of
// unknown shape — used for legacy keys, which may be a string or an array.
func containsOxHook(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, oxHookMarker)
	case []any:
		for _, g := range t {
			if isOxGroup(g) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func resolveSettingsPath(repoRoot, scope string) (string, error) {
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, ".gemini", "settings.json"), nil
	}
	return filepath.Join(repoRoot, ".gemini", "settings.json"), nil
}

func loadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
