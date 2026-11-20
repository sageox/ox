package config

import (
	"os"
	"strings"
)

// Teammate notes: someone watching a live session on SageOx (a teammate, or the
// session's owner) can send its agent a note. This switch is the machine's
// consent. Off tells the server at open (accepts_notes=false, so the web offers
// no note box) and drops any note that still arrives, unshown and unacked.
const (
	// TeammateNotesOn delivers notes the server lists (default).
	TeammateNotesOn = "on"
	// TeammateNotesOff refuses them on this machine.
	TeammateNotesOff = "off"
	// EnvTeammateNotes set to "off" turns notes off; no value turns them on.
	EnvTeammateNotes = "OX_TEAMMATE_NOTES"
)

// TeammateNotesSource names the layer that decided the switch.
type TeammateNotesSource string

const (
	TeammateNotesSourceDefault TeammateNotesSource = "default"
	TeammateNotesSourceEnv     TeammateNotesSource = "env"
	TeammateNotesSourceUser    TeammateNotesSource = "user"
	TeammateNotesSourceRepo    TeammateNotesSource = "repo"
)

// ResolvedTeammateNotes is the effective switch and the layer that set it.
type ResolvedTeammateNotes struct {
	Mode   string
	Source TeammateNotesSource
}

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

// ResolveTeammateNotes resolves the switch for projectRoot ("" outside a repo)
// from OX_TEAMMATE_NOTES, the user config (every repo on this machine) and the
// repo's .sageox/config.json.
//
// Unlike the usual user > repo order, off at ANY layer wins. This is a consent
// switch and the repo file is committed by teammates: it may opt a repo out,
// but must never turn notes back on for a person who turned them off, and a
// person's "on" must not override a repo that opted out. So no value is an
// "on" that beats an "off"; a misspelled env value vetoes nothing, and the
// stored layers decide.
func ResolveTeammateNotes(projectRoot string) ResolvedTeammateNotes {
	type layer struct {
		mode   string
		source TeammateNotesSource
	}
	var set []layer // in display order: env, user, repo
	if v := os.Getenv(EnvTeammateNotes); v != "" {
		set = append(set, layer{NormalizeTeammateNotes(v), TeammateNotesSourceEnv})
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil && cfg.SessionTeammateNotes != "" {
		set = append(set, layer{NormalizeTeammateNotes(cfg.SessionTeammateNotes), TeammateNotesSourceUser})
	}
	if projectRoot != "" {
		if cfg, err := LoadProjectConfig(projectRoot); err == nil && cfg != nil && cfg.SessionTeammateNotes != "" {
			set = append(set, layer{NormalizeTeammateNotes(cfg.SessionTeammateNotes), TeammateNotesSourceRepo})
		}
	}
	for _, l := range set {
		if l.mode == TeammateNotesOff {
			return ResolvedTeammateNotes{Mode: TeammateNotesOff, Source: l.source}
		}
	}
	if len(set) > 0 {
		return ResolvedTeammateNotes{Mode: TeammateNotesOn, Source: set[0].source}
	}
	return ResolvedTeammateNotes{Mode: TeammateNotesOn, Source: TeammateNotesSourceDefault}
}

// TeammateNotesEnabled reports whether notes reach agents in projectRoot.
func TeammateNotesEnabled(projectRoot string) bool {
	return ResolveTeammateNotes(projectRoot).Mode == TeammateNotesOn
}
