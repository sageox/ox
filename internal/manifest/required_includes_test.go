package manifest

import (
	"strings"
	"testing"
)

// GH #862: the server-generated sync.manifest omits agents/, so sparse-checkout
// never materialized it and teamdocs.DiscoverRules walked an empty tree. Nothing
// surfaced, because "the directory is not checked out" and "this team has no
// rules" are the same value at the point anyone was looking. The floor below is
// the client half of the fix; the manifest itself is server-side.

func hasPattern(paths []string, want string) bool {
	for _, p := range paths {
		if strings.Trim(p, "/") == strings.Trim(want, "/") {
			return true
		}
	}
	return false
}

// TestEnsureRequiredIncludes_FloorsAgentsForTeamContext is the whole point: a
// manifest that never mentions agents/ still yields a checkout that has it.
func TestEnsureRequiredIncludes_FloorsAgentsForTeamContext(t *testing.T) {
	// Exactly the shape #862 describes: everything but agents/.
	manifestPaths := []string{"/*", "!/*/", "/.sageox/", "/docs/", "/memory/"}

	got := EnsureRequiredIncludes(manifestPaths, RepoKindTeamContext, nil)

	if !hasPattern(got, "agents") {
		t.Fatalf("agents/ was not floored into the sparse set; team rules and skills "+
			"stay invisible on every client: %v", got)
	}
	// It must be APPENDED. ComputeSparseSet emits "!/*/" to drop root-level
	// directories and --no-cone lets later patterns win, so a prepended include is
	// silently canceled by the very next pattern.
	var agentsIdx, dropIdx = -1, -1
	for i, p := range got {
		if strings.Trim(p, "/") == "agents" {
			agentsIdx = i
		}
		if p == "!/*/" {
			dropIdx = i
		}
	}
	if dropIdx >= 0 && agentsIdx < dropIdx {
		t.Errorf("agents/ at %d precedes the %q drop at %d, so it is canceled: %v",
			agentsIdx, "!/*/", dropIdx, got)
	}
}

// TestEnsureRequiredIncludes_LeavesACorrectManifestAlone: the floor must be
// additive. Once the server manifest is fixed, this becomes a no-op — it must not
// duplicate the entry or reorder anything.
func TestEnsureRequiredIncludes_LeavesACorrectManifestAlone(t *testing.T) {
	for _, spelling := range []string{"agents/", "/agents/", "agents", "/agents"} {
		paths := []string{"/*", "!/*/", "/.sageox/", spelling}
		got := EnsureRequiredIncludes(paths, RepoKindTeamContext, nil)

		if len(got) != len(paths) {
			t.Errorf("spelling %q: floor added a duplicate: %v", spelling, got)
		}
		var count int
		for _, p := range got {
			if strings.Trim(p, "/") == "agents" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("spelling %q: agents/ appears %d times: %v", spelling, count, got)
		}
	}
}

// TestEnsureRequiredIncludes_NeverWidensOtherRepoKinds.
//
// A knowledge bubble is deliberately narrow — control-plane metadata, AGENTS.md,
// and the curated knowledge tree. Flooring a team-context directory into it would
// materialize content the bubble was scoped to exclude, which is a data-exposure
// change, not a convenience.
func TestEnsureRequiredIncludes_NeverWidensOtherRepoKinds(t *testing.T) {
	paths := []string{"/*", "!/*/", "/.sageox/", "/knowledge/"}
	for _, kind := range []RepoKind{RepoKindKB, RepoKind("ledger"), RepoKind("")} {
		got := EnsureRequiredIncludes(paths, kind, nil)
		if len(got) != len(paths) {
			t.Errorf("kind %q: sparse set was widened to %v", kind, got)
		}
		if hasPattern(got, "agents") {
			t.Errorf("kind %q: agents/ was floored into a non-team-context repo", kind)
		}
	}
}

// TestEnsureRequiredIncludes_EmptyInputStillGetsTheFloor: a manifest that parsed
// to nothing must not yield a checkout missing the directories ox reads.
func TestEnsureRequiredIncludes_EmptyInputStillGetsTheFloor(t *testing.T) {
	got := EnsureRequiredIncludes(nil, RepoKindTeamContext, nil)
	if !hasPattern(got, "agents") {
		t.Errorf("empty manifest produced no agents/ floor: %v", got)
	}
}

// TestEnsureRequiredIncludes_DoesNotOverrideAnExplicitDeny.
//
// ComputeSparseSet enforces denies by OMITTING overlapping includes; it never
// emits deny patterns. So a floor appended afterwards would re-include content a
// team deliberately excluded, and under --no-cone the later pattern wins.
func TestEnsureRequiredIncludes_DoesNotOverrideAnExplicitDeny(t *testing.T) {
	paths := []string{"/*", "!/*/", "/.sageox/", "/docs/"}

	for _, deny := range []string{"agents/", "/agents", "agents"} {
		got := EnsureRequiredIncludes(paths, RepoKindTeamContext, []string{deny})
		if hasPattern(got, "agents") {
			t.Errorf("deny %q was overridden by the floor: %v", deny, got)
		}
	}
}

// TestEnsureRequiredIncludes_ReExcludesDeniedDescendants.
//
// The team wants agents/ but not agents/secrets/. Flooring agents/ re-includes
// the denied child unless the negation is emitted AFTER the include, because
// --no-cone lets later patterns win.
func TestEnsureRequiredIncludes_ReExcludesDeniedDescendants(t *testing.T) {
	paths := []string{"/*", "!/*/", "/.sageox/"}
	got := EnsureRequiredIncludes(paths, RepoKindTeamContext, []string{"agents/secrets/"})

	if !hasPattern(got, "agents") {
		t.Fatalf("agents/ was not floored: %v", got)
	}

	var agentsIdx, denyIdx = -1, -1
	for i, p := range got {
		if strings.Trim(p, "/") == "agents" {
			agentsIdx = i
		}
		if strings.HasPrefix(p, "!") && strings.Contains(p, "agents/secrets") {
			denyIdx = i
		}
	}
	if denyIdx < 0 {
		t.Fatalf("the denied descendant was not re-excluded; flooring agents/ re-included it: %v", got)
	}
	if denyIdx < agentsIdx {
		t.Errorf("the negation at %d precedes the include at %d, so it is overridden: %v",
			denyIdx, agentsIdx, got)
	}
}

// TestEnsureRequiredIncludes_UnrelatedDeniesAreNotEmitted: only descendants of
// what was floored are re-excluded. Emitting every deny would change the sparse
// set for paths this floor has nothing to do with.
func TestEnsureRequiredIncludes_UnrelatedDeniesAreNotEmitted(t *testing.T) {
	paths := []string{"/*", "!/*/", "/.sageox/"}
	got := EnsureRequiredIncludes(paths, RepoKindTeamContext, []string{"docs/private/", "memory/"})

	for _, p := range got {
		if strings.HasPrefix(p, "!") && p != "!/*/" {
			t.Errorf("an unrelated deny was emitted: %q in %v", p, got)
		}
	}
}
