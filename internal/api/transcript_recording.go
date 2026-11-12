package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TranscriptRecordingRequest is the POST /api/v1/teams/{team_id}/recordings/transcript
// body: a Discussion made from transcript text alone, with no audio.
type TranscriptRecordingRequest struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
	// SourceFormat is "vtt" or "srt". The server normalizes either to WebVTT
	// and keeps the cue timestamps.
	SourceFormat string `json:"source_format,omitempty"`
	// RecordedAt dates the Discussion when the caller knows it (ox import --date).
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// TranscriptRecordingResponse is the server's answer: the new recording.
type TranscriptRecordingResponse struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// CreateTranscriptRecording calls POST /api/v1/teams/{team_id}/recordings/transcript.
// It returns ErrRecordingUploadUnsupported when the server has no such route,
// so the caller can fall back to the team context document path.
func (c *RepoClient) CreateTranscriptRecording(ctx context.Context, contextType, contextID, idempotencyKey string, req *TranscriptRecordingRequest) (*TranscriptRecordingResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base + "/transcript"

	status, _, body, err := c.postRecordingJSON(ctx, reqURL, idempotencyKey, req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound && !isAPIErrorEnvelope(body) {
		return nil, ErrRecordingUploadUnsupported
	}
	if status < 200 || status >= 300 {
		return nil, newHTTPStatusError(status, body)
	}

	var out TranscriptRecordingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode transcript recording response: %w", err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("transcript recording response has no id")
	}
	return &out, nil
}
