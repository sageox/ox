package addons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// transactionTimeout bounds one add-ons operation: the wait for the Team
// Context's advisory repository lock, the file writes it guards, and the git
// invocations that record them. Matches the budget cmd/ox's other Team
// Context writers use (ox skills publish's teamPublishTimeout) — generous
// for a local commit, short enough that a wedged index fails fast instead of
// hanging a terminal.
const transactionTimeout = 30 * time.Second

// Op names which add-ons operation produced a Result.
type Op string

const (
	OpInstall Op = "install"
	OpUpdate  Op = "update"
	OpRemove  Op = "remove"
)

// Result reports what one Install, Update, or Remove call did to a Team
// Context checkout. Plain data with json tags: a CLI another package owns
// renders it, so this never formats or prints.
type Result struct {
	Addon   string `json:"addon"`
	Op      Op     `json:"op"`
	Source  string `json:"source,omitempty"`
	Version string `json:"version,omitempty"`

	// Written is every path this operation created or overwrote.
	Written []string `json:"written"`
	// Removed is every path this operation deleted — Remove's own paths, or
	// paths an Update dropped because the new version no longer ships them.
	Removed []string `json:"removed"`
	// Refused is set only when the operation hit a namespace collision
	// (ADR-032 D3) and refused to run. The same paths also come back on a
	// *CollisionError from the returned error; nothing in Written or Removed
	// happened.
	Refused []string `json:"refused,omitempty"`
	// Modified names owned paths whose on-disk bytes no longer matched what
	// the lock recorded before this operation ran — the team edited a file
	// ADR-032 D4 says is not theirs to edit. Informational only: Update
	// overwrites and Remove deletes these regardless: there is no merge and
	// Team Context git history is the undo.
	Modified []string `json:"modified,omitempty"`
}

// CollisionError reports that an operation refused to run because one or
// more destination paths already exist — on disk or in git — and the lock
// does not own them. ADR-032 D3's one collision rule: the whole operation
// refuses by name and nothing is written.
type CollisionError struct {
	Addon string
	Paths []string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("%q collides with %d existing path(s) not owned by ox: %s — refusing to overwrite hand-authored or foreign content",
		e.Addon, len(e.Paths), strings.Join(e.Paths, ", "))
}

// ErrAlreadyInstalled is returned by Install when the lock already has a
// record for the add-on; call Update instead.
var ErrAlreadyInstalled = errors.New("add-on already installed")

// ErrNotInstalled is returned by Update and Remove when the lock has no
// record for the add-on.
var ErrNotInstalled = errors.New("add-on not installed")

// Install resolves name at version (empty means the provider's current
// version) and writes it into the Team Context at teamPath. Refuses,
// mutating nothing, when the add-on is already installed (use Update) or
// when any destination path is already occupied by content the lock does
// not own (ADR-032 D3).
func Install(ctx context.Context, teamPath string, p Provider, name, version string) (Result, error) {
	resolved, err := p.Resolve(ctx, name, version)
	if err != nil {
		return Result{}, fmt.Errorf("resolve %s: %w", name, err)
	}
	files, err := validateResolved(name, resolved)
	if err != nil {
		return Result{}, err
	}

	result := &Result{
		Addon: name, Op: OpInstall, Source: resolved.Source, Version: resolved.Version,
		Written: []string{}, Removed: []string{},
	}
	return runTeamTransaction(ctx, teamPath, "add-ons: install "+name+" "+resolved.Version, result,
		func(tx *transaction) error {
			if _, ok := tx.lock.Find(name); ok {
				return fmt.Errorf("%w: %q — run `ox addons update` instead", ErrAlreadyInstalled, name)
			}

			collisions, err := tx.collisionsFor(files, nil)
			if err != nil {
				return err
			}
			if len(collisions) > 0 {
				result.Refused = collisions
				return &CollisionError{Addon: name, Paths: collisions}
			}

			locked := LockedAddon{
				Addon: name, Source: resolved.Source, Version: resolved.Version,
				Digest: resolved.Digest, InstalledAt: time.Now().UTC(),
				Files: make([]LockedFile, 0, len(files)),
			}
			for _, f := range files {
				mode := fileMode(f.Mode)
				if err := tx.write(f.Path, f.Content, mode); err != nil {
					return err
				}
				locked.Files = append(locked.Files, LockedFile{
					Path: f.Path, Digest: contentDigest(f.Content), Mode: uint32(mode),
				})
				result.Written = append(result.Written, f.Path)
			}
			tx.lock.Addons = append(tx.lock.Addons, locked)
			return nil
		})
}

// Update re-resolves name to its provider's current version (Resolve with an
// empty version string) and overwrites every path the installed version
// owns, removing owned paths the new version no longer ships. ADR-032 D4: no
// merge, no conflict state — Team Context git history is the undo. Before
// writing anything, Update compares each currently-owned path's on-disk
// bytes to what the lock recorded and reports a mismatch in Result.Modified
// — honesty that a team edit is about to be discarded, not a chance to keep
// it.
func Update(ctx context.Context, teamPath string, p Provider, name string) (Result, error) {
	resolved, err := p.Resolve(ctx, name, "")
	if err != nil {
		return Result{}, fmt.Errorf("resolve %s: %w", name, err)
	}
	files, err := validateResolved(name, resolved)
	if err != nil {
		return Result{}, err
	}

	result := &Result{
		Addon: name, Op: OpUpdate, Source: resolved.Source, Version: resolved.Version,
		Written: []string{}, Removed: []string{},
	}
	return runTeamTransaction(ctx, teamPath, "add-ons: update "+name+" to "+resolved.Version, result,
		func(tx *transaction) error {
			old, ok := tx.lock.Find(name)
			if !ok {
				return fmt.Errorf("%w: %q — run `ox addons install` instead", ErrNotInstalled, name)
			}
			ownedBefore := make(map[string]bool, len(old.Files))
			for _, f := range old.Files {
				ownedBefore[f.Path] = true
			}

			collisions, err := tx.collisionsFor(files, ownedBefore)
			if err != nil {
				return err
			}
			if len(collisions) > 0 {
				result.Refused = collisions
				return &CollisionError{Addon: name, Paths: collisions}
			}

			// Honesty check BEFORE any write or removal: compare every
			// currently-owned path's on-disk bytes (whether the new version
			// keeps or drops it) against the digest the lock recorded last
			// time.
			modified, err := tx.modifiedSince(old.Files)
			if err != nil {
				return err
			}
			result.Modified = modified

			newPaths := make(map[string]bool, len(files))
			locked := LockedAddon{
				Addon: name, Source: resolved.Source, Version: resolved.Version,
				Digest: resolved.Digest, InstalledAt: time.Now().UTC(),
				Files: make([]LockedFile, 0, len(files)),
			}
			for _, f := range files {
				newPaths[f.Path] = true
				mode := fileMode(f.Mode)
				if err := tx.write(f.Path, f.Content, mode); err != nil {
					return err
				}
				locked.Files = append(locked.Files, LockedFile{
					Path: f.Path, Digest: contentDigest(f.Content), Mode: uint32(mode),
				})
				result.Written = append(result.Written, f.Path)
			}

			var dropped []string
			for _, of := range old.Files {
				if newPaths[of.Path] {
					continue
				}
				if err := tx.remove(of.Path); err != nil {
					return err
				}
				dropped = append(dropped, of.Path)
			}
			sort.Strings(dropped)
			result.Removed = dropped
			tx.pruneEmptyDirs(dropped)

			for i, a := range tx.lock.Addons {
				if a.Addon == name {
					tx.lock.Addons[i] = locked
					return nil
				}
			}
			return fmt.Errorf("internal error: %q vanished from the lock mid-update", name)
		})
}

// Remove deletes every path name's lock record owns and drops the record. It
// never deletes a path the lock does not own, and never touches a
// neighboring add-on's or hand-authored file sharing a directory.
func Remove(ctx context.Context, teamPath string, name string) (Result, error) {
	result := &Result{Addon: name, Op: OpRemove, Written: []string{}, Removed: []string{}}
	return runTeamTransaction(ctx, teamPath, "add-ons: remove "+name, result,
		func(tx *transaction) error {
			old, ok := tx.lock.Find(name)
			if !ok {
				return fmt.Errorf("%w: %q", ErrNotInstalled, name)
			}
			result.Source = old.Source
			result.Version = old.Version

			modified, err := tx.modifiedSince(old.Files)
			if err != nil {
				return err
			}
			result.Modified = modified

			removed := make([]string, 0, len(old.Files))
			for _, of := range old.Files {
				if err := tx.remove(of.Path); err != nil {
					return err
				}
				removed = append(removed, of.Path)
			}
			sort.Strings(removed)
			result.Removed = removed
			tx.pruneEmptyDirs(removed)

			kept := tx.lock.Addons[:0]
			for _, a := range tx.lock.Addons {
				if a.Addon != name {
					kept = append(kept, a)
				}
			}
			tx.lock.Addons = kept
			return nil
		})
}

// validateResolved checks a Provider's answer before the transaction ever
// opens the checkout. Every file must land inside agents/ with a clean,
// non-escaping, non-empty relative path (types.go's contract on File.Path),
// Resolve must have pinned a concrete version, and every destination in one
// add-on must be unique. A Provider breaking any of this is a bug the
// installer refuses rather than writes — third-party add-ons in particular
// are untrusted input (ADR-032, third-party authorship section).
func validateResolved(name string, resolved Resolved) ([]File, error) {
	if resolved.Name != name {
		return nil, fmt.Errorf("provider resolved %q but was asked for %q", resolved.Name, name)
	}
	if resolved.Version == "" {
		return nil, fmt.Errorf("provider did not pin a concrete version for %q", name)
	}
	if len(resolved.Files) == 0 {
		return nil, fmt.Errorf("%q resolved with no files", name)
	}

	// The advertised digest must match the bytes. It is what the lock records
	// as the team's selection, what a reviewer approved, and what
	// `ox addons list` compares to decide whether an update is available —
	// recorded verbatim until now, so a provider could advertise the digest a
	// team reviewed and ship different content, and every consumer downstream
	// would trust the label rather than the bytes. Per-file digests were
	// already recomputed from what gets written; this closes the last place a
	// provider claim was taken at face value.
	if resolved.Digest == "" {
		return nil, fmt.Errorf("%q advertises no digest, so its content cannot be pinned", name)
	}
	if got := addonDigest(resolved.Files); got != resolved.Digest {
		return nil, fmt.Errorf("%q advertises digest %s but its content hashes to %s — refusing content that does not match what it claims to be",
			name, resolved.Digest, got)
	}

	seen := make(map[string]bool, len(resolved.Files))
	files := make([]File, len(resolved.Files))
	for i, f := range resolved.Files {
		clean, err := cleanAddonPath(f.Path)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		if seen[clean] {
			return nil, fmt.Errorf("%q: duplicate destination path %s", name, clean)
		}
		seen[clean] = true
		f.Path = clean
		files[i] = f
	}
	return files, nil
}

// cleanAddonPath validates a Provider-supplied destination path per
// types.go's File.Path contract: slash-separated, relative, non-escaping,
// and inside agents/.
func cleanAddonPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty destination path")
	}
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("destination path %q must use slashes, not backslashes", p)
	}
	if path.Clean(p) != p {
		return "", fmt.Errorf("destination path %q is not a clean relative path", p)
	}
	if path.IsAbs(p) || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("destination path %q escapes the team context", p)
	}
	if !strings.HasPrefix(p, "agents/") {
		return "", fmt.Errorf("destination path %q is outside agents/", p)
	}
	return p, nil
}

// contentDigest is the digest LockedFile.Digest records, and the same
// function applied to on-disk bytes is what a later Update or Remove
// compares against to say "you modified this". It is computed here, from
// the bytes actually written, rather than trusted from a Provider's own
// File.Digest: Resolved.Files' digest format is a Provider's choice, and the
// only way this package can compare "then" to "now" without assuming that
// format is to own the function on both sides itself.
func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fileMode is 0o644 for every add-on file, unconditionally — the
// Provider-supplied mode is deliberately ignored.
//
// It used to honor the permission bits, so a provider supplying 0o755 got an
// executable file in the Team Context for free. catalog.go already states the
// rule this now implements: "the installer must set it explicitly from
// HasScripts, not trust Mode."
//
// Nothing is lost by refusing. The Team Context copy is source material that
// convergence projects into repositories; whether a script is ever runnable is
// decided downstream by `ox skills approve --allow-scripts`, on the projected
// copy, by a human. An executable bit here would pre-empt that decision for a
// file that arrived over a path where, per ADR-032's third-party authorship
// section, nothing verifies signatures.
//
// The embedded provider hardcodes 0o644 and embed.FS cannot carry +x anyway,
// so this closes the gap before a remote provider can walk through it.
func fileMode(uint32) fs.FileMode { return 0o644 }

// transaction is the state one Install, Update, or Remove call mutates while
// holding the Team Context's advisory repository lock. The apply functions
// above close over it; runTeamTransaction owns opening and closing the root,
// loading and writing the lock, and turning tx.written/tx.removed into a
// path-scoped commit and, on failure, a rollback.
type transaction struct {
	ctx      context.Context
	teamPath string
	root     *os.Root
	lock     Lock
	written  []string // paths created or overwritten this invocation
	removed  []string // paths deleted this invocation
}

// collisionsFor reports every path in files that is occupied and not in
// ownedBefore (the operation's own pre-existing ownership; nil for Install,
// where nothing is owned yet). "Occupied" is ADR-032 D3's one collision
// rule: owned by a DIFFERENT add-on, or present in the Team Context — on
// disk or tracked in git — without being owned at all.
func (tx *transaction) collisionsFor(files []File, ownedBefore map[string]bool) ([]string, error) {
	var collisions []string
	for _, f := range files {
		if ownedBefore[f.Path] {
			continue // this add-on already owns it; overwriting is not a collision
		}
		if _, _, ok := tx.lock.Owns(f.Path); ok {
			collisions = append(collisions, f.Path) // owned by a different add-on
			continue
		}
		occupied, err := tx.pathOccupied(f.Path)
		if err != nil {
			return nil, err
		}
		if occupied {
			collisions = append(collisions, f.Path)
		}
	}
	sort.Strings(collisions)
	return collisions, nil
}

// pathOccupied reports whether relPath is already present in the Team
// Context: on disk (including a broken symlink, via Lstat rather than Stat
// so a broken link counts as existing instead of being followed) or tracked
// in git but absent from disk. Both must count: a Team Context is a sparse
// checkout, so a worktree-only check calls a tracked-but-excluded path free,
// and a collision-refusing write would silently replace a teammate's
// history.
func (tx *transaction) pathOccupied(relPath string) (bool, error) {
	switch _, err := tx.root.Lstat(filepath.FromSlash(relPath)); {
	case err == nil:
		return true, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("inspect %s: %w", relPath, err)
	}
	return gitTracksPath(tx.ctx, tx.teamPath, relPath)
}

// modifiedSince compares each locked file's on-disk bytes, if present,
// against the digest recorded when it was last written, returning the
// sorted paths that no longer match. A path a human already deleted by hand
// is not "modified" — there is nothing to compare — so it is simply skipped.
func (tx *transaction) modifiedSince(locked []LockedFile) ([]string, error) {
	var modified []string
	for _, f := range locked {
		current, present, err := tx.readCurrent(f.Path)
		if err != nil {
			return nil, err
		}
		if present && contentDigest(current) != f.Digest {
			modified = append(modified, f.Path)
		}
	}
	sort.Strings(modified)
	return modified, nil
}

// write creates or overwrites relPath with content and records it as
// touched. Every content write in this package goes through here so
// rollback and the commit's pathspec never miss a path.
func (tx *transaction) write(relPath string, content []byte, mode fs.FileMode) error {
	rel := filepath.FromSlash(relPath)
	if dir := filepath.Dir(rel); dir != "." {
		if err := tx.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := tx.root.WriteFile(rel, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	tx.written = append(tx.written, relPath)
	return nil
}

// remove deletes relPath if present and records it as touched. Tolerates an
// already-absent path — a human may have deleted it by hand, and Remove's
// job is to make the lock stop owning it either way.
func (tx *transaction) remove(relPath string) error {
	rel := filepath.FromSlash(relPath)
	if err := tx.root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", relPath, err)
	}
	tx.removed = append(tx.removed, relPath)
	return nil
}

// readCurrent reads relPath's on-disk bytes, reporting ok=false (not an
// error) when the path is simply absent.
func (tx *transaction) readCurrent(relPath string) (content []byte, ok bool, err error) {
	data, err := tx.root.ReadFile(filepath.FromSlash(relPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", relPath, err)
	}
	return data, true, nil
}

// pruneEmptyDirs removes now-empty directories left behind after Update or
// Remove drops owned files — agents/skills/<name>/references/ and similar —
// but never agents/, agents/skills/, or agents/rules/ themselves, which stay
// as the canonical roots regardless of how many add-ons are installed.
// Best-effort: a directory it cannot remove is logged and left behind, never
// a transaction failure.
func (tx *transaction) pruneEmptyDirs(droppedPaths []string) {
	dirs := make(map[string]bool)
	for _, p := range droppedPaths {
		dirs[path.Dir(p)] = true
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	// Deepest first so a parent directory is only checked after its child
	// has already been removed in this same pass.
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })

	pruned := make(map[string]bool)
	for _, start := range ordered {
		for cur := start; cur != "." && cur != "agents" && cur != "agents/skills" && cur != "agents/rules"; cur = path.Dir(cur) {
			if pruned[cur] {
				continue
			}
			entries, err := fs.ReadDir(tx.root.FS(), cur)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := tx.root.Remove(filepath.FromSlash(cur)); err != nil {
				slog.Warn("add-ons prune left a directory behind", "path", cur, "error", err)
				break
			}
			pruned[cur] = true
		}
	}
}

// runTeamTransaction is the one write path Install, Update, and Remove
// share: acquire the Team Context's advisory repository lock, open a rooted
// handle, load the current lock, run apply to mutate the checkout and the
// in-memory lock, write the lock back, and commit disk plus lock together in
// one path-scoped commit. A failure anywhere rolls the checkout back to
// exactly what it was before this call started; a *CollisionError from apply
// mutates nothing in the first place, so there is nothing to roll back.
func runTeamTransaction(ctx context.Context, teamPath, message string, result *Result, apply func(*transaction) error) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, transactionTimeout)
	defer cancel()

	acquired := false
	lockErr := gitutil.WithRepoLock(ctx, teamPath, func() error {
		acquired = true

		// Every write below goes through a handle rooted at the checkout,
		// never a joined absolute path — a Team Context is a
		// remote-controlled clone, and a symlinked agents/ would otherwise
		// deposit files outside the tree (cmd/ox/skills_publish.go's
		// publishTeamSkillSeeds applies the same defense).
		root, err := os.OpenRoot(teamPath)
		if err != nil {
			return fmt.Errorf("open team context: %w", err)
		}
		defer func() { _ = root.Close() }()

		lock, err := loadLockRoot(root)
		if err != nil {
			return err
		}
		tx := &transaction{ctx: ctx, teamPath: teamPath, root: root, lock: lock}

		committed := false
		defer func() {
			if !committed {
				rollbackTeamWrite(teamPath, tx.written, tx.removed)
			}
		}()

		if err := apply(tx); err != nil {
			return err
		}
		if err := WriteLock(root, tx.lock); err != nil {
			return fmt.Errorf("write %s: %w", LockRelativePath, err)
		}
		tx.written = append(tx.written, LockRelativePath)

		if err := commitTeamWrite(ctx, teamPath, tx.written, tx.removed, message); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if lockErr != nil {
		if !acquired && gitutil.IsRepoLockBusy(lockErr) {
			return *result, fmt.Errorf("your Team Context at %s is syncing right now, so ox did not change it — try again in a moment", teamPath)
		}
		return *result, lockErr
	}
	return *result, nil
}

// commitTeamWrite stages and commits exactly the paths this invocation
// touched. The caller holds the repository lock and owns the deadline; this
// takes neither.
//
// Not gitutil.CommitLedgerSnapshot (AGENTS.md's canonical commit helper for
// this repository): that function validates every changed blob as LEDGER
// content (ValidateLedgerBlob) and enforces the ADR-024 sacred-file
// mass-deletion backstop, both scoped to Ledger repositories. A Team Context
// is a different repository kind with different rules, and `ox skills
// publish`'s recordTeamPublish reached the same conclusion for the same
// reason — this reuses its shape.
//
// written and removed are staged differently on purpose. `git add --sparse`
// requires the path to exist somewhere (index or disk); every written path
// was just created by this invocation, so that always holds. A removed path
// may already be gone from git entirely — the lock can be stale relative to
// a human's own `git rm`, or Remove's own tolerant-of-absence semantics —
// and `git add` on a path matching nothing errors outright, so removals use
// `git rm --cached --sparse --ignore-unmatch`, which is a no-op rather than
// a failure when there is nothing left to unstage.
func commitTeamWrite(ctx context.Context, teamPath string, written, removed []string, message string) error {
	for _, rel := range written {
		if _, err := gitutil.RunGit(ctx, teamPath, "add", "--sparse", "--", rel); err != nil {
			return fmt.Errorf("record %s in the Team Context: %w", rel, err)
		}
	}
	// Only removals git ACTUALLY knew about may go into the commit pathspec.
	//
	// `git commit -- <pathspec>` aborts the whole commit when any one pathspec
	// matches nothing git knows — it does not skip it. So a path the lock owns
	// that a human already `git rm`'d would abort every commit that mentions
	// it, which means `ox addons remove` (and an update that drops that path)
	// fails, rolls back, leaves the lock intact, and fails again identically
	// on every retry: the add-on becomes permanently unremovable from a single
	// hand-deleted file.
	//
	// The two steps above are already tolerant of that state — tx.remove
	// ignores fs.ErrNotExist and the unstage passes --ignore-unmatch. This is
	// the third step, which used to throw that tolerance away.
	committableRemovals := make([]string, 0, len(removed))
	for _, rel := range removed {
		known, err := gitTracksPath(ctx, teamPath, rel)
		if err != nil {
			return fmt.Errorf("check whether git tracks %s: %w", rel, err)
		}
		if _, err := gitutil.RunGit(ctx, teamPath, "rm", "--cached", "--sparse", "--ignore-unmatch", "--", rel); err != nil {
			return fmt.Errorf("record removal of %s in the Team Context: %w", rel, err)
		}
		if known {
			committableRemovals = append(committableRemovals, rel)
		}
	}
	all := append(append([]string{}, written...), committableRemovals...)
	if len(all) == 0 {
		return nil
	}
	args := append([]string{"commit", "-m", message, "--"}, all...)
	if _, err := gitutil.RunGit(ctx, teamPath, args...); err != nil {
		return fmt.Errorf("record the add-ons change in the Team Context: %w", err)
	}
	return nil
}

// rollbackTeamWrite puts the Team Context back the way this operation found
// it, for exactly the paths it touched. Best effort and deliberately unable
// to report failure — it only ever runs on a path that already has a real
// error to tell the human about, and an error about the handling of an error
// is how the cause gets lost. Whatever it cannot undo is logged.
//
// Unlike ox skills publish's seed rollback (which only ever creates brand
// new paths, because publish refuses any name that already exists),
// Install, Update, and Remove can touch paths with real history: Update
// overwrites an owned file, Remove deletes one. "Restore from HEAD" only
// means something for those, and a `git checkout HEAD -- <paths>` call
// whose pathspec includes even one path HEAD has never seen fails the WHOLE
// call and restores nothing — confirmed empirically, not assumed. So paths
// are split: those HEAD already has are restored from HEAD (worktree and
// index together, in one call); those it does not (this invocation's own
// brand-new writes) are deleted and unstaged.
func rollbackTeamWrite(teamPath string, written, removed []string) {
	touched := append(append([]string{}, written...), removed...)
	if len(touched) == 0 {
		return
	}
	// A FRESH deadline, not the caller's — an expired context is one of the
	// very failures this rollback exists to clean up after.
	ctx, cancel := context.WithTimeout(context.Background(), transactionTimeout)
	defer cancel()

	var atHEAD, newOnly []string
	for _, p := range touched {
		exists, checked := pathExistsAtHEAD(ctx, teamPath, p)
		if !checked {
			// The check itself did not produce a confident answer (git
			// unavailable, permission error, ...). Treating this as either
			// "new" or "at HEAD" risks deleting committed content or
			// aborting the whole checkout restore; leave it alone and say
			// so loudly instead.
			slog.Error("add-ons rollback could not determine HEAD state; leaving path untouched", "path", p, "team_context", teamPath)
			continue
		}
		if exists {
			atHEAD = append(atHEAD, p)
		} else {
			newOnly = append(newOnly, p)
		}
	}

	if len(atHEAD) > 0 {
		args := append([]string{"checkout", "--quiet", "HEAD", "--"}, atHEAD...)
		if _, err := gitutil.RunGit(ctx, teamPath, args...); err != nil {
			slog.Warn("add-ons rollback incomplete", "step", "checkout", "team_context", teamPath, "error", err)
		}
	}
	for _, p := range newOnly {
		abs := filepath.Join(teamPath, filepath.FromSlash(p))
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			slog.Warn("add-ons rollback incomplete", "step", "remove", "path", abs, "error", err)
		}
	}

	// Unstage everything touched. `git reset HEAD -- <paths>` restores an
	// index entry that exists at HEAD and drops one that does not, in a
	// single call that — unlike checkout — never fails just because some
	// paths are absent from HEAD. On an unborn branch there is no HEAD to
	// reset to, so drop the entries outright instead; --sparse there is
	// mandatory too, or `rm --cached` silently does nothing to a path
	// outside the sparse cone.
	unstage := []string{"reset", "--quiet", "HEAD", "--"}
	if _, err := gitutil.RunGit(ctx, teamPath, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		unstage = []string{"rm", "--cached", "--quiet", "--ignore-unmatch", "--sparse", "--"}
	}
	if _, err := gitutil.RunGit(ctx, teamPath, append(unstage, touched...)...); err != nil {
		slog.Warn("add-ons rollback incomplete", "step", "unstage", "team_context", teamPath, "error", err)
	}
}

// pathExistsAtHEAD reports whether HEAD has a blob at relPath. checked is
// false when the answer is not confidently known (anything other than the
// recognized "not present" shapes `git cat-file -e` produces), so a caller
// can refuse to guess rather than delete committed content or abort an
// otherwise-valid batch restore.
//
// `git cat-file -e HEAD:<path>` reports absence two different ways depending
// on whether the path currently exists on disk — "path '<p>' does not exist
// in 'HEAD'" when it is absent everywhere, "path '<p>' exists on disk, but
// not in 'HEAD'" when a rollback candidate (this invocation's own new write)
// is still sitting in the worktree. Both share the substring "in 'HEAD'";
// confirmed empirically, not assumed, since the two message shapes read as
// unrelated at a glance.
func pathExistsAtHEAD(ctx context.Context, teamPath, relPath string) (exists, checked bool) {
	_, err := gitutil.RunGit(ctx, teamPath, "cat-file", "-e", "HEAD:"+relPath)
	if err == nil {
		return true, true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "in 'HEAD'"):
		return false, true // HEAD exists; this path is not (or not yet) in it
	case strings.Contains(msg, "invalid object name"):
		return false, true // unborn branch: no HEAD at all, so nothing is "at" it
	}
	return false, false
}

// gitTracksPath reports whether relPath is tracked in the Team Context's
// git index, independent of whether it is materialized on disk — the
// sparse-checkout case a worktree-only Lstat cannot see. Mirrors
// cmd/ox/init.go's unexported helper of the same name and purpose; that one
// lives in package main and cannot be imported from here.
func gitTracksPath(ctx context.Context, teamPath, relPath string) (bool, error) {
	_, err := gitutil.RunGit(ctx, teamPath, "ls-files", "--error-unmatch", "--", relPath)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is tracked: %w", relPath, err)
}
