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

// MassDeleteThreshold is the maximum number of sacred ENTITIES — whole plans
// or sessions — a single ledger commit may remove before it is treated as a
// suspected wipe. Shared by the guard and the detector so the two cannot
// drift. Per ADR-024 sacred deletion needs explicit human approval, so err
// toward refusing; the 2026-08-25 incident removed well over a hundred
// entities in one commit.
//
// Counted in ENTITIES, never files. A file count cannot separate a wipe from
// routine churn: one session directory holds 6-7 files and one plan holds 4,
// so any file threshold low enough to catch a wipe also fires on deleting a
// single session. That ambiguity is not theoretical — it produced two tests
// asserting opposite outcomes for the same operation (delete exactly one
// sacred entity), reconciled only because their fixtures happened to seed
// one-file plans on one side and a six-file session on the other.
//
// Deleting one or two plans/sessions is routine churn: it commits, and it is
// not reported. Removing three or more in a single commit is the wipe
// signature.
const MassDeleteThreshold = 2

// EntityOf returns the plan or session directory that owns p — the unit a
// human would call "a plan" or "a session" — or "" when p is not sacred.
//
// Deleting some files inside a session is not losing the session, so callers
// must additionally confirm the entity is gone from the resulting tree before
// counting it. See the .rej sweep noted on MassDeleteThreshold: 15 deleted
// files across 6 session directories, 0 sessions lost.
func EntityOf(p string) string {
	p = strings.TrimSpace(p)
	for _, pre := range Prefixes {
		if !strings.HasPrefix(p, pre) {
			continue
		}
		rest := strings.TrimPrefix(p, pre)
		if rest == "" {
			return ""
		}
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
		}
		if rest == "" {
			return ""
		}
		return pre + rest
	}
	return ""
}

// Entities returns the distinct sacred entities touched by paths, preserving
// first-seen order. Blank and non-sacred entries are dropped so callers can
// pass raw `git` output lines.
func Entities(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		e := EntityOf(p)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

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
