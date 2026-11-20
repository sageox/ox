// Package livesession is the ox client for real-time session capture
// (ADR-247, shadow phase). A coding-agent hook appends committed raw.jsonl
// bytes to a local spool (Spool); the daemon and `session stop` push the spool
// to the server (Drain, Close); `session abort` deletes it everywhere (Abort).
//
// Session start only prepares a stream (Prepare, no network). It opens once its
// spool holds a human prompt (ErrAwaitingPrompt until then), so a prime or a
// /clear boundary alone never reaches the server; a recording that ends before
// its first prompt leaves the stream StatusEmpty.
//
// Nothing here may break or slow the v1 git push: every network call is bounded
// by a caller-supplied budget, and every failure degrades to "retry later". One
// outcome stops it on purpose: when the server says the session was deleted,
// the session is held on this machine (session.HoldDeletedOnServer), so the v1
// push never publishes what someone deleted on SageOx.
//
// Per-session files (all 0600, in the session directory):
//
//	spool.jsonl          the spool: append-only, whole JSONL lines; byte-identical
//	                     to the server's copy once drained. The one rewrite: an
//	                     unsent batch the secret scan refused becomes a gap line
//	                     (only the unsent tail is rewritten)
//	stream-state.json    stream state: ids, endpoint, acked offset, status
//	live.lock            advisory lock taken by whoever appends to or drains the spool
//	close-requested      stop asked for the close; kept until the stream ends, so
//	                     a close that could not take live.lock is never lost
//	delete-requested     abort recorded (or completed) the server-side delete;
//	                     while it exists the session reads as deleted
//	spool-lost           one line per batch Spool could not write; append-only,
//	                     never removed
//	spool-lost-declared  how many of those bytes gap lines already declared; the
//	                     next write or the close declares the rest
//	withheld-notice-owed the person may still be owed the notice that the stream
//	                     withheld a secret; the next session start or stop says it
//
// None of them may be copied into the Ledger (IsLocalFile). Teammate-note
// receipts (which notes the agent was shown, which receipts are settled or owed
// again) are NOT kept in the session folder: it is part of a pulled Ledger
// checkout, where anyone who can write the Ledger could plant a log and forge a
// delivery. They live under the data dir keyed by conversation id (ReceiptsDir);
// receipt files found in the folder are ignored. Abort's
// pending-deletes queue lives outside the session folder, in the data dir, so
// removing the folder never loses a delete. So does live-secret-rules.toml, the
// last upstream secret catalog the daemon's refresh validated.
//
// Every batch and close summary passes the publication secret scan before it
// leaves the machine (checkPublication), as Ledger and LFS publication scan
// session content, but without waiting on the network for the current rules:
// every publishing process scans with the catalog the daemon kept
// (RefreshSecretRules). A refused batch is replaced in the spool by one
// secret_withheld gap line and the stream goes on (withhold).
package livesession
