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
// tight: a single plan or session can exceed this file-count threshold. A hit
// is a safety backstop, not proof of a mass wipe or unintended deletion.
// Per ADR-024 sacred deletion needs explicit human approval, so err toward
// refusing. The 2026-08-25 incident staged 1000+ sacred deletions in one commit.
//
// Used by the commit-time guard, which BLOCKS. Left in files deliberately:
// erring toward refusing costs a caller one failed commit and loses nothing,
// so it is the safe direction for a gate that stands between an automated
// reconcile and permanent deletion.
const MassDeleteThreshold = 5

// DetectorEntityThreshold is the equivalent for the daemon's periodic history
// scan, which only REPORTS — it never blocks, restores, or deletes.
//
// Counted in whole plans/sessions rather than files, and deliberately NOT
// shared with MassDeleteThreshold, because the two mechanisms fail in opposite
// directions. A guard that fires needlessly costs a refused commit; a detector
// that fires needlessly costs the operator's attention every 15 minutes,
// forever, until they stop reading it — which is how a real alert gets missed.
// Counting files made every ordinary `ox session delete` (6-7 files) and every
// stale-artifact sweep look like a wipe: on one real ledger, 32 of 33 reported
// "mass deletions" were nothing of the kind.
const DetectorEntityThreshold = 2

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
