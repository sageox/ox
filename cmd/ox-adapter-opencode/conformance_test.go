package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance_RealTranscript is intentionally Unproven: no real OpenCode
// message/part content is available on any machine reachable from this
// session, and this package refuses to hand-author a fixture in the format
// it assumes OpenCode writes — that exact mistake (a self-consistent fixture
// that can never fail) is what this whole conformance epic exists to
// eliminate.
//
// What was checked before concluding this:
//
//  1. ~/.local/share/opencode/opencode.db (this machine's real OpenCode data
//     dir): session=1 row, message=0 rows, part=0 rows, session_message=0
//     rows. The one session row (ses_096fdea05ffeagb7OGYpJCte6a, directory
//     "/Users/ryan") has no messages — readMessages() correctly returns zero
//     entries against it; there is nothing to read.
//  2. A second real OpenCode data dir (Conductor's embedded copy) was also
//     checked: 67 session rows, all with zero messages/parts.
//  3. Why the store is empty: this machine runs opencode 1.1.19 (binary
//     dated January 2026; upstream is 1.18.15 as of this writing). Upstream
//     moved session storage from JSON files to SQLite around 1.1.53, and a
//     known upstream bug silently skips the JSON->SQLite migration on
//     incremental upgrades — this install's real conversation history is
//     still sitting in ~/.local/share/opencode/storage/message/ses_*/,
//     invisible to the SQLite code path session.go reads from.
//  4. A live capture was attempted (running the actual opencode 1.1.19
//     binary against a local Ollama model, in a fully isolated fresh
//     XDG_DATA_HOME with no prior state to migrate) to see whether a from-
//     scratch install would populate SQLite. It did not: even a brand-new
//     1.1.19 session produced only storage/message/<session>/<msg>.json and
//     storage/part/<msg>/<part>.json files, no opencode.db rows at all. This
//     confirms 1.1.19 predates SQLite-backed session storage entirely —
//     it isn't only a migration gap on upgrade.
//  5. The pre-migration JSON files ARE real message content, but they are
//     not a drop-in fixture for readMessages(): the stored shape differs
//     from what session.go's ocMessageData/ocPartData expect for the SQL
//     data blob column (e.g. the file format nests {"model":{"providerID",
//     "modelID"}}, whereas ocMessageData expects flat top-level modelID/
//     providerID fields). Translating one shape into the other by hand
//     would itself be an invented mapping — precisely the class of mistake
//     this suite exists to catch, just one layer removed.
//
// This test — and the schema-level tests in session_test.go, built against
// the real captured DDL — remain the strongest evidence available on this
// machine. Closing this gap needs either a real opencode.db that has
// actually completed the JSON->SQLite migration (v1.1.53+), or a verified
// reading of that migration's code confirming the resulting data blob shape.
func TestConformance_RealTranscript(t *testing.T) {
	adaptertest.Unproven(t, "opencode",
		"no real opencode.db on any reachable machine has non-empty message/part rows (this install is 1.1.19, which predates the JSON->SQLite session storage migration — see the file doc comment for what was checked and why the pre-migration JSON store is not a drop-in fixture)")
}
