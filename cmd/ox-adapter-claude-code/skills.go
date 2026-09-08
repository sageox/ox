package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const (
	skillFileName      = skills.SkillFileName
	oxSkillStampPrefix = "ox"
)

// Claude Code does not use .agents/skills as a supported discovery location.
// The source remains portable Agent Skills; this is only its managed target.
func skillsDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "skills")
}

func handleInstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.InstallSkillsResponse, error) {
	result, err := skillmanager.Install(p, skillsDir(p.RepoRoot))
	if err != nil {
		return nil, err
	}
	// This is a Claude-only migration from old slash-command surfaces. The
	// shared installer owns all Agent Skills lifecycle mechanics.
	desired := skillmanager.AddBundles(skillmanager.DesiredSkills{Names: p.Names}, p.Bundles...)
	desired, retired := skillmanager.RemoveRetiredSelections(desired)
	if retired && len(desired.Names) == 0 && len(desired.Bundles) == 0 {
		// An explicit retired-only request must not fall back to cleaning up
		// commands for every default skill.
		return result, nil
	}
	bundles := make([]string, 0, len(desired.Bundles))
	for _, bundle := range desired.Bundles {
		bundles = append(bundles, bundle.ID)
	}
	selected, err := skills.SelectedBundles(p.Version, desired.Names, bundles)
	if err != nil {
		return nil, err
	}
	cleanupLegacyCommandFilesForSkills(p.RepoRoot, selected)
	return result, nil
}

func handleCheckSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.CheckSkillsResponse, error) {
	return skillmanager.Check(p, skillsDir(p.RepoRoot))
}

func handleUninstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.UninstallSkillsResponse, error) {
	return skillmanager.Uninstall(p, skillsDir(p.RepoRoot))
}

func cleanupLegacyCommandFilesForSkills(repoRoot string, selected []skills.Skill) {
	commandsDir := filepath.Join(repoRoot, ".claude", "commands")
	for _, skill := range selected {
		legacyPath := filepath.Join(commandsDir, skill.Name+".md")
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			continue
		}
		if hash, _, _ := adapterstamp.ExtractStampAnywhere(data, "ox"); hash == "" {
			continue
		}
		if err := os.Remove(legacyPath); err != nil {
			slog.Warn("skills: failed to remove legacy command file superseded by skill", "id", skill.Name, "path", legacyPath, "error", err)
		}
	}
}
