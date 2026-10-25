package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
			name: "same-size local copy whose bytes differ from the Ledger's",
			setup: func(t *testing.T, f cacheFixture) {
				other := strings.Replace(emptyRaw, "OxRiLy", "OxRiLz", 1)
				require.Len(t, other, len(emptyRaw))
				f.write(t, "raw.jsonl", other)
				f.write(t, ".needs-summary", "{}")
				f.ledgerEntry(t, emptyRaw, false)
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

	req, err := h.BuildPrompt(items[0])
	require.NoError(t, err)
	payload := items[0].Payload.(*SessionFinalizePayload)
	assert.False(t, payload.emptyTranscript, "the worker must not settle a copy the Ledger no longer describes")
	// the copy still holds no conversation entries: that is a prefilter stub,
	// never a prompt with no transcript sent to the LLM
	assert.True(t, req.SkipLLM, "zero parsed entries must not reach the LLM")
}

// TestProcessResult_SettlesEmptyLedgerDownload drives the worker end to end
// for a re-armed empty download: no LLM, "Brief session" at status ok, and the
// Ledger's own record kept. Failure prevented: finalizing a download started
// meta.json from blank and copied it over the Ledger's, erasing repo_id,
// user_id and model, re-deriving identity from the transcript header, and
// minting a new session id that 404s /c/ links.
func TestProcessResult_SettlesEmptyLedgerDownload(t *testing.T) {
	const name = "2026-09-24T23-03-riley-OxRiLy"
	legacy := &lfs.SessionMeta{RepoID: "repo_empty_test", SessionName: name}
	for _, tc := range []struct {
		name      string
		sessionID string
		wantID    string
	}{
		{name: "session id kept", sessionID: "ses_01950000-0000-7000-8000-000000001106", wantID: "ses_01950000-0000-7000-8000-000000001106"},
		{name: "legacy session keeps its derived id", sessionID: "", wantID: legacy.EffectiveSessionID()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			f := cacheFixture{
				ledgerPath: ledgerPath,
				name:       name,
				cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
			}
			f.write(t, "raw.jsonl", emptyRaw)
			f.write(t, ".needs-summary", "{}")
			ledgerMeta, err := json.Marshal(map[string]any{
				"version": "1.0", "session_name": name, "session_id": tc.sessionID,
				"username": "riley", "user_id": "usr_riley", "repo_id": "repo_empty_test",
				"agent_id": "OxRiLy", "agent_type": "claude-code", "model": "claude-opus",
				"created_at":       "2026-09-24T23:03:00Z",
				"summary_status":   "unrecoverable",
				"validation_error": "content validation failed: title too short (0 chars, minimum 3)",
				"summary_attempts": 3,
				"files":            map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef([]byte(emptyRaw))},
			})
			require.NoError(t, err)
			f.ledgerMeta(t, string(ledgerMeta))

			tel := &fakeTelemetry{}
			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			h.SetTelemetry(tel)
			items := detectCacheOnly(t, h, ledgerPath)
			require.Len(t, items, 1, "precondition: the empty download is queued")

			req, err := h.BuildPrompt(items[0])
			require.NoError(t, err)
			require.True(t, req.SkipLLM, "an empty transcript must never reach the LLM")
			require.NoError(t, h.ProcessResult(items[0], &RunResult{}))

			got, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", name))
			require.NoError(t, err)
			assert.Equal(t, "Brief session", got.Title)
			assert.Equal(t, "ok", got.SummaryStatus)
			assert.Empty(t, got.ValidationError)
			assert.Zero(t, got.SummaryAttempts)
			assert.Equal(t, "repo_empty_test", got.RepoID)
			assert.Equal(t, "usr_riley", got.UserID)
			assert.Equal(t, "claude-opus", got.Model)
			// The transcript header here carries only agent_id: identity must come
			// from the Ledger's record, not be re-derived from the header.
			assert.Equal(t, "riley", got.Username)
			assert.Equal(t, "OxRiLy", got.AgentID)
			assert.Equal(t, "claude-code", got.AgentType)
			assert.Equal(t, tc.sessionID, got.SessionID, "the stored session id is kept as is, never backfilled")
			assert.Equal(t, tc.wantID, got.EffectiveSessionID(), "the session id the team resolves must not change")
			assert.NoDirExists(t, f.cacheDir, "the download is removed once the session is published")

			skipped := tel.lastByName("summarization_skipped")
			require.NotNil(t, skipped)
			assert.Equal(t, "empty_transcript", skipped.props["skip_kind"])
			assert.Nil(t, tel.lastByName("summarization"), "no LLM ran, so no summarization event")
		})
	}
}

// TestProcessResult_PrefilterSkipDiscardsLocalCopy: a thin local recording the
// prefilter judged not worth summarizing is discarded without the LLM and never
// staged into the Ledger. It shares ProcessResult's summary switch with the
// empty-transcript path above. Failure prevented: a trivial session the team
// never needed reaches the shared Ledger.
func TestProcessResult_PrefilterSkipDiscardsLocalCopy(t *testing.T) {
	ledgerPath := t.TempDir()
	name := "2026-05-04T15-00-testuser-OxThIN"
	f := cacheFixture{
		ledgerPath: ledgerPath,
		name:       name,
		cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
	}
	f.write(t, "raw.jsonl", `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}`+"\n"+
		`{"type":"user","content":"hi","seq":1}`+"\n")

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	items := detectCacheOnly(t, h, ledgerPath)
	require.Len(t, items, 1, "precondition: the local recording is queued")

	req, err := h.BuildPrompt(items[0])
	require.NoError(t, err)
	require.True(t, req.SkipLLM, "the prefilter must skip the LLM")
	require.NoError(t, h.ProcessResult(items[0], &RunResult{}))

	assert.NoDirExists(t, f.cacheDir, "the discarded recording is removed")
	_, statErr := os.Stat(filepath.Join(ledgerPath, "sessions", name))
	assert.True(t, os.IsNotExist(statErr), "a discarded recording must never be staged into the Ledger")
}
