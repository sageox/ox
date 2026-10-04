// Package addons implements the Add-on Catalog: the team-scoped selection of
// optional, versioned content a team installs into its Team Context.
//
// The boundary this package defends, from ADR-032:
//
//	ox addons chooses and updates what the team owns.
//	ox sync   distributes everything the team owns.
//
// Nothing here fetches, converges, or projects into a product repository. An
// add-on operation writes the Team Context checkout and stops; delivery is
// internal/teamconverge's job through the same pipeline hand-authored content
// already uses. There is deliberately no `ox addons sync`.
package addons

import (
	"context"
	"strings"
	"time"
)

// LockSchemaVersion is the on-disk schema of add-ons.lock.json. Bump it only
// for a change a previous ox cannot read; an older binary refuses a newer
// schema rather than guessing at fields it does not know.
const LockSchemaVersion = 1

// LockRelativePath is the committed record of what the team selected, relative
// to the Team Context root.
//
// It MUST be re-included in the Team Context `.sageox/.gitignore` allow-list
// (internal/gitserver/gitignore.go). That file is deny-all by design, so a lock
// left out of the allow-list is never committed, never reaches a teammate, and
// says nothing about it — ADR-032 D5 names this as one of two path
// consequences that fail silently.
const LockRelativePath = ".sageox/add-ons.lock.json"

// SourceBuiltin is the only provider that exists today: add-ons compiled into
// the ox binary under extensions/addons/. The Source field is recorded per
// add-on anyway, because a remote provider is the reason this contract is
// shaped as List/Resolve/Fetch rather than a map lookup.
const SourceBuiltin = "builtin:sageox"

// ArtifactKind is what an add-on file becomes once installed. ADR-032 D2
// allows exactly two: there is no free-standing "context" artifact and no tool
// kind, because neither has an activation trigger or a handler behind it.
type ArtifactKind string

const (
	// KindSkill installs under agents/skills/<name>/, carrying its own
	// references/, assets/ and scripts/ subtrees.
	KindSkill ArtifactKind = "skill"
	// KindRule installs as agents/rules/<name>.md.
	KindRule ArtifactKind = "rule"
)

// Descriptor is what a catalog advertises before anything is installed: enough
// to list, compare and pin, without reading file bodies.
type Descriptor struct {
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Summary      string         `json:"summary"`
	Source       string         `json:"source"`
	Digest       string         `json:"digest"`
	Skills       []string       `json:"skills,omitempty"`
	Rules        []string       `json:"rules,omitempty"`
	Kinds        []ArtifactKind `json:"kinds,omitempty"`
	HasScripts   bool           `json:"has_scripts"`
	ValidThrough string         `json:"valid_through,omitempty"`
}

// File is one installable byte sequence and the repository-relative Team
// Context path it lands on. Path is always slash-separated and always inside
// agents/; a Provider returning anything else is a bug the installer refuses
// rather than writes.
type File struct {
	Path    string `json:"path"`
	Content []byte `json:"-"`
	Mode    uint32 `json:"mode"`
	Digest  string `json:"digest"`
}

// Resolved is an exact, immutable pin: the bytes that will be written and the
// digest that identifies them. Resolve must be deterministic — the same
// (name, version) yields the same digest on every machine — because the lock
// records that digest as the team's selection.
type Resolved struct {
	Descriptor
	Files []File `json:"files"`
}

// Provider is the catalog contract. The embedded provider implements it today;
// a remote one implements the same three methods. Nothing above this interface
// knows which it is talking to, which is the point.
type Provider interface {
	// Source identifies the provider in lock records, e.g. SourceBuiltin.
	Source() string
	// List returns every add-on this provider offers, sorted by name.
	List(ctx context.Context) ([]Descriptor, error)
	// Resolve pins one add-on to exact bytes. An empty version means "the
	// version this provider currently offers"; the returned Resolved always
	// carries the concrete version it chose.
	Resolve(ctx context.Context, name, version string) (Resolved, error)
}

// LockedFile is the per-path record that lets ox say "you modified this; the
// next update will overwrite it" BEFORE running the update. Per ADR-032 D4
// these digests exist for honesty, not for conflict resolution — there is no
// merge, and Team Context git history is the undo.
type LockedFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode"`
}

// LockedAddon is one installed add-on's ownership record. Paths is the
// authority on what ox owns: ADR-032 D3 puts add-on content in the SAME roots
// as hand-authored content, so ownership is recorded here and never inferred
// from a path prefix.
type LockedAddon struct {
	Addon       string       `json:"addon"`
	Source      string       `json:"source"`
	Version     string       `json:"version"`
	Digest      string       `json:"digest"`
	InstalledAt time.Time    `json:"installed_at"`
	Files       []LockedFile `json:"files"`
}

// Lock is the committed selection. It is a whole-file rewrite on every change
// so a reader never sees a half-updated selection.
type Lock struct {
	SchemaVersion int           `json:"schema_version"`
	Addons        []LockedAddon `json:"addons"`
}

// Owns reports whether any installed add-on owns the given Team Context path,
// and which one. This is the function the collision rule turns on: a path the
// lock owns is overwritten on update; a path it does not own is a namespace
// collision that refuses the install and mutates nothing.
func (l Lock) Owns(relPath string) (LockedAddon, LockedFile, bool) {
	for _, a := range l.Addons {
		for _, f := range a.Files {
			if f.Path == relPath {
				return a, f, true
			}
		}
	}
	return LockedAddon{}, LockedFile{}, false
}

// Find returns the installed record for an add-on by name.
func (l Lock) Find(name string) (LockedAddon, bool) {
	for _, a := range l.Addons {
		if a.Addon == name {
			return a, true
		}
	}
	return LockedAddon{}, false
}

// OwnsUnder reports whether any installed add-on owns a path BENEATH the given
// directory, and which one.
//
// It exists because the two sides describe a skill differently: the lock
// records files ("agents/skills/deploy/SKILL.md") while Team Context discovery
// describes a skill by its directory ("agents/skills/deploy"). Owns alone
// answers "no" for the directory form, and "no" here means OriginLoose — a
// valid-looking answer that silently reattributes add-on content to the team.
//
// The returned LockedFile is the lexicographically first owned file under the
// directory, so the answer is deterministic rather than map-iteration order.
func (l Lock) OwnsUnder(relDir string) (LockedAddon, LockedFile, bool) {
	if relDir == "" {
		return LockedAddon{}, LockedFile{}, false
	}
	prefix := strings.TrimSuffix(relDir, "/") + "/"

	var (
		bestAddon LockedAddon
		bestFile  LockedFile
		found     bool
	)
	for _, a := range l.Addons {
		for _, f := range a.Files {
			if !strings.HasPrefix(f.Path, prefix) {
				continue
			}
			if !found || f.Path < bestFile.Path {
				bestAddon, bestFile, found = a, f, true
			}
		}
	}
	return bestAddon, bestFile, found
}
