package teamrules

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/stretchr/testify/require"
)

func TestDeliveryMode_PreservesScopeAndVisibility(t *testing.T) {
	t.Parallel()

	scopedAlways := teamdocs.TeamRule{Visibility: teamdocs.VisibilityAlways, Globs: []string{"**/*.go"}}
	unscopedAlways := teamdocs.TeamRule{Visibility: teamdocs.VisibilityAlways}
	unscopedIndexed := teamdocs.TeamRule{Visibility: teamdocs.VisibilityIndexed}

	tests := []struct {
		name, agent string
		rule        teamdocs.TeamRule
		want        DeliveryMode
	}{
		{"Claude scopes natively", "claude", scopedAlways, DeliveryNative},
		{"Cursor scopes natively", "cursor", scopedAlways, DeliveryNative},
		{"Copilot scopes natively", "copilot", scopedAlways, DeliveryNative},
		{"Cline scopes natively", "cline", scopedAlways, DeliveryNative},
		{"Kiro scopes natively", "kiro", scopedAlways, DeliveryNative},
		{"Droid cannot erase scope", "droid", scopedAlways, DeliveryPrimeIndexed},
		{"Windsurf cannot erase scope", "windsurf", scopedAlways, DeliveryPrimeIndexed},
		{"Droid can preserve unscoped always", "droid", unscopedAlways, DeliveryNative},
		{"indexed stays lazy", "claude", unscopedIndexed, DeliveryPrimeIndexed},
		{"non-native agent gets inline always", "codex", unscopedAlways, DeliveryPrimeInline},
		{"non-native scoped rule stays lazy", "codex", scopedAlways, DeliveryPrimeIndexed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, ModeForAgent(tt.agent, tt.rule))
		})
	}
}

func TestReconcile_ProjectsOnceAndRemovesRetiredRules(t *testing.T) {
	project := t.TempDir()
	rulesRoot := filepath.Join(project, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", ".gitignore"),
		[]byte("rules/sageox-team-*\n"), 0o644))
	git := exec.Command("git", "init", "-q")
	git.Dir = project
	require.NoError(t, git.Run())

	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "go.md")
	require.NoError(t, os.WriteFile(source, []byte("---\nname: go-style\n---\n\nUse gofmt.\n"), 0o644))
	rule := teamdocs.TeamRule{
		Name: "go-style", Description: "Go conventions", RelPath: "go.md", AbsPath: source,
		Globs: []string{"**/*.go"}, Visibility: teamdocs.VisibilityAlways,
	}

	result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
	require.NoError(t, err)
	require.Equal(t, []string{"claude"}, result.NativeAgents[rule.Name])
	require.Len(t, result.Written, 1)
	native, ok := NativePath(project, "claude", rule)
	require.True(t, ok)
	content, err := os.ReadFile(native)
	require.NoError(t, err)
	require.Contains(t, string(content), `globs: "**/*.go"`)
	require.Contains(t, string(content), "Use gofmt.")

	// A current native copy suppresses prime for Claude, while Codex receives a
	// lazy index entry. The same rule therefore reaches either agent exactly once.
	require.Empty(t, ForPrime(project, "claude", []teamdocs.TeamRule{rule}))
	require.NoError(t, os.WriteFile(native, []byte("locally modified\n"), 0o644))
	require.Empty(t, ForPrime(project, "claude", []teamdocs.TeamRule{rule}),
		"an existing native projection must not be duplicated through prime while repair is pending")
	codex := ForPrime(project, "codex", []teamdocs.TeamRule{rule})
	require.Len(t, codex, 1)
	require.Equal(t, teamdocs.VisibilityIndexed, codex[0].Visibility)
	require.Empty(t, codex[0].Body)

	result, err = Reconcile(context.Background(), project, nil)
	require.NoError(t, err)
	require.Contains(t, result.Removed, filepath.ToSlash(filepath.Join(".claude", "rules", filepath.Base(native))))
	require.NoFileExists(t, native, "a retired Team Rule survived native reconciliation")
}

func TestReconcile_EachSupportedAgentGetsExactlyOneDelivery(t *testing.T) {
	project := t.TempDir()
	nativeAgents := []string{"claude", "cursor", "copilot", "cline", "kiro", "droid", "windsurf"}
	for _, agent := range nativeAgents {
		p, ok := policyFor(agent)
		require.True(t, ok)
		require.NoError(t, os.MkdirAll(filepath.Join(project, filepath.FromSlash(p.Root)), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte(strings.Join([]string{
		".claude/rules/sageox-team-*",
		".cursor/rules/sageox-team-*",
		".github/instructions/sageox-team-*",
		".clinerules/sageox-team-*",
		".kiro/steering/sageox-team-*",
		".factory/rules/sageox-team-*",
		".windsurf/rules/sageox-team-*",
	}, "\n")+"\n"), 0o644))
	git := exec.Command("git", "init", "-q")
	git.Dir = project
	require.NoError(t, git.Run())

	sourceRoot := t.TempDir()
	scopedPath := filepath.Join(sourceRoot, "scoped.md")
	alwaysPath := filepath.Join(sourceRoot, "always.md")
	require.NoError(t, os.WriteFile(scopedPath, []byte("Scoped body.\n"), 0o644))
	require.NoError(t, os.WriteFile(alwaysPath, []byte("Always body.\n"), 0o644))
	scoped := teamdocs.TeamRule{
		Name: "scoped", RelPath: "scoped.md", AbsPath: scopedPath,
		Globs: []string{"**/*.go"}, Visibility: teamdocs.VisibilityAlways,
	}
	always := teamdocs.TeamRule{
		Name: "always", RelPath: "always.md", AbsPath: alwaysPath,
		Visibility: teamdocs.VisibilityAlways,
	}

	result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{scoped, always})
	require.NoError(t, err)
	for _, agent := range []string{"claude", "cursor", "copilot", "cline", "kiro"} {
		require.Contains(t, result.NativeAgents[scoped.Name], agent)
		require.Empty(t, ForPrime(project, agent, []teamdocs.TeamRule{scoped}), agent)
	}
	for _, agent := range []string{"droid", "windsurf", "codex", "gemini", "amp", "opencode", "pi", "omp", "goose"} {
		require.Len(t, ForPrime(project, agent, []teamdocs.TeamRule{scoped}), 1, agent)
	}
	for _, agent := range nativeAgents {
		require.Contains(t, result.NativeAgents[always.Name], agent)
		require.Empty(t, ForPrime(project, agent, []teamdocs.TeamRule{always}), agent)
	}
	for _, agent := range []string{"codex", "gemini", "amp", "opencode", "pi", "omp", "goose"} {
		require.Len(t, ForPrime(project, agent, []teamdocs.TeamRule{always}), 1, agent)
	}
}

func TestReconcile_RefusesToChangeTrackedProjection(t *testing.T) {
	project := t.TempDir()
	rulesRoot := filepath.Join(project, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
		[]byte(".claude/rules/sageox-team-*\n"), 0o644))
	git := exec.Command("git", "init", "-q")
	git.Dir = project
	require.NoError(t, git.Run())

	source := filepath.Join(t.TempDir(), "security.md")
	require.NoError(t, os.WriteFile(source, []byte("New body.\n"), 0o644))
	rule := teamdocs.TeamRule{
		Name: "security", RelPath: "security.md", AbsPath: source,
		Visibility: teamdocs.VisibilityAlways,
	}
	native, ok := NativePath(project, "claude", rule)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(native, []byte("tracked body\n"), 0o644))
	git = exec.Command("git", "add", "-f", "--", filepath.ToSlash(strings.TrimPrefix(native, project+string(filepath.Separator))))
	git.Dir = project
	require.NoError(t, git.Run())

	_, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
	require.ErrorContains(t, err, "refusing to update tracked Team Rule projection")
	content, readErr := os.ReadFile(native)
	require.NoError(t, readErr)
	require.Equal(t, "tracked body\n", string(content))

	_, err = Reconcile(context.Background(), project, nil)
	require.ErrorContains(t, err, "refusing to remove tracked Team Rule projection")
	require.FileExists(t, native)
}

func TestReconcile_DroidScopedRuleFallsBackWithoutWriting(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".factory", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".factory", ".gitignore"),
		[]byte("rules/sageox-team-*\n"), 0o644))
	git := exec.Command("git", "init", "-q")
	git.Dir = project
	require.NoError(t, git.Run())

	source := filepath.Join(t.TempDir(), "terraform.md")
	require.NoError(t, os.WriteFile(source, []byte("Use tofu.\n"), 0o644))
	rule := teamdocs.TeamRule{
		Name: "terraform", RelPath: "terraform.md", AbsPath: source,
		Globs: []string{"**/*.tf"}, Visibility: teamdocs.VisibilityAlways,
	}

	result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
	require.NoError(t, err)
	require.Empty(t, result.NativeAgents[rule.Name])
	require.Contains(t, result.Fallbacks[rule.Name][0].Reason, "globs")
	entries, err := os.ReadDir(filepath.Join(project, ".factory", "rules"))
	require.NoError(t, err)
	require.Empty(t, entries)

	prime := ForPrime(project, "droid", []teamdocs.TeamRule{rule})
	require.Len(t, prime, 1)
	require.Equal(t, teamdocs.VisibilityIndexed, prime[0].Visibility)
	require.Empty(t, prime[0].Body)
}
