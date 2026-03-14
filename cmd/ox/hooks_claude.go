package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/constants"
)

// ClaudeHook represents a single hook configuration
type ClaudeHook struct {
	Command string `json:"command"`
	Type    string `json:"type"`
}

// ClaudeHookEntry represents an entry in the hooks array
type ClaudeHookEntry struct {
	Hooks   []ClaudeHook `json:"hooks"`
	Matcher string       `json:"matcher"`
}

// ClaudeSettings represents the structure of ~/.claude/settings.json
type ClaudeSettings struct {
	Hooks map[string][]ClaudeHookEntry `json:"hooks,omitempty"`
}

// readSettingsFileRaw reads a settings file preserving all top-level keys.
// Returns typed hooks and a raw map of everything else, preventing data loss
// when writing back (e.g., preserving "permissions" alongside "hooks").
func readSettingsFileRaw(path string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &ClaudeSettings{
			Hooks: make(map[string][]ClaudeHookEntry),
		}, make(map[string]json.RawMessage), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read settings file: %w", err)
	}

	if len(data) == 0 {
		return &ClaudeSettings{
			Hooks: make(map[string][]ClaudeHookEntry),
		}, make(map[string]json.RawMessage), nil
	}

	// parse all top-level keys as raw JSON
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, nil, fmt.Errorf("failed to parse settings file: %w", err)
	}

	// extract hooks into typed struct
	var settings ClaudeSettings
	if hooksRaw, ok := rawMap["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			return nil, nil, fmt.Errorf("failed to parse hooks: %w", err)
		}
	}
	if settings.Hooks == nil {
		settings.Hooks = make(map[string][]ClaudeHookEntry)
	}

	return &settings, rawMap, nil
}

// writeSettingsFileRaw writes settings back, merging typed hooks into the raw map
// to preserve all non-hook keys (permissions, etc.).
func writeSettingsFileRaw(path string, settings *ClaudeSettings, rawMap map[string]json.RawMessage, perm os.FileMode) error {
	// ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// marshal hooks back into raw map
	if rawMap == nil {
		rawMap = make(map[string]json.RawMessage)
	}

	if settings.Hooks != nil && len(settings.Hooks) > 0 {
		hooksJSON, err := json.Marshal(settings.Hooks)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		rawMap["hooks"] = hooksJSON
	} else {
		delete(rawMap, "hooks")
	}

	data, err := json.MarshalIndent(rawMap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
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

func hasOxPrimeHook(entry ClaudeHookEntry) bool {
	for _, hook := range entry.Hooks {
		if hook.Type == hookType && isOxPrimeCommand(hook.Command) {
			return true
		}
	}
	return false
}

// hasAnyOxHook checks if an entry contains any ox hook command (prime or lifecycle).
func hasAnyOxHook(entry ClaudeHookEntry) bool {
	for _, hook := range entry.Hooks {
		if hook.Type == hookType && isAnyOxCommand(hook.Command) {
			return true
		}
	}
	return false
}

// isOxPrimeCommand checks if a command is any variant of ox agent prime.
// Recognizes both legacy commands (without AGENT_ENV) and new commands (with AGENT_ENV prefix).
func isOxPrimeCommand(cmd string) bool {
	return cmd == oxPrimeCommand || cmd == oxPrimeLegacy || strings.Contains(cmd, "ox agent prime")
}

// isOxHookCommand checks if a command is any variant of ox agent hook.
func isOxHookCommand(cmd string) bool {
	return strings.Contains(cmd, "ox agent hook")
}

// isAnyOxCommand checks if a command is any ox hook command (prime or lifecycle hook).
func isAnyOxCommand(cmd string) bool {
	return isOxPrimeCommand(cmd) || isOxHookCommand(cmd)
}

func removeOxPrimeHook(entry *ClaudeHookEntry) {
	filtered := make([]ClaudeHook, 0)
	for _, hook := range entry.Hooks {
		// remove both legacy and new format
		if !isOxPrimeCommand(hook.Command) || hook.Type != hookType {
			filtered = append(filtered, hook)
		}
	}
	entry.Hooks = filtered
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

// removeAnyOxHook removes all ox commands (prime and lifecycle) from an entry.
func removeAnyOxHook(entry *ClaudeHookEntry) {
	filtered := make([]ClaudeHook, 0)
	for _, hook := range entry.Hooks {
		if !isAnyOxCommand(hook.Command) || hook.Type != hookType {
			filtered = append(filtered, hook)
		}
	}
	entry.Hooks = filtered
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
	return filepath.Join(gitRoot, ".claude", "settings.json")
}

// getLocalClaudeSettingsPath returns the path to .claude/settings.local.json (gitignored, personal).
func getLocalClaudeSettingsPath(gitRoot string) string {
	return filepath.Join(gitRoot, ".claude", "settings.local.json")
}

// readSharedClaudeSettings reads .claude/settings.json using raw-preserving parse.
func readSharedClaudeSettings(gitRoot string) (*ClaudeSettings, map[string]json.RawMessage, error) {
	return readSettingsFileRaw(getSharedClaudeSettingsPath(gitRoot))
}

// writeSharedClaudeSettings writes .claude/settings.json using raw-preserving write.
func writeSharedClaudeSettings(gitRoot string, settings *ClaudeSettings, rawMap map[string]json.RawMessage) error {
	return writeSettingsFileRaw(getSharedClaudeSettingsPath(gitRoot), settings, rawMap, sharedSettingsPerm)
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
	localPath := getLocalClaudeSettingsPath(gitRoot)
	localSettings, _, err := readSettingsFileRaw(localPath)
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
	return fmt.Sprintf(constants.OxHookCommandClaudeCodeTemplate, event)
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
	settings, rawMap, err := readSharedClaudeSettings(gitRoot)
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

	if err := writeSharedClaudeSettings(gitRoot, settings, rawMap); err != nil {
		return err
	}

	// clean up stale ox hooks from settings.local.json
	_ = cleanupLocalSettingsOxHooks(gitRoot)

	return nil
}

// mergeHookEntries merges new hook entries with existing ones.
// Preserves existing non-ox hooks while updating/adding ox hooks.
// Strips both old (ox agent prime) and new (ox agent hook) commands during merge.
func mergeHookEntries(existing, new []ClaudeHookEntry) []ClaudeHookEntry {
	// build map of new entries by matcher
	newByMatcher := make(map[string]ClaudeHookEntry)
	for _, entry := range new {
		newByMatcher[entry.Matcher] = entry
	}

	// track which matchers we've handled
	handled := make(map[string]bool)

	// process existing entries: update ox hooks, preserve others
	result := make([]ClaudeHookEntry, 0, len(existing)+len(new))
	for _, entry := range existing {
		if newEntry, hasNew := newByMatcher[entry.Matcher]; hasNew {
			// matcher exists in new - merge hooks
			mergedHooks := make([]ClaudeHook, 0)
			// add non-ox hooks from existing (strip both old and new ox commands)
			for _, hook := range entry.Hooks {
				if !isAnyOxCommand(hook.Command) {
					mergedHooks = append(mergedHooks, hook)
				}
			}
			// add ox hooks from new
			mergedHooks = append(mergedHooks, newEntry.Hooks...)
			result = append(result, ClaudeHookEntry{
				Matcher: entry.Matcher,
				Hooks:   mergedHooks,
			})
			handled[entry.Matcher] = true
		} else {
			// check if this is an old ox-only entry with a specific matcher
			// (e.g., old "startup", "resume", "clear", "compact" matchers)
			// If it only contains ox commands, skip it entirely (superseded by new format)
			hasNonOx := false
			for _, hook := range entry.Hooks {
				if !isAnyOxCommand(hook.Command) {
					hasNonOx = true
					break
				}
			}
			if hasNonOx {
				result = append(result, entry)
			}
			// else: pure ox entry with old matcher — drop it (superseded)
		}
	}

	// add new entries that weren't handled
	for _, entry := range new {
		if !handled[entry.Matcher] {
			result = append(result, entry)
		}
	}

	return result
}

// HasProjectClaudeHooks checks if ox hooks are in .claude/settings.json (shared).
// Falls back to settings.local.json during migration period.
// Returns true only if BOTH SessionStart AND PreCompact have at least one ox hook.
func HasProjectClaudeHooks(gitRoot string) bool {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return false
	}

	// check all lifecycle events, not just SessionStart/PreCompact,
	// to detect stale hook installations missing newer events like PostToolUse
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
	localPath := getLocalClaudeSettingsPath(gitRoot)

	settings, rawMap, err := readSettingsFileRaw(localPath)
	if err != nil {
		return err
	}

	// if no hooks at all, nothing to clean
	if len(settings.Hooks) == 0 {
		return nil
	}

	// strip ox hooks from all events
	changed := false
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
		return os.Remove(localPath)
	}

	return writeSettingsFileRaw(localPath, settings, rawMap, settingsPerm)
}

// hasLocalSettingsOxHooks checks if settings.local.json contains any ox hooks.
func hasLocalSettingsOxHooks(gitRoot string) bool {
	localPath := getLocalClaudeSettingsPath(gitRoot)
	settings, _, err := readSettingsFileRaw(localPath)
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
