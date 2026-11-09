package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ox import <transcript> through POST …/recordings/transcript ---

// transcriptRequestWire mirrors api-go's CreateTranscriptRequest, including
// fields ox never sends. Decoded with DisallowUnknownFields so a field ox
// invents fails the test.
type transcriptRequestWire struct {
	Title        string     `json:"title"`
	Content      string     `json:"content"`
	SourceFormat string     `json:"source_format"`
	Summary      string     `json:"summary"`
	Participants []string   `json:"participants"`
	RecordedAt   *time.Time `json:"recorded_at"`
}

// serveTranscript plays POST …/recordings/transcript under the fake's mode:
// "single" creates the Discussion, "unsupported" is an unrouted 404, and an
// HTTP status answers with an error envelope.
func (s *fakeRecordingServer) serveTranscript(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.transcriptBodies = append(s.transcriptBodies, raw)
	s.transcriptHdrs = append(s.transcriptHdrs, r.Header.Clone())
	code, msg := s.errCode, s.errMsg
	s.mu.Unlock()

	switch s.mode {
	case "unsupported":
		http.Error(w, "404 page not found", http.StatusNotFound)
	case "single":
		// carries fields ox does not read, as api-go's response does
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":           fakeRecordingID,
			"context_type": "team",
			"context_id":   importRetryTeamID,
			"title":        "ignored by ox",
			"status":       "ready",
			"created_by":   "usr_test",
			"created_at":   time.Now().UTC().Format(time.RFC3339),
		})
	default:
		status, err := strconv.Atoi(s.mode)
		if err != nil {
			s.t.Errorf("unknown fake mode %q", s.mode)
			http.Error(w, "bad fake mode", http.StatusInternalServerError)
			return
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
}

// strictTranscript decodes the single transcript request the server received.
func (s *fakeRecordingServer) strictTranscript(t *testing.T) transcriptRequestWire {
	t.Helper()
	bodies := s.snapshot().transcripts
	require.Len(t, bodies, 1, "exactly one transcript request")
	var req transcriptRequestWire
	dec := json.NewDecoder(bytes.NewReader(bodies[0]))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&req))
	return req
}

// newTranscriptFixture is a media fixture whose file holds transcript text.
func newTranscriptFixture(t *testing.T, name, content string) *mediaFixture {
	t.Helper()
	f := newMediaFixture(t, name, 1)
	require.NoError(t, os.WriteFile(f.media, []byte(content), 0o644))
	f.content = []byte(content)
	return f
}

var transcriptCases = []struct {
	file        string
	content     string
	format      string
	contentType string
}{
	{"standup.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nPerson A: good morning\n", "vtt", "text/vtt"},
	{"retro.srt", "1\n00:00:00,000 --> 00:00:02,000\nPerson B: let's start\n", "srt", "application/x-subrip"},
}

// TestImportTranscript_CreatesDiscussion: a .vtt or .srt file becomes a
// Discussion directly, like audio — its text, format, title and --date reach
// the transcript route, and nothing goes through LFS or git.
// Failure prevented: a transcript filed as a team context document, which the
// server indexed and never turned into a Discussion, or a Discussion dated by
// the day it was imported instead of the day it happened.
func TestImportTranscript_CreatesDiscussion(t *testing.T) {
	for _, tt := range transcriptCases {
		t.Run(tt.file, func(t *testing.T) {
			f := newTranscriptFixture(t, tt.file, tt.content)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

			stdout, stderr, err := f.importMedia(false, false)
			require.NoError(t, err)
			assert.Empty(t, stderr)

			req := srv.strictTranscript(t)
			assert.Equal(t, tt.content, req.Content, "the transcript text, verbatim")
			assert.Equal(t, tt.format, req.SourceFormat)
			slug := strings.TrimSuffix(tt.file, filepath.Ext(tt.file))
			assert.Equal(t, slug, req.Title)
			require.NotNil(t, req.RecordedAt, "--date dates the Discussion")
			assert.True(t, req.RecordedAt.Equal(time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)), "recorded_at = %s", req.RecordedAt)
			assert.Empty(t, req.Participants)

			snap := srv.snapshot()
			hdr := snap.transcriptHdrs[0]
			assert.Equal(t, "Bearer test-access-token", hdr.Get("Authorization"))
			assert.NotEmpty(t, hdr.Get("Idempotency-Key"))
			assert.Empty(t, snap.uploads, "a transcript is not a media upload")
			assert.Empty(t, snap.notifies, "a transcript is not a team context document")
			f.assertNoDocumentPath(t)
			assert.Contains(t, stdout, "Imported: "+slug+"\nRecording: "+fakeRecordingID+"\n")
		})
	}
}

// TestImportTranscript_WithoutDateLetsServerDate: no --date sends no
// recorded_at, so a downloaded file's mtime never dates the Discussion.
// Failure prevented: every imported talk dated the afternoon it was downloaded.
func TestImportTranscript_WithoutDateLetsServerDate(t *testing.T) {
	f := newTranscriptFixture(t, transcriptCases[0].file, transcriptCases[0].content)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

	importFlags = importFlagsT{team: importRetryTeamID}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", false, "")
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	require.NoError(t, runImport(cmd, []string{f.media}))

	assert.Nil(t, srv.strictTranscript(t).RecordedAt)
}

// TestImportTranscript_SecondImportIsRecognized: the same transcript imports
// once unless --force, as audio does.
// Failure prevented: a retried command creating a duplicate Discussion.
func TestImportTranscript_SecondImportIsRecognized(t *testing.T) {
	f := newTranscriptFixture(t, transcriptCases[0].file, transcriptCases[0].content)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

	_, _, err := f.importMedia(false, false)
	require.NoError(t, err)

	stdout, _, err := f.importMedia(false, false)
	require.NoError(t, err)
	assert.Equal(t, "Already imported (id: "+fakeRecordingID+"). Use --force to reimport.\n", stdout)
	assert.Len(t, srv.snapshot().transcripts, 1, "a recognized import sends nothing")

	_, _, err = f.importMedia(true, false)
	require.NoError(t, err)
	snap := srv.snapshot()
	require.Len(t, snap.transcripts, 2, "--force imports again")
	assert.NotEqual(t, snap.transcriptHdrs[0].Get("Idempotency-Key"), snap.transcriptHdrs[1].Get("Idempotency-Key"))
}

// TestImportTranscript_UnsupportedServerFallsBackToDocumentPath: a server
// without the transcript route gets the document path, declared with the
// content type its dispatcher routes to the import-transcript workflow.
// Failure prevented: the fallback sending "text/plain; charset=utf-8", which
// the dispatcher matches to nothing, so the import is silently skipped.
func TestImportTranscript_UnsupportedServerFallsBackToDocumentPath(t *testing.T) {
	for _, tt := range transcriptCases {
		t.Run(tt.file, func(t *testing.T) {
			f := newTranscriptFixture(t, tt.file, tt.content)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "unsupported")

			_, stderr, err := f.importMedia(false, false)
			require.NoError(t, err)

			assert.Len(t, srv.snapshot().transcripts, 1, "the transcript route is tried first")
			assert.Contains(t, stderr, "does not accept transcript discussions")
			assert.Positive(t, f.uploads.Load(), "the transcript goes through LFS")
			meta := srv.strictNotify(t)
			assert.Equal(t, tt.file, meta.SourceFilename)
			assert.Equal(t, tt.contentType, meta.ContentType, "the content type the server routes transcripts on")
			assert.Equal(t, "sha256:"+f.sha256Hex(), meta.SourceOID)
		})
	}
}

// TestImportTranscript_RefusalShowsServerMessage: a deliberate refusal (a
// transcript over the size cap, a non-member) reaches the person verbatim and
// nothing falls back.
// Failure prevented: a refused import quietly retried as a document.
func TestImportTranscript_RefusalShowsServerMessage(t *testing.T) {
	tests := []struct {
		status int
		code   string
		msg    string
	}{
		{http.StatusRequestEntityTooLarge, "transcript_too_large", "transcript exceeds the size limit"},
		{http.StatusForbidden, "forbidden", "you are not a member of this team"},
		{http.StatusNotFound, "not_found", "team not found"},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			f := newTranscriptFixture(t, transcriptCases[0].file, transcriptCases[0].content)
			srv := newFakeRecordingServer(t, f.importRetryFixture, strconv.Itoa(tt.status))
			srv.set(func(s *fakeRecordingServer) { s.errCode, s.errMsg = tt.code, tt.msg })

			_, stderr, err := f.importMedia(false, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d %s: %s", tt.status, tt.code, tt.msg))
			assert.NotContains(t, stderr, "does not accept transcript discussions")
			assert.Empty(t, srv.snapshot().notifies)
			f.assertNoDocumentPath(t)
		})
	}
}

// TestImportTranscript_RejectsNonUTF8: a UTF-16 export fails locally instead
// of arriving as replacement characters.
// Failure prevented: a Discussion whose transcript is unreadable garbage.
func TestImportTranscript_RejectsNonUTF8(t *testing.T) {
	f := newTranscriptFixture(t, "standup.vtt", "\xff\xfeW\x00E\x00B\x00")
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

	_, _, err := f.importMedia(false, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not UTF-8")
	assert.Empty(t, srv.snapshot().transcripts)
	f.assertNoDocumentPath(t)
}

// TestTranscriptImportRouting pins which files take the transcript route.
func TestTranscriptImportRouting(t *testing.T) {
	for _, tt := range transcriptCases {
		format, ok := transcriptImportFormat("Keynote" + strings.ToUpper(filepath.Ext(tt.file)))
		assert.True(t, ok, "extension match is case-insensitive: %s", tt.file)
		assert.Equal(t, tt.format, format)
		assert.Equal(t, tt.contentType, detectContentType(tt.file, nil))
	}
	_, ok := transcriptImportFormat("notes.txt")
	assert.False(t, ok, ".txt stays on the document path")
}
