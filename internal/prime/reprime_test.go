package prime

import "testing"

// TestIsForceReprimeSource pins the exact source values that force a full
// re-prime. This is the safety gate agent_hook.go and agent_prime.go both
// rely on — a wrong answer here means either a compact re-prime silently
// fires after /clear or /compact (dropped steering), or a genuine
// clear/compact event never gets its forced re-prime.
func TestIsForceReprimeSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"clear forces full", "clear", true},
		{"compact forces full", "compact", true},
		{"startup does not force", "startup", false},
		{"resume does not force", "resume", false},
		{"empty (direct invocation, no hook stdin) does not force", "", false},
		{"unknown source does not force", "some-future-source", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsForceReprimeSource(tt.source); got != tt.want {
				t.Errorf("IsForceReprimeSource(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

// TestShouldCompactReprime is the safety-critical gate: it decides whether
// an agent gets the full steering preamble again or a compact delta. Every
// case here documents WHY the decision was made — a passing test that
// doesn't explain the failure mode it prevents is not trustworthy for a
// gate this sensitive. See bd ox-32f6.
func TestShouldCompactReprime(t *testing.T) {
	tests := []struct {
		name           string
		primeCallCount int
		hookSource     string
		want           bool
		why            string
	}{
		{
			name:           "first prime ever (call count 1), no source",
			primeCallCount: 1,
			hookSource:     "",
			want:           false,
			why:            "the agent has never seen the preamble — must be FULL",
		},
		{
			name:           "first prime ever (call count 1), startup source",
			primeCallCount: 1,
			hookSource:     "startup",
			want:           false,
			why:            "same as above; source is irrelevant when this is genuinely the first prime",
		},
		{
			name:           "first prime ever (call count 1), even if source claims clear",
			primeCallCount: 1,
			hookSource:     "clear",
			want:           false,
			why:            "primeCallCount==1 alone is enough to force full; a clear source on a fresh identity is still full",
		},
		{
			name:           "routine re-prime, no hook source (CLAUDE.md BLOCKING re-invocation)",
			primeCallCount: 2,
			hookSource:     "",
			want:           true,
			why:            "the canonical compact case: a direct re-invocation moments after the agent was already primed this window, e.g. via the CLAUDE.md BLOCKING instruction which has no piped hook stdin",
		},
		{
			name:           "routine re-prime, resume source",
			primeCallCount: 3,
			hookSource:     "resume",
			want:           true,
			why:            "resume is not a force signal — the agent's transcript (and therefore its earlier prime output) survives",
		},
		{
			name:           "routine re-prime, startup source",
			primeCallCount: 2,
			hookSource:     "startup",
			want:           true,
			why:            "primeCallCount>1 with a non-force source is compact regardless of which non-force source it is",
		},
		{
			name:           "clear re-prime, high call count",
			primeCallCount: 5,
			hookSource:     "clear",
			want:           false,
			why:            "SAFETY-CRITICAL: PrimeCallCount alone would wrongly say compact here, but /clear just wiped the context window — the agent needs everything again. This is the exact scenario the belt-and-suspenders design targets: a long session that already had several redundant re-primes (bumping PrimeCallCount up) followed by a genuine /clear.",
		},
		{
			name:           "compact re-prime, high call count",
			primeCallCount: 9,
			hookSource:     "compact",
			want:           false,
			why:            "same as clear: /compact just wiped the context window, must be FULL regardless of PrimeCallCount",
		},
		{
			name:           "call count zero (defensive — should never happen but must not panic into compact)",
			primeCallCount: 0,
			hookSource:     "",
			want:           false,
			why:            "0 is not > 1, so this fails safe to full — defensive against an uninitialized/unexpected call count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldCompactReprime(tt.primeCallCount, tt.hookSource); got != tt.want {
				t.Errorf("ShouldCompactReprime(%d, %q) = %v, want %v — %s",
					tt.primeCallCount, tt.hookSource, got, tt.want, tt.why)
			}
		})
	}
}

// TestPreCompactForcesFullReprime pins the regression that shipped a PR with a
// SageOx-Session trailer but no `ox pr header` credit line and no saved plan.
//
// Claude Code fires PreCompact as its own event and leaves `source` empty
// (agentx.HookInput: "Source only for session start/end"). Reading source alone
// answered "not a force signal" at the exact moment the context window was
// about to be wiped, so ox emitted the compact delta — which deliberately omits
// the static-instructions block where the PR-header guidance and the hidden
// `ox plan save` command are taught. The trailer survived because it rides
// session state, re-emitted every prime; its top-of-body counterpart did not.
func TestPreCompactForcesFullReprime(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		primeCalls    int
		hookEventName string
		hookSource    string
		wantCompact   bool
	}{
		{"precompact with empty source still forces full", 4, "PreCompact", "", false},
		{"precompact ignores a stale source value", 4, "PreCompact", "resume", false},
		{"sessionstart compact forces full", 4, "SessionStart", "compact", false},
		{"sessionstart clear forces full", 4, "SessionStart", "clear", false},
		{"first prime is always full", 1, "PreCompact", "", false},
		// The tier still exists: a redundant same-window re-invocation is the
		// case it was built for, and must keep getting the delta.
		{"redundant direct re-invocation stays compact", 4, "", "", true},
		{"routine sessionstart resume stays compact", 4, "SessionStart", "resume", true},
		{"userpromptsubmit stays compact", 4, "UserPromptSubmit", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldCompactReprimeForEvent(tc.primeCalls, tc.hookEventName, tc.hookSource)
			if got != tc.wantCompact {
				t.Errorf("ShouldCompactReprimeForEvent(%d, %q, %q) = %v, want %v",
					tc.primeCalls, tc.hookEventName, tc.hookSource, got, tc.wantCompact)
			}
		})
	}
}

// TestShouldCompactReprimeKeepsTwoArgBehaviour pins the legacy wrapper: a caller
// with only a source must behave exactly as before.
func TestShouldCompactReprimeKeepsTwoArgBehaviour(t *testing.T) {
	t.Parallel()

	if ShouldCompactReprime(4, "compact") {
		t.Error("source=compact must still force a full preamble")
	}
	if !ShouldCompactReprime(4, "resume") {
		t.Error("source=resume must still take the compact tier")
	}
	if ShouldCompactReprime(1, "") {
		t.Error("a first prime must still be full")
	}
}
