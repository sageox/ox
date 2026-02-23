package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// isOxPrimeCommand checks if a command is any variant of ox agent prime.
// Recognizes both legacy commands (without AGENT_ENV) and new commands (with AGENT_ENV prefix).
func isOxPrimeCommand(cmd string) bool {
	return cmd == oxPrimeCommand || cmd == oxPrimeLegacy || strings.Contains(cmd, "ox agent prime")
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

	hookEvents := []string{claudeSessionStart, claudePreCompact}

	for _, eventName := range hookEvents {
		entries := settings.Hooks[eventName]

		for i := range entries {
			removeOxPrimeHook(&entries[i])
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
			// remove the event key if no entries remain
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
			if hasOxPrimeHook(entry) {
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

// getProjectClaudeSettingsPath returns the path to .claude/settings.local.json in the project
func getProjectClaudeSettingsPath(gitRoot string) string {
	return filepath.Join(gitRoot, ".claude", "settings.local.json")
}

// readProjectClaudeSettings reads .claude/settings.local.json from the project
func readProjectClaudeSettings(gitRoot string) (*ClaudeSettings, error) {
	settingsPath := getProjectClaudeSettingsPath(gitRoot)

	// create settings file if it doesn't exist
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
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

// writeProjectClaudeSettings writes .claude/settings.local.json to the project
func writeProjectClaudeSettings(gitRoot string, settings *ClaudeSettings) error {
	settingsPath := getProjectClaudeSettingsPath(gitRoot)

	// ensure .claude directory exists
	claudeDir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(claudeDir, dirPerm); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
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

// InstallProjectClaudeHooks installs ox prime hooks to .claude/settings.local.json in the project.
//
// Hook configuration (belt and suspenders approach):
//
// SessionStart hooks with matchers:
//   - startup: New session → --idempotent (marker shouldn't exist, saves tokens if it does)
//   - resume:  Continuing → --idempotent (context intact, skip if primed)
//   - clear:   Context wiped → force (re-prime with same agent_id from marker)
//   - compact: Context reduced → force (re-prime with same agent_id from marker)
//
// PreCompact hook:
//   - Belt: force re-prime before compaction to ensure context survives
func InstallProjectClaudeHooks(gitRoot string) error {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return err
	}

	// install SessionStart hooks with matchers for different sources
	sessionStartEntries := []ClaudeHookEntry{
		// startup: new session - idempotent (fresh session, marker shouldn't exist)
		{
			Matcher: matcherStartup,
			Hooks: []ClaudeHook{
				{Command: oxPrimeCommandIdempotent, Type: hookType},
			},
		},
		// resume: --resume/--continue - idempotent (context should be intact)
		{
			Matcher: matcherResume,
			Hooks: []ClaudeHook{
				{Command: oxPrimeCommandIdempotent, Type: hookType},
			},
		},
		// clear: /clear command - force (context was wiped, need re-prime)
		{
			Matcher: matcherClear,
			Hooks: []ClaudeHook{
				{Command: oxPrimeCommand, Type: hookType},
			},
		},
		// compact: auto/manual compaction - force (context was reduced, need re-prime)
		{
			Matcher: matcherCompact,
			Hooks: []ClaudeHook{
				{Command: oxPrimeCommand, Type: hookType},
			},
		},
	}

	// merge with existing SessionStart hooks (preserve non-ox hooks)
	existingSessionStart := settings.Hooks[claudeSessionStart]
	settings.Hooks[claudeSessionStart] = mergeHookEntries(existingSessionStart, sessionStartEntries)

	// install PreCompact hook (belt: force re-prime before compaction)
	preCompactEntry := ClaudeHookEntry{
		Matcher: emptyMatcher,
		Hooks: []ClaudeHook{
			{Command: oxPrimeCommand, Type: hookType},
		},
	}
	existingPreCompact := settings.Hooks[claudePreCompact]
	settings.Hooks[claudePreCompact] = mergeHookEntries(existingPreCompact, []ClaudeHookEntry{preCompactEntry})

	return writeProjectClaudeSettings(gitRoot, settings)
}

// mergeHookEntries merges new hook entries with existing ones.
// Preserves existing non-ox hooks while updating/adding ox hooks.
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
			// add non-ox hooks from existing
			for _, hook := range entry.Hooks {
				if !isOxPrimeCommand(hook.Command) {
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
			// no new entry for this matcher - preserve as-is
			result = append(result, entry)
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

// HasProjectClaudeHooks checks if ox prime hooks are already in .claude/settings.local.json.
// Returns true only if BOTH SessionStart AND PreCompact have at least one ox prime hook.
func HasProjectClaudeHooks(gitRoot string) bool {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return false
	}

	for _, eventName := range []string{claudeSessionStart, claudePreCompact} {
		found := false
		for _, entry := range settings.Hooks[eventName] {
			if hasOxPrimeHook(entry) {
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
func listProjectClaudeHooks(gitRoot string) map[string]bool {
	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return make(map[string]bool)
	}
	status := make(map[string]bool)
	for _, eventName := range []string{claudeSessionStart, claudePreCompact} {
		for _, entry := range settings.Hooks[eventName] {
			if hasOxPrimeHook(entry) {
				status[eventName] = true
				break
			}
		}
	}
	return status
}
