package skillmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// A removed catalog entry must not wedge upgrades for projects that selected it.
func TestRetiredAttestSelectionsConvergeWithoutChangingCommittedIntent(t *testing.T) {
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		for _, selection := range []string{"bundle", "names", "legacy names", "only retired"} {
			t.Run(root+"/"+selection, func(t *testing.T) {
				repo := t.TempDir()
				target := sharedTarget()
				target.Root = root
				desired := desiredFor(target)
				names := []string{"ox-cli-attest-goal", "ox-cli-attest-create"}
				switch selection {
				case "bundle":
					desired.Bundles = append(desired.Bundles, BundleRef{ID: "attest"})
				case "legacy names":
					names = []string{"ox-attest-goal", "ox-attest-create"}
					desired.Names = names
				case "names":
					desired.Names = names
				case "only retired":
					desired.Bundles = []BundleRef{{ID: "attest"}}
				}
				targets := []adapterprotocol.SkillTarget{target}
				old := multiCatalog{revision: "old", skills: []skills.Skill{namedSkill(names[0], "original"), namedSkill(names[1], "original")}}
				plan, err := planWithSource(repo, "1.0.0", desired, targets, old)
				require.NoError(t, err)
				require.NoError(t, Apply(plan))
				lockBefore, err := os.ReadFile(LockPath(repo))
				require.NoError(t, err)
				modified := filepath.Join(repo, root, names[1], "SKILL.md")
				mine := []byte("my customized playbook\n")
				require.NoError(t, os.WriteFile(modified, mine, 0o644))
				unowned := filepath.Join(repo, root, names[0], "notes.txt")
				require.NoError(t, os.WriteFile(unowned, mine, 0o644))
				plan, err = Plan(repo, "1.1.0", desired, targets)
				require.NoError(t, err)
				require.NotEmpty(t, plan.Removes)
				require.NotEmpty(t, plan.Conflicts)
				require.NoError(t, Apply(plan))
				require.NoFileExists(t, filepath.Join(repo, root, names[0], "SKILL.md"))
				for _, path := range []string{modified, unowned} {
					got, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, mine, got)
				}
				lockAfter, err := os.ReadFile(LockPath(repo))
				require.NoError(t, err)
				require.Equal(t, lockBefore, lockAfter, "automatic reconciliation must not rewrite team intent")
				if selection == "only retired" {
					require.Zero(t, plan.DesiredFileCount)
				} else {
					require.FileExists(t, filepath.Join(repo, root, "ox-cli-consult", "SKILL.md"))
				}
				plan, err = Plan(repo, "1.1.0", desired, targets)
				require.NoError(t, err)
				require.Empty(t, plan.Removes)
			})
		}
	}
}

// Retirement must not turn unrelated misspellings into successful empty installs.
func TestRetirementStillRejectsUnknownSelections(t *testing.T) {
	for _, desired := range []DesiredSkills{
		{Bundles: []BundleRef{{ID: "attest"}, {ID: "unknown-bundle"}}},
		{Names: []string{"ox-cli-attest-goal", "unknown-skill"}},
	} {
		_, err := Plan(t.TempDir(), "1.0.0", desired, nil)
		require.ErrorContains(t, err, "unknown")
	}
}

// Pre-lock projects must retain target discovery when their only bundle was retired.
func TestLegacyRetiredSkillsStillSelectTheirTarget(t *testing.T) {
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		for _, modified := range []bool{false, true} {
			t.Run(root+map[bool]string{false: "/unchanged", true: "/modified"}[modified], func(t *testing.T) {
				repo := t.TempDir()
				target := sharedTarget()
				target.Root = root
				path := filepath.Join(repo, root, "ox-cli-attest-goal", "SKILL.md")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				content := agentx.StampedContent([]byte("old playbook\n"), "0.14.0", "ox")
				if modified {
					content = append(content, []byte("my edit\n")...)
				}
				require.NoError(t, os.WriteFile(path, content, 0o644))
				bundles, err := LegacyBundles(repo, target)
				require.NoError(t, err)
				if modified {
					require.Empty(t, bundles)
					return
				}
				require.NotEmpty(t, bundles)
				desired := AddTargets(AddBundles(DesiredSkills{}, bundles...), target)
				plan, err := Reconcile(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target})
				require.NoError(t, err)
				require.NotEmpty(t, plan.Removes)
				require.NoFileExists(t, path)
				require.FileExists(t, filepath.Join(repo, root, "ox-cli-consult", "SKILL.md"))
			})
		}
	}
}

// Saved names from any retired catalog generation must not block surviving skills.
func TestAllRetiredSelectionsAllowReconciliationAndRepair(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}
	desired := DesiredSkills{
		Names:   append(append([]string(nil), skills.Retired...), "ox-cli-consult"),
		Targets: []string{target.Key},
	}
	plan, err := Reconcile(repo, "1.0.0", desired, targets)
	require.NoError(t, err)
	require.True(t, plan.RetiredSelections)
	require.FileExists(t, filepath.Join(repo, target.Root, "ox-cli-consult", "SKILL.md"))
	saved, _, err := LoadDesired(repo)
	require.NoError(t, err)
	require.ElementsMatch(t, desired.Names, saved.Names, "automatic reconciliation preserves committed intent")
	repaired, removed := RemoveRetiredSelections(saved)
	require.True(t, removed)
	require.Equal(t, []string{"ox-cli-consult"}, repaired.Names)
	require.ElementsMatch(t, desired.Names, saved.Names, "migration must not mutate its input")
	plan, err = Reconcile(repo, "1.0.0", repaired, targets)
	require.NoError(t, err)
	require.False(t, plan.RetiredSelections)
	saved, _, err = LoadDesired(repo)
	require.NoError(t, err)
	require.Equal(t, []string{"ox-cli-consult"}, saved.Names)
}
