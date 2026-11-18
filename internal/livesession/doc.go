// Package livesession is the ox client for real-time session capture
// (ADR-247, shadow phase). A coding-agent hook appends committed raw.jsonl
// bytes to a local spool (Spool); the daemon and `session stop` push the spool
// to the server (Drain, Close); `session abort` deletes it everywhere (Abort).
//
// Nothing here may break or slow the v1 git push: every network call is bounded
// by a caller-supplied budget, and every failure degrades to "retry later".
//
// Per-session files (all 0600, in the session directory):
//
//	live.ndjson  the spool: append-only, whole NDJSON lines
//	live.json    State: ids, endpoint, acked offset, status
//	live.lock    advisory lock taken by whoever appends to or drains the spool
package livesession
