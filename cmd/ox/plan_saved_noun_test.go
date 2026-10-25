package main

import "testing"

// TestSavedNoun: the save confirmation names WHAT was saved. Saving a design
// mockup and being told "Saved plan to ledger" is the single-noun collapse the
// --kind flag exists to end — and it is what a real `--kind mockup` save
// printed on 2026-09-22.
func TestSavedNoun(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"mockup", "mockup"},
		{"review", "review"},
		{"evidence", "evidence"},
		{"plan", "plan"},
		// Empty is KindPlan, so every artifact saved before kinds existed keeps
		// reading exactly as it always did.
		{"", "plan"},
		// Normalization is the store's job; the confirmation must not print a
		// differently-cased echo of what the author typed.
		{" Mockup ", "mockup"},
	} {
		if got := savedNoun(tc.in); got != tc.want {
			t.Errorf("savedNoun(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
