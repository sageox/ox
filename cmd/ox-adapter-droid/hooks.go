package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// hook events that ox uses for session recording.
// Verified against https://docs.factory.ai/reference/hooks-reference
var hookEvents = []string{
	"PreToolUse",
	"PostToolUse",
	"Stop",
}

// hookCommand returns the shell command to install for a given event.
func hookCommand(event string) string {
	return fmt.Sprintf(
		`if command -v ox >/dev/null 2>&1; then AGENT_ENV=droid ox agent hook %s 2>&1 || true; fi`,
		event,
	)
}

// oxHookMarker is the string we search for to identify ox-installed hooks.
const oxHookMarker = "ox agent hook"

// Factory Droid hook format (from https://docs.factory.ai/reference/hooks-reference):
//
//	{"hooks": {"EventName": [{"matcher": "...", "hooks": [{"type": "command", "command": "..."}]}]}}
//
// - matcher is optional (only meaningful for PreToolUse/PostToolUse)
// - hooks[].type is always "command"
// - hooks[].timeout defaults to 60s

// hookRuleEntry is one rule in the event's array.
type hookRuleEntry struct {
	Matcher string         `json:"matcher,omitempty"`
	Hooks   []hookCmdEntry `json:"hooks"`
}

// hookCmdEntry is a single command within a rule.
type hookCmdEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	settingsPath := resolveSettingsPath(p.RepoRoot, p.Scope)

	settings, err := loadSettings(settingsPath)
	if err != nil {
		settings = make(map[string]any)
	}

	hooks := getHooksMap(settings)

	for _, event := range hookEvents {
		cmd := hookCommand(event)
		if eventHasOxHook(hooks, event) {
			continue
		}
		rule := hookRuleEntry{
			Hooks: []hookCmdEntry{{Type: "command", Command: cmd}},
		}
		appendHookRule(hooks, event, rule)
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
	settingsPath := resolveSettingsPath(p.RepoRoot, p.Scope)

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	hooks := getHooksMap(settings)

	for _, event := range hookEvents {
		if !eventHasOxHook(hooks, event) {
			return &adapterprotocol.CheckHooksResponse{
				Installed: false,
				Scope:     p.Scope,
				HookFiles: []string{settingsPath},
			}, nil
		}
	}

	return &adapterprotocol.CheckHooksResponse{
		Installed: true,
		Scope:     p.Scope,
		HookFiles: []string{settingsPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	settingsPath := resolveSettingsPath(p.RepoRoot, p.Scope)

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled: true,
		}, nil
	}

	hooks := getHooksMap(settings)

	modified := false
	for _, event := range hookEvents {
		if removeOxHookRules(hooks, event) {
			modified = true
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

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{settingsPath},
	}, nil
}

// --- hook helpers ---

// getHooksMap extracts or creates the "hooks" map from settings.
func getHooksMap(settings map[string]any) map[string]any {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}
	return hooks
}

// eventHasOxHook checks whether any rule for the given event contains an ox hook command.
func eventHasOxHook(hooks map[string]any, event string) bool {
	rules, ok := hooks[event].([]any)
	if !ok {
		return false
	}
	for _, r := range rules {
		ruleMap, ok := r.(map[string]any)
		if !ok {
			continue
		}
		cmds, ok := ruleMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, c := range cmds {
			cmdMap, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := cmdMap["command"].(string); containsOxHook(cmd) {
				return true
			}
		}
	}
	return false
}

// appendHookRule adds a new rule to the event's rule array.
func appendHookRule(hooks map[string]any, event string, rule hookRuleEntry) {
	existing, _ := hooks[event].([]any)
	// marshal then unmarshal to get map[string]any for consistent JSON handling
	data, _ := json.Marshal(rule)
	var ruleMap map[string]any
	_ = json.Unmarshal(data, &ruleMap)
	hooks[event] = append(existing, ruleMap)
}

// removeOxHookRules removes rules containing ox hook commands from the event.
// Returns true if any rules were removed.
func removeOxHookRules(hooks map[string]any, event string) bool {
	rules, ok := hooks[event].([]any)
	if !ok {
		return false
	}

	var kept []any
	removed := false
	for _, r := range rules {
		ruleMap, ok := r.(map[string]any)
		if !ok {
			kept = append(kept, r)
			continue
		}
		if ruleHasOxHook(ruleMap) {
			removed = true
		} else {
			kept = append(kept, r)
		}
	}

	if !removed {
		return false
	}

	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	return true
}

func ruleHasOxHook(ruleMap map[string]any) bool {
	cmds, ok := ruleMap["hooks"].([]any)
	if !ok {
		return false
	}
	for _, c := range cmds {
		cmdMap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := cmdMap["command"].(string); containsOxHook(cmd) {
			return true
		}
	}
	return false
}

func containsOxHook(s string) bool {
	return len(s) > 0 && contains(s, oxHookMarker)
}

// contains is a simple string search (avoids importing strings for one call).
func contains(s, substr string) bool {
	return len(substr) <= len(s) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- settings I/O ---

func resolveSettingsPath(repoRoot, scope string) string {
	if scope == "user" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".factory", "settings.json")
	}
	return filepath.Join(repoRoot, ".factory", "settings.json")
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
	return os.WriteFile(path, data, 0644)
}
