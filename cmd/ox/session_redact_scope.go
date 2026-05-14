package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionScope narrows the set of session directories that
// hydrateAllSessionsForScan, scanLedgerForSecrets, and
// enumerateRedactHistoryFindings will visit. It is the single source of
// truth for filter semantics shared by `ox session audit` and `ox
// session redact`.
//
// An empty scope (no Names, no Since/Until, AllowAll=false) is the
// "bare invocation" state and is rejected at the workflow entry point —
// the previous bare behavior (silently hydrate + scan the entire
// ledger) was the hazard documented at session_redact_history.go:38-40
// translated into the CLI default. AllowAll=true is the opt-in to that
// previous behavior with explicit operator consent.
type sessionScope struct {
	// Names is a literal allowlist. Each entry must correspond to an
	// existing directory under <ledger>/sessions/; missing names are
	// surfaced before any hydration begins (Validate).
	Names []string

	// Since/Until are lexicographic bounds against the session-name
	// prefix. Session names are ISO-prefixed (e.g.
	// "2026-04-25T15-22-ryan-OxGtDf"), so string comparison against
	// "2026-04-25" or "2026-04-25T15-22" is correct without parsing.
	// Empty string disables that bound.
	//
	// Range is half-open: [Since, Until). Until-exclusive lets
	// day-aligned ranges compose without overlap.
	Since string
	Until string

	// AllowAll opts back into the pre-#608 behavior of scanning every
	// session in the ledger. Mutually exclusive with the other fields
	// (enforced by cobra's MarkFlagsMutuallyExclusive on the command).
	AllowAll bool
}

// IsEmpty returns true when no scope has been specified — this is the
// bare-invocation state the workflow rejects.
func (s *sessionScope) IsEmpty() bool {
	if s == nil {
		return true
	}
	return len(s.Names) == 0 && s.Since == "" && s.Until == "" && !s.AllowAll
}

// Match decides whether a session-directory name should be included.
// Per the AllowAll-mutex rule, callers should not combine AllowAll with
// the other fields; if they do, AllowAll wins.
func (s *sessionScope) Match(name string) bool {
	if s == nil {
		return false
	}
	if s.AllowAll {
		return true
	}
	for _, n := range s.Names {
		if n == name {
			return true
		}
	}
	// Date bounds are independent of Names — a name match wins above;
	// a date match here is the union path. Both bounds use
	// lexicographic compare against the ISO-prefixed session name.
	if s.Since == "" && s.Until == "" {
		return false
	}
	if s.Since != "" && name < s.Since {
		return false
	}
	if s.Until != "" && name >= s.Until {
		return false
	}
	return true
}

// Validate runs cheap pre-hydration checks: each explicit Name must
// correspond to a directory under sessionsRoot, and the resolved scope
// must match at least one session. Both failure modes return BEFORE any
// LFS Batch call so a typo'd --session doesn't trigger a multi-minute
// fetch followed by an error.
func (s *sessionScope) Validate(sessionsRoot string) error {
	if s == nil || s.IsEmpty() {
		return fmt.Errorf("specify --all, --session, or --since/--until — bare invocation is rejected because it scans the entire ledger (slow + bulk)")
	}
	if s.AllowAll {
		// No further validation: --all matches every session by definition.
		// An empty ledger isn't an error here; the scan will just be a no-op.
		return nil
	}

	// Explicit-name check.
	var missing []string
	for _, n := range s.Names {
		info, err := os.Stat(filepath.Join(sessionsRoot, n))
		if err != nil || !info.IsDir() {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("no session named %q in ledger %s",
			strings.Join(missing, `", "`), filepath.Dir(sessionsRoot))
	}

	// Date-range or names-only — confirm at least one session matches.
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		// sessions root missing is benign — the workflow will report
		// "nothing to scan". Don't escalate here.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read sessions dir for scope validation: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if s.Match(e.Name()) {
			return nil
		}
	}
	return fmt.Errorf("no sessions match the requested scope (names=%v since=%q until=%q)",
		s.Names, s.Since, s.Until)
}

// Matcher returns a closure suitable for passing to
// hydrateAllSessionsForScan / scanLedgerForSecrets /
// enumerateRedactHistoryFindings. A nil scope returns a nil matcher,
// which the callers treat as "match all" — keeps the doctor surface
// (which has no scope concept) backward-compatible.
func (s *sessionScope) Matcher() func(name string) bool {
	if s == nil {
		return nil
	}
	return s.Match
}
