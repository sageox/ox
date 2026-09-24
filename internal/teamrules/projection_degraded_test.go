package teamrules

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/stretchr/testify/require"
)

// Every projection fixture in projection_test.go stages the SAME well-formed
// repository: a real git repo whose ignore rules already cover the managed
// prefix. That is realistic, and it is also the one axis along which the
// exactly-once contract has never been exercised — Reconcile's "not protected"
// branch and ForPrime's delivery decision are two halves of one promise, and
// only the Reconcile half has ever been observed in a repository that lacks
// the protection.
//
// ruleRootProtection is that axis, made explicit so a test can opt into the
// degraded state deliberately instead of staging a clean repo and hoping.
type ruleRootProtection int

const (
	// protectedRoot is what every other fixture in this package stages, kept
	// here as the positive control: without it a passing degraded case cannot
	// be distinguished from a fixture that simply never reached the code.
	protectedRoot ruleRootProtection = iota
	// unignoredRoot is a git repository whose ignore rules do not cover the
	// managed prefix. A hand-edited .gitignore, an ignore entry moved between
	// the repo root and .claude/.gitignore with the path prefix left wrong, or
	// a clone where `ox doctor` has not yet repaired the tracked ignore setup
	// all land here.
	unignoredRoot
	// ungitRoot is a directory that is not a git repository at all, so
	// `git check-ignore` fails outright rather than answering "not ignored".
	// managedPathIgnored collapses both into false; this pins that the collapse
	// falls to the safe side.
	ungitRoot
)

// stageRuleRoot builds a project holding a Claude rule root in the requested
// state, plus one unscoped always-on rule that every native agent can carry.
func stageRuleRoot(t *testing.T, protection ruleRootProtection) (string, teamdocs.TeamRule) {
	t.Helper()
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "rules"), 0o755))

	switch protection {
	case protectedRoot:
		require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
			[]byte(".claude/rules/*-team.md\n"), 0o644))
	case unignoredRoot:
		// An ignore file that exists and covers something else entirely: the
		// realistic shape of the mistake, not an empty file.
		require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
			[]byte("node_modules/\n.claude/settings.local.json\n"), 0o644))
	}
	if protection != ungitRoot {
		git := exec.Command("git", "init", "-q")
		git.Dir = project
		require.NoError(t, git.Run())
	}

	source := filepath.Join(t.TempDir(), "security.md")
	require.NoError(t, os.WriteFile(source, []byte("Never log credentials.\n"), 0o644))
	return project, teamdocs.TeamRule{
		Name: "security", Description: "Security conventions", RelPath: "security.md",
		AbsPath: source, Visibility: teamdocs.VisibilityAlways,
	}
}

// TestReconcile_UnprotectedRootHandsTheRuleToPrimeForReal is the degraded
// sibling of TestReconcile_EachSupportedAgentGetsExactlyOneDelivery.
//
// The failure it prevents: Reconcile declines to write an unignored rule root
// and records "using the prime index" as the delivery path, but nothing has
// ever checked that prime then actually delivers. If ForPrime suppressed the
// rule here — as it does once any file sits at the projection's path — the
// coworker would receive the Team Rule through NEITHER surface while both
// halves of ox reported success.
//
// The body assertion is not decoration: a rule handed to prime as an empty
// indexed entry is a catalog line, not a rule. The coworker has to get the
// text, because native delivery is what would have given them the text.
func TestReconcile_UnprotectedRootHandsTheRuleToPrimeForReal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		protection ruleRootProtection
		// wantNative is whether the rule is expected to land as a file.
		wantNative bool
	}{
		{"positive control: a protected root still projects natively", protectedRoot, true},
		{"ignore rules do not cover the managed prefix", unignoredRoot, false},
		{"the project is not a git repository at all", ungitRoot, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			project, rule := stageRuleRoot(t, tt.protection)

			result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
			require.NoError(t, err, "an unprotected rule root must be a fallback, never a failure")

			native, ok := NativePath(project, "claude", rule)
			require.True(t, ok)
			prime := ForPrime(project, "claude", []teamdocs.TeamRule{rule})

			if tt.wantNative {
				require.FileExists(t, native)
				require.Contains(t, result.NativeAgents[rule.Name], "claude")
				require.Empty(t, prime,
					"a rule delivered natively was ALSO handed to prime, so the coworker receives it twice")
				return
			}

			require.Empty(t, result.Written,
				"ox wrote an unignored file into the working tree; the whole point of the protection check is that it must not")
			require.NoFileExists(t, native)
			require.Empty(t, result.NativeAgents[rule.Name])
			require.NotEmpty(t, result.Fallbacks[rule.Name],
				"Reconcile skipped the rule without recording why, so no surface claims responsibility for it")
			require.Contains(t, result.Fallbacks[rule.Name][0].Reason, "not ignored")

			require.Len(t, prime, 1,
				"Reconcile fell back to the prime index and prime delivered nothing; the rule reached no surface at all")
			require.Equal(t, rule.Name, prime[0].Name)
			require.Equal(t, teamdocs.VisibilityAlways, prime[0].Visibility,
				"an unscoped always-on rule was demoted to a lazy index entry it was never scoped to be")
			require.Contains(t, prime[0].Body, "Never log credentials.",
				"prime delivered a catalog entry with no rule text, which is not delivery")
		})
	}
}

// TestReconcile_SymlinkedRuleRootNeverWritesOutsideTheRepository pins an
// invariant that nothing in ox currently enforces on purpose.
//
// `.claude/rules` is a path the user owns; pointing it at a directory outside
// the repository is a one-line change. ox never checks for that. What stops a
// write today is incidental: `git check-ignore` will not report any path under
// a symlinked directory as ignored — verified here against a repo-relative
// pattern, a `**` pattern, and a whole-directory pattern — so the protection
// probe always answers false and Reconcile falls back to prime.
//
// That makes replacing the `git check-ignore` subprocess with an in-process
// ignore matcher — an obvious optimization, since this runs per rule root on
// the convergence tick — silently load-bearing: a pure pattern matcher would
// answer "ignored" and ox would begin creating and DELETING *-team.md
// files in an arbitrary directory on the machine. This test is what catches it.
func TestReconcile_SymlinkedRuleRootNeverWritesOutsideTheRepository(t *testing.T) {
	t.Parallel()

	patterns := map[string]string{
		"repo-relative pattern": ".claude/rules/*-team.md\n",
		"recursive pattern":     "**/*-team.md\n",
		"whole-directory":       ".claude/**\n",
	}

	for name, pattern := range patterns {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			project := t.TempDir()
			outside := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
			if err := os.Symlink(outside, filepath.Join(project, ".claude", "rules")); err != nil {
				t.Skipf("symlinks are not available here: %v", err)
			}
			require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte(pattern), 0o644))
			git := exec.Command("git", "init", "-q")
			git.Dir = project
			require.NoError(t, git.Run())

			source := filepath.Join(t.TempDir(), "security.md")
			require.NoError(t, os.WriteFile(source, []byte("Never log credentials.\n"), 0o644))
			rule := teamdocs.TeamRule{
				Name: "security", RelPath: "security.md", AbsPath: source,
				Visibility: teamdocs.VisibilityAlways,
			}

			result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
			require.NoError(t, err)

			entries, readErr := os.ReadDir(outside)
			require.NoError(t, readErr)
			require.Empty(t, entries,
				"ox wrote through a symlinked rule root into a directory outside the repository")
			require.Empty(t, result.Written)

			require.Len(t, ForPrime(project, "claude", []teamdocs.TeamRule{rule}), 1,
				"refusing the symlinked root must hand the rule to prime, not drop it")
		})
	}
}

// TestReconcile_StaleProjectionInAnUnprotectedRootIsStrandedBetweenBothSurfaces
// is a DEFECT REPORT, not a passing guard. It is skipped because the behavior
// it asserts is the behavior ox should have, and today it does not.
//
// Observed (probe run 2026-09-21, this fixture without the skip):
//
//	phase 1 protected:   Reconcile writes .claude/rules/security-*-team.md
//	                     ForPrime("claude") -> 0 rules   (correct: native owns it)
//	phase 2 unprotected: Reconcile -> Written=[] Removed=[]
//	                     Fallbacks[security] = "native rule root exists but Team
//	                     Rule files are not ignored by git"
//	                     the stale projection is STILL on disk
//	                     ForPrime("claude") -> 0 rules   <-- the disagreement
//	phase 3 team edits the rule:
//	                     Reconcile -> Written=[] (the root is skipped entirely)
//	                     the on-disk projection still holds the OLD text
//	                     ForPrime("claude") -> 0 rules
//
// So once protection is lost after a projection has landed, Reconcile reports
// that delivery has fallen back to the prime index while ForPrime — seeing an
// ox-owned file at the path — suppresses prime delivery. The coworker keeps
// reading a frozen copy of the rule forever, and every later edit the team
// makes is silently dropped. Neither half is wrong in isolation, which is why
// this is invisible to the existing tests: Reconcile's fallback is asserted in
// TestProjectionHelpers_DefensiveAndFallbackBranches against an EMPTY root,
// where no projection exists to strand.
//
// It is the same class as the bug nativePresent's own comment documents ("it
// reached neither surface"), one step further along: it reaches exactly one
// surface, permanently stale.
//
// Reachable without anything exotic: edit .gitignore, move the ignore entry
// between the repo root and .claude/.gitignore with the path prefix wrong, or
// simply have not run `ox doctor` yet. Reconcile's own comment states the
// intent this violates — "When protection is missing, prime remains the
// delivery path and doctor owns repairing the tracked ignore setup."
//
// Fixing it belongs in production code (internal/teamrules/projection.go),
// FIXED: ForPrime now consults the same protection probe Reconcile uses before
// treating a native file as delivered, so a root that has lost its ignore rule
// falls back to prime instead of stranding the rule on a frozen copy. Delivering
// twice in that state is deliberate — duplicated beats silently stale.
func TestReconcile_StaleProjectionInAnUnprotectedRootIsStrandedBetweenBothSurfaces(t *testing.T) {

	project, rule := stageRuleRoot(t, protectedRoot)

	_, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
	require.NoError(t, err)
	native, ok := NativePath(project, "claude", rule)
	require.True(t, ok)
	require.FileExists(t, native, "setup failed: the rule never projected while protected")
	require.Empty(t, ForPrime(project, "claude", []teamdocs.TeamRule{rule}))

	// Protection goes away after the projection already landed.
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte("node_modules/\n"), 0o644))

	// The team edits the rule; the coworker must end up reading the new text
	// through SOME surface.
	require.NoError(t, os.WriteFile(rule.AbsPath, []byte("Rotate credentials quarterly.\n"), 0o644))
	result, err := Reconcile(context.Background(), project, []teamdocs.TeamRule{rule})
	require.NoError(t, err)
	require.NotEmpty(t, result.Fallbacks[rule.Name], "fixture no longer reaches the unprotected branch")

	prime := ForPrime(project, "claude", []teamdocs.TeamRule{rule})
	require.Len(t, prime, 1,
		"Reconcile handed the rule to prime and prime suppressed it because a stale projection is still on disk")
	require.Contains(t, prime[0].Body, "Rotate credentials quarterly.",
		"the coworker is reading a frozen copy of a Team Rule that the team has since changed")
}
