package skillmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/rulecatalog"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

type testRuleCatalog struct {
	files []rulecatalog.File
}

func (c testRuleCatalog) Digest() (string, error) { return "test-rules", nil }

func (c testRuleCatalog) Select(adapterprotocol.SkillTarget) ([]rulecatalog.File, error) {
	return append([]rulecatalog.File(nil), c.files...), nil
}

func TestRuleCatalogRetirementUsesInventoryDiff(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	target := adapterprotocol.SkillTarget{
		Key:        "claude-rules",
		Root:       ".claude/rules",
		Format:     adapterprotocol.RuleFormatMarkdownV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	desired := AddTargets(DesiredSkills{}, target)
	firstCatalog := testRuleCatalog{files: []rulecatalog.File{{
		Path:    "ox-cli-retired.md",
		Content: []byte("---\ndescription: retired\n---\nold rule\n"),
	}}}

	first, err := planWithCatalogs(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, firstCatalog)
	require.NoError(t, err)
	require.Len(t, first.Creates, 1)
	require.NoError(t, Apply(first))

	rulePath := filepath.Join(repo, ".claude", "rules", "ox-cli-retired.md")
	require.FileExists(t, rulePath)

	second, err := planWithCatalogs(repo, "1.0.1", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, testRuleCatalog{})
	require.NoError(t, err)
	require.Len(t, second.Removes, 1, "a catalog deletion must become an inventory removal")
	require.Equal(t, ".claude/rules/ox-cli-retired.md", second.Removes[0].Path)
	require.NoError(t, Apply(second))
	require.NoFileExists(t, rulePath)
}

func TestManagedRuleDriftIsRepaired(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	target := adapterprotocol.SkillTarget{
		Key:        "claude-rules",
		Root:       ".claude/rules",
		Format:     adapterprotocol.RuleFormatMarkdownV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	desired := AddTargets(DesiredSkills{}, target)
	catalog := testRuleCatalog{files: []rulecatalog.File{{
		Path:    "ox-cli.md",
		Content: []byte("canonical\n"),
	}}}

	first, err := planWithCatalogs(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, catalog)
	require.NoError(t, err)
	require.NoError(t, Apply(first))

	rulePath := filepath.Join(repo, ".claude", "rules", "ox-cli.md")
	require.NoError(t, os.WriteFile(rulePath, []byte("tampered\n"), 0o644))

	second, err := planWithCatalogs(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, catalog)
	require.NoError(t, err)
	require.Len(t, second.Updates, 1)
	require.NoError(t, Apply(second))
	restored, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.Equal(t, []byte("canonical\n"), restored)
}

func TestRuleCatalogRetirementPreservesLocalEditAndRelinquishesOwnership(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	target := adapterprotocol.SkillTarget{
		Key:        "claude-rules",
		Root:       ".claude/rules",
		Format:     adapterprotocol.RuleFormatMarkdownV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	desired := AddTargets(DesiredSkills{}, target)
	firstCatalog := testRuleCatalog{files: []rulecatalog.File{{
		Path:    "ox-cli-retired.md",
		Content: []byte("managed\n"),
	}}}

	first, err := planWithCatalogs(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, firstCatalog)
	require.NoError(t, err)
	require.NoError(t, Apply(first))

	rulePath := filepath.Join(repo, ".claude", "rules", "ox-cli-retired.md")
	require.NoError(t, os.WriteFile(rulePath, []byte("my local edit\n"), 0o644))

	second, err := planWithCatalogs(repo, "1.0.1", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, testRuleCatalog{})
	require.NoError(t, err)
	require.Empty(t, second.Removes)
	require.Len(t, second.Conflicts, 1)
	require.NoError(t, Apply(second))
	require.FileExists(t, rulePath)

	third, err := planWithCatalogs(repo, "1.0.1", desired, []adapterprotocol.SkillTarget{target}, builtInCatalog{}, testRuleCatalog{})
	require.NoError(t, err)
	require.Empty(t, third.Conflicts, "retired edited rules must not become permanent doctor conflicts")
}

func TestLegacyRuleMigrationDiscoversVerifiedContentNotFilenames(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	target := adapterprotocol.SkillTarget{
		Key:        "claude-rules",
		Root:       ".claude/rules",
		Format:     adapterprotocol.RuleFormatMarkdownV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	legacyRel := filepath.Join(".claude", "rules", "forgotten", "name-never-listed.md")
	legacyPath := filepath.Join(repo, legacyRel)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o755))
	body := []byte("# old generated rule\n")
	legacy := []byte("---\ndescription: " + rulecatalog.PrimaryDescription + "\n---\n")
	legacy = append(legacy, agentx.StampedContent(body, "0.14.0", agentx.DefaultStampPrefix)...)
	require.NoError(t, os.WriteFile(legacyPath, legacy, 0o644))

	userPath := filepath.Join(repo, ".claude", "rules", "forgotten", "mine.md")
	require.NoError(t, os.WriteFile(userPath, []byte("# mine\n"), 0o644))

	selected, err := HasLegacyRules(repo, target)
	require.NoError(t, err)
	require.True(t, selected)

	plan, err := planWithCatalogs(
		repo,
		"1.0.0",
		AddTargets(DesiredSkills{}, target),
		[]adapterprotocol.SkillTarget{target},
		builtInCatalog{},
		testRuleCatalog{},
	)
	require.NoError(t, err)
	require.Len(t, plan.Removes, 1)
	require.Equal(t, filepath.ToSlash(legacyRel), plan.Removes[0].Path)
	require.NoError(t, Apply(plan))
	require.NoFileExists(t, legacyPath)
	require.FileExists(t, userPath)
}

func TestLegacyRuleUninstallNeedsNoSelectedTarget(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	target := adapterprotocol.SkillTarget{
		Key:        "claude-rules",
		Root:       ".claude/rules",
		Format:     adapterprotocol.RuleFormatMarkdownV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	legacyPath := filepath.Join(repo, ".claude", "rules", "arbitrary-old-name.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o755))
	body := []byte("# old generated rule\n")
	legacy := []byte("---\ndescription: " + rulecatalog.PrimaryDescription + "\n---\n")
	legacy = append(legacy, agentx.StampedContent(body, "0.14.0", agentx.DefaultStampPrefix)...)
	require.NoError(t, os.WriteFile(legacyPath, legacy, 0o644))

	plan, err := planWithCatalogs(
		repo, "1.0.0", DesiredSkills{}, []adapterprotocol.SkillTarget{target},
		builtInCatalog{}, testRuleCatalog{},
	)
	require.NoError(t, err)
	require.Len(t, plan.Removes, 1,
		"explicit uninstall must adopt and remove verified legacy rules even before a target was selected")
	require.NoError(t, Apply(plan))
	require.NoFileExists(t, legacyPath)
}
