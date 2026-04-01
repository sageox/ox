package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/ui"
)

// CodexHook represents a single hook command for Codex CLI
type CodexHook struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

// CodexHookEntry represents a matcher group containing hooks
type CodexHookEntry struct {
	Matcher string     `json:"matcher"`
	Hooks   []CodexHook `json:"hooks"`
}

// CodexHooksConfig represents the hooks section of .codex/hooks.json
type CodexHooksConfig struct {
	Hooks map[string][]CodexHookEntry `json:"hooks,omitempty"`
}

// codexLifecycleEvents lists the Codex events that get ox agent hook handlers.
var codexLifecycleEvents = []string{
	"SessionStart",
}

// oxHookCommandForCodexEvent returns the ox agent hook shell command for a Codex event.
func oxHookCommandForCodexEvent(event string) string {
	return fmt.Sprintf(constants.OxHookCommandCodexTemplate, event)
}

// codexConfigFileName is the Codex CLI config file.
const codexConfigFileName = "config.toml"

// getCodexConfigPath returns the path to config.toml for Codex CLI.
func getCodexConfigPath(user bool) (string, error) {
	if user {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		return filepath.Join(homeDir, codexUserPath, codexConfigFileName), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	return filepath.Join(cwd, codexProjectPath, codexConfigFileName), nil
}

// ensureCodexHooksFeatureFlag ensures [features] codex_hooks = true is set in
// the project-level .codex/config.toml. Codex gates hook execution behind this
// flag — without it, hooks.json is ignored entirely.
func ensureCodexHooksFeatureFlag(user bool) error {
	path, err := getCodexConfigPath(user)
	if err != nil {
		return err
	}

	// read existing config (preserving other keys)
	var config map[string]interface{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	// ensure [features] section with codex_hooks = true
	features, ok := config["features"].(map[string]interface{})
	if !ok {
		features = make(map[string]interface{})
	}

	if features["codex_hooks"] == true {
		return nil // already set
	}

	features["codex_hooks"] = true
	config["features"] = features

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}
	return os.WriteFile(path, data, perm)
}

// removeCodexHooksFeatureFlag removes the codex_hooks feature flag from
// .codex/config.toml. Does not remove other feature flags or config keys.
func removeCodexHooksFeatureFlag(user bool) error {
	path, err := getCodexConfigPath(user)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil // file doesn't exist, nothing to remove
	}

	var config map[string]interface{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil // can't parse, leave it alone
	}

	features, ok := config["features"].(map[string]interface{})
	if !ok {
		return nil // no features section
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

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}
	return os.WriteFile(path, out, perm)
}

// getCodexHooksPath returns the path to hooks.json for Codex CLI.
func getCodexHooksPath(user bool) (string, error) {
	if user {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		return filepath.Join(homeDir, codexUserPath, codexHooksFileName), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	return filepath.Join(cwd, codexProjectPath, codexHooksFileName), nil
}

// readCodexHooksRaw reads hooks.json preserving all top-level keys.
func readCodexHooksRaw(path string) (*CodexHooksConfig, map[string]json.RawMessage, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &CodexHooksConfig{
			Hooks: make(map[string][]CodexHookEntry),
		}, make(map[string]json.RawMessage), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read hooks file: %w", err)
	}

	if len(data) == 0 {
		return &CodexHooksConfig{
			Hooks: make(map[string][]CodexHookEntry),
		}, make(map[string]json.RawMessage), nil
	}

	// parse into raw map to preserve unknown keys
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, nil, fmt.Errorf("failed to parse hooks file: %w", err)
	}

	var config CodexHooksConfig
	if hooksRaw, ok := rawMap["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &config.Hooks); err != nil {
			return nil, nil, fmt.Errorf("failed to parse hooks section: %w", err)
		}
		delete(rawMap, "hooks")
	} else {
		config.Hooks = make(map[string][]CodexHookEntry)
	}

	return &config, rawMap, nil
}

// writeCodexHooksRaw writes hooks.json merging typed hooks into the raw map.
func writeCodexHooksRaw(path string, config *CodexHooksConfig, rawMap map[string]json.RawMessage, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// merge hooks back into raw map
	outMap := make(map[string]json.RawMessage)
	for k, v := range rawMap {
		outMap[k] = v
	}

	if len(config.Hooks) > 0 {
		hooksData, err := json.Marshal(config.Hooks)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		outMap["hooks"] = hooksData
	}

	data, err := json.MarshalIndent(outMap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hooks file: %w", err)
	}

	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("failed to write hooks file: %w", err)
	}

	return nil
}

// isOxCodexHook checks if a hook command is an ox-managed command.
func isOxCodexHook(cmd string) bool {
	return isOxPrimeCommand(cmd) || isOxHookCommand(cmd)
}

// installCodexHooks installs ox lifecycle hooks to Codex hooks.json
// and enables the codex_hooks feature flag in .codex/config.toml.
func installCodexHooks(user bool) error {
	path, err := getCodexHooksPath(user)
	if err != nil {
		return err
	}

	config, rawMap, err := readCodexHooksRaw(path)
	if err != nil {
		return err
	}

	for _, event := range codexLifecycleEvents {
		oxCmd := oxHookCommandForCodexEvent(event)
		config.Hooks[event] = mergeCodexHookEntries(config.Hooks[event], oxCmd, event)
	}

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}

	if err := writeCodexHooksRaw(path, config, rawMap, perm); err != nil {
		return err
	}

	// enable the codex_hooks feature flag — without it, Codex ignores hooks.json
	if err := ensureCodexHooksFeatureFlag(user); err != nil {
		return fmt.Errorf("hooks.json written but failed to enable feature flag: %w", err)
	}

	location := "project"
	displayPath := filepath.Join(codexProjectPath, codexHooksFileName)
	if user {
		location = "user"
		displayPath = filepath.Join("~/"+codexUserPath, codexHooksFileName)
	}
	fmt.Println(ui.PassStyle.Render("✓") + " Codex CLI " + location + "-level hooks installed at " + displayPath)

	return nil
}

// mergeCodexHookEntries ensures ox hooks are present without duplicating.
func mergeCodexHookEntries(existing []CodexHookEntry, oxCmd string, event string) []CodexHookEntry {
	hasOx := false

	// scan existing entries for ox hooks
	for i, entry := range existing {
		for j, hook := range entry.Hooks {
			if isOxCodexHook(hook.Command) {
				// replace with new ox command
				existing[i].Hooks[j] = CodexHook{
					Type:          hookType,
					Command:       oxCmd,
					StatusMessage: "Loading SageOx context",
				}
				hasOx = true
			}
		}
	}

	// add ox hook if not present
	if !hasOx {
		existing = append(existing, CodexHookEntry{
			Matcher: emptyMatcher,
			Hooks: []CodexHook{
				{
					Type:          hookType,
					Command:       oxCmd,
					StatusMessage: "Loading SageOx context",
				},
			},
		})
	}

	return existing
}

// uninstallCodexHooks removes ox hooks from Codex hooks.json and the codex_hooks
// feature flag from config.toml, preserving other hooks/config.
func uninstallCodexHooks(user bool) error {
	path, err := getCodexHooksPath(user)
	if err != nil {
		return err
	}

	config, rawMap, err := readCodexHooksRaw(path)
	if err != nil {
		return err
	}

	if len(config.Hooks) == 0 {
		fmt.Println("Codex CLI ox hooks not found at " + path)
		return nil
	}

	changed := false
	for eventName, entries := range config.Hooks {
		var filtered []CodexHookEntry
		for _, entry := range entries {
			var filteredHooks []CodexHook
			for _, hook := range entry.Hooks {
				if isOxCodexHook(hook.Command) {
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
			config.Hooks[eventName] = filtered
		} else {
			delete(config.Hooks, eventName)
		}
	}

	if !changed {
		fmt.Println("Codex CLI ox hooks not found at " + path)
		return nil
	}

	// clean up the feature flag if no ox hooks remain
	if len(config.Hooks) == 0 {
		_ = removeCodexHooksFeatureFlag(user)
	}

	// if file is now empty, remove it
	hasOtherKeys := false
	for range rawMap {
		hasOtherKeys = true
		break
	}
	if len(config.Hooks) == 0 && !hasOtherKeys {
		return os.Remove(path)
	}

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}

	return writeCodexHooksRaw(path, config, rawMap, perm)
}

// hasCodexHooks checks if ox hooks are installed in Codex hooks.json.
func hasCodexHooks(user bool) bool {
	path, err := getCodexHooksPath(user)
	if err != nil {
		return false
	}

	config, _, err := readCodexHooksRaw(path)
	if err != nil {
		return false
	}

	for _, entries := range config.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if isOxCodexHook(hook.Command) {
					return true
				}
			}
		}
	}

	return false
}

// listCodexHooks returns the installation status of Codex CLI hooks.
func listCodexHooks() map[string]bool {
	return map[string]bool{
		"Project": hasCodexHooks(false),
		"User":    hasCodexHooks(true),
	}
}
