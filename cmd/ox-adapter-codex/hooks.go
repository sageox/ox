package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

const (
	codexProjectPath   = ".codex"
	codexUserPath      = ".codex"
	codexHooksFileName = "hooks.json"
	codexConfigFile    = "config.toml"
)

// all Codex CLI hook events ox should install handlers for
var codexHookEvents = []string{
	"SessionStart",
	"PreToolUse",
	"PostToolUse",
	"UserPromptSubmit",
	"Stop",
}

// hookCommand returns the shell command for a given event.
func hookCommand(event string) string {
	return fmt.Sprintf(
		`if command -v ox >/dev/null 2>&1; then AGENT_ENV=codex ox agent hook %s 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi`,
		event,
	)
}

// --- hooks.json types ---

type codexHookEntry struct {
	Matcher string      `json:"matcher"`
	Hooks   []codexHook `json:"hooks"`
}

type codexHook struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	StatusMessage string `json:"statusMessage,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
}

// --- install ---

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	hooksPath := resolveHooksPath(p.RepoRoot, p.Scope)

	hooksMap, rawMap, err := readHooksFile(hooksPath)
	if err != nil {
		return nil, err
	}

	for _, event := range codexHookEvents {
		cmd := hookCommand(event)
		hooksMap[event] = mergeHookEntries(hooksMap[event], cmd, event)
	}

	if err := writeHooksFile(hooksPath, hooksMap, rawMap, p.Scope); err != nil {
		return nil, err
	}

	// enable the codex_hooks feature flag
	if err := ensureFeatureFlag(p.RepoRoot, p.Scope); err != nil {
		return nil, fmt.Errorf("hooks.json written but failed to enable feature flag: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{hooksPath},
		Hooks:        codexHookEvents,
	}, nil
}

// --- check ---

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	hooksPath := resolveHooksPath(p.RepoRoot, p.Scope)

	hooksMap, _, err := readHooksFile(hooksPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	for _, event := range codexHookEvents {
		if !eventHasOxHook(hooksMap[event]) {
			return &adapterprotocol.CheckHooksResponse{
				Installed: false,
				Scope:     p.Scope,
				HookFiles: []string{hooksPath},
			}, nil
		}
	}

	return &adapterprotocol.CheckHooksResponse{
		Installed: true,
		Scope:     p.Scope,
		HookFiles: []string{hooksPath},
	}, nil
}

// --- uninstall ---

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	hooksPath := resolveHooksPath(p.RepoRoot, p.Scope)

	hooksMap, rawMap, err := readHooksFile(hooksPath)
	if err != nil {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	changed := false
	for eventName, entries := range hooksMap {
		var filtered []codexHookEntry
		for _, entry := range entries {
			var filteredHooks []codexHook
			for _, hook := range entry.Hooks {
				if isOxCommand(hook.Command) {
					changed = true
					continue
				}
				filteredHooks = append(filteredHooks, hook)
			}
			if len(filteredHooks) > 0 {
				entry.Hooks = filteredHooks
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) > 0 {
			hooksMap[eventName] = filtered
		} else {
			delete(hooksMap, eventName)
		}
	}

	if !changed {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	// remove feature flag if no ox hooks remain
	if len(hooksMap) == 0 {
		_ = removeFeatureFlag(p.RepoRoot, p.Scope)
	}

	// if file is now empty, remove it
	if len(hooksMap) == 0 && len(rawMap) == 0 {
		_ = os.Remove(hooksPath)
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled:   true,
			FilesModified: []string{hooksPath},
		}, nil
	}

	if err := writeHooksFile(hooksPath, hooksMap, rawMap, p.Scope); err != nil {
		return nil, err
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{hooksPath},
	}, nil
}

// --- helpers ---

func resolveHooksPath(repoRoot, scope string) string {
	if scope == "user" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, codexUserPath, codexHooksFileName)
	}
	if repoRoot != "" {
		return filepath.Join(repoRoot, codexProjectPath, codexHooksFileName)
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, codexProjectPath, codexHooksFileName)
}

func resolveConfigPath(repoRoot, scope string) string {
	if scope == "user" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, codexUserPath, codexConfigFile)
	}
	if repoRoot != "" {
		return filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, codexProjectPath, codexConfigFile)
}

func readHooksFile(path string) (map[string][]codexHookEntry, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string][]codexHookEntry), make(map[string]json.RawMessage), nil
	}
	if len(data) == 0 {
		return make(map[string][]codexHookEntry), make(map[string]json.RawMessage), nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, nil, fmt.Errorf("failed to parse hooks file: %w", err)
	}

	hooksMap := make(map[string][]codexHookEntry)
	if hooksRaw, ok := rawMap["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
			return nil, nil, fmt.Errorf("failed to parse hooks section: %w", err)
		}
		delete(rawMap, "hooks")
	}

	return hooksMap, rawMap, nil
}

func writeHooksFile(path string, hooksMap map[string][]codexHookEntry, rawMap map[string]json.RawMessage, scope string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	outMap := make(map[string]json.RawMessage)
	for k, v := range rawMap {
		outMap[k] = v
	}
	if len(hooksMap) > 0 {
		hooksData, err := json.Marshal(hooksMap)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		outMap["hooks"] = hooksData
	}

	data, err := json.MarshalIndent(outMap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hooks file: %w", err)
	}

	perm := os.FileMode(0644)
	if scope == "user" {
		perm = 0600
	}
	return os.WriteFile(path, data, perm)
}

func mergeHookEntries(existing []codexHookEntry, oxCmd string, event string) []codexHookEntry {
	hasOx := false
	for i, entry := range existing {
		for j, hook := range entry.Hooks {
			if isOxCommand(hook.Command) {
				existing[i].Hooks[j] = codexHook{
					Type:          "command",
					Command:       oxCmd,
					StatusMessage: statusMessageForEvent(event),
				}
				hasOx = true
			}
		}
	}

	if !hasOx {
		existing = append(existing, codexHookEntry{
			Matcher: "",
			Hooks: []codexHook{
				{
					Type:          "command",
					Command:       oxCmd,
					StatusMessage: statusMessageForEvent(event),
				},
			},
		})
	}

	return existing
}

func statusMessageForEvent(event string) string {
	switch event {
	case "SessionStart":
		return "Loading SageOx context"
	default:
		return ""
	}
}

func eventHasOxHook(entries []codexHookEntry) bool {
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if isOxCommand(hook.Command) {
				return true
			}
		}
	}
	return false
}

func isOxCommand(cmd string) bool {
	return strings.Contains(cmd, "ox agent hook") || strings.Contains(cmd, "ox agent prime")
}

// --- config.toml feature flag ---

func ensureFeatureFlag(repoRoot, scope string) error {
	path := resolveConfigPath(repoRoot, scope)

	var config map[string]any
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}
	if config == nil {
		config = make(map[string]any)
	}

	features, ok := config["features"].(map[string]any)
	if !ok {
		features = make(map[string]any)
	}

	if features["codex_hooks"] == true {
		return nil
	}

	features["codex_hooks"] = true
	config["features"] = features

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	perm := os.FileMode(0644)
	if scope == "user" {
		perm = 0600
	}
	return os.WriteFile(path, data, perm)
}

func removeFeatureFlag(repoRoot, scope string) error {
	path := resolveConfigPath(repoRoot, scope)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil
	}

	features, ok := config["features"].(map[string]any)
	if !ok {
		return nil
	}

	delete(features, "codex_hooks")
	if len(features) == 0 {
		delete(config, "features")
	} else {
		config["features"] = features
	}

	if len(config) == 0 {
		return os.Remove(path)
	}

	out, err := toml.Marshal(config)
	if err != nil {
		return err
	}

	perm := os.FileMode(0644)
	if scope == "user" {
		perm = 0600
	}
	return os.WriteFile(path, out, perm)
}
