package session

import ss "github.com/sageox/ox/pkg/sessionsummary"

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

// IsNoiseCommand checks if a command is noise (low-value unless it fails).
// Delegates to pkg/sessionsummary.
func IsNoiseCommand(cmd string) bool {
	return ss.IsNoiseCommand(cmd)
}
