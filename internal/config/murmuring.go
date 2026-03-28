package config

import (
	"errors"
	"log/slog"
	"os"
)

// Murmuring mode constants.
const (
	MurmuringManual = "manual"
	MurmuringAuto   = "auto"
)

// ValidMurmuringModes lists all valid values for the murmuring config field.
var ValidMurmuringModes = []string{MurmuringManual, MurmuringAuto}

// IsValidMurmuringMode returns true if the mode is a recognized murmuring value.
func IsValidMurmuringMode(mode string) bool {
	switch mode {
	case MurmuringManual, MurmuringAuto, "":
		return true
	}
	return false
}

// NormalizeMurmuring returns the canonical murmuring mode.
// Empty string and unrecognized values default to "manual".
func NormalizeMurmuring(mode string) string {
	switch mode {
	case MurmuringAuto:
		return MurmuringAuto
	default:
		return MurmuringManual
	}
}

// ResolveMurmuring determines the effective murmuring mode for a project.
// Reads from project config; defaults to "manual".
func ResolveMurmuring(projectRoot string) string {
	if projectRoot == "" {
		return MurmuringManual
	}
	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("failed to load project config for murmuring", "error", err)
		}
		return MurmuringManual
	}
	if cfg == nil {
		return MurmuringManual
	}
	return NormalizeMurmuring(cfg.Murmuring)
}

// MurmuringEnabled returns true if auto-murmuring is active for the project.
func MurmuringEnabled(projectRoot string) bool {
	return ResolveMurmuring(projectRoot) == MurmuringAuto
}
