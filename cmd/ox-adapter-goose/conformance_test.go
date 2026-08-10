package main

import (
	"database/sql"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// TestConformance runs the shared cross-adapter conformance suite
// (pkg/adaptertest) against the real-schema fixture database built from
// testdata/schema.sql and testdata/seed.sql — real Goose sessions.db DDL and
// content_json shapes captured from a real ~/.local/share/goose/sessions.db,
// with conversation text synthesized. See testdata_fixture_test.go and the
// seed.sql header comment for full provenance.
//
// The fixture's eight message rows produce eight entries: one user turn, one
// assistant turn (the thinking/image-only row drops both blocks), and three
// tool call/result pairs — one of which is the real "outer status stays
// success while value.isError is true" failure shape (Finding 1).
func TestConformance_RealSchemaFixture(t *testing.T) {
	db := newRealSchemaFixtureDB(t)

	adaptertest.Run(t, adaptertest.Suite{
		Adapter: "goose",
		Provenance: "Goose sessions.db real DDL (sqlite3 -readonly ~/.local/share/goose/sessions.db \".schema\", captured 2026-08-09) " +
			"+ content_json shapes copied from real rows in that database, conversation text synthesized — see testdata/seed.sql header",

		ReadAll: func() ([]adapterprotocol.RawEntry, error) {
			entries, _, err := readMessages(db, "fx_session_1", 0)
			return entries, err
		},

		ReadFrom: func(afterID int64) ([]adapterprotocol.RawEntry, int64, error) {
			return readMessages(db, "fx_session_1", afterID)
		},
		EndOffset: func() (int64, error) {
			return maxMessageID(db, "fx_session_1")
		},
		ResumePoints: func() ([]int64, error) {
			return resumeRowIDs(t, db, "fx_session_1", 1, 4), nil
		},

		Want: adaptertest.Want{
			MinEntries:     8,
			UserTurns:      1,
			AssistantTurns: 1,
			ToolCalls:      3,
			ToolResults:    3,
			PairedResults:  3,
			ErroredResults: 1,
		},
	})
}

// resumeRowIDs returns the message ids at the given 1-indexed positions in
// fx_session_1's insertion order, so ResumePoints tracks the fixture's real
// AUTOINCREMENT ids rather than hardcoding them.
func resumeRowIDs(t *testing.T, db *sql.DB, sessionID string, positions ...int) []int64 {
	t.Helper()
	rows, err := db.Query("SELECT id FROM messages WHERE session_id = ? ORDER BY id ASC", sessionID)
	if err != nil {
		t.Fatalf("query message ids: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan message id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate message ids: %v", err)
	}

	var out []int64
	for _, p := range positions {
		if p < 1 || p > len(ids) {
			t.Fatalf("resume position %d out of range for %d messages", p, len(ids))
		}
		out = append(out, ids[p-1])
	}
	return out
}
