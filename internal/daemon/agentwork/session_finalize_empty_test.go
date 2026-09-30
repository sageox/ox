package agentwork

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The finalize scan's decision for an empty transcript in the ledger cache
// (GH #1106). Only a re-armed download of a session already in the Ledger is
// settled; every other empty copy stays skipped, so a new empty recording is
// never published and a stale copy never settles a real session.

const emptyRaw = `{"type":"header","metadata":{"agent_id":"OxRiLy"}}` + "\n" +
	`{"type":"footer","stopped_at":"2026-09-24T23:04:00Z"}` + "\n"

// ledgerEntry writes the Ledger's meta.json for the session with a manifest
// whose raw.jsonl entry describes transcript.
func (f cacheFixture) ledgerEntry(t *testing.T, transcript string, draft bool) {
	t.Helper()
	meta := map[string]any{
		"version":          "1.0",
		"summary_status":   "unrecoverable",
		"validation_error": "content validation failed: title too short (0 chars, minimum 3)",
		"summary_attempts": 3,
		"files":            map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef([]byte(transcript))},
	}
	if draft {
		meta = map[string]any{"version": "1.0", "draft": true}
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	f.ledgerMeta(t, string(data))
}

func TestDetect_EmptyTranscriptDecision(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f cacheFixture)
		want  string
	}{
		{
			name: "re-armed download of an empty session already in the Ledger",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
				f.ledgerEntry(t, emptyRaw, false)
			},
			want: outcomeFinalize,
		},
		{
			name: "new empty recording with no ledger entry is never published",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
			},
			want: outcomeSkipped,
		},
		{
			name: "empty recording whose ledger entry is a live draft",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
				f.ledgerEntry(t, "", true)
			},
			want: outcomeSkipped,
		},
		{
			name: "settled here, push failed, waiting to be published",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				for _, a := range requiredArtifacts {
					f.write(t, a, `{"title":"Brief session"}`)
				}
				f.write(t, "meta.json", `{"version":"1.0","title":"Brief session","summary_status":"ok"}`)
				f.ledgerEntry(t, emptyRaw, false)
			},
			want: outcomeUploadOnly,
		},
		{
			name: "stale local meta.json from an old failed attempt is never published",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				for _, a := range requiredArtifacts {
					f.write(t, a, `{"title":""}`)
				}
				f.write(t, "meta.json", `{"version":"1.0","summary_status":"unrecoverable","summary_attempts":3}`)
				f.ledgerEntry(t, emptyRaw, false)
			},
			want: outcomeSkipped,
		},
		{
			name: "empty download nobody asked to summarize",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.ledgerEntry(t, emptyRaw, false)
			},
			want: outcomeSkipped,
		},
		{
			name: "stale empty local copy of a session that has a conversation",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
				f.ledgerEntry(t, testRawContent, false)
			},
			want: outcomeSkipped,
		},
		{
			name: "ledger manifest does not describe the transcript",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
				f.ledgerMeta(t, `{"version":"1.0","summary_status":"unrecoverable"}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unreadable ledger meta.json",
			setup: func(t *testing.T, f cacheFixture) {
				f.write(t, "raw.jsonl", emptyRaw)
				f.write(t, ".needs-summary", "{}")
				f.ledgerMeta(t, `{"version":`)
			},
			want: outcomeSkipped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			name := "2026-09-24T23-03-riley-OxRiLy"
			f := cacheFixture{
				ledgerPath: ledgerPath,
				name:       name,
				cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
			}
			tc.setup(t, f)

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items := detectCacheOnly(t, h, ledgerPath)

			got := outcomeSkipped
			if len(items) == 1 {
				got = outcomeFinalize
				if items[0].Payload.(*SessionFinalizePayload).UploadOnly {
					got = outcomeUploadOnly
				}
			}
			require.LessOrEqual(t, len(items), 1)
			assert.Equal(t, tc.want, got)

			// The worker makes the same decision before the LLM.
			if len(items) == 1 {
				req, err := h.BuildPrompt(items[0])
				require.NoError(t, err)
				assert.True(t, req.SkipLLM, "an empty transcript must never reach the LLM")
			}
		})
	}
}

// TestBuildPrompt_EmptyTranscriptRechecksLedger: the scan queued an empty
// copy, then the Ledger's entry changed before the worker ran (a teammate's
// commit arrived). The worker must re-check, not trust the queue: with the
// Ledger now describing a different transcript, the copy is not settled.
func TestBuildPrompt_EmptyTranscriptRechecksLedger(t *testing.T) {
	ledgerPath := t.TempDir()
	name := "2026-09-24T23-03-riley-OxRiLy"
	f := cacheFixture{
		ledgerPath: ledgerPath,
		name:       name,
		cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
	}
	f.write(t, "raw.jsonl", emptyRaw)
	f.write(t, ".needs-summary", "{}")
	f.ledgerEntry(t, emptyRaw, false)

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	items := detectCacheOnly(t, h, ledgerPath)
	require.Len(t, items, 1, "precondition: the empty download is queued")

	f.ledgerEntry(t, testRawContent, false)

	_, err := h.BuildPrompt(items[0])
	require.NoError(t, err)
	payload := items[0].Payload.(*SessionFinalizePayload)
	assert.False(t, payload.emptyTranscript, "the worker must not settle a copy the Ledger no longer describes")
}
