package prime

// IsForceReprimeSource reports whether a SessionStart hook `source` value
// (startup, resume, clear, compact) means the agent's context window was
// just wiped and `ox agent prime` MUST emit the full preamble — never a
// compact re-prime delta.
//
// Used by two call sites that must never disagree:
//   - the hook dispatcher (cmd/ox/agent_hook.go handleStart) — decides
//     whether to re-invoke prime at all on a SessionStart event when a
//     session marker already exists.
//   - prime itself (cmd/ox/agent_prime.go runAgentPrime) — decides FULL vs
//     compact when re-invoked directly, e.g. via the CLAUDE.md BLOCKING
//     instruction, which has no hook stdin at all. IsForceReprimeSource("")
//     is correctly false: a direct re-invocation with no clear/compact
//     signal is exactly the redundant, same-context-window case the
//     compact re-prime tier exists for.
func IsForceReprimeSource(source string) bool {
	return source == "clear" || source == "compact"
}

// ShouldCompactReprime reports whether a prime call should emit the
// compact re-prime delta (session-state only) instead of the full
// preamble (static instructions, command reference, team knowledge).
//
// True only when BOTH hold:
//   - primeCallCount > 1 — this is NOT the agent's first prime for this
//     store-tracked agent identity; an earlier call already delivered the
//     full preamble.
//   - the triggering source is NOT clear/compact (IsForceReprimeSource) —
//     the agent's context window was not just wiped, so it still holds
//     what that earlier prime call delivered.
//
// Belt-and-suspenders by design: PrimeCallCount alone cannot distinguish
// "redundant direct re-invocation within the same context window" from "a
// forced re-prime after /clear that happened to reuse the same agent_id
// and therefore inherited a PrimeCallCount > 1." Requiring both signals
// means a single false positive on either one fails safe to FULL, never
// to compact — a silently dropped directive is a serious regression, a
// redundant full preamble is only a wasted few thousand tokens. See bd
// ox-32f6.
func ShouldCompactReprime(primeCallCount int, hookSource string) bool {
	return primeCallCount > 1 && !IsForceReprimeSource(hookSource)
}
