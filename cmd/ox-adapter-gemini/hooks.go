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
		`if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook %s 2>&1 || true; fi`,
		event,
	)
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

	for _, event := range hookEvents {
		hookCmd := hookCommand(event)
		existing, _ := hooks[event].(string)
		if existing == "" {
			hooks[event] = hookCmd
		} else if !strings.Contains(existing, "ox agent hook") {
			hooks[event] = existing + " && " + hookCmd
		}
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
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

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
	settingsPath, err := resolveSettingsPath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled: true,
		}, nil
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled: true,
		}, nil
	}

	modified := false
	for _, event := range hookEvents {
		hookVal, _ := hooks[event].(string)
		if strings.Contains(hookVal, "ox agent hook") {
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

	resp := &adapterprotocol.UninstallHooksResponse{
		Uninstalled: true,
	}
	if modified {
		resp.FilesModified = []string{settingsPath}
	}
	return resp, nil
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

// removeOxHookFromCommand strips the ox hook portion from a compound hook command.
func removeOxHookFromCommand(cmd string) string {
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
