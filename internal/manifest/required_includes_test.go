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

	got := EnsureRequiredIncludes(manifestPaths, RepoKindTeamContext)

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
		got := EnsureRequiredIncludes(paths, RepoKindTeamContext)

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
		got := EnsureRequiredIncludes(paths, kind)
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
	got := EnsureRequiredIncludes(nil, RepoKindTeamContext)
	if !hasPattern(got, "agents") {
		t.Errorf("empty manifest produced no agents/ floor: %v", got)
	}
}
