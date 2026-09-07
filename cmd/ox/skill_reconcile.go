package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// skillTargetsForAdapters resolves native target descriptors and deduplicates
// shared projections such as Codex and Gemini's .agents/skills root.
func skillTargetsForAdapters(repoRoot string, candidates []*adapters.ExternalAdapter) ([]adapterprotocol.SkillTarget, error) {
	var targets []adapterprotocol.SkillTarget
	for _, adapter := range candidates {
		if !adapter.HasCapability(adapterprotocol.CapSkillsInstaller) || adapter.Info() == nil {
			continue
		}
		targets = append(targets, adapter.Info().SkillTargets...)
	}
	return skillmanager.CanonicalizeTargets(repoRoot, targets)
}

func detectedSkillTargets(repoRoot string) ([]adapterprotocol.SkillTarget, error) {
	var candidates []*adapters.ExternalAdapter
	for _, adapter := range adapters.DiscoverExternalAdapters() {
		if adapter.HasCapability(adapterprotocol.CapSkillsInstaller) && adapter.Detect() {
			candidates = append(candidates, adapter)
		}
	}
	return skillTargetsForAdapters(repoRoot, candidates)
}

func reconcileSelectedSkills(repoRoot string, selected []adapterprotocol.SkillTarget) (*skillmanager.ReconcilePlan, error) {
	plan, err := skillmanager.ReconcileUpdate(repoRoot, version.Version, func(desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		targets = append(targets, selected...)
		var err error
		targets, err = skillmanager.CanonicalizeTargets(repoRoot, targets)
		if err != nil {
			return desired, nil, err
		}
		for _, id := range skills.DefaultBundleIDs() {
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

// retireLegacyClaudeCommands removes ox-stamped files from .claude/commands that
// the CLI no longer ships.
//
// It DELETES, so it is deliberately NOT wired into the generic reconcile path.
// Only two callers may invoke it: `ox init`, which the user ran on purpose, and
// the `Legacy ox files` doctor check, which carries the full set of migration
// guards. Everything else that reconciles — `ox agent prime` on the session hot
// path, the daemon's background tick, an adapter RPC — must be able to run
// against any directory without removing files from it.
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
	if current, err := skills.BundleNames(skills.DefaultBundleIDs()); err == nil {
		names = append(names, current...)
	}
	for _, name := range names {
		path := filepath.Join(repoRoot, ".claude", "commands", name+".md")
		data, readErr := os.ReadFile(path)
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
		if err := os.Remove(path); err != nil {
			slog.Warn("skills: failed to remove retired Claude command", "path", path, "error", err)
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
		if len(current.Targets) > 0 {
			return current, currentTargets, nil
		}
		return bootstrapLegacySkillState(repoRoot, current, currentTargets)
	})
	return plan, err
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
			for _, id := range skills.DefaultBundleIDs() {
				desired = skillmanager.AddBundles(desired, id)
			}
		}
	}
	return desired, targets, nil
}

func addSkillBundle(repoRoot, bundle string) (*skillmanager.ReconcilePlan, error) {
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return nil, err
	}
	if len(desired.Targets) == 0 {
		targets, err = detectedSkillTargets(repoRoot)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no detected AI coworker supports native Agent Skills; Attest remains available through the ox CLI")
		}
		desired = skillmanager.DefaultDesired(targets)
	}
	return skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		if len(current.Targets) == 0 {
			current = desired
			currentTargets = targets
		}
		current = skillmanager.AddBundles(current, bundle)
		return current, currentTargets, nil
	})
}

func uninstallManagedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	// Include detected descriptors only to migrate and remove old stamped
	// projections from projects that predate the manifest.
	detected, err := detectedSkillTargets(repoRoot)
	if err != nil {
		return nil, err
	}
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
