package session

// finalize.go — shared entry point for session finalization, encoding the
// sync-vs-async decision as a small pure function rather than letting each
// caller re-derive it from config and environment state.
//
// Background. There are three players in the session-stop pipeline:
//
//   1. The CLI's `ox agent <id> session stop` path
//      (cmd/ox/agent_session.go::runAgentSessionStop). This is the
//      canonical session-finish path and always runs inline.
//
//   2. The daemon's anti-entropy session-finalize handler
//      (internal/daemon/agentwork/session_finalize.go). This runs
//      periodically in the background, detecting orphaned sessions
//      that never got committed/pushed (because session-stop's network
//      call failed, the process crashed, or the user opted into
//      "delegated" summarization).
//
//   3. The daemon's signaled "finalize this session for me" RPC
//      (`signalDaemonSessionFinalize` in agent_session.go), used by
//      the `delegated` summarizer mode for non-blocking session-stop.
//
// The pre-existing assumption was that (3) is always available — when
// the user opts into delegated, the daemon must be running to receive
// the signal. In a sandbox / ephemeral environment that assumption
// silently breaks: the daemon is off (DaemonViable=false), the signal
// either fails or no-ops, and the session sits in the local cache until
// `ox doctor` (which itself only runs interactively) sweeps it.
//
// FinalizeDispatchMode encodes the right decision at the entry to the
// session-stop pipeline. Callers ask "should I run sync or signal the
// daemon?" and get back a single enum. Tests exercise both branches with
// explicit booleans; no global state.

// FinalizeDispatchMode is the answer to "how should this session-stop
// route the upload?" — a single switch the CLI consults.
type FinalizeDispatchMode int

const (
	// FinalizeSync — upload synchronously inline in the CLI process
	// before returning to the user. Slower (blocks the terminal until
	// the LFS upload + git push complete) but guaranteed: when the
	// CLI exits, the session is durable. The default for any
	// environment where the daemon isn't viable.
	FinalizeSync FinalizeDispatchMode = iota

	// FinalizeAsyncDaemon — copy the session into the ledger cache
	// locally, then fire a fire-and-forget IPC signal to the daemon
	// to run the upload + push in the background. Returns to the user
	// in milliseconds. Requires DaemonViable=true; falls back to
	// FinalizeSync when not available.
	FinalizeAsyncDaemon
)

// ChooseFinalizeMode picks sync-vs-async for a session finalize. The
// `userPrefersAsync` argument is the user's config preference
// (config.AgentSummarizerDelegated today; future modes can be added
// without changing this signature — they all reduce to "the user is
// OK paying token cost for a non-blocking exit").
//
// Decision matrix:
//
//	| daemonViable | userPrefersAsync | result            |
//	|--------------|------------------|-------------------|
//	| false        | *                | FinalizeSync      |
//	| true         | false            | FinalizeSync      |
//	| true         | true             | FinalizeAsyncDaemon|
//
// The (true, false) cell is the historical default for `inline` /
// `off` summarizer modes — sync upload, summarization either inline by
// the calling agent or skipped. The (true, true) cell is the
// `delegated` summarizer mode. The (false, *) cell is the bugfix:
// when daemon isn't viable, "delegated" silently degrades to sync
// instead of leaving the session orphaned.
//
// Callers that need to know "did delegated downgrade to sync?" should
// compare `userPrefersAsync && result == FinalizeSync`; the
// information is intentionally not encoded in the enum so the dispatch
// switch stays a two-way branch.
func ChooseFinalizeMode(daemonViable, userPrefersAsync bool) FinalizeDispatchMode {
	if !daemonViable {
		return FinalizeSync
	}
	if userPrefersAsync {
		return FinalizeAsyncDaemon
	}
	return FinalizeSync
}
