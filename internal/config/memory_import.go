package config

import "log/slog"

// MemoryImport mode constants.
// Default is "manual" (empty string normalizes to manual).
const (
	MemoryImportDisabled = "disabled" // no memory import
	MemoryImportManual   = "manual"   // CLI only: ox memory import --type=claude
	MemoryImportAuto     = "auto"     // daemon imports automatically on timer
)

// ValidMemoryImportModes lists all valid memory_import mode values.
var ValidMemoryImportModes = []string{MemoryImportDisabled, MemoryImportManual, MemoryImportAuto}

// IsValidMemoryImportMode returns true if the mode is a recognized value.
func IsValidMemoryImportMode(mode string) bool {
	switch mode {
	case MemoryImportDisabled, MemoryImportManual, MemoryImportAuto, "":
		return true
	}
	return false
}

// NormalizeMemoryImport normalizes memory_import values.
// Returns "manual" for empty or unrecognized values (default).
func NormalizeMemoryImport(mode string) string {
	switch mode {
	case MemoryImportDisabled, MemoryImportManual, MemoryImportAuto:
		return mode
	default:
		return MemoryImportManual
	}
}

// IsMemoryImportEnabled returns true if memory import is enabled (manual or auto).
func IsMemoryImportEnabled(projectRoot string) bool {
	mode := GetMemoryImport(projectRoot)
	return mode == MemoryImportManual || mode == MemoryImportAuto
}

// IsMemoryImportAuto returns true if memory import is set to automatic (daemon).
func IsMemoryImportAuto(projectRoot string) bool {
	return GetMemoryImport(projectRoot) == MemoryImportAuto
}

// GetMemoryImport returns the effective memory_import mode for a project.
// Defaults to "manual" if not configured.
func GetMemoryImport(projectRoot string) string {
	if projectRoot == "" {
		return MemoryImportManual
	}
	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil {
		slog.Debug("could not load project config for memory_import, defaulting to manual", "error", err)
		return MemoryImportManual
	}
	if cfg == nil {
		return MemoryImportManual
	}
	return NormalizeMemoryImport(cfg.MemoryImport)
}
