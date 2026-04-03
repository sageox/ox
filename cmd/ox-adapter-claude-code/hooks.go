package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// hook events that ox uses for session recording
var hookEvents = []string{
	"PreToolUse",
	"PostToolUse",
	"Stop",
}

// hookCommand returns the shell command to install for a given event.
func hookCommand(event string) string {
	return fmt.Sprintf(
		`if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent hook %s 2>&1 || true; fi`,
		event,
	)
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	settingsPath := resolveSettingsPath(p.RepoRoot, p.Scope)

	settings, err := loadSettings(settingsPath)
	if err != nil {
		settings = make(map[string]interface{})
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	for _, event := range hookEvents {
		hookCmd := hookCommand(event)
		existing, _ := hooks[event].(string)
		if existing == "" {
			hooks[event] = hookCmd
		} else if !strings.Contains(existing, "ox agent hook") {
			// append to existing hook
			hooks[event] = existing + " && " + hookCmd
		}
		// if already contains ox hook, leave as-is (idempotent)
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

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	// check all required hooks are present
	for _, event := range hookEvents {
		hookVal, _ := hooks[event].(string)
		if !strings.Contains(hookVal, "ox agent hook") {
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

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled: true,
		}, nil
	}

	modified := false
	for _, event := range hookEvents {
		hookVal, _ := hooks[event].(string)
		if strings.Contains(hookVal, "ox agent hook") {
			// remove the ox portion
			cleaned := removeOxHookFromCommand(hookVal)
			if cleaned == "" {
				delete(hooks, event)
			} else {
				hooks[event] = cleaned
			}
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

func resolveSettingsPath(repoRoot, scope string) string {
	if scope == "user" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".claude", "settings.json")
	}
	return filepath.Join(repoRoot, ".claude", "settings.json")
}

func loadSettings(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]interface{}) error {
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

// removeOxHookFromCommand strips the ox hook portion from a compound hook command.
func removeOxHookFromCommand(cmd string) string {
	// split on && and remove parts containing "ox agent hook"
	parts := strings.Split(cmd, "&&")
	var kept []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if !strings.Contains(trimmed, "ox agent hook") {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " && ")
}
