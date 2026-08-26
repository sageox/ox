// Package sacred defines the ledger paths that hold never-auto-delete work
// product per ADR-024 (data-protection hierarchy) — saved plans and recorded
// sessions — plus the mass-deletion policy shared by the two lines of defense:
//
//   - the commit-time guard in cmd/ox (assertNoSacredMassDeletion), which
//     refuses a ledger commit that would delete a mass of sacred files, and
//   - the periodic daemon detector in internal/doctor/autofix, which flags a
//     sacred mass-deletion that already landed in history (e.g. via an older
//     binary with no guard, or a force-push).
//
// One source of truth so the two cannot drift: if the guard tightens, the
// detector tightens with it.
package sacred

import "strings"

// Prefixes are the ledger paths holding never-auto-delete work product.
var Prefixes = []string{"data/plans/", "sessions/"}

// MassDeleteThreshold is the maximum number of sacred-path files a single ledger
// commit may delete before it is treated as a suspected wipe. Set deliberately
// tight: a routine `ox plan` delete or session removal touches one dir (its ~5
// artifact files) and passes; removing two or more plans/sessions at once trips
// it. Per ADR-024 sacred deletion needs explicit human approval, so err toward
// refusing. The 2026-08-25 incident staged 1000+ sacred deletions in one commit.
const MassDeleteThreshold = 5

// OverrideEnv, when set to "1", lets a deliberate bulk removal through the
// commit-time guard. The detector ignores it — a historical mass deletion is
// always worth surfacing, override or not.
const OverrideEnv = "OX_ALLOW_SACRED_MASS_DELETE"

// HasPrefix reports whether p lives under a sacred prefix.
func HasPrefix(p string) bool {
	for _, pre := range Prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// Filter returns the subset of paths that live under a sacred prefix, preserving
// order. Blank entries are dropped so callers can pass raw `git` output lines.
func Filter(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if HasPrefix(p) {
			out = append(out, p)
		}
	}
	return out
}
