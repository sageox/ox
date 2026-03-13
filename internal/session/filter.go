package session

import (
	"strings"
)

// SessionFilterMode represents the level of session recording.
// Values: "none", "all"
type SessionFilterMode string

const (
	SessionFilterModeNone SessionFilterMode = "none" // no automatic sessions
	SessionFilterModeAll  SessionFilterMode = "all"  // all coding agent sessions
)

// IsValid returns true if the mode is a recognized value.
func (m SessionFilterMode) IsValid() bool {
	switch m {
	case SessionFilterModeNone, SessionFilterModeAll, "":
		return true
	}
	return false
}

// ShouldRecord returns true if this mode enables any recording.
func (m SessionFilterMode) ShouldRecord() bool {
	return m != SessionFilterModeNone && m != ""
}

// noise commands that are always filtered out (unless they fail)
var noiseCommands = []string{
	"ls", "pwd", "cd", "clear",
	"cat", "head", "tail", "less", "more",
	"echo", "printf",
	"which", "whereis", "type",
	"env", "export",
}

// IsNoiseCommand checks if a command is noise (low-value unless it fails).
func IsNoiseCommand(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, pattern := range noiseCommands {
		if strings.HasPrefix(cmdLower, pattern+" ") || cmdLower == pattern {
			return true
		}
	}
	return false
}

