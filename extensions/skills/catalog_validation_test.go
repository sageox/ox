package skills

import (
	"testing"
)

// The catalog is compiled into the binary and its digest drives the session hot
// path: prime compares it to decide whether the materialized inventory is stale.
// A malformed entry therefore has to fail at build/test time, not at a customer's
// terminal.

// TestDigest_IsStableAcrossCalls: prime calls Digest once per session start and
// compares it to a recorded value. A digest that varied between calls would make
// every session look stale and re-materialize the whole inventory.
func TestDigest_IsStableAcrossCalls(t *testing.T) {
	first, err := Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if first == "" {
		t.Fatal("empty digest: every repository would compare as stale forever")
	}
	for i := 0; i < 3; i++ {
		again, err := Digest()
		if err != nil {
			t.Fatalf("Digest repeat %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("digest changed between calls: %q then %q", first, again)
		}
	}
}

// TestIsRetired_MatchesOnlyTheRetiredNames: a false positive here DELETES a
// surface ox still ships; a false negative leaves an orphan behind forever, which
// is the class of bug this list exists to end.
func TestIsRetired_MatchesOnlyTheRetiredNames(t *testing.T) {
	if len(Retired) == 0 {
		t.Skip("no retired names declared")
	}
	for _, name := range Retired {
		if !IsRetired(name) {
			t.Errorf("declared retired name %q not matched; the orphan survives", name)
		}
	}
	for _, live := range []string{"ox-cli-plan", "ox-cli-consult", "sageox", "my-own-skill", ""} {
		if IsRetired(live) {
			t.Errorf("%q matched the retired list; ox would delete a surface it still ships", live)
		}
	}
}

// TestDefaultBundleIDs_ResolveToRealSkills: doctor re-asserts these on every fix
// run. A default naming a bundle the catalog does not contain would make every
// reconcile fail on a repository that had done nothing wrong.
func TestDefaultBundleIDs_ResolveToRealSkills(t *testing.T) {
	ids := DefaultBundleIDs()
	if len(ids) == 0 {
		t.Fatal("no default bundles: a fresh ox init would install nothing")
	}
	names, err := BundleNames(ids)
	if err != nil {
		t.Fatalf("the default bundles do not resolve: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the default bundles resolve to no skills")
	}
	for _, n := range names {
		if !IsKnown(n) {
			t.Errorf("default bundles name %q, which the catalog does not contain", n)
		}
	}
}

// TestBundleNames_UnknownBundleIsAnErrorNotAnEmptySet: silently resolving an
// unknown bundle to nothing would make a typo in the defaults install zero
// skills while every check reported success.
func TestBundleNames_UnknownBundleIsAnErrorNotAnEmptySet(t *testing.T) {
	if _, err := BundleNames([]string{"no-such-bundle"}); err == nil {
		t.Error("an unknown bundle id resolved silently instead of erroring")
	}
}

// TestValidate_AcceptsTheCompiledCatalog is the build-time gate: every shipped
// skill must parse, carry frontmatter, and satisfy the naming contract. A
// malformed entry has to fail here, not at a customer's terminal.
func TestValidate_AcceptsTheCompiledCatalog(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("the compiled catalog is invalid: %v", err)
	}
}

// TestIsKnown_OwnershipBoundary: IsKnown decides whether a name belongs to the
// catalog at all, which gates removal.
func TestIsKnown_OwnershipBoundary(t *testing.T) {
	for _, n := range []string{"my-own-skill", "", "ox-cli-does-not-exist"} {
		if IsKnown(n) {
			t.Errorf("%q reported as a catalog skill", n)
		}
	}
}

// TestSelected_UnknownNameIsRejected: reconcile builds its plan from these
// names. Accepting an unknown one would produce a plan that silently installs
// nothing for that entry.
func TestSelected_UnknownNameIsRejected(t *testing.T) {
	if _, err := Selected("1.0.0", []string{"definitely-not-a-skill"}); err == nil {
		t.Error("an unknown skill name was accepted")
	}
}
