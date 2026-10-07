package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestRecordingUpload_Errors maps each server answer to what the CLI
// needs to decide: fall back, show the server's words, or re-login.
// Failure prevented: an older server's 404 failing every media import, a
// "team not found" 404 silently retried through the document path, or a
// 403/409 reaching the person as a generic error.
func TestRequestRecordingUpload_Errors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantMsg string
	}{
		{"unrouted 404 means no upload route", http.StatusNotFound, "404 page not found\n", ErrRecordingUploadUnsupported, ""},
		{"unrouted 404 with an empty body", http.StatusNotFound, "", ErrRecordingUploadUnsupported, ""},
		{"404 with a flat body is not api-go's envelope", http.StatusNotFound, `{"error":"not found"}`, ErrRecordingUploadUnsupported, ""},
		{"deliberate 404 keeps the server's words", http.StatusNotFound, `{"error":{"code":"not_found","message":"team not found"}}`, nil, "HTTP 404 not_found: team not found"},
		{"nested envelope keeps code and message", http.StatusConflict, `{"error":{"code":"idempotency_key_reused","message":"key was used with a different body"}}`, nil, "HTTP 409 idempotency_key_reused: key was used with a different body"},
		{"flat envelope", http.StatusForbidden, `{"success":false,"error":"not a member of this team"}`, nil, "HTTP 403: not a member of this team"},
		{"plain text body", http.StatusBadGateway, "upstream unavailable\n", nil, "HTTP 502: upstream unavailable"},
		{"401 is the shared unauthorized error", http.StatusUnauthorized, "", ErrUnauthorized, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").
				RequestRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "", &RecordingUploadRequest{Filename: "a.m4a", Size: 1})

			require.Error(t, err)
			if tt.wantIs != nil {
				assert.True(t, errors.Is(err, tt.wantIs), "got %v", err)
				return
			}
			var statusErr *HTTPStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, tt.status, statusErr.StatusCode)
			assert.Equal(t, tt.wantMsg, err.Error())
		})
	}
}

// TestRequestRecordingUpload_IdempotencyKey: the key rides the header when
// given and is absent otherwise.
// Failure prevented: a re-presign the server treats as a new upload (a second
// recording), or an empty Idempotency-Key the server rejects.
func TestRequestRecordingUpload_IdempotencyKey(t *testing.T) {
	var got []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"recording_id":"rec_1","upload_id":"upl_1","upload_url":"https://bucket.example.test/o"}`)
	}))
	defer srv.Close()
	client := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")
	req := &RecordingUploadRequest{Filename: "a.m4a", Size: 1}

	resp, err := client.RequestRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "0199a2b4-0000-7000-8000-000000000001", req)
	require.NoError(t, err)
	assert.Equal(t, "upl_1", resp.UploadID)
	_, err = client.RequestRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "", req)
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, "0199a2b4-0000-7000-8000-000000000001", got[0].Get("Idempotency-Key"))
	_, present := got[1]["Idempotency-Key"]
	assert.False(t, present, "no key, no header")
}

// TestRequestRecordingUpload_RejectsUnusableAnswer: a 2xx without anywhere to
// put the bytes is an error, not a silent no-op import.
func TestRequestRecordingUpload_RejectsUnusableAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"recording_id":"rec_1"}`)
	}))
	defer srv.Close()

	_, err := NewRepoClientWithEndpoint(srv.URL).RequestRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "", &RecordingUploadRequest{})
	require.ErrorContains(t, err, "neither upload_url nor multipart")
}

// TestConfirmRecordingUpload_BodyOnlyWhenGiven: a nil request sends no body.
// Failure prevented: an older server, which issued no upload_id, receiving a
// body it was never built to read.
func TestConfirmRecordingUpload_BodyOnlyWhenGiven(t *testing.T) {
	var bodies []string
	var contentTypes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		assert.Equal(t, "/api/v1/teams/team_1/recordings/rec_1/confirm", r.URL.Path)
		_, _ = io.WriteString(w, `{"id":"rec_1","status":"processing"}`)
	}))
	defer srv.Close()
	client := NewRepoClientWithEndpoint(srv.URL)

	resp, err := client.ConfirmRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "rec_1", nil)
	require.NoError(t, err)
	assert.Equal(t, "processing", resp.Status)
	_, err = client.ConfirmRecordingUpload(t.Context(), ContextTypeTeam, "team_1", "rec_1", &ConfirmRecordingUploadRequest{
		UploadID: "upl_1", Parts: []CompletedPart{{PartNumber: 1, ETag: `"e1"`}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"", `{"upload_id":"upl_1","parts":[{"part_number":1,"etag":"\"e1\""}]}`}, bodies)
	assert.Equal(t, []string{"", "application/json"}, contentTypes)
}
