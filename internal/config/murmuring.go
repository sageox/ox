package config

// Murmuring mode constants.
const (
	MurmuringOff  = "off"
	MurmuringAuto = "auto"
)

// ValidMurmuringModes lists all valid values for the murmuring config field.
var ValidMurmuringModes = []string{MurmuringOff, MurmuringAuto}

// IsValidMurmuringMode returns true if the mode is a recognized murmuring value.
func IsValidMurmuringMode(mode string) bool {
	switch mode {
	case MurmuringOff, MurmuringAuto, "":
		return true
	}
	return false
}

// NormalizeMurmuring returns the canonical murmuring mode.
// Empty string and unrecognized values default to "off".
func NormalizeMurmuring(mode string) string {
	switch mode {
	case MurmuringAuto:
		return MurmuringAuto
	default:
		return MurmuringOff
	}
}

// ResolveMurmuring determines the effective murmuring mode for a project.
// Reads from project config; defaults to "off".
func ResolveMurmuring(projectRoot string) string {
	if projectRoot == "" {
		return MurmuringOff
	}
	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil {
		return MurmuringOff
	}
	return NormalizeMurmuring(cfg.Murmuring)
}

// MurmuringEnabled returns true if auto-murmuring is active for the project.
func MurmuringEnabled(projectRoot string) bool {
	return ResolveMurmuring(projectRoot) == MurmuringAuto
}
