package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	// Err is why the deciding layer could not be read. That layer counts as
	// off: an opt-out in a file that stopped parsing must not become the
	// default on.
	Err error
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
		err    error
	}
	var set []layer // in display order: env, user, repo
	if v := os.Getenv(EnvTeammateNotes); v != "" {
		set = append(set, layer{mode: NormalizeTeammateNotes(v), source: TeammateNotesSourceEnv})
	}
	// a layer that cannot be read counts as off (fail closed): it may hold the
	// opt-out. Absent files are not errors, so they still mean the default.
	if cfg, err := LoadUserConfig(); err != nil {
		set = append(set, layer{mode: TeammateNotesOff, source: TeammateNotesSourceUser, err: fmt.Errorf("read user config: %w", err)})
	} else if cfg != nil && cfg.SessionTeammateNotes != "" {
		set = append(set, layer{mode: NormalizeTeammateNotes(cfg.SessionTeammateNotes), source: TeammateNotesSourceUser})
	}
	if projectRoot != "" {
		if cfg, err := LoadProjectConfig(projectRoot); err != nil {
			set = append(set, layer{mode: TeammateNotesOff, source: TeammateNotesSourceRepo, err: fmt.Errorf("read repo config: %w", err)})
		} else if cfg != nil && cfg.SessionTeammateNotes != "" {
			set = append(set, layer{mode: NormalizeTeammateNotes(cfg.SessionTeammateNotes), source: TeammateNotesSourceRepo})
		}
	}
	for _, l := range set {
		if l.mode == TeammateNotesOff {
			return ResolvedTeammateNotes{Mode: TeammateNotesOff, Source: l.source, Err: l.err}
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

// RepoYAMLTeammateNotes reports the session_teammate_notes value that
// .sageox/config.yaml carries, if any. LoadProjectConfig lets that file
// override config.json, the only file `ox config set --repo` writes, so a
// change written to config.json alone cannot alter a value set there.
func RepoYAMLTeammateNotes(projectRoot string) (value string, set bool, err error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, sageoxDir, projectConfigYAMLFilename))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read repo yaml config: %w", err)
	}
	// decided by the loader's own merge, so the key matching (encoding/json
	// ignores case) and null handling (a null changes nothing) cannot drift
	// from what LoadProjectConfig does. Merging over two different seeds tells
	// an explicit value, "" included, from none: only a value the file sets
	// replaces both. No reserved string could do that, since a committed file
	// can spell any string.
	merged := func(seed string) (string, error) {
		cfg := ProjectConfig{SessionTeammateNotes: seed}
		if err := mergeProjectConfigYAML(data, &cfg); err != nil {
			return "", fmt.Errorf("parse repo yaml config: %w", err)
		}
		return cfg.SessionTeammateNotes, nil
	}
	a, err := merged(TeammateNotesOn)
	if err != nil {
		return "", false, err
	}
	b, err := merged(TeammateNotesOff)
	if err != nil {
		return "", false, err
	}
	if a != b {
		return "", false, nil
	}
	return a, true, nil
}
