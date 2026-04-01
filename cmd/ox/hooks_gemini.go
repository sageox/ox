package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/ui"
)

// GeminiHook represents a hook configuration for Gemini CLI
type GeminiHook struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// GeminiSettings represents the structure of ~/.gemini/settings.json
type GeminiSettings struct {
	Hooks map[string]GeminiHook `json:"hooks,omitempty"`
}

// geminiLifecycleEvents lists the Gemini CLI events that get ox agent hook handlers.
var geminiLifecycleEvents = []string{
	"SessionStart", // session initialization (PhaseStart)
	"BeforeAgent",  // whisper delivery (PhasePrompt)
	"AfterTool",    // incremental drain (PhaseAfterTool)
	"SessionEnd",   // session teardown (PhaseEnd)
}

// oxHookCommandForGeminiEvent returns the ox agent hook shell command for a Gemini event.
func oxHookCommandForGeminiEvent(event string) string {
	return fmt.Sprintf(constants.OxHookCommandGeminiTemplate, event)
}

// isOxGeminiHook checks if a command is any ox-managed command (prime or hook).
func isOxGeminiHook(cmd string) bool {
	return isOxPrimeCommand(cmd) || isOxHookCommand(cmd)
}

// getGeminiSettingsPath returns the path to Gemini CLI settings.json
func getGeminiSettingsPath(user bool) (string, error) {
	if user {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		return filepath.Join(homeDir, geminiUserPath, geminiSettingsFileName), nil
	}
	// project-level
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	return filepath.Join(cwd, geminiProjectPath, geminiSettingsFileName), nil
}

// readGeminiSettingsRaw reads Gemini CLI settings.json preserving all top-level keys.
// Returns typed hooks and a raw map of everything else, preventing data loss
// when writing back (e.g., preserving non-hook keys alongside "hooks").
func readGeminiSettingsRaw(path string) (*GeminiSettings, map[string]json.RawMessage, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &GeminiSettings{
			Hooks: make(map[string]GeminiHook),
		}, make(map[string]json.RawMessage), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	if len(data) == 0 {
		return &GeminiSettings{
			Hooks: make(map[string]GeminiHook),
		}, make(map[string]json.RawMessage), nil
	}

	// parse into raw map to preserve unknown keys
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, nil, fmt.Errorf("failed to parse settings file: %w", err)
	}

	var settings GeminiSettings
	if hooksRaw, ok := rawMap["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			return nil, nil, fmt.Errorf("failed to parse hooks section: %w", err)
		}
		delete(rawMap, "hooks")
	} else {
		settings.Hooks = make(map[string]GeminiHook)
	}

	return &settings, rawMap, nil
}

// readGeminiSettings reads and parses Gemini CLI settings.json (convenience wrapper).
func readGeminiSettings(user bool) (*GeminiSettings, error) {
	path, err := getGeminiSettingsPath(user)
	if err != nil {
		return nil, err
	}
	settings, _, err := readGeminiSettingsRaw(path)
	return settings, err
}

// writeGeminiSettingsRaw writes settings back, merging typed hooks into the raw map
// to preserve all non-hook keys.
func writeGeminiSettingsRaw(path string, settings *GeminiSettings, rawMap map[string]json.RawMessage, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// merge hooks back into raw map
	outMap := make(map[string]json.RawMessage)
	for k, v := range rawMap {
		outMap[k] = v
	}

	if len(settings.Hooks) > 0 {
		hooksData, err := json.Marshal(settings.Hooks)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		outMap["hooks"] = hooksData
	}

	data, err := json.MarshalIndent(outMap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

// hasGeminiHooks checks if ox lifecycle hooks are installed in Gemini CLI settings.
// Returns true only if ALL lifecycle events have an ox hook.
func hasGeminiHooks(user bool) bool {
	path, err := getGeminiSettingsPath(user)
	if err != nil {
		return false
	}

	settings, _, err := readGeminiSettingsRaw(path)
	if err != nil {
		return false
	}

	for _, event := range geminiLifecycleEvents {
		hook, exists := settings.Hooks[event]
		if !exists || hook.Command != oxHookCommandForGeminiEvent(event) {
			return false
		}
	}
	return true
}

// installGeminiHooks installs ox lifecycle hooks for Gemini CLI.
// Replaces legacy prime-only hooks with full lifecycle hooks.
func installGeminiHooks(user bool) error {
	path, err := getGeminiSettingsPath(user)
	if err != nil {
		return err
	}

	settings, rawMap, err := readGeminiSettingsRaw(path)
	if err != nil {
		return err
	}

	// check if all lifecycle hooks are already installed with current commands
	allCurrent := true
	for _, event := range geminiLifecycleEvents {
		hook, exists := settings.Hooks[event]
		expected := oxHookCommandForGeminiEvent(event)
		if !exists || hook.Command != expected {
			allCurrent = false
			break
		}
	}

	if allCurrent {
		fmt.Println(ui.PassStyle.Render("✓") + " Gemini CLI hooks already installed at " + path)
		return nil
	}

	// remove legacy prime-only SessionStart hook if present
	if hook, exists := settings.Hooks[geminiSessionStart]; exists && isOxPrimeCommand(hook.Command) {
		delete(settings.Hooks, geminiSessionStart)
	}

	// install all lifecycle hooks
	for _, event := range geminiLifecycleEvents {
		settings.Hooks[event] = GeminiHook{
			Command: oxHookCommandForGeminiEvent(event),
			Timeout: defaultHookTimeout,
		}
	}

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}

	return writeGeminiSettingsRaw(path, settings, rawMap, perm)
}

// uninstallGeminiHooks removes ox lifecycle hooks from Gemini CLI settings.
// Removes both legacy prime hooks and new lifecycle hooks.
func uninstallGeminiHooks(user bool) error {
	path, err := getGeminiSettingsPath(user)
	if err != nil {
		return err
	}

	settings, rawMap, err := readGeminiSettingsRaw(path)
	if err != nil {
		return err
	}

	// collect all events to check (lifecycle + legacy SessionStart)
	allEvents := make(map[string]bool)
	for _, event := range geminiLifecycleEvents {
		allEvents[event] = true
	}
	allEvents[geminiSessionStart] = true // ensure legacy is covered

	changed := false
	for event := range allEvents {
		hook, exists := settings.Hooks[event]
		if exists && isOxGeminiHook(hook.Command) {
			delete(settings.Hooks, event)
			changed = true
		}
	}

	if !changed {
		fmt.Println("Gemini CLI ox hooks not found at " + path)
		return nil
	}

	// if file is now effectively empty (no hooks and no other keys), remove it
	hasOtherKeys := false
	for range rawMap {
		hasOtherKeys = true
		break
	}
	if len(settings.Hooks) == 0 && !hasOtherKeys {
		return os.Remove(path)
	}

	perm := os.FileMode(settingsPerm)
	if !user {
		perm = sharedSettingsPerm
	}

	return writeGeminiSettingsRaw(path, settings, rawMap, perm)
}

// listGeminiHooks returns the installation status of Gemini CLI hooks
func listGeminiHooks() map[string]bool {
	return map[string]bool{
		"Project": hasGeminiHooks(false),
		"User":    hasGeminiHooks(true),
	}
}
