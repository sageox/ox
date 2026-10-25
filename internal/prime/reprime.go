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

// preCompactEventName is Claude Code's compaction hook event. It is a distinct
// EVENT, not a SessionStart `source` value, which is exactly why it needs its
// own check — see IsForceReprimeEvent.
const preCompactEventName = "PreCompact"

// IsForceReprimeEvent is IsForceReprimeSource plus the one signal that field
// cannot carry: the PreCompact hook.
//
// Claude Code fires PreCompact as its own event, and agentx.HookInput documents
// that `source` is populated "only for session start/end" — so on a PreCompact
// payload `source` is EMPTY. Reading source alone therefore answers "not a
// force signal" at the one moment the context window is about to be wiped, and
// the agent gets the compact delta precisely when it is about to lose
// everything the full preamble already gave it.
//
// That is not theoretical. It is how a PR shipped with the `SageOx-Session:`
// trailer but no `ox pr header` credit line and no saved plan: the trailer is
// re-emitted with session state on every prime, while the PR-header guidance
// and the `ox plan save` teaching live in the static-instructions block the
// compact delta drops. Half the attribution contract survived compaction; the
// half that had to be re-read did not.
//
// Fails the same direction as its sibling: a redundant full preamble costs a
// few thousand tokens, a silently dropped directive costs the artifact.
func IsForceReprimeEvent(hookEventName, source string) bool {
	return hookEventName == preCompactEventName || IsForceReprimeSource(source)
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
	return ShouldCompactReprimeForEvent(primeCallCount, "", hookSource)
}

// ShouldCompactReprimeForEvent is ShouldCompactReprime with the hook EVENT in
// hand as well as the source field. Prefer it at any call site that has parsed
// hook stdin; the two-argument form remains for callers that genuinely have
// only a source (a direct re-invocation with no piped payload).
func ShouldCompactReprimeForEvent(primeCallCount int, hookEventName, hookSource string) bool {
	return primeCallCount > 1 && !IsForceReprimeEvent(hookEventName, hookSource)
}
