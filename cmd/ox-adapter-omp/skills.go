package main

import (
	"path/filepath"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// OMP discovers the portable Agent Skills convention at .agents/skills.
// Sharing this target with Codex and Gemini lets reconciliation write one
// projection when a project enables several AI teammates.
func skillsDir(root string) string { return filepath.Join(root, ".agents", "skills") }

func handleInstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.InstallSkillsResponse, error) {
	return skillmanager.Install(p, skillsDir(p.RepoRoot))
}

func handleCheckSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.CheckSkillsResponse, error) {
	return skillmanager.Check(p, skillsDir(p.RepoRoot))
}

func handleUninstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.UninstallSkillsResponse, error) {
	return skillmanager.Uninstall(p, skillsDir(p.RepoRoot))
}
