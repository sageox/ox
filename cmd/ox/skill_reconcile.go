package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// skillTargetsForAdapters resolves every native inventory target declared by
// adapters. The historical name remains because callers already use it, but
// the returned descriptors now cover both Agent Skills and ox-owned rules.
func skillTargetsForAdapters(repoRoot string, candidates []*adapters.ExternalAdapter) ([]adapterprotocol.SkillTarget, error) {
	var targets []adapterprotocol.SkillTarget
	for _, adapter := range candidates {
		if adapter.Info() == nil {
			continue
		}
		targets = append(targets, adapter.Info().SkillTargets...)
		targets = append(targets, adapter.Info().RuleTargets...)
	}
	return skillmanager.CanonicalizeTargets(repoRoot, targets)
}

func detectedSkillTargets(repoRoot string) ([]adapterprotocol.SkillTarget, error) {
	var candidates []*adapters.ExternalAdapter
	for _, adapter := range adapters.DiscoverExternalAdapters() {
		info := adapter.Info()
		if info != nil && (len(info.SkillTargets) > 0 || len(info.RuleTargets) > 0) && adapter.Detect() {
			candidates = append(candidates, adapter)
		}
	}
	return skillTargetsForAdapters(repoRoot, candidates)
}

// enabledBundleIDs is the set of embedded skill bundles THIS binary should
// install: the always-on defaults, plus any bundle whose feature is currently
// enabled.
//
// The catalog stays declarative and this is where policy lives, mirroring
// syncFeatureGatedCommands for commands. Without the gate, `ox-cli-cart*`
// installed for everyone and taught an AI coworker to drive `ox carts`, which
// refuses when FEATURE_CARTS is off — a skill pointing at a wall.
//
// Turning the feature off is not just a no-op for future installs: the cart
// bundle drops out of the desired set, so the normal retirement path removes
// the already-installed copies on the next reconcile. That is the intended
// behavior — ox owns those files and they describe a command that is gone.
func enabledBundleIDs() []string {
	ids := skills.DefaultBundleIDs()
	if auth.IsCartsEnabled() {
		ids = append(ids, "carts")
	}
	return ids
}

func reconcileSelectedSkills(repoRoot string, selected []adapterprotocol.SkillTarget) (*skillmanager.ReconcilePlan, error) {
	plan, err := skillmanager.ReconcileUpdate(repoRoot, version.Version, func(desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		targets = append(targets, selected...)
		var err error
		targets, err = skillmanager.CanonicalizeTargets(repoRoot, targets)
		if err != nil {
			return desired, nil, err
		}
		for _, id := range enabledBundleIDs() {
			desired = skillmanager.AddBundles(desired, id)
		}
		desired = skillmanager.AddTargets(desired, selected...)
		return desired, targets, nil
	})
	if err != nil {
		return nil, err
	}
	retireLegacyClaudeCommands(repoRoot, selected)
	return plan, nil
}

// retireLegacyClaudeCommands removes untracked ox-stamped files from
// .claude/commands that the CLI no longer ships.
//
// It DELETES, so it is deliberately wired only into `ox init`, which the user
// ran on purpose. Tracked commands are left for the guarded `Legacy ox files`
// Doctor migration: init's rollback tracker cannot restore or stage deletions
// performed inside this helper, and deleting the file would also remove the
// on-disk bytes Doctor needs to verify ownership.
//
// That separation is not theoretical. While this was wired into the shared
// reconcile path, running the test suite deleted sixteen tracked files from the
// developer's own checkout, because doctor checks resolve their repository from
// the process working directory and a test process sits inside a real repo.
// Concentrating every destructive step behind the guarded migration removes that
// whole class of accident.
//
// Only files carrying a VERIFYING ox stamp are removed. A user-authored command
// of the same name, or an ox-stamped one the user has since edited, is left alone.
func retireLegacyClaudeCommands(repoRoot string, targets []adapterprotocol.SkillTarget) {
	var hasClaude bool
	for _, target := range targets {
		if target.Root == ".claude/skills" {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return
	}
	// Retired names are what make this sweep work after the 0.15.0 fold: the files
	// on disk carry the OLD names (ox-prime.md) while the catalog has already moved
	// to the new ones (ox-cli-prime). Keyed on current names alone, this would match
	// nothing and every legacy command file would survive forever — the old slash
	// command still serving stale guidance beside its replacement. Current names are
	// included too, for the case where a skill superseded a same-named command.
	names := append([]string{}, skills.Retired...)
	if current, err := skills.BundleNames(enabledBundleIDs()); err == nil {
		names = append(names, current...)
	}
	for _, name := range names {
		commandPath := filepath.Join(repoRoot, ".claude", "commands", name+".md")
		data, readErr := os.ReadFile(commandPath)
		if readErr != nil {
			continue
		}
		// Verify the stamp against the body, not merely that a stamp exists. An
		// ox-stamped file the user has since EDITED no longer hashes to its stamp,
		// and deleting it would destroy their work — these are legacy files written
		// before the reserved-prefix contract existed, so their author never agreed
		// to "ox owns this path". Unverified files are left in place.
		hash, _, body := adapterstamp.ExtractStampAnywhere(data, "ox")
		if hash == "" || agentx.ContentHash(body) != hash {
			continue
		}
		tracked, trackErr := gitTracksPath(repoRoot,
			filepath.ToSlash(filepath.Join(".claude", "commands", name+".md")))
		if trackErr != nil || tracked {
			continue
		}
		if err := os.Remove(commandPath); err != nil {
			slog.Warn("skills: failed to remove retired Claude command", "path", commandPath, "error", err)
		}
	}
}

func planCommittedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	desired, targets, err := committedSkillState(repoRoot)
	if err != nil {
		return nil, err
	}
	return skillmanager.Plan(repoRoot, version.Version, desired, targets)
}

func reconcileCommittedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	plan, err := skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		current, _ = skillmanager.RemoveRetiredSelections(current)
		var err error
		current, currentTargets, err = bootstrapLegacySkillState(repoRoot, current, currentTargets)
		if err != nil {
			return current, currentTargets, err
		}
		// Re-assert the DEFAULT bundles. Without this a bundle introduced by a new
		// release never reaches an existing project: the lockfile records the
		// bundles chosen at `ox init` time, and reconcile would faithfully install
		// exactly those forever.
		//
		// That is not hypothetical — it is how the `sageox` on-ramp, the ONE file
		// this whole design keeps in git, failed to install into a real repository
		// during the first end-to-end run. A default bundle means "ox ships this to
		// everyone"; a project that never opted out must get it.
		//
		// Deliberately doctor-only: this can change the committed lockfile, and
		// `ox doctor` is the human-initiated path that already owns the index. Prime
		// and the daemon stay on the recorded state so neither writes a tracked file.
		for _, id := range enabledBundleIDs() {
			current = skillmanager.AddBundles(current, id)
		}
		return current, currentTargets, nil
	})
	return plan, err
}

// reconcileSelectedSkills applies exactly the selection already committed in
// skills.lock.json. Approval is an authority change, not a selection change: it
// must never add newly-default bundles, bootstrap targets, or rewrite project
// intent as a side effect of materializing newly approved bytes.
func reconcileExactSelectedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return nil, err
	}
	if len(desired.Targets) == 0 {
		return nil, fmt.Errorf("no selected skill targets; run `ox init`")
	}
	return skillmanager.Reconcile(repoRoot, version.Version, desired, targets)
}

// reconcileCommittedSkillsNonBlocking is reconcileCommittedSkills for the session
// hot path: it never waits on the reconcile lock, returning
// skillmanager.ErrApplyInProgress instead of stalling a session start behind a
// daemon tick or a concurrent `ox init`.
func reconcileCommittedSkillsNonBlocking(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	return skillmanager.ReconcileUpdateNonBlocking(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		if len(current.Targets) > 0 {
			return current, currentTargets, nil
		}
		return bootstrapLegacySkillState(repoRoot, current, currentTargets)
	})
}

func committedSkillState(repoRoot string) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return skillmanager.DesiredSkills{}, nil, err
	}
	return bootstrapLegacySkillState(repoRoot, desired, targets)
}

func bootstrapLegacySkillState(repoRoot string, desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
	if len(desired.Targets) == 0 {
		candidates, detectErr := detectedSkillTargets(repoRoot)
		if detectErr != nil {
			return skillmanager.DesiredSkills{}, nil, detectErr
		}
		for _, target := range candidates {
			if target.Format != adapterprotocol.SkillFormatAgentSkillsV1 {
				continue
			}
			legacyBundles, legacyErr := skillmanager.LegacyBundles(repoRoot, target)
			if legacyErr != nil {
				return skillmanager.DesiredSkills{}, nil, legacyErr
			}
			if len(legacyBundles) > 0 {
				targets = append(targets, target)
				desired = skillmanager.AddTargets(desired, target)
				desired = skillmanager.AddBundles(desired, legacyBundles...)
			}
		}
		if len(desired.Targets) > 0 {
			for _, id := range enabledBundleIDs() {
				desired = skillmanager.AddBundles(desired, id)
			}
		}
	}
	// A project that has ox-stamped COMMANDS but no ox skills predates the skills
	// installer entirely. Its command surface is nonetheless proof that it selected
	// Claude Code, so treat that as the selection signal.
	//
	// Without this, the 0.15.0 fold is a pure regression for such a project: the
	// migration removes the eight legacy command files and, finding no recorded
	// skill target, installs no replacement — so the user loses /ox-prime and every
	// other lifecycle surface and gains nothing. Observed on a real repository.
	if len(desired.Targets) == 0 && hasLegacyOxCommands(repoRoot) {
		for _, target := range detectedOrClaudeTargets(repoRoot) {
			targets = append(targets, target)
			desired = skillmanager.AddTargets(desired, target)
		}
		if len(desired.Targets) > 0 {
			for _, id := range enabledBundleIDs() {
				desired = skillmanager.AddBundles(desired, id)
			}
		}
	}
	// Adapter-installed rules predate the shared inventory. Their verified stamp
	// is the authorization signal: adopt that native target, then let the normal
	// catalog diff replace current files and retire removed ones. No filename
	// sweep is needed, and repositories that never selected the adapter remain
	// untouched.
	ruleTargets, ruleTargetsErr := declaredRuleTargets(repoRoot)
	if ruleTargetsErr != nil {
		return skillmanager.DesiredSkills{}, nil, ruleTargetsErr
	}
	for _, target := range ruleTargets {
		selected, selectErr := skillmanager.HasLegacyRules(repoRoot, target)
		if selectErr != nil {
			return skillmanager.DesiredSkills{}, nil, selectErr
		}
		if selected {
			targets = append(targets, target)
			desired = skillmanager.AddTargets(desired, target)
		}
	}
	var canonicalErr error
	targets, canonicalErr = skillmanager.CanonicalizeTargets(repoRoot, targets)
	if canonicalErr != nil {
		return skillmanager.DesiredSkills{}, nil, canonicalErr
	}
	return desired, targets, nil
}

func declaredRuleTargets(repoRoot string) ([]adapterprotocol.SkillTarget, error) {
	return declaredRuleTargetsFromAdapters(repoRoot, adapters.DiscoverExternalAdapters())
}

func declaredRuleTargetsFromAdapters(repoRoot string, external []*adapters.ExternalAdapter) ([]adapterprotocol.SkillTarget, error) {
	var targets []adapterprotocol.SkillTarget
	for _, adapter := range external {
		if info := adapter.Info(); info != nil {
			targets = append(targets, info.RuleTargets...)
		}
	}
	return skillmanager.CanonicalizeTargets(repoRoot, targets)
}

// hasLegacyOxCommands reports whether .claude/commands holds a file ox installed.
// Only an ox-stamped file counts; a command the user wrote themselves says nothing
// about whether they ever ran `ox init`.
func hasLegacyOxCommands(repoRoot string) bool {
	dir := filepath.Join(repoRoot, ".claude", "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			continue
		}
		// Verify the stamp against the body, matching validLegacyStamp. A stamped
		// file the user has since EDITED is not ox state, and accepting it here
		// would add Claude targets and default bundles off the back of a file the
		// reconciler will not classify as ox-owned.
		if hash, _, body := adapterstamp.ExtractStampAnywhere(data, "ox"); hash != "" && agentx.ContentHash(body) == hash {
			return true
		}
	}
	return false
}

// detectedOrClaudeTargets prefers live adapter detection and falls back to the
// canonical Claude Code projection, because the evidence that got us here — an
// ox-stamped .claude/commands file — is Claude-specific.
func detectedOrClaudeTargets(repoRoot string) []adapterprotocol.SkillTarget {
	if detected, err := detectedSkillTargets(repoRoot); err == nil && len(detected) > 0 {
		return detected
	}
	canonical, err := skillmanager.CanonicalizeTargets(repoRoot, []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})
	if err != nil {
		return nil
	}
	return canonical
}

func uninstallManagedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	// Include detected descriptors only to migrate and remove old stamped
	// projections from projects that predate the manifest.
	detected, err := detectedSkillTargets(repoRoot)
	if err != nil {
		return nil, err
	}
	ruleTargets, err := declaredRuleTargets(repoRoot)
	if err != nil {
		return nil, err
	}
	detected = append(detected, ruleTargets...)
	return skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		currentTargets = append(currentTargets, detected...)
		var err error
		currentTargets, err = skillmanager.CanonicalizeTargets(repoRoot, currentTargets)
		if err != nil {
			return current, nil, err
		}
		current.Targets = nil
		return current, currentTargets, nil
	})
}
