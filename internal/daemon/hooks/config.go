package hooks

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
	"gopkg.in/yaml.v3"
)

// HookConfig defines a user-registered hook script triggered by daemon events.
type HookConfig struct {
	Event   string `yaml:"event" json:"event"`
	Command string `yaml:"command" json:"command"`
}

type hooksFile struct {
	Hooks []HookConfig `yaml:"hooks"`
}

// HooksFilePath returns the path to the hooks.yaml config file.
func HooksFilePath() string {
	return filepath.Join(paths.ConfigDir(), "hooks.yaml")
}

// LoadHooks reads and validates hook configurations from hooks.yaml.
// Returns an empty slice (not error) if the file doesn't exist.
func LoadHooks() ([]HookConfig, error) {
	return LoadHooksFrom(HooksFilePath())
}

// LoadHooksFrom reads hooks from a specific file path. Exported for testing.
func LoadHooksFrom(path string) ([]HookConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var f hooksFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}

	return validateAndDedup(f.Hooks), nil
}

func validateAndDedup(hooks []HookConfig) []HookConfig {
	seen := make(map[string]bool)
	var result []HookConfig

	for _, h := range hooks {
		if h.Command == "" {
			slog.Warn("hook skipped: empty command", "event", h.Event)
			continue
		}
		if h.Event != "*" && !ValidEvent(h.Event) {
			slog.Warn("hook skipped: unknown event", "event", h.Event, "command", h.Command)
			continue
		}

		key := h.Event + "\x00" + h.Command
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, h)
	}

	return result
}

// SaveHook appends a hook to the hooks.yaml file.
// Creates the file if it doesn't exist. Uses atomic write-via-rename
// to prevent corruption from crashes or concurrent writers.
func SaveHook(hook HookConfig) error {
	existing, err := LoadHooks()
	if err != nil {
		return err
	}

	existing = append(existing, hook)
	f := hooksFile{Hooks: existing}
	data, err := yaml.Marshal(&f)
	if err != nil {
		return err
	}

	path := HooksFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".hooks-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, path)
}
