package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adaptertest"
)

// realFixture is a transcript Gemini CLI 0.36.0 wrote on its own. Only
// identifying leaf strings were substituted; the structure is byte-faithful.
// Full provenance: testdata/PROVENANCE.md.
const realFixture = "testdata/session-2026-04-03T22-06-4b1c7e02.json"

func readRealFixture(t *testing.T) []adapterprotocol.RawEntry {
	t.Helper()
	data, err := os.ReadFile(realFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	entries, _, err := parseGeminiSession(data)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return entries
}

// TestConformance runs the shared adapter conformance suite against real
// Gemini output. The Want counts below are the true contents of the fixture:
// 1 user turn, 4 model turns, and 3 list_directory calls each with a paired
// result — 11 entries in total.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, adaptertest.Suite{
		Adapter: "gemini",
		Provenance: "captured from Gemini CLI 0.36.0, " +
			"~/.gemini/tmp/<project_hash>/chats/session-2026-04-03T22-06-5d09fc36.json, " +
			"written 2026-04-03, copied 2026-08-09; session/message/tool ids, project hash " +
			"and the absolute capture path anonymized — see testdata/PROVENANCE.md",

		ReadAll: func() ([]adapterprotocol.RawEntry, error) {
			data, err := os.ReadFile(realFixture)
			if err != nil {
				return nil, err
			}
			entries, _, err := parseGeminiSession(data)
			return entries, err
		},

		ReadFrom: func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
			return geminiReadFromOffset(realFixture, offset)
		},

		EndOffset: func() (int64, error) {
			data, err := os.ReadFile(realFixture)
			if err != nil {
				return 0, err
			}
			entries, _, err := parseGeminiSession(data)
			if err != nil {
				return 0, err
			}
			return int64(len(entries)), nil
		},

		// mid-transcript offsets: one inside the first tool exchange, one
		// after it. Both must return exactly the tail of a full read.
		ResumePoints: func() ([]int64, error) {
			return []int64{1, 4, 8}, nil
		},

		Want: adaptertest.Want{
			MinEntries:     11,
			UserTurns:      1,
			AssistantTurns: 4,
			ToolCalls:      3,
			ToolResults:    3,
			PairedResults:  3,

			// ErroredResults is deliberately 0: see Unproven below.
			Unproven: []string{
				"failed tool calls — every toolCalls[].status in all 68 real gemini " +
					"sessions on the capture machine is \"success\", so is_error is proved " +
					"only by unit tests against the status values read out of the 0.36.0 bundle",
				"SessionMetadata.AgentVersion — gemini does not record its own version " +
					"anywhere in the session file",
				"multi-user-turn transcripts — no real session on the capture machine " +
					"has more than one user message",
			},
		},
	})
}

// TestFixtureIsTheFormatGeminiWrites pins the envelope keys that distinguish a
// real gemini session from the array-of-{role,parts} shape this adapter used to
// expect. If gemini ever changes the envelope, this fails next to the parser
// rather than showing up as an empty ledger.
func TestFixtureIsTheFormatGeminiWrites(t *testing.T) {
	data, err := os.ReadFile(realFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	session, err := decodeGeminiSession(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if session.SessionID == "" {
		t.Error("sessionId is empty — real gemini sessions always carry one")
	}
	if session.Kind != "main" {
		t.Errorf("kind = %q, want \"main\"", session.Kind)
	}
	if len(session.Messages) != 5 {
		t.Fatalf("got %d messages, want 5", len(session.Messages))
	}

	// content is polymorphic: array of parts for the user, bare string for
	// the model. A parser that assumes one shape drops the other silently.
	if got := firstByte(session.Messages[0].Content); got != "[" {
		t.Errorf("user content starts with %q, want a JSON array", got)
	}
	if got := firstByte(session.Messages[1].Content); got != `"` {
		t.Errorf("model content starts with %q, want a JSON string", got)
	}
	if session.Messages[0].Type != "user" || session.Messages[1].Type != "gemini" {
		t.Errorf("message types = %q/%q, want user/gemini",
			session.Messages[0].Type, session.Messages[1].Type)
	}
}

// firstByte reports the leading byte of a raw JSON value, or "" when the field
// was absent. Returning "" rather than panicking matters: this assertion is
// meant to go red when the parser stops binding "content", and a panic there
// would abort the test binary and hide every other failure.
func firstByte(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw[:1])
}

// TestReadFromOffsetTracksAppendedTurns proves the entry-count offset survives
// gemini rewriting the whole file: a poll that has already seen the fixture
// must return only what a later turn added.
func TestReadFromOffsetTracksAppendedTurns(t *testing.T) {
	full := readRealFixture(t)

	live := filepath.Join(t.TempDir(), "session-2026-04-03T22-06-4b1c7e02.json")
	data, err := os.ReadFile(realFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, offset, err := geminiReadFromOffset(live, 0)
	if err != nil {
		t.Fatalf("initial read: %v", err)
	}
	if offset != int64(len(full)) {
		t.Fatalf("offset after full read = %d, want %d", offset, len(full))
	}

	// rewrite the file with one extra model turn, as gemini does each turn
	grown := appendModelTurn(t, data)
	if err := os.WriteFile(live, grown, 0o600); err != nil {
		t.Fatal(err)
	}

	entries, newOffset, err := geminiReadFromOffset(live, offset)
	if err != nil {
		t.Fatalf("incremental read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d new entries, want 1", len(entries))
	}
	if entries[0].Role != adapterprotocol.RoleAssistant {
		t.Errorf("new entry role = %q, want assistant", entries[0].Role)
	}
	if entries[0].Content != "one more turn" {
		t.Errorf("new entry content = %q", entries[0].Content)
	}
	if newOffset != offset+1 {
		t.Errorf("new offset = %d, want %d", newOffset, offset+1)
	}
}

// appendModelTurn adds a model message to a real session file the way gemini
// would, by editing the decoded envelope rather than hand-writing JSON.
func appendModelTurn(t *testing.T, data []byte) []byte {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	msgs, _ := raw["messages"].([]any)
	raw["messages"] = append(msgs, map[string]any{
		"id":        "aaaaaaaa-0000-4000-8000-000000000006",
		"timestamp": "2026-04-03T22:07:00.000Z",
		"type":      "gemini",
		"content":   "one more turn",
		"model":     "gemini-3-flash-preview",
	})
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
