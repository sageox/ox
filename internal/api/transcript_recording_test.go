package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateTranscriptRecording_PostsTranscript: the request reaches the team's
// transcript route with its idempotency key and body, and the new recording
// comes back.
// Failure prevented: a transcript Discussion created under the wrong route, or
// a retried request the server cannot recognize as a replay.
func TestCreateTranscriptRecording_PostsTranscript(t *testing.T) {
	recordedAt := time.Date(2024, 4, 20, 0, 0, 0, 0, time.UTC)
	var got TranscriptRecordingRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/teams/team_1/recordings/transcript", r.URL.Path)
		assert.Equal(t, "key-1", r.Header.Get("Idempotency-Key"))
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"rec_1","title":"keynote","status":"ready"}`)
	}))
	defer srv.Close()

	resp, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").CreateTranscriptRecording(t.Context(), ContextTypeTeam, "team_1", "key-1",
		&TranscriptRecordingRequest{Title: "keynote", Content: "WEBVTT\n", SourceFormat: "vtt", RecordedAt: &recordedAt})

	require.NoError(t, err)
	assert.Equal(t, &TranscriptRecordingResponse{ID: "rec_1", Title: "keynote", Status: "ready"}, resp)
	require.NotNil(t, got.RecordedAt)
	assert.True(t, recordedAt.Equal(*got.RecordedAt))
	assert.Equal(t, "vtt", got.SourceFormat)
}

// TestCreateTranscriptRecording_Errors maps each server answer to what the CLI
// decides: fall back to the document path or show the server's words.
// Failure prevented: an older server's 404 failing every transcript import, a
// deliberate refusal silently retried as a document, or an unusable answer
// reported as an imported Discussion.
func TestCreateTranscriptRecording_Errors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantMsg string
	}{
		{"unrouted 404 means no transcript route", http.StatusNotFound, "404 page not found\n", ErrRecordingUploadUnsupported, ""},
		{"deliberate 404 keeps the server's words", http.StatusNotFound, `{"error":{"code":"not_found","message":"team not found"}}`, nil, "HTTP 404 not_found: team not found"},
		{"413 keeps the server's words", http.StatusRequestEntityTooLarge, `{"error":{"code":"transcript_too_large","message":"too large"}}`, nil, "HTTP 413 transcript_too_large: too large"},
		{"undecodable answer", http.StatusCreated, "not json", nil, "decode transcript recording response"},
		{"answer without an id", http.StatusCreated, `{"status":"ready"}`, nil, "transcript recording response has no id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			resp, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").
				CreateTranscriptRecording(t.Context(), ContextTypeTeam, "team_1", "key-1", &TranscriptRecordingRequest{Content: "WEBVTT\n"})

			require.Error(t, err)
			assert.Nil(t, resp)
			if tt.wantIs != nil {
				assert.True(t, errors.Is(err, tt.wantIs), "got %v", err)
				return
			}
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// TestCreateTranscriptRecording_ClientFailures: a bad context or an
// unreachable server fails before anything is created.
func TestCreateTranscriptRecording_ClientFailures(t *testing.T) {
	client := NewRepoClientWithEndpoint("http://127.0.0.1:1")
	_, err := client.CreateTranscriptRecording(t.Context(), ContextTypeTeam, "", "key-1", &TranscriptRecordingRequest{})
	require.ErrorContains(t, err, "context ID is required")

	_, err = client.CreateTranscriptRecording(t.Context(), ContextTypeTeam, "team_1", "key-1", &TranscriptRecordingRequest{})
	require.Error(t, err)
}
