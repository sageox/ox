// Package skillmanager projects the canonical SageOx skill catalog into the
// native, project-scoped discovery roots declared by coding-agent adapters.
package skillmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const (
	lockSchemaVersion   = 1
	lockRelativePath    = ".sageox/skills.lock.json"
	journalRelativePath = ".sageox/cache/skills-apply.json"
	stampPrefix         = "ox"
)

// BundleRef leaves room for an authenticated source identity without coupling
// the v1 lockfile to a transport. Built-in bundles use only ID.
type BundleRef struct {
	ID string `json:"id"`
}

// DesiredSkills is the project-selected skill state. Targets contain stable
// target keys, not adapter names, so two adapters may share one projection.
type DesiredSkills struct {
	Bundles []BundleRef `json:"bundles"`
	Names   []string    `json:"names,omitempty"`
	Targets []string    `json:"targets"`
}

// FileAction is one recoverable, repository-relative mutation.
type FileAction struct {
	TargetKey      string
	Path           string
	Content        []byte
	Mode           fs.FileMode
	PreviousDigest string
	Digest         string
}

// Conflict is a path ox deliberately preserves because ownership is absent or
// the bytes no longer match the last installed digest.
type Conflict struct {
	TargetKey string
	Path      string
	Reason    string
}

// ReconcilePlan is a read-only description of the work required to converge.
// Apply writes individual files, commits the lockfile last, and is recoverable
// through a pending-operation journal.
type ReconcilePlan struct {
	Creates          []FileAction
	Updates          []FileAction
	Removes          []FileAction
	Conflicts        []Conflict
	Preserves        []string
	Warnings         []string
	TargetCount      int
	DesiredFileCount int

	repoRoot    string
	nextLock    lockFile
	lockChanged bool
	journal     applyJournal
}

type lockFile struct {
	SchemaVersion int                           `json:"schema_version"`
	Source        lockSource                    `json:"source"`
	Desired       desiredLock                   `json:"desired"`
	Targets       []adapterprotocol.SkillTarget `json:"targets"`
	ManagedFiles  []managedFile                 `json:"managed_files"`
}

type lockSource struct {
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
	Version  string `json:"version"`
}

type desiredLock struct {
	Bundles []string `json:"bundles"`
	Names   []string `json:"names,omitempty"`
	Targets []string `json:"targets"`
}

type managedFile struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   string `json:"mode"`
}

type applyJournal struct {
	SchemaVersion int             `json:"schema_version"`
	Actions       []journalAction `json:"actions"`
	NextLock      lockFile        `json:"next_lock"`
}

type catalogSource interface {
	Digest() (string, error)
	Select(version string, desired DesiredSkills) ([]skills.Skill, error)
}

type builtInCatalog struct{}

func (builtInCatalog) Digest() (string, error) { return skills.Digest() }
func (builtInCatalog) Select(version string, desired DesiredSkills) ([]skills.Skill, error) {
	return selectDesiredSkills(version, desired)
}

type journalAction struct {
	Path           string `json:"path"`
	PreviousDigest string `json:"previous_digest,omitempty"`
	Digest         string `json:"digest,omitempty"`
}

// LockPath returns the project ownership manifest path.
func LockPath(repoRoot string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(lockRelativePath))
}

func journalPath(repoRoot string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(journalRelativePath))
}

// WrittenPaths returns repository-relative files created or updated by Apply.
func (plan *ReconcilePlan) WrittenPaths() []string {
	return actionPaths(plan.Creates, plan.Updates)
}

// RemovedPaths returns repository-relative files removed by Apply.
func (plan *ReconcilePlan) RemovedPaths() []string { return actionPaths(plan.Removes) }

// LockChanged reports whether Apply committed a new desired/ownership state.
func (plan *ReconcilePlan) LockChanged() bool { return plan.lockChanged }

// Converged reports whether applying the plan can fully realize desired state.
func (plan *ReconcilePlan) Converged() bool {
	return len(plan.Conflicts) == 0 && len(plan.Warnings) == 0
}

// LegacyBundles infers bundle opt-ins from verified pre-lockfile stamps. Any
// valid member is enough to select its bundle so a partial install is repaired.
func LegacyBundles(repoRoot string, target adapterprotocol.SkillTarget) ([]string, error) {
	target, err := normalizeTarget(repoRoot, target)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(repoRoot, filepath.FromSlash(target.Root))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	managedNames := map[string]struct{}{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, readErr := readNoSymlink(filepath.Join(root, entry.Name(), skills.SkillFileName))
		if os.IsNotExist(readErr) {
			continue
		}
		// A symlinked or non-regular SKILL.md here belongs to SOMEONE ELSE. This
		// directory is shared: a repo puts its own skills beside ox's, and some
		// of them are generated symlinks (the sageox monorepo's agent-parity
		// mirrors are exactly this). Such a file cannot carry a valid ox stamp,
		// so it can never be one of ours — skipping it is the correct answer.
		//
		// Aborting instead is what made skill rollout silently dead: this loop
		// exists only to discover which bundles are already installed, and one
		// foreign symlink returned an error for the WHOLE scan, so `ox doctor`
		// reported "cannot inspect managed skills" and reconciled nothing. Every
		// later ox release then failed to reach the repo — which is how a repo
		// ends up missing ox-pr-header while the guidance points at it.
		if errors.Is(readErr, ErrNonRegularFile) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		if validLegacyStamp(data) {
			managedNames[entry.Name()] = struct{}{}
		}
	}
	var bundles []string
	for _, bundle := range skills.Catalog {
		for _, name := range bundle.SkillIDs {
			if _, ok := managedNames[name]; ok {
				bundles = append(bundles, bundle.ID)
				break
			}
		}
	}
	return sortedUnique(bundles), nil
}

// CanonicalizeTargets validates, normalizes, and deduplicates target
// descriptors. A shared root with incompatible metadata is an error.
func CanonicalizeTargets(repoRoot string, targets []adapterprotocol.SkillTarget) ([]adapterprotocol.SkillTarget, error) {
	byProjection := map[string]adapterprotocol.SkillTarget{}
	byKey := map[string]adapterprotocol.SkillTarget{}
	for _, target := range targets {
		normalized, err := normalizeTarget(repoRoot, target)
		if err != nil {
			return nil, err
		}
		if prior, ok := byKey[normalized.Key]; ok && prior != normalized {
			return nil, fmt.Errorf("skill target %q has conflicting declarations", normalized.Key)
		}
		byKey[normalized.Key] = normalized
		projection := normalized.Root
		if prior, ok := byProjection[projection]; ok {
			if prior.Format != normalized.Format || prior.Scope != normalized.Scope || prior.LinkPolicy != normalized.LinkPolicy {
				return nil, fmt.Errorf("skill target root %q has incompatible declarations", normalized.Root)
			}
			continue
		}
		byProjection[projection] = normalized
	}
	out := make([]adapterprotocol.SkillTarget, 0, len(byProjection))
	for _, target := range byProjection {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root == out[j].Root {
			return out[i].Format < out[j].Format
		}
		return out[i].Root < out[j].Root
	})
	return out, nil
}

func normalizeTarget(repoRoot string, target adapterprotocol.SkillTarget) (adapterprotocol.SkillTarget, error) {
	if target.Key == "" || target.Format == "" {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target key and format are required")
	}
	if target.Scope == "" {
		target.Scope = adapterprotocol.SkillScopeProject
	}
	if target.Scope != adapterprotocol.SkillScopeProject {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target %q uses unsupported scope %q", target.Key, target.Scope)
	}
	if target.LinkPolicy == "" {
		target.LinkPolicy = adapterprotocol.SkillLinkPolicyReject
	}
	if target.LinkPolicy != adapterprotocol.SkillLinkPolicyReject {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target %q uses unsupported link policy %q", target.Key, target.LinkPolicy)
	}
	root := filepath.Clean(filepath.FromSlash(target.Root))
	if root == "." || filepath.IsAbs(root) {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target %q root must be repository-relative", target.Key)
	}
	if err := ensureWithin(repoRoot, filepath.Join(repoRoot, root)); err != nil {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target %q: %w", target.Key, err)
	}
	first := strings.Split(filepath.ToSlash(root), "/")[0]
	if first == ".git" || first == ".sageox" {
		return adapterprotocol.SkillTarget{}, fmt.Errorf("skill target %q uses reserved root %q", target.Key, first)
	}
	target.Root = filepath.ToSlash(root)
	return target, nil
}

// LoadDesired returns the committed project selection and target snapshots.
// A missing lockfile is a valid empty selection.
func LoadDesired(repoRoot string) (DesiredSkills, []adapterprotocol.SkillTarget, error) {
	lock, _, err := readLock(repoRoot)
	if err != nil {
		return DesiredSkills{}, nil, err
	}
	return desiredFromLock(lock), append([]adapterprotocol.SkillTarget(nil), lock.Targets...), nil
}

func desiredFromLock(lock lockFile) DesiredSkills {
	desired := DesiredSkills{Names: append([]string(nil), lock.Desired.Names...), Targets: append([]string(nil), lock.Desired.Targets...)}
	for _, id := range lock.Desired.Bundles {
		desired.Bundles = append(desired.Bundles, BundleRef{ID: id})
	}
	return desired
}

// DefaultDesired selects built-in default bundles for the supplied targets.
func DefaultDesired(targets []adapterprotocol.SkillTarget) DesiredSkills {
	desired := DesiredSkills{}
	for _, id := range skills.DefaultBundleIDs() {
		desired.Bundles = append(desired.Bundles, BundleRef{ID: id})
	}
	for _, target := range targets {
		desired.Targets = append(desired.Targets, target.Key)
	}
	return normalizeDesired(desired)
}

// AddBundles returns a normalized desired state with bundle opt-ins added.
func AddBundles(desired DesiredSkills, ids ...string) DesiredSkills {
	for _, id := range ids {
		desired.Bundles = append(desired.Bundles, BundleRef{ID: id})
	}
	return normalizeDesired(desired)
}

// AddTargets returns a normalized desired state with target selections added.
func AddTargets(desired DesiredSkills, targets ...adapterprotocol.SkillTarget) DesiredSkills {
	for _, target := range targets {
		desired.Targets = append(desired.Targets, target.Key)
	}
	return normalizeDesired(desired)
}

func normalizeDesired(desired DesiredSkills) DesiredSkills {
	bundles := map[string]struct{}{}
	for _, bundle := range desired.Bundles {
		if bundle.ID != "" {
			bundles[bundle.ID] = struct{}{}
		}
	}
	desired.Bundles = desired.Bundles[:0]
	for id := range bundles {
		desired.Bundles = append(desired.Bundles, BundleRef{ID: id})
	}
	sort.Slice(desired.Bundles, func(i, j int) bool { return desired.Bundles[i].ID < desired.Bundles[j].ID })
	desired.Names = sortedUnique(desired.Names)
	desired.Targets = sortedUnique(desired.Targets)
	return desired
}

// Plan inspects the project without writing and returns a deterministic plan.
func Plan(repoRoot, version string, desired DesiredSkills, targets []adapterprotocol.SkillTarget) (*ReconcilePlan, error) {
	return planWithSource(repoRoot, version, desired, targets, builtInCatalog{})
}

func planWithSource(repoRoot, version string, desired DesiredSkills, targets []adapterprotocol.SkillTarget, source catalogSource) (*ReconcilePlan, error) {
	desired = normalizeDesired(desired)
	targets, err := CanonicalizeTargets(repoRoot, targets)
	if err != nil {
		return nil, err
	}
	old, oldBytes, err := readLock(repoRoot)
	if err != nil {
		return nil, err
	}
	journal, err := readJournal(repoRoot)
	if err != nil {
		return nil, err
	}
	plan := &ReconcilePlan{repoRoot: repoRoot, journal: journal, TargetCount: len(desired.Targets)}
	if old.SchemaVersion > lockSchemaVersion {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills lock schema %d is newer than this ox supports", old.SchemaVersion))
		plan.nextLock = old
		return plan, nil
	}
	if version != "" && old.Source.Version != "" && agentx.CompareVersions(version, old.Source.Version) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills were installed by newer ox %s; refusing downgrade to %s", old.Source.Version, version))
		plan.nextLock = old
		return plan, nil
	}

	targetByKey := map[string]adapterprotocol.SkillTarget{}
	for _, target := range old.Targets {
		normalized, normalizeErr := normalizeTarget(repoRoot, target)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid skill target in lockfile: %w", normalizeErr)
		}
		targetByKey[normalized.Key] = normalized
	}
	for _, target := range targets {
		if prior, ok := targetByKey[target.Key]; ok && prior != target {
			return nil, fmt.Errorf("skill target %q changed incompatibly", target.Key)
		}
		targetByKey[target.Key] = target
	}
	for _, key := range desired.Targets {
		if _, ok := targetByKey[key]; !ok {
			return nil, fmt.Errorf("desired skill target %q has no descriptor", key)
		}
	}

	digest, err := source.Digest()
	if err != nil {
		return nil, err
	}
	sourceVersion := version
	if sourceVersion == "" {
		sourceVersion = old.Source.Version
	}
	next := lockFile{
		SchemaVersion: lockSchemaVersion,
		Source:        lockSource{Kind: "builtin", Revision: digest, Version: sourceVersion},
		Desired: desiredLock{
			Bundles: bundleIDs(desired.Bundles),
			Names:   append([]string(nil), desired.Names...),
			Targets: append([]string(nil), desired.Targets...),
		},
	}

	selectedSkills, err := source.Select(version, desired)
	if err != nil {
		return nil, err
	}
	oldFiles := make(map[string]managedFile, len(old.ManagedFiles))
	for _, file := range old.ManagedFiles {
		oldFiles[file.Path] = file
	}
	journalFiles := journalOwnership(journal)
	desiredPaths := map[string]struct{}{}
	selectedTargets := stringSet(desired.Targets)
	plan.TargetCount = len(selectedTargets)
	for _, skill := range selectedSkills {
		plan.DesiredFileCount += len(skill.Files) * len(selectedTargets)
	}

	keys := make([]string, 0, len(targetByKey))
	for key := range targetByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := targetByKey[key]
		if err := checkDir(repoRoot, filepath.Join(repoRoot, filepath.FromSlash(target.Root))); err != nil {
			return nil, fmt.Errorf("inspect skill target %s: %w", key, err)
		}
		if _, selected := selectedTargets[key]; !selected {
			continue
		}
		next.Targets = append(next.Targets, target)
		for _, skill := range selectedSkills {
			skillRoot := filepath.ToSlash(filepath.Join(target.Root, skill.Name))
			migrationOwned := false
			skillPath := filepath.ToSlash(filepath.Join(skillRoot, skills.SkillFileName))
			if _, locked := oldFiles[skillPath]; !locked {
				if data, readErr := readRepoFile(repoRoot, skillPath); readErr == nil {
					migrationOwned = validLegacyStamp(data)
					if action, ok := journalFiles[skillPath]; ok {
						actualDigest := digestBytes(data)
						migrationOwned = migrationOwned || actualDigest == action.PreviousDigest || actualDigest == action.Digest
					}
					if !migrationOwned {
						plan.addConflict(key, skillPath, "existing skill is not managed by ox")
						for _, file := range skill.Files {
							path := filepath.ToSlash(filepath.Join(skillRoot, file.Path))
							desiredPaths[path] = struct{}{}
							plan.Preserves = append(plan.Preserves, path)
						}
						continue
					}
				} else if readErr != nil && !os.IsNotExist(readErr) {
					return nil, readErr
				}
			}
			for _, file := range skill.Files {
				path := filepath.ToSlash(filepath.Join(skillRoot, file.Path))
				desiredPaths[path] = struct{}{}
				content := file.Content
				mode := desiredFileMode(file.Path)
				want := digestBytes(content)
				oldFile, locked := oldFiles[path]
				actual, actualMode, readErr := inspectRepoFile(repoRoot, path)
				if readErr != nil && !os.IsNotExist(readErr) {
					return nil, readErr
				}
				if os.IsNotExist(readErr) {
					plan.Creates = append(plan.Creates, FileAction{TargetKey: key, Path: path, Content: content, Mode: mode, Digest: want})
					next.ManagedFiles = append(next.ManagedFiles, managedFile{Target: key, Path: path, Digest: want, Mode: modeString(mode)})
					continue
				}
				actualDigest := digestBytes(actual)
				owned := locked && actualDigest == oldFile.Digest
				if action, ok := journalFiles[path]; ok && (actualDigest == action.PreviousDigest || actualDigest == action.Digest) {
					owned = true
				}
				if !owned && migrationOwned && (file.Path == skills.SkillFileName || actualDigest == want) {
					owned = true
				}
				if !owned {
					plan.addConflict(key, path, "current digest differs from the last installed digest")
					plan.Preserves = append(plan.Preserves, path)
					if locked {
						next.ManagedFiles = append(next.ManagedFiles, oldFile)
					}
					continue
				}
				if actualDigest != want || actualMode.Perm() != mode.Perm() {
					plan.Updates = append(plan.Updates, FileAction{TargetKey: key, Path: path, Content: content, Mode: mode, PreviousDigest: actualDigest, Digest: want})
				} else {
					plan.Preserves = append(plan.Preserves, path)
				}
				next.ManagedFiles = append(next.ManagedFiles, managedFile{Target: key, Path: path, Digest: want, Mode: modeString(mode)})
			}
		}
	}

	for _, oldFile := range old.ManagedFiles {
		if _, wanted := desiredPaths[oldFile.Path]; wanted {
			continue
		}
		actual, _, readErr := inspectRepoFile(repoRoot, oldFile.Path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		actualDigest := digestBytes(actual)
		owned := actualDigest == oldFile.Digest
		if action, ok := journalFiles[oldFile.Path]; ok && (actualDigest == action.PreviousDigest || actualDigest == action.Digest) {
			owned = true
		}
		if !owned {
			plan.addConflict(oldFile.Target, oldFile.Path, "retired managed file was modified and will be preserved")
			plan.Preserves = append(plan.Preserves, oldFile.Path)
			continue // relinquish ownership so uninstall/retirement can converge
		}
		plan.Removes = append(plan.Removes, FileAction{TargetKey: oldFile.Target, Path: oldFile.Path, PreviousDigest: actualDigest})
	}

	// Inline stamps are one-release migration evidence for retired skills that
	// predate the lockfile. Only their verified SKILL.md is attributable.
	for _, target := range targetByKey {
		retired, retiredErr := retiredLegacyFiles(repoRoot, target, desiredPaths, oldFiles)
		if retiredErr != nil {
			return nil, retiredErr
		}
		plan.Removes = append(plan.Removes, retired...)
	}

	neededTargets := stringSet(next.Desired.Targets)
	for _, file := range next.ManagedFiles {
		neededTargets[file.Target] = struct{}{}
	}
	for _, key := range sortedSet(neededTargets) {
		if target, ok := targetByKey[key]; ok && !containsTarget(next.Targets, key) {
			next.Targets = append(next.Targets, target)
		}
	}
	sortLock(&next)
	plan.nextLock = next
	nextBytes, err := marshalLock(next)
	if err != nil {
		return nil, err
	}
	plan.lockChanged = !bytes.Equal(oldBytes, nextBytes)
	plan.journal = makeJournal(plan, next)
	plan.sort()
	return plan, nil
}

// Apply executes a previously built plan. Files are updated individually and
// the lockfile is committed last. The journal makes old and new action digests
// valid recovery states if the process exits between those steps.
func Apply(plan *ReconcilePlan) error {
	if plan == nil {
		return fmt.Errorf("nil skill reconcile plan")
	}
	if len(plan.Warnings) > 0 {
		return nil
	}
	if len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && !plan.lockChanged {
		_ = os.Remove(journalPath(plan.repoRoot))
		return nil
	}
	if err := ensureDir(plan.repoRoot, filepath.Dir(journalPath(plan.repoRoot))); err != nil {
		return err
	}
	journalBytes, err := json.MarshalIndent(plan.journal, "", "  ")
	if err != nil {
		return err
	}
	journalBytes = append(journalBytes, '\n')
	if err := atomicWriteNoSymlink(journalPath(plan.repoRoot), journalBytes, 0o600); err != nil {
		return fmt.Errorf("write skill apply journal: %w", err)
	}
	for _, action := range append(append([]FileAction{}, plan.Creates...), plan.Updates...) {
		if digestBytes(action.Content) != action.Digest {
			return fmt.Errorf("skill action content digest changed for %s", action.Path)
		}
		path := filepath.Join(plan.repoRoot, filepath.FromSlash(action.Path))
		if err := ensureDir(plan.repoRoot, filepath.Dir(path)); err != nil {
			return err
		}
		actual, _, readErr := inspectRepoFile(plan.repoRoot, action.Path)
		if readErr == nil {
			actualDigest := digestBytes(actual)
			if actualDigest == action.Digest {
				continue
			}
			if action.PreviousDigest == "" || actualDigest != action.PreviousDigest {
				return fmt.Errorf("skill file changed after planning: %s", action.Path)
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		} else if action.PreviousDigest != "" {
			return fmt.Errorf("skill file disappeared after planning: %s", action.Path)
		}
		if err := atomicWriteNoSymlink(path, action.Content, action.Mode); err != nil {
			return fmt.Errorf("write skill file %s: %w", action.Path, err)
		}
	}
	for _, action := range plan.Removes {
		path := filepath.Join(plan.repoRoot, filepath.FromSlash(action.Path))
		actual, _, readErr := inspectRepoFile(plan.repoRoot, action.Path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if digestBytes(actual) != action.PreviousDigest {
			return fmt.Errorf("skill file changed after planning: %s", action.Path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove skill file %s: %w", action.Path, err)
		}
		removeEmptyParents(plan.repoRoot, filepath.Dir(path))
	}
	if err := ensureDir(plan.repoRoot, filepath.Dir(LockPath(plan.repoRoot))); err != nil {
		return err
	}
	lockBytes, err := marshalLock(plan.nextLock)
	if err != nil {
		return err
	}
	if err := atomicWriteNoSymlink(LockPath(plan.repoRoot), lockBytes, 0o644); err != nil {
		return fmt.Errorf("write skills lockfile: %w", err)
	}
	if err := os.Remove(journalPath(plan.repoRoot)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove skill apply journal: %w", err)
	}
	return nil
}

// DesiredUpdate computes a desired-state mutation while the project manifest
// lock is held, preventing concurrent lifecycle commands from losing targets
// or bundle opt-ins through a stale read-modify-write.
type DesiredUpdate func(DesiredSkills, []adapterprotocol.SkillTarget) (DesiredSkills, []adapterprotocol.SkillTarget, error)

// ReconcileUpdate serializes desired-state mutation, Plan, and Apply.
func ReconcileUpdate(repoRoot, version string, update DesiredUpdate) (*ReconcilePlan, error) {
	var plan *ReconcilePlan
	err := fileutil.WithFileLock(context.Background(), LockPath(repoRoot), func() error {
		desired, targets, err := LoadDesired(repoRoot)
		if err != nil {
			return err
		}
		desired, targets, err = update(desired, targets)
		if err != nil {
			return err
		}
		plan, err = Plan(repoRoot, version, desired, targets)
		if err != nil {
			return err
		}
		return Apply(plan)
	})
	return plan, err
}

// Reconcile replaces desired state and is primarily useful to tests and
// compatibility callers. Lifecycle read-modify-write paths use ReconcileUpdate.
func Reconcile(repoRoot, version string, desired DesiredSkills, targets []adapterprotocol.SkillTarget) (*ReconcilePlan, error) {
	return ReconcileUpdate(repoRoot, version, func(DesiredSkills, []adapterprotocol.SkillTarget) (DesiredSkills, []adapterprotocol.SkillTarget, error) {
		return desired, targets, nil
	})
}

// Install, Check, and Uninstall retain the adapter RPC lifecycle for one
// compatibility release. Built-in CLI paths use the target-centric API above.
func Install(p adapterprotocol.SkillsParams, dir string) (*adapterprotocol.InstallSkillsResponse, error) {
	target, err := targetForDir(p.RepoRoot, dir)
	if err != nil {
		return nil, err
	}
	plan, err := ReconcileUpdate(p.RepoRoot, p.Version, func(desired DesiredSkills, targets []adapterprotocol.SkillTarget) (DesiredSkills, []adapterprotocol.SkillTarget, error) {
		if len(desired.Bundles) == 0 && len(desired.Names) == 0 {
			desired = DefaultDesired(nil)
		}
		desired = AddTargets(desired, target)
		targets = append(targets, target)
		if len(p.Bundles) > 0 {
			desired = AddBundles(desired, p.Bundles...)
		}
		if len(p.Names) > 0 {
			desired.Names = append(desired.Names, p.Names...)
			desired = normalizeDesired(desired)
		}
		return desired, targets, nil
	})
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.InstallSkillsResponse{
		Installed:    len(plan.Conflicts) == 0 && len(plan.Warnings) == 0,
		FilesWritten: actionPaths(plan.Creates, plan.Updates),
		Conflicts:    conflictPaths(plan.Conflicts),
	}, nil
}

func Check(p adapterprotocol.SkillsParams, dir string) (*adapterprotocol.CheckSkillsResponse, error) {
	target, err := targetForDir(p.RepoRoot, dir)
	if err != nil {
		return nil, err
	}
	desired, targets, err := LoadDesired(p.RepoRoot)
	if err != nil {
		return nil, err
	}
	if len(desired.Bundles) == 0 && len(desired.Names) == 0 {
		desired = DefaultDesired([]adapterprotocol.SkillTarget{target})
	}
	if len(p.Bundles) > 0 {
		desired = AddBundles(desired, p.Bundles...)
	}
	if len(p.Names) > 0 {
		desired.Names = append(desired.Names, p.Names...)
		desired = normalizeDesired(desired)
	}
	desired = AddTargets(desired, target)
	targets = append(targets, target)
	plan, err := Plan(p.RepoRoot, p.Version, desired, targets)
	if err != nil {
		return nil, err
	}
	missing, stale := classifyActions(target.Root, plan)
	conflicts := conflictSkills(target.Root, plan.Conflicts)
	return &adapterprotocol.CheckSkillsResponse{
		Installed: len(missing) == 0 && len(stale) == 0 && len(conflicts) == 0 && len(plan.Warnings) == 0,
		Missing:   missing, Stale: stale, Conflicts: conflicts,
		SkillsDir: filepath.Join(p.RepoRoot, filepath.FromSlash(target.Root)), Total: desiredSkillCount(p.Version, desired),
	}, nil
}

func Uninstall(p adapterprotocol.SkillsParams, dir string) (*adapterprotocol.UninstallSkillsResponse, error) {
	target, err := targetForDir(p.RepoRoot, dir)
	if err != nil {
		return nil, err
	}
	plan, err := ReconcileUpdate(p.RepoRoot, p.Version, func(desired DesiredSkills, targets []adapterprotocol.SkillTarget) (DesiredSkills, []adapterprotocol.SkillTarget, error) {
		desired.Targets = removeString(desired.Targets, target.Key)
		targets = append(targets, target)
		return desired, targets, nil
	})
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.UninstallSkillsResponse{
		Uninstalled:  len(plan.Removes) > 0,
		FilesRemoved: actionPaths(plan.Removes),
		Conflicts:    conflictPaths(plan.Conflicts),
	}, nil
}

func targetForDir(repoRoot, dir string) (adapterprotocol.SkillTarget, error) {
	rel, err := filepath.Rel(repoRoot, dir)
	if err != nil {
		return adapterprotocol.SkillTarget{}, err
	}
	root := filepath.ToSlash(filepath.Clean(rel))
	key := strings.Trim(strings.ReplaceAll(root, "/", "-"), ".-")
	switch root {
	case ".agents/skills":
		key = "agents-project"
	case ".claude/skills":
		key = "claude-project"
	}
	return normalizeTarget(repoRoot, adapterprotocol.SkillTarget{Key: key, Root: root, Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject})
}

func selectDesiredSkills(version string, desired DesiredSkills) ([]skills.Skill, error) {
	ids := bundleIDs(desired.Bundles)
	names, err := skills.BundleNames(ids)
	if err != nil {
		return nil, err
	}
	names = append(names, desired.Names...)
	if len(names) == 0 {
		return nil, nil
	}
	return skills.Selected(version, sortedUnique(names))
}

func bundleIDs(refs []BundleRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return sortedUnique(ids)
}

func readLock(repoRoot string) (lockFile, []byte, error) {
	path := LockPath(repoRoot)
	data, err := readNoSymlink(path)
	if os.IsNotExist(err) {
		return lockFile{}, nil, nil
	}
	if err != nil {
		return lockFile{}, nil, fmt.Errorf("read skills lockfile: %w", err)
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return lockFile{}, nil, fmt.Errorf("parse skills lockfile: %w", err)
	}
	if lock.SchemaVersion <= 0 {
		return lockFile{}, nil, fmt.Errorf("parse skills lockfile: missing schema_version")
	}
	canonical, err := marshalLock(lock)
	if err != nil {
		return lockFile{}, nil, err
	}
	return lock, canonical, nil
}

func readJournal(repoRoot string) (applyJournal, error) {
	data, err := readNoSymlink(journalPath(repoRoot))
	if os.IsNotExist(err) {
		return applyJournal{}, nil
	}
	if err != nil {
		return applyJournal{}, fmt.Errorf("read skill apply journal: %w", err)
	}
	var journal applyJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return applyJournal{}, fmt.Errorf("parse skill apply journal: %w", err)
	}
	if journal.SchemaVersion != lockSchemaVersion {
		return applyJournal{}, fmt.Errorf("unsupported skill apply journal schema %d", journal.SchemaVersion)
	}
	return journal, nil
}

func marshalLock(lock lockFile) ([]byte, error) {
	sortLock(&lock)
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func sortLock(lock *lockFile) {
	lock.Desired.Bundles = sortedUnique(lock.Desired.Bundles)
	lock.Desired.Names = sortedUnique(lock.Desired.Names)
	lock.Desired.Targets = sortedUnique(lock.Desired.Targets)
	sort.Slice(lock.Targets, func(i, j int) bool { return lock.Targets[i].Key < lock.Targets[j].Key })
	sort.Slice(lock.ManagedFiles, func(i, j int) bool { return lock.ManagedFiles[i].Path < lock.ManagedFiles[j].Path })
}

func makeJournal(plan *ReconcilePlan, next lockFile) applyJournal {
	journal := applyJournal{SchemaVersion: lockSchemaVersion, NextLock: next}
	for _, action := range append(append(append([]FileAction{}, plan.Creates...), plan.Updates...), plan.Removes...) {
		journal.Actions = append(journal.Actions, journalAction{Path: action.Path, PreviousDigest: action.PreviousDigest, Digest: action.Digest})
	}
	sort.Slice(journal.Actions, func(i, j int) bool { return journal.Actions[i].Path < journal.Actions[j].Path })
	return journal
}

func journalOwnership(journal applyJournal) map[string]journalAction {
	out := make(map[string]journalAction, len(journal.Actions))
	for _, action := range journal.Actions {
		out[action.Path] = action
	}
	return out
}

func retiredLegacyFiles(repoRoot string, target adapterprotocol.SkillTarget, desired map[string]struct{}, old map[string]managedFile) ([]FileAction, error) {
	root := filepath.Join(repoRoot, filepath.FromSlash(target.Root))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var actions []FileAction
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.ToSlash(filepath.Join(target.Root, entry.Name(), skills.SkillFileName))
		if _, wanted := desired[path]; wanted {
			continue
		}
		if _, locked := old[path]; locked {
			continue
		}
		data, readErr := readRepoFile(repoRoot, path)
		if readErr != nil {
			// Same shared-directory reasoning as LegacyBundles: this walks EVERY
			// skill dir looking for retired ox skills, and a symlinked SKILL.md
			// belongs to the repo, not to ox. It cannot carry a valid ox stamp, so
			// it can never be a retirement candidate — skipping it is the answer.
			// Returning the error instead aborted the entire plan, which is what
			// made `ox doctor` report "cannot inspect managed skills" and reconcile
			// nothing for any skill.
			if os.IsNotExist(readErr) || errors.Is(readErr, ErrNonRegularFile) {
				continue
			}
			return nil, readErr
		}
		if validLegacyStamp(data) {
			actions = append(actions, FileAction{TargetKey: target.Key, Path: path, PreviousDigest: digestBytes(data)})
		}
	}
	return actions, nil
}

func validLegacyStamp(data []byte) bool {
	hash, _, body := adapterstamp.ExtractStampAnywhere(data, stampPrefix)
	return hash != "" && agentx.ContentHash(body) == hash
}

func inspectRepoFile(repoRoot, relative string) ([]byte, fs.FileMode, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(relative))
	if err := ensureWithin(repoRoot, path); err != nil {
		return nil, 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s", ErrNonRegularFile, path)
	}
	data, err := os.ReadFile(path)
	return data, info.Mode(), err
}

func readRepoFile(repoRoot, relative string) ([]byte, error) {
	data, _, err := inspectRepoFile(repoRoot, relative)
	return data, err
}

// ErrNonRegularFile marks a path ox declined to read because it is a symlink or
// otherwise not a regular file. Callers that are INSPECTING A FILE OX MANAGES
// must treat it as fatal — following a symlink out of the repo is the
// path-escape this refusal exists to prevent. Callers that are merely SCANNING
// a shared directory to discover which skills are ox's should skip it: a file
// ox cannot read is a file ox does not manage, which is an answer, not a
// failure.
var ErrNonRegularFile = errors.New("refusing non-regular or symlink file")

func readNoSymlink(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNonRegularFile, path)
	}
	return os.ReadFile(path)
}

func atomicWriteNoSymlink(path string, content []byte, mode fs.FileMode) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	// fileutil.AtomicWriteBytes intentionally follows symlinks for instruction
	// files. Managed skill paths have the opposite contract, so use a local
	// temp+rename: rename replaces a raced symlink instead of following it.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ox-skill-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	success = true
	return nil
}

func ensureDir(root, dir string) error {
	if err := ensureWithin(root, dir); err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, dir)
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			if err := os.Mkdir(cur, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing non-directory or symlink path %s", cur)
		}
	}
	return nil
}

func checkDir(root, dir string) error {
	if err := ensureWithin(root, dir); err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, dir)
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing non-directory or symlink path %s", cur)
		}
	}
	return nil
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes repository")
	}
	return nil
}

func removeEmptyParents(repoRoot, dir string) {
	for dir != repoRoot {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func desiredFileMode(path string) fs.FileMode {
	if strings.HasPrefix(filepath.ToSlash(path), "scripts/") {
		return 0o755
	}
	return 0o644
}

func modeString(mode fs.FileMode) string { return fmt.Sprintf("%04o", mode.Perm()) }

func (plan *ReconcilePlan) addConflict(target, path, reason string) {
	plan.Conflicts = append(plan.Conflicts, Conflict{TargetKey: target, Path: path, Reason: reason})
}

func (plan *ReconcilePlan) sort() {
	byPath := func(actions []FileAction) {
		sort.Slice(actions, func(i, j int) bool { return actions[i].Path < actions[j].Path })
	}
	byPath(plan.Creates)
	byPath(plan.Updates)
	byPath(plan.Removes)
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].Path < plan.Conflicts[j].Path })
	plan.Preserves = sortedUnique(plan.Preserves)
	plan.Warnings = sortedUnique(plan.Warnings)
}

func actionPaths(groups ...[]FileAction) []string {
	var paths []string
	for _, group := range groups {
		for _, action := range group {
			paths = append(paths, filepath.FromSlash(action.Path))
		}
	}
	return paths
}

func conflictPaths(conflicts []Conflict) []string {
	paths := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		paths = append(paths, filepath.FromSlash(conflict.Path))
	}
	return paths
}

func classifyActions(root string, plan *ReconcilePlan) ([]string, []string) {
	var missing, stale []string
	for _, action := range plan.Creates {
		if skill := skillName(root, action.Path); skill != "" {
			missing = append(missing, skill)
		}
	}
	for _, action := range append(append([]FileAction{}, plan.Updates...), plan.Removes...) {
		if skill := skillName(root, action.Path); skill != "" {
			stale = append(stale, skill)
		}
	}
	return sortedUnique(missing), sortedUnique(stale)
}

func conflictSkills(root string, conflicts []Conflict) []string {
	var names []string
	for _, conflict := range conflicts {
		if name := skillName(root, conflict.Path); name != "" {
			names = append(names, name)
		}
	}
	return sortedUnique(names)
}

func skillName(root, path string) string {
	rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Split(filepath.ToSlash(rel), "/")[0]
}

func desiredSkillCount(version string, desired DesiredSkills) int {
	selected, err := selectDesiredSkills(version, desired)
	if err != nil {
		return 0
	}
	return len(selected)
}

func containsTarget(targets []adapterprotocol.SkillTarget, key string) bool {
	for _, target := range targets {
		if target.Key == key {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) []string {
	set := stringSet(values)
	return sortedSet(set)
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func removeString(values []string, remove string) []string {
	out := values[:0]
	for _, value := range values {
		if value != remove {
			out = append(out, value)
		}
	}
	return out
}
