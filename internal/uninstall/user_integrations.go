package uninstall

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sageox/ox/internal/constants"

	"github.com/sageox/ox/internal/config"
)

// RemovalItem represents a user-level item that can be removed
type UserIntegrationItem struct {
	Path        string // full path to the item
	Type        string // type of item: "hook", "config", "plugin"
	Agent       string // which agent it belongs to: "claude", "opencode", "gemini", etc.
	Description string // human-readable description
}

// UserIntegrationsFinder finds all user-level SageOx integrations
type UserIntegrationsFinder struct {
	homeDir string
}

// NewUserIntegrationsFinder creates a new finder
func NewUserIntegrationsFinder() (*UserIntegrationsFinder, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	return &UserIntegrationsFinder{
		homeDir: homeDir,
	}, nil
}

// FindAll finds all user-level SageOx integrations
func (f *UserIntegrationsFinder) FindAll() ([]UserIntegrationItem, error) {
	var items []UserIntegrationItem

	// find Claude Code hooks in ~/.claude/settings.json
	claudeItems, err := f.findClaudeHooks()
	if err != nil {
		slog.Debug("error finding claude hooks", "error", err)
	} else {
		items = append(items, claudeItems...)
	}

	// find Claude Code CLAUDE.md user-level integration
	claudeMDItems, err := f.findClaudeMD()
	if err != nil {
		slog.Debug("error finding claude md", "error", err)
	} else {
		items = append(items, claudeMDItems...)
	}

	// find OpenCode plugins in ~/.config/opencode/plugin/
	openCodeItems, err := f.findOpenCodePlugins()
	if err != nil {
		slog.Debug("error finding opencode plugins", "error", err)
	} else {
		items = append(items, openCodeItems...)
	}

	// find Gemini CLI hooks in ~/.gemini/settings.json
	geminiItems, err := f.findGeminiHooks()
	if err != nil {
		slog.Debug("error finding gemini hooks", "error", err)
	} else {
		items = append(items, geminiItems...)
	}

	// find user-level git hooks in ~/.config/git/hooks/
	gitHookItems, err := f.findUserGitHooks()
	if err != nil {
		slog.Debug("error finding git hooks", "error", err)
	} else {
		items = append(items, gitHookItems...)
	}

	return items, nil
}

// findClaudeHooks finds Claude Code hooks in ~/.claude/settings.json
func (f *UserIntegrationsFinder) findClaudeHooks() ([]UserIntegrationItem, error) {
	settingsPath := filepath.Join(f.homeDir, ".claude", "settings.json")

	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil, nil
	}

	// read and parse settings
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read claude settings: %w", err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Type    string `json:"type"`
			} `json:"hooks"`
		} `json:"hooks,omitempty"`
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse claude settings: %w", err)
	}

	// check if ox prime hooks exist
	hasOxPrime := false
	for eventName, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if isOxPrimeCommand(hook.Command) && hook.Type == "command" {
					hasOxPrime = true
					slog.Debug("found ox prime hook", "event", eventName, "command", hook.Command)
					break
				}
			}
			if hasOxPrime {
				break
			}
		}
		if hasOxPrime {
			break
		}
	}

	if !hasOxPrime {
		return nil, nil
	}

	return []UserIntegrationItem{
		{
			Path:        settingsPath,
			Type:        "hook",
			Agent:       "claude",
			Description: "Claude Code hooks (SessionStart, PreCompact)",
		},
	}, nil
}

// findClaudeMD finds user-level Claude MD integration
func (f *UserIntegrationsFinder) findClaudeMD() ([]UserIntegrationItem, error) {
	claudeMDPath := filepath.Join(f.homeDir, ".claude", "CLAUDE.md")

	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		return nil, nil
	}

	content, err := os.ReadFile(claudeMDPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read claude md: %w", err)
	}

	// check if it contains ox agent prime reference
	contentStr := string(content)
	if !containsOxPrime(contentStr) {
		return nil, nil
	}

	return []UserIntegrationItem{
		{
			Path:        claudeMDPath,
			Type:        "config",
			Agent:       "claude",
			Description: "User-level Claude configuration (~/.claude/CLAUDE.md)",
		},
	}, nil
}

// findOpenCodePlugins finds OpenCode plugins in ~/.config/opencode/plugin/
func (f *UserIntegrationsFinder) findOpenCodePlugins() ([]UserIntegrationItem, error) {
	pluginPath := filepath.Join(f.homeDir, ".config", "opencode", "plugin", "ox-prime.ts")

	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return nil, nil
	}

	return []UserIntegrationItem{
		{
			Path:        pluginPath,
			Type:        "plugin",
			Agent:       "opencode",
			Description: "OpenCode plugin (ox-prime.ts)",
		},
	}, nil
}

// findGeminiHooks finds Gemini CLI hooks in ~/.gemini/settings.json
func (f *UserIntegrationsFinder) findGeminiHooks() ([]UserIntegrationItem, error) {
	settingsPath := filepath.Join(f.homeDir, ".gemini", "settings.json")

	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read gemini settings: %w", err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var settings struct {
		Hooks map[string]struct {
			Command string `json:"command"`
		} `json:"hooks,omitempty"`
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse gemini settings: %w", err)
	}

	// check if ox prime hook exists
	if hook, exists := settings.Hooks["SessionStart"]; exists && isOxPrimeCommand(hook.Command) {
		return []UserIntegrationItem{
			{
				Path:        settingsPath,
				Type:        "hook",
				Agent:       "gemini",
				Description: "Gemini CLI hooks (SessionStart)",
			},
		}, nil
	}

	return nil, nil
}

// findUserGitHooks finds user-level git hooks containing ox prime
func (f *UserIntegrationsFinder) findUserGitHooks() ([]UserIntegrationItem, error) {
	var items []UserIntegrationItem

	// determine git hooks directory based on XDG and platform
	hooksDir := f.getUserGitHooksDir()
	if hooksDir == "" {
		return nil, nil
	}

	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		return nil, nil
	}

	// read all files in hooks directory
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read git hooks directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		hookPath := filepath.Join(hooksDir, entry.Name())
		content, err := os.ReadFile(hookPath)
		if err != nil {
			slog.Debug("failed to read git hook", "path", hookPath, "error", err)
			continue
		}

		// check if hook contains ox prime reference
		if containsOxPrime(string(content)) {
			items = append(items, UserIntegrationItem{
				Path:        hookPath,
				Type:        "hook",
				Agent:       "git",
				Description: fmt.Sprintf("User-level git hook (%s)", entry.Name()),
			})
		}
	}

	return items, nil
}

// getUserGitHooksDir returns the user-level git hooks directory
func (f *UserIntegrationsFinder) getUserGitHooksDir() string {
	// check XDG_CONFIG_HOME first
	if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
		return filepath.Join(xdgConfigHome, "git", "hooks")
	}

	// use platform-specific default
	// intentionally use ~/.config on all platforms for consistency (same as user config)
	return filepath.Join(f.homeDir, ".config", "git", "hooks")
}

// isOxPrimeCommand checks if a command is an ox agent prime variant
func isOxPrimeCommand(cmd string) bool {
	// check for various forms of ox agent prime
	return containsOxPrime(cmd)
}

// containsOxPrime checks if a string contains ox agent prime reference
func containsOxPrime(s string) bool {
	return s != "" && (
	// direct command
	s == "ox agent prime" ||
		s == "ox agent prime --user" ||
		// with fallback (legacy without AGENT_ENV)
		s == constants.OxPrimeCommand ||
		// agent-specific commands (with AGENT_ENV prefix)
		s == constants.OxPrimeCommandClaudeCode ||
		s == constants.OxPrimeCommandGemini ||
		// simple contains check for other variants
		(len(s) > 0 && (containsSubstring(s, "ox agent prime") ||
			containsSubstring(s, "ox prime"))))
}

// containsSubstring is a helper for case-sensitive substring check
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0
}

// indexOfSubstring returns the index of substr in s, or -1
func indexOfSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// RemoveUserIntegrations removes user-level SageOx integrations
// dryRun: if true, only shows what would be removed without actually removing
func RemoveUserIntegrations(items []UserIntegrationItem, dryRun bool) error {
	if len(items) == 0 {
		slog.Info("no user integrations found")
		return nil
	}

	for _, item := range items {
		if dryRun {
			slog.Info("would remove", "path", item.Path, "type", item.Type, "agent", item.Agent)
			continue
		}

		// determine if it's a file or directory
		info, err := os.Stat(item.Path)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Debug("item already removed", "path", item.Path)
				continue
			}
			return fmt.Errorf("failed to stat %s: %w", item.Path, err)
		}

		if info.IsDir() {
			// remove directory
			if err := os.RemoveAll(item.Path); err != nil {
				return fmt.Errorf("failed to remove directory %s: %w", item.Path, err)
			}
			slog.Info("removed directory", "path", item.Path, "agent", item.Agent)
		} else {
			// for hooks in settings files, we need to edit the file, not delete it
			if item.Type == "hook" && (item.Agent == "claude" || item.Agent == "gemini") {
				if err := removeHooksFromFile(item); err != nil {
					return fmt.Errorf("failed to remove hooks from %s: %w", item.Path, err)
				}
				slog.Info("removed hooks from file", "path", item.Path, "agent", item.Agent)
			} else {
				// remove file
				if err := os.Remove(item.Path); err != nil {
					return fmt.Errorf("failed to remove file %s: %w", item.Path, err)
				}
				slog.Info("removed file", "path", item.Path, "agent", item.Agent)
			}
		}
	}

	return nil
}

// removeHooksFromFile removes ox prime hooks from Claude/Gemini settings files
func removeHooksFromFile(item UserIntegrationItem) error {
	data, err := os.ReadFile(item.Path)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	switch item.Agent {
	case "claude":
		return removeClaudeHooks(item.Path, data)
	case "gemini":
		return removeGeminiHooks(item.Path, data)
	default:
		return fmt.Errorf("unsupported agent for hook removal: %s", item.Agent)
	}
}

// removeClaudeHooks removes ox prime hooks from Claude settings
func removeClaudeHooks(path string, data []byte) error {
	var settings struct {
		Hooks map[string][]struct {
			Hooks   []map[string]interface{} `json:"hooks"`
			Matcher string                   `json:"matcher"`
		} `json:"hooks,omitempty"`
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	if settings.Hooks == nil {
		return nil
	}

	// remove ox prime hooks from SessionStart and PreCompact
	hookEvents := []string{"SessionStart", "PreCompact"}
	for _, eventName := range hookEvents {
		entries := settings.Hooks[eventName]
		for i := range entries {
			filtered := make([]map[string]interface{}, 0)
			for _, hook := range entries[i].Hooks {
				cmd, _ := hook["command"].(string)
				hookType, _ := hook["type"].(string)
				if !isOxPrimeCommand(cmd) || hookType != "command" {
					filtered = append(filtered, hook)
				}
			}
			entries[i].Hooks = filtered
		}

		// remove empty entries
		filtered := make([]struct {
			Hooks   []map[string]interface{} `json:"hooks"`
			Matcher string                   `json:"matcher"`
		}, 0)
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

	// write back
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(path, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	return nil
}

// removeGeminiHooks removes ox prime hooks from Gemini settings
func removeGeminiHooks(path string, data []byte) error {
	var settings struct {
		Hooks map[string]struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout,omitempty"`
		} `json:"hooks,omitempty"`
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	if settings.Hooks == nil {
		return nil
	}

	// remove SessionStart hook if it's ox prime
	if hook, exists := settings.Hooks["SessionStart"]; exists && isOxPrimeCommand(hook.Command) {
		delete(settings.Hooks, "SessionStart")
	}

	// write back
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(path, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	return nil
}

// GetPlatformInfo returns platform-specific information for display
func GetPlatformInfo() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

// ShouldRemoveUserConfig determines if user config should be removed
// This is intentionally conservative - we only suggest it, never do it automatically
func ShouldRemoveUserConfig() bool {
	configDir := config.GetUserConfigDir()
	if configDir == "" {
		return false
	}

	// check if config directory exists
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		return false
	}

	return true
}
