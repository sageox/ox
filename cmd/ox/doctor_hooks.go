package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugHookCompleteness,
		Name:        "Hook completeness",
		Category:    "Integration",
		FixLevel:    FixLevelSuggested,
		Description: "Verifies project hooks have ox prime for all required events",
		Run: func(fix bool) checkResult {
			return checkProjectHookCompleteness(fix)
		},
	})
}

// checkSessionStartHookBug warns about Claude Code bug #10373 where SessionStart
// hook output is discarded for new sessions.
//
// Workaround: Ensure CLAUDE.md/AGENTS.md contains the ox:prime marker
// as a fallback when hooks don't deliver the output.
//
// Reference: https://github.com/anthropics/claude-code/issues/10373
func checkSessionStartHookBug() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("SessionStart hook reliability", "not in git repo", "")
	}

	// check if project-level hooks are configured
	if !HasProjectClaudeHooks(gitRoot) {
		return SkippedCheck("SessionStart hook reliability", "no project hooks configured", "")
	}

	// hooks exist - check for fallback in CLAUDE.md/AGENTS.md
	hasFallback := hasUserLevelOxPrime()

	if !hasFallback {
		hasFallback = HasOxPrimeMarker(gitRoot)
	}

	if hasFallback {
		return PassedCheck("SessionStart hook reliability", "fallback configured")
	}

	return WarningCheck("SessionStart hook reliability",
		"new sessions may not receive hook output",
		"Claude Code bug #10373: SessionStart hooks don't work for new sessions.\n"+
			"       Run `ox integrate install --user` to add CLAUDE.md fallback.")
}

// knownOxSubcommands defines valid ox subcommands for hook validation.
// This is the source of truth for what commands can be invoked in hooks.
var knownOxSubcommands = map[string]bool{
	"ox agent prime": true,
	"ox agent hook":  true,
	"ox doctor":      true,
	"ox init":        true,
	"ox version":     true,
	"ox integrate":   true,
	"ox login":       true,
	"ox logout":      true,
	"ox status":      true,
}

// legacyOxCommands maps deprecated commands to their valid replacements.
// This provides helpful suggestions when invalid commands are detected.
var legacyOxCommands = map[string]string{
	"ox prime": "ox agent prime",
}

// hookCommandPattern matches ox commands in shell scripts.
// Matches: ox <subcommand>, handles shell conditionals and pipes.
var hookCommandPattern = regexp.MustCompile(`\box\s+([a-z]+(?:\s+[a-z]+)?)`)

// singleQuotedString strips single-quoted strings before command extraction,
// preventing false positives from echo messages like 'install .../ox for optimized...'.
var singleQuotedString = regexp.MustCompile(`'[^']*'`)

// checkHookCommands validates ox commands in ~/.claude/settings.json hooks.
// Returns warnings for invalid commands with suggestions for fixes.
func checkHookCommands() checkResult {
	settingsPath, err := getClaudeSettingsPath()
	if err != nil {
		return SkippedCheck("Hook commands", "cannot determine settings path", "")
	}

	// check if settings.json exists
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return SkippedCheck("Hook commands", "no settings.json", "")
	}

	settings, err := readClaudeSettings()
	if err != nil {
		return WarningCheck("Hook commands", "read error", err.Error())
	}

	if len(settings.Hooks) == 0 {
		return SkippedCheck("Hook commands", "no hooks configured", "")
	}

	// collect all invalid commands found
	var invalidCommands []invalidHookCommand
	var validCount int

	for eventName, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if hook.Type != "command" {
					continue
				}

				// extract ox commands from the hook command string
				oxCommands := extractOxCommands(hook.Command)
				for _, cmd := range oxCommands {
					if isValidOxCommand(cmd) {
						validCount++
					} else {
						suggestion := getSuggestionForInvalidCommand(cmd)
						invalidCommands = append(invalidCommands, invalidHookCommand{
							event:      eventName,
							command:    cmd,
							suggestion: suggestion,
						})
					}
				}
			}
		}
	}

	if len(invalidCommands) == 0 {
		if validCount == 0 {
			return SkippedCheck("Hook commands", "no ox commands in hooks", "")
		}
		return PassedCheck("Hook commands", fmt.Sprintf("%d valid", validCount))
	}

	// build warning message with all invalid commands
	detail := formatInvalidCommandsDetail(invalidCommands, settingsPath)
	return WarningCheck("Hook commands",
		fmt.Sprintf("%d invalid command(s)", len(invalidCommands)),
		detail)
}

// invalidHookCommand captures details about an invalid command in a hook.
type invalidHookCommand struct {
	event      string
	command    string
	suggestion string
}

// extractOxCommands finds all ox commands in a hook command string.
// Handles shell scripts like: "if command -v ox >/dev/null; then ox agent prime; fi"
// Strips single-quoted strings first to avoid false positives from echo messages.
func extractOxCommands(hookCmd string) []string {
	// strip single-quoted strings to avoid matching ox inside echo messages
	cleaned := singleQuotedString.ReplaceAllString(hookCmd, "")

	var commands []string
	matches := hookCommandPattern.FindAllStringSubmatch(cleaned, -1)
	for _, match := range matches {
		if len(match) >= 2 {
			cmd := "ox " + strings.TrimSpace(match[1])
			commands = append(commands, cmd)
		}
	}
	return commands
}

// isValidOxCommand checks if a command is a known valid ox subcommand.
func isValidOxCommand(cmd string) bool {
	// direct match
	if knownOxSubcommands[cmd] {
		return true
	}

	// check if it's a command with additional arguments (e.g., "ox agent prime --user")
	for validCmd := range knownOxSubcommands {
		if strings.HasPrefix(cmd, validCmd) {
			return true
		}
	}

	return false
}

// getSuggestionForInvalidCommand returns a suggested replacement for an invalid command.
func getSuggestionForInvalidCommand(cmd string) string {
	// check if it's a known legacy command
	if suggestion, ok := legacyOxCommands[cmd]; ok {
		return suggestion
	}

	// check for partial matches in legacy commands
	for legacy, replacement := range legacyOxCommands {
		if strings.HasPrefix(cmd, legacy) {
			// preserve any additional flags/args
			suffix := strings.TrimPrefix(cmd, legacy)
			return replacement + suffix
		}
	}

	// no specific suggestion available
	return ""
}

// formatInvalidCommandsDetail formats the detail message for invalid commands.
func formatInvalidCommandsDetail(invalids []invalidHookCommand, settingsPath string) string {
	var parts []string

	// group by event for clarity
	eventCommands := make(map[string][]invalidHookCommand)
	for _, inv := range invalids {
		eventCommands[inv.event] = append(eventCommands[inv.event], inv)
	}

	for _, inv := range invalids {
		part := fmt.Sprintf("'%s'", inv.command)
		if inv.suggestion != "" {
			part += fmt.Sprintf(" -> use '%s'", inv.suggestion)
		}
		parts = append(parts, part)
	}

	// add file location hint
	detail := strings.Join(parts, ", ")
	detail += fmt.Sprintf("\n       Edit %s to fix", settingsPath)

	return detail
}

// ValidateHookCommand checks if a hook command contains valid ox commands.
// Exported for use by other packages that install hooks.
func ValidateHookCommand(hookCmd string) (valid bool, invalidCommands []string, suggestions map[string]string) {
	suggestions = make(map[string]string)
	oxCommands := extractOxCommands(hookCmd)

	if len(oxCommands) == 0 {
		return true, nil, nil
	}

	valid = true
	for _, cmd := range oxCommands {
		if !isValidOxCommand(cmd) {
			valid = false
			invalidCommands = append(invalidCommands, cmd)
			if suggestion := getSuggestionForInvalidCommand(cmd); suggestion != "" {
				suggestions[cmd] = suggestion
			}
		}
	}

	return valid, invalidCommands, suggestions
}

// ClaudeSettingsForValidation reads settings.json and returns hook commands for validation.
// This is a lighter-weight read specifically for validation purposes.
func ClaudeSettingsForValidation() (map[string][]string, error) {
	settingsPath, err := getClaudeSettingsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	hooks, ok := raw["hooks"].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	result := make(map[string][]string)
	for event, entries := range hooks {
		entriesSlice, ok := entries.([]interface{})
		if !ok {
			continue
		}

		for _, entry := range entriesSlice {
			entryMap, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}

			hooksSlice, ok := entryMap["hooks"].([]interface{})
			if !ok {
				continue
			}

			for _, hook := range hooksSlice {
				hookMap, ok := hook.(map[string]interface{})
				if !ok {
					continue
				}

				if hookMap["type"] == "command" {
					if cmd, ok := hookMap["command"].(string); ok {
						result[event] = append(result[event], cmd)
					}
				}
			}
		}
	}

	return result, nil
}

// checkProjectHookCommands validates ox commands in project-level .claude/settings.local.json.
// This is similar to checkHookCommands but for project-level hooks.
func checkProjectHookCommands() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Project hook commands", "not in git repo", "")
	}

	settingsPath := filepath.Join(gitRoot, ".claude", "settings.local.json")

	// check if settings.local.json exists
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return SkippedCheck("Project hook commands", "no settings.local.json", "")
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return WarningCheck("Project hook commands", "read error", err.Error())
	}

	if len(data) == 0 {
		return SkippedCheck("Project hook commands", "empty file", "")
	}

	var settings ClaudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return WarningCheck("Project hook commands", "parse error", err.Error())
	}

	if len(settings.Hooks) == 0 {
		return SkippedCheck("Project hook commands", "no hooks configured", "")
	}

	// collect all invalid commands found
	var invalidCommands []invalidHookCommand
	var validCount int

	for eventName, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if hook.Type != "command" {
					continue
				}

				oxCommands := extractOxCommands(hook.Command)
				for _, cmd := range oxCommands {
					if isValidOxCommand(cmd) {
						validCount++
					} else {
						suggestion := getSuggestionForInvalidCommand(cmd)
						invalidCommands = append(invalidCommands, invalidHookCommand{
							event:      eventName,
							command:    cmd,
							suggestion: suggestion,
						})
					}
				}
			}
		}
	}

	if len(invalidCommands) == 0 {
		if validCount == 0 {
			return SkippedCheck("Project hook commands", "no ox commands in hooks", "")
		}
		return PassedCheck("Project hook commands", fmt.Sprintf("%d valid", validCount))
	}

	detail := formatInvalidCommandsDetail(invalidCommands, settingsPath)
	return WarningCheck("Project hook commands",
		fmt.Sprintf("%d invalid command(s)", len(invalidCommands)),
		detail)
}

// checkProjectHookCompleteness verifies that project-level hooks have ox prime
// hooks for ALL required events (SessionStart and PreCompact). Detects partial
// installations and can auto-repair by re-running InstallProjectClaudeHooks.
func checkProjectHookCompleteness(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Hook completeness", "not in git repo", "")
	}

	settings, err := readProjectClaudeSettings(gitRoot)
	if err != nil {
		return SkippedCheck("Hook completeness", "no project settings", "")
	}

	if len(settings.Hooks) == 0 {
		return SkippedCheck("Hook completeness", "no hooks configured", "")
	}

	// check each required event has ox hooks (prime or lifecycle)
	var missing []string
	for _, event := range []string{claudeSessionStart, claudePreCompact} {
		found := false
		for _, entry := range settings.Hooks[event] {
			if hasAnyOxHook(entry) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, event)
		}
	}

	if len(missing) == 0 {
		return PassedCheck("Hook completeness", "all events configured")
	}

	if fix {
		if err := InstallProjectClaudeHooks(gitRoot); err != nil {
			return FailedCheck("Hook completeness", "repair failed", err.Error())
		}
		return PassedCheck("Hook completeness", "repaired (re-installed hooks)")
	}

	return FailedCheck("Hook completeness",
		fmt.Sprintf("missing hooks for: %s", strings.Join(missing, ", ")),
		"Run `ox doctor --fix` to repair")
}
