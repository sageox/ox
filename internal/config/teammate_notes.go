package config

import (
	"os"
	"strings"
)

// Teammate notes: a teammate watching a live session can send its agent a note.
// The owner's server opt-in is the first consent layer; this machine-local
// switch is the second. When off, notes are dropped unshown and unacked.
const (
	// TeammateNotesOn delivers notes the server lists (default).
	TeammateNotesOn = "on"
	// TeammateNotesOff drops them on this machine.
	TeammateNotesOff = "off"
	// EnvTeammateNotes overrides the user config: "off" disables notes.
	EnvTeammateNotes = "OX_TEAMMATE_NOTES"
)

// ValidTeammateNotesModes lists the modes `ox config set session.teammate_notes` accepts.
var ValidTeammateNotesModes = []string{TeammateNotesOn, TeammateNotesOff}

// IsValidTeammateNotes reports whether mode is accepted. Empty (unset) is valid.
func IsValidTeammateNotes(mode string) bool {
	switch mode {
	case TeammateNotesOn, TeammateNotesOff, "":
		return true
	}
	return false
}

// NormalizeTeammateNotes returns the canonical mode; anything but "off" is on.
func NormalizeTeammateNotes(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), TeammateNotesOff) {
		return TeammateNotesOff
	}
	return TeammateNotesOn
}

// TeammateNotesEnabled resolves the switch: OX_TEAMMATE_NOTES=off wins, then
// session.teammate_notes in user config, then the default (on).
func TeammateNotesEnabled() bool {
	if v := os.Getenv(EnvTeammateNotes); v != "" {
		return NormalizeTeammateNotes(v) == TeammateNotesOn
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil && cfg.SessionTeammateNotes != "" {
		return NormalizeTeammateNotes(cfg.SessionTeammateNotes) == TeammateNotesOn
	}
	return true
}
