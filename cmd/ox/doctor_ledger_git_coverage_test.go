package main

import (
	"sort"
	"testing"
)

// ledgerGitHealthCategory is the category name both the registry and
// checkLedgerGitHealth key off.
const ledgerGitHealthCategory = "Ledger Git Health"

// TestLedgerGitHealthOrderCoversRegistry is the ox-3vl6 drift guard.
//
// checkLedgerGitHealth used to carry a hand-written call list plus one positional
// bool per check (8 bools and a variadic by the end). Nothing tied that list to
// the registry, so checks could be registered — and look covered — while never
// running on the default `ox doctor` / `ox doctor --fix` path. Four had already
// drifted out: ledger-cache-tracked, ledger-rej-tracked, ledger-sparse-checkout,
// and session-ids-backfilled. Their repair code worked when invoked by slug
// directly, which is exactly what made the gap invisible.
//
// A registered check that never executes violates the "Doctor as Last Line of
// Defense" rule in CLAUDE.md: doctor must detect and repair every known failure
// mode, and a check nobody runs is indistinguishable from a check that passes.
func TestLedgerGitHealthOrderCoversRegistry(t *testing.T) {
	registered := make(map[string]bool)
	for slug, check := range DoctorCheckRegistry {
		if check.Category == ledgerGitHealthCategory {
			registered[slug] = true
		}
	}
	if len(registered) == 0 {
		t.Fatalf("no checks registered in category %q — registry lookup is broken", ledgerGitHealthCategory)
	}

	ordered := make(map[string]bool, len(ledgerGitHealthOrder))
	for _, slug := range ledgerGitHealthOrder {
		if ordered[slug] {
			t.Errorf("slug %q appears twice in ledgerGitHealthOrder — it would run twice", slug)
		}
		ordered[slug] = true
	}

	var missing []string
	for slug := range registered {
		if !ordered[slug] {
			missing = append(missing, slug)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("checks registered in %q but absent from ledgerGitHealthOrder, so they never run: %v\n"+
			"Add each slug to ledgerGitHealthOrder at the position its ordering constraints require.",
			ledgerGitHealthCategory, missing)
	}

	var stale []string
	for _, slug := range ledgerGitHealthOrder {
		if !registered[slug] {
			stale = append(stale, slug)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("slugs in ledgerGitHealthOrder that are not registered in %q: %v\n"+
			"Remove them, or fix the registration's Category.", ledgerGitHealthCategory, stale)
	}
}

// TestLedgerGitHealthOrderPreservesWedgeOrdering pins the two orderings that are
// load-bearing rather than cosmetic.
//
// ox-8zd3: a stuck merge/rebase/cherry-pick that left U-state files must surface
// BEFORE clean-workdir, or the wedge hides inside the dirty-workdir counter
// ("3 modified") instead of producing an actionable P0.
//
// ox-j3cl: the no-conflict twin — a stuck operation with NO U-state files — must
// surface before branch-status, so the reconcile there acts on a cleared repo
// instead of pulling on top of the wedge.
func TestLedgerGitHealthOrderPreservesWedgeOrdering(t *testing.T) {
	pos := make(map[string]int, len(ledgerGitHealthOrder))
	for i, slug := range ledgerGitHealthOrder {
		pos[slug] = i
	}

	tests := []struct {
		name   string
		before string
		after  string
		why    string
	}{
		{
			name:   "ox-8zd3 unmerged paths before clean workdir",
			before: CheckSlugLedgerUnmergedPaths,
			after:  CheckSlugLedgerCleanWorkdir,
			why:    "otherwise the wedge hides inside the dirty-workdir counter instead of raising a P0",
		},
		{
			name:   "ox-j3cl stuck operation before branch status",
			before: CheckSlugLedgerStuckOperation,
			after:  CheckSlugLedgerBranchStatus,
			why:    "otherwise branch-status reconciles by pulling on top of an unclearable rebase dir",
		},
		{
			name:   "ox-akab stranded commits rescued before unmerged-paths can abort",
			before: CheckSlugLedgerStrandedCommits,
			after:  CheckSlugLedgerUnmergedPaths,
			why:    "the unmerged-paths fix aborts an in-progress operation, which moves HEAD and turns stranded commits into unreferenced ones",
		},
		{
			name:   "ox-akab stranded commits rescued before stuck-operation can abort",
			before: CheckSlugLedgerStrandedCommits,
			after:  CheckSlugLedgerStuckOperation,
			why:    "the stuck-operation fix clears a rebase, which can move HEAD out from under commits that exist nowhere else",
		},
		{
			name:   "ox-akab stranded commits rescued before branch-status reconciles",
			before: CheckSlugLedgerStrandedCommits,
			after:  CheckSlugLedgerBranchStatus,
			why:    "branch-status reconciles a diverged ledger; rescuing afterwards is rescuing what is already gone",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, ok := pos[tc.before]
			if !ok {
				t.Fatalf("%q missing from ledgerGitHealthOrder", tc.before)
			}
			a, ok := pos[tc.after]
			if !ok {
				t.Fatalf("%q missing from ledgerGitHealthOrder", tc.after)
			}
			if b >= a {
				t.Errorf("%q (index %d) must run before %q (index %d): %s", tc.before, b, tc.after, a, tc.why)
			}
		})
	}
}

// TestLedgerGitHealthNoLedgerReturnsNil keeps the early-out contract: with no
// ledger configured the whole category is skipped rather than emitting a row of
// spurious failures.
func TestLedgerGitHealthNoLedgerReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SAGEOX_LEDGER_PATH", "")

	// getLedgerPath resolves against the working tree; in a temp HOME with no
	// configured ledger it must not invent one.
	if got := getLedgerPath(); got != "" {
		t.Skipf("a ledger resolved in this environment (%q); early-out path not exercised", got)
	}

	if checks := checkLedgerGitHealth(doctorOptions{}); checks != nil {
		t.Errorf("expected nil checks when no ledger is configured, got %d", len(checks))
	}
}
