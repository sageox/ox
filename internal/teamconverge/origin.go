package teamconverge

import (
	"fmt"

	"github.com/sageox/ox/internal/addons"
)

// OriginResolver answers "who owns this Team Context artifact" against one
// already-loaded add-ons lock. ADR-032 D3: ownership is recorded in the
// committed lock, never inferred from a path prefix, so the only source of
// truth here is Lock.Owns.
//
// Construct one per convergence pass (via NewOriginResolver), not per
// artifact — see NewOriginResolver's doc for why.
type OriginResolver struct {
	lock addons.Lock
}

// NewOriginResolver builds a resolver from a Lock the caller already loaded.
//
// Discovery walks every artifact in a Team Context in one pass. Reloading and
// re-parsing add-ons.lock.json for each artifact turns a single-file read
// into an O(artifact count) tax on every `ox sync` — the caller (today,
// internal/teamconverge/discovery.go's FilesystemDiscovery.Discover) should
// call addons.LoadLock(request.TeamPath) exactly once at the top of Discover,
// then construct one OriginResolver and call Resolve for every artifact it
// produces. ResolveOrigin below stays as the single-lookup convenience path
// for callers that only ever resolve one artifact.
func NewOriginResolver(lock addons.Lock) OriginResolver {
	return OriginResolver{lock: lock}
}

// Resolve returns OriginAddon (with Addon/AddonVersion/Digest populated) when
// the resolver's lock owns sourcePath, and OriginLoose otherwise. sourcePath
// must be the same Team-Context-relative, slash-separated form discovery
// already produces (e.g. "agents/skills/deploy/SKILL.md") — the same shape
// LockedFile.Path is written in, since the installer and discovery walk the
// same checkout root.
func (r OriginResolver) Resolve(sourcePath string) Origin {
	lockedAddon, lockedFile, ok := r.lock.Owns(sourcePath)
	if !ok {
		// Discovery describes a SKILL by its directory ("agents/skills/deploy")
		// while the lock records the individual files inside it
		// ("agents/skills/deploy/SKILL.md"). An exact-match-only lookup
		// therefore reports every add-on-installed skill as hand-authored —
		// and reports it silently, because OriginLoose is a perfectly valid
		// answer that nothing downstream questions.
		//
		// A directory is add-on-owned when the lock owns anything beneath it.
		// Rules are already file paths and match exactly above, so this arm
		// only ever fires for the directory-shaped artifacts.
		lockedAddon, lockedFile, ok = r.lock.OwnsUnder(sourcePath)
	}
	if !ok {
		return Origin{Kind: OriginLoose}
	}
	return Origin{
		Kind:         OriginAddon,
		Addon:        lockedAddon.Addon,
		AddonVersion: lockedAddon.Version,
		Digest:       lockedFile.Digest,
	}
}

// ResolveOrigin is the single-lookup convenience path: it loads the Team
// Context's add-ons lock itself and resolves one path against it.
//
// A caller resolving many artifacts in the same convergence pass (discovery)
// must NOT call this per artifact — see NewOriginResolver's doc — but a
// caller that only ever needs one answer (a diagnostic, a single-artifact
// tool) can use this directly without hand-loading a Lock first.
//
// addons.LoadLock is trusted to distinguish "no lock file" (a team that has
// never used the Add-on Catalog — returns a zero-value Lock and a nil error,
// so every artifact resolves loose) from "a lock file exists but could not be
// read or parsed" (returns a non-nil error). This function does not itself
// paper over that distinction: an error from LoadLock is returned to the
// caller, never swallowed into a false OriginLoose. Silently treating a
// malformed lock as "no lock" would make every add-on-installed artifact look
// hand-authored — the same absent-match trap as an empty grep meaning either
// "clean" or "the check is broken".
func ResolveOrigin(teamPath, sourcePath string) (Origin, error) {
	lock, err := addons.LoadLock(teamPath)
	if err != nil {
		return Origin{}, fmt.Errorf("load add-ons lock: %w", err)
	}
	return NewOriginResolver(lock).Resolve(sourcePath), nil
}
