package teamrules

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	require.True(t, verifiedProjection(content))

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
	require.ErrorContains(t, err, "not a verified ox projection")
	require.FileExists(t, native, "a locally edited projection must be preserved")

	// Retirement is safe once the exact managed projection is restored.
	require.NoError(t, os.WriteFile(native, content, 0o644))
	result, err = Reconcile(context.Background(), project, nil)
	require.NoError(t, err)
	require.Contains(t, result.Removed, filepath.ToSlash(filepath.Join(".claude", "rules", filepath.Base(native))))
	require.NoFileExists(t, native, "a retired Team Rule survived native reconciliation")
}

func TestReconcile_PreservesUntrackedReservedFilesWithoutOwnershipProof(t *testing.T) {
	setup := func(t *testing.T) (string, string, teamdocs.TeamRule) {
		t.Helper()
		project := t.TempDir()
		rulesRoot := filepath.Join(project, ".claude", "rules")
		require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
			[]byte(".claude/rules/sageox-team-*\n"), 0o644))
		git := exec.Command("git", "init", "-q")
		git.Dir = project
		require.NoError(t, git.Run())

		source := filepath.Join(t.TempDir(), "security.md")
		require.NoError(t, os.WriteFile(source, []byte("Canonical body.\n"), 0o644))
		rule := teamdocs.TeamRule{
			Name: "security", RelPath: "security.md", AbsPath: source,
			Visibility: teamdocs.VisibilityAlways,
		}
		return project, rulesRoot, rule
	}

	t.Run("desired filename collision", func(t *testing.T) {
		project, _, rule := setup(t)
		native, ok := NativePath(project, "claude", rule)
		require.True(t, ok)
		const local = "hand-authored local rule\n"
		require.NoError(t, os.WriteFile(native, []byte(local), 0o644))

		_, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
		require.ErrorContains(t, err, "not a verified ox projection")
		require.ErrorIs(t, err, ErrProjectionConflict)
		got, readErr := os.ReadFile(native)
		require.NoError(t, readErr)
		require.Equal(t, local, string(got))
	})

	t.Run("retired reserved filename", func(t *testing.T) {
		project, rulesRoot, _ := setup(t)
		localPath := filepath.Join(rulesRoot, "sageox-team-hand-authored.md")
		const local = "hand-authored retired rule\n"
		require.NoError(t, os.WriteFile(localPath, []byte(local), 0o644))

		_, err := Reconcile(context.Background(), project, nil)
		require.ErrorContains(t, err, "not a verified ox projection")
		require.ErrorIs(t, err, ErrProjectionConflict)
		got, readErr := os.ReadFile(localPath)
		require.NoError(t, readErr)
		require.Equal(t, local, string(got))
	})
}

func TestReconcile_MigratesLegacyCommentOnlyProjections(t *testing.T) {
	setup := func(t *testing.T) (string, string, teamdocs.TeamRule) {
		t.Helper()
		project := t.TempDir()
		rulesRoot := filepath.Join(project, ".claude", "rules")
		require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
			[]byte(".claude/rules/sageox-team-*\n"), 0o644))
		git := exec.Command("git", "init", "-q")
		git.Dir = project
		require.NoError(t, git.Run())

		source := filepath.Join(t.TempDir(), "security.md")
		require.NoError(t, os.WriteFile(source, []byte("Current body.\n"), 0o644))
		rule := teamdocs.TeamRule{
			Name: "security", Description: "Current description", RelPath: "security.md", AbsPath: source,
			Visibility: teamdocs.VisibilityAlways,
		}
		return project, rulesRoot, rule
	}

	t.Run("update replaces exact legacy format with verified projection", func(t *testing.T) {
		project, _, rule := setup(t)
		native, ok := NativePath(project, "claude", rule)
		require.True(t, ok)
		legacy := "---\ndescription: \"Old description\"\n---\n\n" + legacyProjectionMarker + "\nOld body.\n"
		require.True(t, legacyProjectionOwned([]byte(legacy), policies[0]), "test fixture must match the exact legacy format")
		require.NoError(t, os.WriteFile(native, []byte(legacy), 0o644))

		_, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
		require.NoError(t, err)
		got, readErr := os.ReadFile(native)
		require.NoError(t, readErr)
		require.True(t, verifiedProjection(got))
		require.Contains(t, string(got), "Current body.")
		require.NotContains(t, string(got), "Old body.")
	})

	t.Run("retirement removes exact legacy format", func(t *testing.T) {
		project, rulesRoot, _ := setup(t)
		legacyPath := filepath.Join(rulesRoot, "sageox-team-retired.md")
		require.NoError(t, os.WriteFile(legacyPath,
			[]byte(legacyProjectionMarker+"\nOld body.\n"), 0o644))

		result, err := Reconcile(context.Background(), project, nil)
		require.NoError(t, err)
		require.Contains(t, result.Removed, ".claude/rules/sageox-team-retired.md")
		require.NoFileExists(t, legacyPath)
	})
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

func TestProjectionHelpers_DefensiveAndFallbackBranches(t *testing.T) {
	t.Run("non-directory and unprotected roots do not receive writes", func(t *testing.T) {
		project := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", "rules"), []byte("file"), 0o644))
		result, err := Reconcile(context.Background(), project, nil)
		require.NoError(t, err)
		require.Empty(t, result.Written)

		project = t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "rules"), 0o755))
		git := exec.Command("git", "init", "-q")
		git.Dir = project
		require.NoError(t, git.Run())
		source := filepath.Join(t.TempDir(), "rule.md")
		require.NoError(t, os.WriteFile(source, []byte("Body.\n"), 0o644))
		rule := teamdocs.TeamRule{Name: "security", AbsPath: source, Visibility: teamdocs.VisibilityAlways}
		result, err = Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
		require.NoError(t, err)
		require.Empty(t, result.NativeAgents[rule.Name])
		require.Contains(t, result.Fallbacks[rule.Name][0].Reason, "not ignored")
	})

	t.Run("native projection detection and aliases", func(t *testing.T) {
		project := t.TempDir()
		require.False(t, HasNativeProjections(project))
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "rules"), 0o755))
		projection := filepath.Join(project, ".claude", "rules", "sageox-team-a.md")
		require.NoError(t, os.WriteFile(projection, []byte("x"), 0o644))
		require.False(t, HasNativeProjections(project), "a reserved name is not ownership proof")
		require.NoError(t, os.WriteFile(projection, []byte(legacyProjectionMarker+"\nlegacy\n"), 0o644))
		require.True(t, HasNativeProjections(project), "the exact preceding ox format must remain migratable")
		require.NoError(t, os.WriteFile(projection, stampProjection([]byte("x")), 0o644))
		require.True(t, HasNativeProjections(project))

		// A reserved-looking DIRECTORY, and a reserved-prefixed file with the
		// wrong extension, are both somebody else's: ox must walk past them
		// rather than read them as its own projection.
		bare := t.TempDir()
		rulesDir := filepath.Join(bare, ".claude", "rules")
		require.NoError(t, os.MkdirAll(filepath.Join(rulesDir, managedPrefix+"adir.md"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(rulesDir, managedPrefix+"a.txt"),
			[]byte(legacyProjectionMarker+"\nbody\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "mine.md"),
			[]byte(legacyProjectionMarker+"\nbody\n"), 0o644))
		require.False(t, HasNativeProjections(bare),
			"a directory, a foreign extension, and an unreserved name are none of them an ox projection")

		rule := teamdocs.TeamRule{Name: "A Rule", RelPath: "a.md"}
		_, ok := NativePath(project, "unknown", rule)
		require.False(t, ok)
		claude, ok := NativePath(project, "claude-code", rule)
		require.True(t, ok)
		require.Contains(t, claude, ".claude")
	})

	t.Run("render and filename boundaries", func(t *testing.T) {
		_, err := render(teamdocs.TeamRule{AbsPath: filepath.Join(t.TempDir(), "missing")}, policies[0])
		require.Error(t, err)

		source := filepath.Join(t.TempDir(), "rule.md")
		require.NoError(t, os.WriteFile(source, []byte("Body without newline"), 0o644))
		content, err := render(teamdocs.TeamRule{
			Name: "Rule", Description: "description", AbsPath: source,
		}, policy{GlobField: "globs", AlwaysApplyField: "alwaysApply"})
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(string(content), "\n"))
		require.Contains(t, string(content), "alwaysApply: true")
		require.True(t, verifiedProjection(content))
		require.False(t, verifiedProjection(append(append([]byte(nil), content...), []byte("edited\n")...)))
		require.False(t, verifiedProjection([]byte("unstamped\n")))
		require.True(t, legacyProjectionOwned([]byte(legacyProjectionMarker+"\nbody\n"), policies[0]))
		require.True(t, legacyProjectionOwned([]byte("---\ndescription: \"x\"\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]))
		require.True(t, legacyProjectionOwned([]byte("---\ndescription: \"x\"\nglobs: \"**/*.go\"\nalwaysApply: false\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[1]))
		require.True(t, legacyProjectionOwned([]byte(legacyProjectionMarker+"\nbody\n"+legacyProjectionMarker+"\n"), policies[0]),
			"a rule body may itself contain the legacy marker")
		require.False(t, legacyProjectionOwned([]byte("user text\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"the old marker embedded in user content is not the exact legacy format")
		require.False(t, legacyProjectionOwned([]byte("---\napplyTo: \"**/*.go\"\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"another agent's legacy frontmatter is not an exact format match")
		require.False(t, legacyProjectionOwned([]byte("---\ndescription: \"\"\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"the legacy renderer omitted empty descriptions")
		require.False(t, legacyProjectionOwned([]byte("---\ndescription: `x`\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"the legacy renderer emitted canonical strconv-quoted strings")
		require.False(t, legacyProjectionOwned([]byte("---\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"the legacy renderer never emitted an empty frontmatter block")
		require.False(t, legacyProjectionOwned([]byte("---\ndescription: \"x\"\nalwaysApply: false\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[1]),
			"alwaysApply must agree with whether globs were emitted, or the file is not the exact legacy format")
		require.False(t, legacyProjectionOwned([]byte("---\ndescription: \"x\"\nstray\n---\n\n"+legacyProjectionMarker+"\nbody\n"), policies[0]),
			"a frontmatter line that is not key: value at all was never emitted by the legacy renderer")

		emptySlug := nativeFilename(teamdocs.TeamRule{Name: "!!!", RelPath: "x"}, policies[0])
		require.Contains(t, emptySlug, "sageox-team-rule-")
		longSlug := nativeFilename(teamdocs.TeamRule{Name: strings.Repeat("Long Name ", 10), RelPath: "x"}, policies[0])
		stem := strings.TrimSuffix(strings.TrimPrefix(longSlug, managedPrefix), policies[0].Extension)
		slug := stem[:strings.LastIndex(stem, "-")]
		require.LessOrEqual(t, len(slug), 40)
	})
}

func TestReconcileRoot_DefensiveFilesystemBranches(t *testing.T) {
	project := t.TempDir()
	rootPath := filepath.Join(project, "rules")
	require.NoError(t, os.MkdirAll(rootPath, 0o755))
	p := policy{Root: "rules", Extension: ".md"}

	_, _, err := reconcileRoot(context.Background(), project, filepath.Join(project, "missing"), p, nil)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(rootPath, "same.md"), []byte("same"), 0o644))
	written, removed, err := reconcileRoot(context.Background(), project, rootPath, p, map[string][]byte{"same.md": []byte("same")})
	require.NoError(t, err)
	require.Empty(t, written)
	require.Empty(t, removed)

	require.NoError(t, os.Mkdir(filepath.Join(rootPath, "blocked.md"), 0o755))
	_, _, err = reconcileRoot(context.Background(), project, rootPath, p, map[string][]byte{"blocked.md": []byte("new")})
	require.Error(t, err)

	root, err := os.OpenRoot(rootPath)
	require.NoError(t, err)
	defer root.Close()
	require.Error(t, atomicWrite(root, "missing/child.md", []byte("x")))
	require.NoError(t, os.Mkdir(filepath.Join(rootPath, "destination.md"), 0o755))
	require.Error(t, atomicWrite(root, "destination.md", []byte("x")))

	retired := filepath.Join(rootPath, managedPrefix+"retired.md")
	require.NoError(t, os.Mkdir(retired, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(retired, "child"), []byte("x"), 0o644))
	_, _, err = reconcileRoot(context.Background(), project, rootPath, p, nil)
	require.Error(t, err)
}

// TestReconcile_FailedTrackedCheckNeverReadsAsUntracked is the red-first proof
// for the tracked-path conflation: `git ls-files --error-unmatch` used to be
// consulted as `cmd.Run() == nil`, so a canceled context — the ordinary outcome
// when automatic convergence hits its deadline — looked exactly like "git does
// not track this path". Reconcile then overwrote or deleted a TRACKED rule
// projection and reported it applied, leaving an uncommitted rule change in the
// working tree with no pending state scheduled to revisit it.
//
// The assertion that matters is not merely "an error came back": it is that the
// error is NOT ErrProjectionConflict, because the convergence coordinator
// settles conflicts and retries everything else. A deadline must retry.
//
// Cancellation is driven by a SIGNAL, not a timeout. A fixed deadline racing
// real work is flaky in the direction that matters least — it expires before
// the run reaches `git ls-files` at all, the ignore probe reports the root
// unprotected, reconcileRoot never runs, and the test fails against correct
// code. Here the git shim announces that it reached `ls-files` and then blocks;
// only then does the test cancel, so the cancellation always lands inside the
// call under test.
func TestReconcile_FailedTrackedCheckNeverReadsAsUntracked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shim that blocks git ls-files is a POSIX shell script")
	}
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	project := t.TempDir()
	rulesRoot := filepath.Join(project, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
		[]byte(".claude/rules/sageox-team-*\n"), 0o644))
	gitInit := exec.Command(realGit, "init", "-q")
	gitInit.Dir = project
	require.NoError(t, gitInit.Run())

	source := filepath.Join(t.TempDir(), "security.md")
	require.NoError(t, os.WriteFile(source, []byte("New body.\n"), 0o644))
	rule := teamdocs.TeamRule{
		Name: "security", RelPath: "security.md", AbsPath: source,
		Visibility: teamdocs.VisibilityAlways,
	}
	native, ok := NativePath(project, "claude", rule)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(native, []byte("tracked body\n"), 0o644))
	gitAdd := exec.Command(realGit, "add", "-f", "--",
		filepath.ToSlash(strings.TrimPrefix(native, project+string(filepath.Separator))))
	gitAdd.Dir = project
	require.NoError(t, gitAdd.Run())

	// Block only `ls-files`. The ignore probe (`check-ignore`) must still answer
	// instantly, or reconcileRoot is never reached and the test proves nothing.
	shimDir := t.TempDir()
	reached := filepath.Join(shimDir, "ls-files-reached")
	shim := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"ls-files\" ]; then : > " + reached + "; sleep 300; break; fi\ndone\nexec " + realGit + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// cancelDuringTrackedCheck runs one Reconcile, waits until the shim reports
	// that git reached `ls-files`, cancels there, and returns what Reconcile said.
	cancelDuringTrackedCheck := func(t *testing.T, rules []teamdocs.TeamRule) error {
		t.Helper()
		require.NoError(t, os.RemoveAll(reached))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, reconcileErr := Reconcile(ctx, project, rules)
			done <- reconcileErr
		}()
		require.Eventually(t, func() bool {
			_, statErr := os.Stat(reached)
			return statErr == nil
		}, 30*time.Second, 5*time.Millisecond, "reconcile never reached the tracked-path check")
		cancel()
		select {
		case reconcileErr := <-done:
			return reconcileErr
		case <-time.After(30 * time.Second):
			t.Fatal("reconcile did not return after its context was canceled")
			return nil
		}
	}

	assertRetryable := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "a git check that never answered must not be reported as success")
		require.NotErrorIs(t, err, ErrProjectionConflict,
			"a failed tracked-check must stay retryable; ErrProjectionConflict settles it and the retry never happens")
	}

	assertRetryable(t, cancelDuringTrackedCheck(t, []teamdocs.TeamRule{rule}))
	content, readErr := os.ReadFile(native)
	require.NoError(t, readErr)
	require.Equal(t, "tracked body\n", string(content),
		"the tracked projection was overwritten while the tracked-check was unanswered")

	assertRetryable(t, cancelDuringTrackedCheck(t, nil))
	require.FileExists(t, native,
		"the tracked projection was removed while the tracked-check was unanswered")
}
