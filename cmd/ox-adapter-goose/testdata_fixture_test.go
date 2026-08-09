package main

import (
	"database/sql"
	_ "embed"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// realSchemaSQL and realSeedSQL build a fixture database from Goose's actual
// sessions.db DDL rather than a schema this adapter merely assumes. See
// testdata/schema.sql and testdata/seed.sql for provenance, and
// .context/adapter-format-audit.md for why a self-consistent, hand-guessed
// fixture can never catch a schema-drift bug: it would still pass even if
// the adapter's assumptions about the real format were wrong.
//
//go:embed testdata/schema.sql
var realSchemaSQL string

//go:embed testdata/seed.sql
var realSeedSQL string

func newRealSchemaFixtureDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(realSchemaSQL); err != nil {
		t.Fatalf("apply real schema: %v", err)
	}
	if _, err := db.Exec(realSeedSQL); err != nil {
		t.Fatalf("apply seed data: %v", err)
	}
	return db
}

// toolResultEntry finds the tool-result entry (not the tool-call entry) for
// a given call ID. Both call and result entries share Role "tool" and
// CallID; the result is the one carrying ToolOutput.
func toolResultEntry(t *testing.T, entries []adapterprotocol.RawEntry, callID string) adapterprotocol.RawEntry {
	t.Helper()
	for _, e := range entries {
		if e.CallID == callID && e.ToolOutput != "" {
			return e
		}
	}
	t.Fatalf("no tool result entry found for call id %q among %d entries", callID, len(entries))
	return adapterprotocol.RawEntry{}
}

// TestRealSchemaFixture_FailingToolReportsIsError is the regression test for
// Finding 1: Goose's real envelope keeps the outer toolResult.status
// "success" for a failed command and nests the actual failure at
// toolResult.value.isError. Before the fix, toolResponseFields only checked
// the outer status, so every real failed-command row on the machine this
// fixture was captured from surfaced is_error absent (false).
func TestRealSchemaFixture_FailingToolReportsIsError(t *testing.T) {
	db := newRealSchemaFixtureDB(t)

	entries, _, err := readMessages(db, "fx_session_1", 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	result := toolResultEntry(t, entries, "fx_fail1")
	if !result.IsError {
		t.Error("failed tool response must surface is_error true, even though the outer status is \"success\"")
	}
	if !strings.Contains(result.ToolOutput, "Command exited with code 1") {
		t.Errorf("tool_output = %q, want the human-readable failure text extracted from value.content", result.ToolOutput)
	}
}

// TestRealSchemaFixture_SuccessfulToolIsNotError guards the other side of
// Finding 1: a genuinely successful response must not be miscategorized once
// the nested value.isError becomes authoritative.
func TestRealSchemaFixture_SuccessfulToolIsNotError(t *testing.T) {
	db := newRealSchemaFixtureDB(t)

	entries, _, err := readMessages(db, "fx_session_1", 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	result := toolResultEntry(t, entries, "fx_ok1")
	if result.IsError {
		t.Error("successful tool response must not be reported as an error")
	}
	if !strings.Contains(result.ToolOutput, "README.md") {
		t.Errorf("tool_output = %q, want the extracted text, not raw JSON noise", result.ToolOutput)
	}
}

// TestRealSchemaFixture_MultipleTextBlocksAreJoined is the regression test
// for Finding 2: tool_output must be human-readable text extracted from
// value.content, not the raw serialized JSON blob, and multiple text blocks
// must be concatenated rather than only the first kept.
func TestRealSchemaFixture_MultipleTextBlocksAreJoined(t *testing.T) {
	db := newRealSchemaFixtureDB(t)

	entries, _, err := readMessages(db, "fx_session_1", 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	result := toolResultEntry(t, entries, "fx_multi1")
	if result.IsError {
		t.Error("successful multi-block response must not be reported as an error")
	}
	if strings.Contains(result.ToolOutput, `"type":"text"`) {
		t.Errorf("tool_output = %q, contains raw JSON — text was not extracted", result.ToolOutput)
	}
	if !strings.Contains(result.ToolOutput, "./main.go") || !strings.Contains(result.ToolOutput, "truncated for fixture brevity") {
		t.Errorf("tool_output = %q, want both text blocks joined", result.ToolOutput)
	}
}
