package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
	claude "github.com/sageox/ox/internal/hooks/claude"

	"github.com/sageox/ox/internal/constants"
)

// ClaudeHook is an alias for the extracted type.
type ClaudeHook = claude.Hook

// ClaudeHookEntry is an alias for the extracted type.
type ClaudeHookEntry = claude.HookEntry

// ClaudeSettings is an alias for the extracted type.
type ClaudeSettings = claude.Settings

// readSettingsFileRaw reads a settings file preserving all top-level keys.
// Returns typed hooks and a raw map of everything else, preventing data loss
// when writing back (e.g., preserving "permissions" alongside "hooks").
func readSettingsFileRaw(path string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	return readSettingsFileRawFrom(os.ReadFile, path)
}

func readSettingsFileRawFrom(readFile func(string) ([]byte, error), path string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	data, err := readFile(path)
	if os.IsNotExist(err) {
		return &ClaudeSettings{
			Hooks: make(map[string][]ClaudeHookEntry),
		}, make(map[string]json.RawMessage), nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	settings, rawMap, err := claude.ParseSettingsRaw(data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse settings file: %w", err)
	}

	return settings, rawMap, nil
}

// writeSettingsFileRaw writes settings back, merging typed hooks into the raw map
// to preserve all non-hook keys (permissions, etc.).
func writeSettingsFileRaw(path string, settings *ClaudeSettings, rawMap map[string]json.RawMessage, perm os.FileMode) error {
	// ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := claude.MarshalSettings(settings, rawMap)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

func getClaudeSettingsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, claudeDirName, claudeSettingsFile), nil
}

func readClaudeSettings() (*ClaudeSettings, error) {
	settingsPath, err := getClaudeSettingsPath()
	if err != nil {
		return nil, err
	}

	// create settings file if it doesn't exist
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		// create .claude directory if needed
		claudeDir := filepath.Dir(settingsPath)
		if err := os.MkdirAll(claudeDir, dirPerm); err != nil {
			return nil, fmt.Errorf("failed to create .claude directory: %w", err)
		}

		// return empty settings
		return &ClaudeSettings{
			Hooks: make(map[string][]ClaudeHookEntry),
		}, nil
	}

	// read existing settings
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	// handle empty file
	if len(data) == 0 {
		return &ClaudeSettings{
			Hooks: make(map[string][]ClaudeHookEntry),
		}, nil
	}

	var settings ClaudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings file: %w", err)
	}

	// ensure hooks map exists
	if settings.Hooks == nil {
		settings.Hooks = make(map[string][]ClaudeHookEntry)
	}

	return &settings, nil
}

func writeClaudeSettings(settings *ClaudeSettings) error {
	settingsPath, err := getClaudeSettingsPath()
	if err != nil {
		return err
	}

	// marshal with indentation for readability
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// write to file
	if err := os.WriteFile(settingsPath, data, settingsPerm); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

// delegates to extracted pure functions
func hasOxPrimeHook(entry ClaudeHookEntry) bool { return claude.HasOxPrimeHook(entry) }
func hasAnyOxHook(entry ClaudeHookEntry) bool   { return claude.HasAnyOxHook(entry) }
func isOxPrimeCommand(cmd string) bool          { return claude.IsOxPrimeCommand(cmd) }
func isOxHookCommand(cmd string) bool           { return claude.IsOxHookCommand(cmd) }
func isAnyOxCommand(cmd string) bool            { return claude.IsAnyOxCommand(cmd) }
func removeOxPrimeHook(entry *ClaudeHookEntry)  { claude.RemoveOxPrimeHook(entry) }
func removeAnyOxHook(entry *ClaudeHookEntry)    { claude.RemoveAnyOxHook(entry) }

func mergeHookEntries(existing, new []ClaudeHookEntry) []ClaudeHookEntry {
	return claude.MergeHookEntries(existing, new)
}

func uninstallClaudeHooks() error {
	settings, err := readClaudeSettings()
	if err != nil {
		return err
	}

	// uninstall from both legacy events and all lifecycle events
	allEvents := append([]string{claudeSessionStart, claudePreCompact}, claudeLifecycleEvents...)
	// deduplicate
	seen := make(map[string]bool)
	var hookEvents []string
	for _, e := range allEvents {
		if !seen[e] {
			seen[e] = true
			hookEvents = append(hookEvents, e)
		}
	}

	for _, eventName := range hookEvents {
		entries := settings.Hooks[eventName]

		for i := range entries {
			removeAnyOxHook(&entries[i])
		}

		// remove empty entries
		filtered := make([]ClaudeHookEntry, 0)
		for _, entry := range entries {
			if len(entry.Hooks) > 0 {
				filtered = append(filtered, entry)
			}
		}

		if len(filtered) > 0 {
			settings.Hooks[eventName] = filtered
		} else {
			delete(settings.Hooks, eventName)
		}
	}

	return writeClaudeSettings(settings)
}

func listClaudeHooks() (map[string]bool, error) {
	settings, err := readClaudeSettings()
	if err != nil {
		return nil, err
	}

	status := make(map[string]bool)
	hookEvents := []string{claudeSessionStart, claudePreCompact}

	for _, eventName := range hookEvents {
		installed := false
		entries := settings.Hooks[eventName]

		for _, entry := range entries {
			if hasAnyOxHook(entry) {
				installed = true
				break
			}
		}

		status[eventName] = installed
	}

	return status, nil
}

// hasUserLevelOxPrime checks if the ox:prime marker exists in the user-level
// context file for the detected agent (defaults to Claude Code).
func hasUserLevelOxPrime() bool {
	return hasUserLevelAgentMarker(detectActiveAgent())
}

// getSharedClaudeSettingsPath returns the path to .claude/settings.json (shared, git-tracked).
func getSharedClaudeSettingsPath(gitRoot string) string {
	return filepath.Join(gitRoot, projectClaudeSettingsFile)
}

// getLocalClaudeSettingsPath returns the path to .claude/settings.local.json (gitignored, personal).
func getLocalClaudeSettingsPath(gitRoot string) string {
	return filepath.Join(gitRoot, projectClaudeLocalSettingsFile)
}

const projectClaudeSettingsFile = ".claude/settings.json"
const projectClaudeLocalSettingsFile = ".claude/settings.local.json"

func readProjectSettingsFileRaw(gitRoot, name string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	root, err := os.OpenRoot(gitRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	return readSettingsFileRawFrom(root.ReadFile, name)
}

// Keep creation and replacement relative to the opened checkout, not a joined
// pathname: both .claude and the settings file can be repository symlinks.
func writeSettingsFileRawInRoot(root *os.Root, name string, settings *ClaudeSettings, rawMap map[string]json.RawMessage, perm os.FileMode) error {
	data, err := claude.MarshalSettings(settings, rawMap)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(name), dirPerm); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}
	return fileutil.AtomicWriteBytesInRoot(root, name, data, perm)
}

// readSharedClaudeSettings reads .claude/settings.json using raw-preserving parse.
func readSharedClaudeSettings(gitRoot string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	return readProjectSettingsFileRaw(gitRoot, projectClaudeSettingsFile)
}

// writeSharedClaudeSettings writes .claude/settings.json using raw-preserving write.
func writeSharedClaudeSettings(gitRoot string, settings *ClaudeSettings, rawMap map[string]json.RawMessage) error {
	root, err := os.OpenRoot(gitRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeSettingsFileRawInRoot(root, projectClaudeSettingsFile, settings, rawMap, sharedSettingsPerm)
}

// readProjectClaudeSettings reads .claude/settings.json (shared) from the project.
// Falls back to settings.local.json during migration period.
func readProjectClaudeSettings(gitRoot string) (*ClaudeSettings, error) {
	settings, _, err := readSharedClaudeSettings(gitRoot)
	if err != nil {
		return nil, err
	}

	// if shared file has ox hooks, use it
	if len(settings.Hooks) > 0 {
		return settings, nil
	}

	// fall back to local file for migration period
	localSettings, _, err := readProjectSettingsFileRaw(gitRoot, projectClaudeLocalSettingsFile)
	if err != nil {
		return settings, nil // return empty shared settings on local read error
	}

	return localSettings, nil
}

// claudeLifecycleEvents lists all Claude Code events that get ox agent hook handlers.
var claudeLifecycleEvents = []string{
	"SessionStart",
	"PreCompact",
	"PostToolUse",
	"Stop",
	"SessionEnd",
	"UserPromptSubmit",
}

// oxHookCommandForEvent returns the ox agent hook shell command for a Claude Code event.
func oxHookCommandForEvent(event string) string {
	return constants.HookCommand(constants.OxHookCommandClaudeCodeTemplate, event)
}

// InstallProjectClaudeHooks installs ox lifecycle hooks to .claude/settings.json (shared).
//
// Uses the generalized ox agent hook command — one entry per event.
// The hook handler reads stdin JSON to determine behavior (source, trigger, etc.)
// so matchers are no longer needed.
//
// Uses raw JSON preservation to avoid dropping non-hook keys (permissions, etc.).
// After successful install, cleans up stale ox hooks from settings.local.json.
func InstallProjectClaudeHooks(gitRoot string) error {
	root, err := os.OpenRoot(gitRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	settings, rawMap, err := readSettingsFileRawFrom(root.ReadFile, projectClaudeSettingsFile)
	if err != nil {
		return err
	}

	for _, event := range claudeLifecycleEvents {
		hookCmd := oxHookCommandForEvent(event)
		newEntry := ClaudeHookEntry{
			Matcher: emptyMatcher,
			Hooks: []ClaudeHook{
				{Command: hookCmd, Type: hookType},
			},
		}

		existing := settings.Hooks[event]
		settings.Hooks[event] = mergeHookEntries(existing, []ClaudeHookEntry{newEntry})
	}

	if err := writeSettingsFileRawInRoot(root, projectClaudeSettingsFile, settings, rawMap, sharedSettingsPerm); err != nil {
		return err
	}

	// clean up stale ox hooks from settings.local.json
	if err := cleanupLocalSettingsOxHooksInRoot(root); err != nil {
		return err
	}

	// The ox status line ships with the hooks: it is the one Claude Code
	// surface that renders color. It is a per-coworker preference, so it goes
	// in settings.local.json (see statusline_install.go), and a coworker's own
	// statusLine is never replaced.
	if _, err := syncProjectStatusLineInRoot(root); err != nil {
		return err
	}

	return nil
}

// HasProjectClaudeHooks checks if ox hooks are in .claude/settings.json (shared).
// Falls back to settings.local.json during migration period.
// Returns true only if ALL lifecycle events have at least one ox hook.
func HasProjectClaudeHooks(gitRoot string) bool {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return false
	}

	// check all lifecycle events to detect stale hook installations
	for _, eventName := range claudeLifecycleEvents {
		found := false
		for _, entry := range settings.Hooks[eventName] {
			if hasAnyOxHook(entry) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// listProjectClaudeHooks returns per-event hook status from project-level settings.
// Checks shared settings.json first, falls back to settings.local.json.
func listProjectClaudeHooks(gitRoot string) map[string]bool {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return make(map[string]bool)
	}
	status := make(map[string]bool)
	for _, eventName := range claudeLifecycleEvents {
		for _, entry := range settings.Hooks[eventName] {
			if hasAnyOxHook(entry) {
				status[eventName] = true
				break
			}
		}
	}
	return status
}

// cleanupLocalSettingsOxHooks removes ox hooks from settings.local.json,
// preserving non-ox content. Deletes the file if it becomes empty.
func cleanupLocalSettingsOxHooks(gitRoot string) error {
	root, err := os.OpenRoot(gitRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return cleanupLocalSettingsOxHooksInRoot(root)
}

func cleanupLocalSettingsOxHooksInRoot(root *os.Root) error {
	settings, rawMap, err := readSettingsFileRawFrom(root.ReadFile, projectClaudeLocalSettingsFile)
	if err != nil {
		return err
	}

	// if no hooks at all, nothing to clean (the ox statusLine lives here on
	// purpose; syncProjectStatusLine owns it)
	if len(settings.Hooks) == 0 {
		return nil
	}

	changed := false

	// strip ox hooks from all events
	for eventName, entries := range settings.Hooks {
		var filtered []ClaudeHookEntry
		for _, entry := range entries {
			before := len(entry.Hooks)
			removeAnyOxHook(&entry)
			if len(entry.Hooks) != before {
				changed = true
			}
			if len(entry.Hooks) > 0 {
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) > 0 {
			settings.Hooks[eventName] = filtered
		} else {
			delete(settings.Hooks, eventName)
			changed = true
		}
	}

	if !changed {
		return nil
	}

	// check if file is now effectively empty (no hooks and no other keys)
	hasOtherKeys := false
	for k := range rawMap {
		if k != "hooks" {
			hasOtherKeys = true
			break
		}
	}

	if len(settings.Hooks) == 0 && !hasOtherKeys {
		return root.Remove(projectClaudeLocalSettingsFile)
	}

	return writeSettingsFileRawInRoot(root, projectClaudeLocalSettingsFile, settings, rawMap, settingsPerm)
}

// hasLocalSettingsOxHooks checks if settings.local.json contains any ox hooks.
func hasLocalSettingsOxHooks(gitRoot string) bool {
	settings, _, err := readProjectSettingsFileRaw(gitRoot, projectClaudeLocalSettingsFile)
	if err != nil {
		return false
	}
	for _, entries := range settings.Hooks {
		for _, entry := range entries {
			if hasAnyOxHook(entry) {
				return true
			}
		}
	}
	return false
}
