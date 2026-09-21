package skillmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestBuiltInRuleCatalogAndLegacyRuleBoundaries(t *testing.T) {
	target := adapterprotocol.SkillTarget{
		Key: "claude-rules", Root: ".claude/rules", Format: adapterprotocol.RuleFormatMarkdownV1,
		Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	files, err := (builtInRuleCatalog{}).Select(target)
	require.NoError(t, err)
	require.Len(t, files, 2)

	bad := target
	bad.Root = "../outside"
	_, err = HasLegacyRules(t.TempDir(), bad)
	require.Error(t, err)

	skillTarget := target
	skillTarget.Root = ".agents/skills"
	skillTarget.Format = adapterprotocol.SkillFormatAgentSkillsV1
	found, err := HasLegacyRules(t.TempDir(), skillTarget)
	require.NoError(t, err)
	require.False(t, found)
}

func TestInventoryDiscovery_ReportsInvalidRootsAndMissingTargets(t *testing.T) {
	target := adapterprotocol.SkillTarget{
		Key: "claude-rules", Root: ".claude/rules", Format: adapterprotocol.RuleFormatMarkdownV1,
		Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	missingRepo := filepath.Join(t.TempDir(), "missing")
	_, err := discoverLegacyRules(missingRepo, map[string]adapterprotocol.SkillTarget{target.Key: target}, nil)
	require.Error(t, err)
	_, err = orphanedTeamFiles(missingRepo, target, nil, nil, nil)
	require.Error(t, err)

	repo := t.TempDir()
	legacy, err := discoverLegacyRules(repo, map[string]adapterprotocol.SkillTarget{target.Key: target}, nil)
	require.NoError(t, err)
	require.Empty(t, legacy)
	orphans, err := orphanedTeamFiles(repo, target, nil, nil, nil)
	require.NoError(t, err)
	require.Empty(t, orphans)

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "rules"), []byte("not a directory"), 0o644))
	_, err = discoverLegacyRules(repo, map[string]adapterprotocol.SkillTarget{target.Key: target}, nil)
	require.ErrorContains(t, err, "discover legacy rules")
	_, err = orphanedTeamFiles(repo, target, nil, nil, nil)
	require.Error(t, err)
}
