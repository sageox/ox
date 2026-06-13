package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// Recording context types. The backend mounts the same recording routes under
// /api/v1/teams/{team_id}/recordings and /api/v1/kb/{kb_id}/recordings; every
// recording client method takes one of these plus the matching context ID so
// team and Knowledge Bubble imports share a single code path.
const (
	ContextTypeTeam = "team"
	ContextTypeKB   = "kb"
)

// recordingsBase returns the recordings collection path for a context
// ("/api/v1/teams/{id}/recordings" or "/api/v1/kb/{id}/recordings").
// Validated here, once, so callers can't accidentally fabricate a path for an
// unknown context type.
func recordingsBase(contextType, contextID string) (string, error) {
	if contextID == "" {
		return "", fmt.Errorf("context ID is required")
	}
	switch contextType {
	case ContextTypeTeam:
		return fmt.Sprintf("/api/v1/teams/%s/recordings", contextID), nil
	case ContextTypeKB:
		return fmt.Sprintf("/api/v1/kb/%s/recordings", contextID), nil
	default:
		return "", fmt.Errorf("invalid context type %q (want %q or %q)", contextType, ContextTypeTeam, ContextTypeKB)
	}
}

// ImportVideoURLRequest represents the POST request to import a video by URL
type ImportVideoURLRequest struct {
	URL   string `json:"source_url"`
	Title string `json:"title,omitempty"`
}

// ImportVideoURLResponse represents the response from importing a video URL.
// import_id and recording_id are the same value — a stable ID assigned upfront
// that can be used with --status immediately, before processing completes.
type ImportVideoURLResponse struct {
	ImportID    string `json:"import_id"`
	RecordingID string `json:"recording_id"`
	Status      string `json:"status"`
	Title       string `json:"title,omitempty"`
}

// VideoStatusResponse represents the status of a single recording
type VideoStatusResponse struct {
	ID              string                    `json:"id"`
	Title           string                    `json:"title"`
	Status          string                    `json:"status"`
	MimeType        string                    `json:"mime_type,omitempty"`
	Duration        *float64                  `json:"duration,omitempty"`
	ProcessingSteps map[string]map[string]any `json:"processing_steps,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	CompletedAt     *time.Time                `json:"completed_at,omitempty"`
}

// ListVideosResponse represents the paginated list of recordings
type ListVideosResponse struct {
	Recordings []RecordingListItem `json:"recordings"`
	Pagination PaginationResponse  `json:"pagination"`
}

// RecordingListItem represents a single recording in a list response
type RecordingListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	MimeType  string    `json:"mime_type,omitempty"`
	Duration  *float64  `json:"duration,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PaginationResponse contains pagination metadata for list endpoints
type PaginationResponse struct {
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

// ImportVideoURL calls POST /api/v1/{teams|kb}/{id}/recordings/import/url
// Returns nil, nil if the endpoint returns 404 (graceful degradation)
func (c *RepoClient) ImportVideoURL(contextType, contextID string, req *ImportVideoURLRequest) (*ImportVideoURLResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base + "/import/url"

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	// handle 404 gracefully - endpoint not yet deployed
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// handle non-2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	logger.LogHTTPResponseBody(string(bodyBytes))

	var importResp ImportVideoURLResponse
	if err := json.Unmarshal(bodyBytes, &importResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &importResp, nil
}

// GetVideoStatus calls GET /api/v1/{teams|kb}/{id}/recordings/{recording_id}
// Returns nil, nil on 404 (graceful degradation); all other errors are returned.
func (c *RepoClient) GetVideoStatus(contextType, contextID, recordingID string) (*VideoStatusResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base + "/" + recordingID

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("get video status: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	// handle 404 gracefully — recording not found
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read video status response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("get video status: HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("get video status: HTTP %d: %s", resp.StatusCode, errMsg)
	}

	var statusResp VideoStatusResponse
	if err := json.Unmarshal(bodyBytes, &statusResp); err != nil {
		return nil, fmt.Errorf("decode video status: %w", err)
	}

	return &statusResp, nil
}

// ListVideos calls GET /api/v1/{teams|kb}/{id}/recordings with pagination
// Returns nil, nil on 404 (graceful degradation)
func (c *RepoClient) ListVideos(contextType, contextID string, limit, offset int) (*ListVideosResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base
	reqURL += fmt.Sprintf("?limit=%d&offset=%d", limit, offset)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("list videos: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	// handle 404 gracefully — endpoint not deployed
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read list videos response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("list videos: HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("list videos: HTTP %d: %s", resp.StatusCode, errMsg)
	}

	var listResp ListVideosResponse
	if err := json.Unmarshal(bodyBytes, &listResp); err != nil {
		return nil, fmt.Errorf("decode list videos: %w", err)
	}

	return &listResp, nil
}

// --- bulk recording-file import (POST …/recordings/import) ---

// ImportRecordingFileRequest is the metadata sent to
// POST /api/v1/{teams|kb}/{id}/recordings/import. The server validates the
// format, creates a recording row with source="import", and returns a
// presigned PUT URL for the file content.
type ImportRecordingFileRequest struct {
	Filename    string     `json:"filename"`
	ContentType string     `json:"content_type,omitempty"`
	Size        int64      `json:"size"`
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description,omitempty"`
	RecordedAt  *time.Time `json:"recorded_at,omitempty"` // back-dates historical imports
}

// importRecordingInitResponse is the wire shape returned by the import-init
// POST. Only the fields the client acts on are decoded; the server may send
// more (status, created_at, upload.fields, …).
type importRecordingInitResponse struct {
	RecordingID string `json:"recording_id"`
	Upload      struct {
		UploadURL string    `json:"upload_url"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"upload"`
	Status string `json:"status"`
}

// completeRecordingRequest is the body for POST …/recordings/{rec_id}/complete.
// Imports are a single presigned PUT, never chunked, so chunk_count is omitted —
// the server branches on metadata.source="import".
type completeRecordingRequest struct {
	TotalSize int64 `json:"total_size"`
}

// ImportRecordingFileResult is the final state after the 3-step import flow.
type ImportRecordingFileResult struct {
	RecordingID string `json:"recording_id"`
	Status      string `json:"status"`
}

// ImportRecordingFile imports a pre-existing local media file as a recording
// using the 3-step flow:
//
//  1. POST {base}/import with file metadata → recording_id + presigned PUT URL
//  2. PUT the file bytes to the presigned URL (streamed from disk)
//  3. POST {base}/{recording_id}/complete to finalize and kick off processing
//
// Returns nil, nil if step 1 returns 404 (endpoint not yet deployed —
// graceful degradation, matching ImportVideoURL). meta.Size is always taken
// from the file on disk so the presigned request can never disagree with the
// bytes actually uploaded.
func (c *RepoClient) ImportRecordingFile(ctx context.Context, contextType, contextID, filePath string, meta ImportRecordingFileRequest) (*ImportRecordingFileResult, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat media file: %w", err)
	}
	meta.Size = info.Size()
	if meta.Size <= 0 {
		return nil, fmt.Errorf("media file is empty: %s", filePath)
	}

	// step 1: initialize the import, get the presigned PUT URL
	initURL := strings.TrimSuffix(c.baseURL, "/") + base + "/import"
	status, respBody, err := c.postJSON(ctx, initURL, meta)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, httpStatusError(status, initURL, respBody)
	}

	var initResp importRecordingInitResponse
	if err := json.Unmarshal(respBody, &initResp); err != nil {
		return nil, fmt.Errorf("decode import response: %w", err)
	}
	if initResp.RecordingID == "" || initResp.Upload.UploadURL == "" {
		return nil, fmt.Errorf("import response missing recording_id or upload_url")
	}

	// step 2: stream the file to the presigned URL
	if err := c.uploadFilePut(ctx, initResp.Upload.UploadURL, filePath, meta.ContentType, meta.Size); err != nil {
		return nil, fmt.Errorf("upload media file: %w", err)
	}

	// step 3: finalize — this flips status and starts server-side processing
	completeURL := strings.TrimSuffix(c.baseURL, "/") + base + "/" + initResp.RecordingID + "/complete"
	status, respBody, err = c.postJSON(ctx, completeURL, completeRecordingRequest{TotalSize: meta.Size})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, httpStatusError(status, completeURL, respBody)
	}

	result := &ImportRecordingFileResult{RecordingID: initResp.RecordingID, Status: initResp.Status}
	// the complete response carries the authoritative post-finalize status;
	// tolerate a missing/empty body since the recording ID is already known
	var completeResp ImportRecordingFileResult
	if err := json.Unmarshal(respBody, &completeResp); err == nil && completeResp.Status != "" {
		result.Status = completeResp.Status
	}
	return result, nil
}

// postJSON sends an authenticated JSON POST and returns the status code and
// raw response body. Shared by the import-init and complete legs.
func (c *RepoClient) postJSON(ctx context.Context, reqURL string, body any) (int, []byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, "POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return 0, nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// uploadFilePut streams a local file to a presigned PUT URL. The file is never
// read fully into memory — the open handle is the request body and
// ContentLength is set explicitly (required because net/http cannot infer the
// length of an *os.File body).
func (c *RepoClient) uploadFilePut(ctx context.Context, uploadURL, filePath, contentType string, size int64) error {
	f, err := os.Open(filePath) //nolint:gosec // G304 — path is the user's own import argument
	if err != nil {
		return fmt.Errorf("open media file: %w", err)
	}
	defer f.Close()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, f)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	httpReq.ContentLength = size
	if contentType != "" {
		// presigned URLs may sign the Content-Type; it must match what the
		// server was told at import-init time
		httpReq.Header.Set("Content-Type", contentType)
	}

	logger.LogHTTPRequest("PUT", uploadURL)
	start := time.Now()

	// media files routinely exceed the client's default 10s timeout — use a
	// dedicated transport-default client and rely on ctx for cancellation
	uploadClient := &http.Client{}
	resp, err := uploadClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("PUT", uploadURL, err, duration)
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("PUT", uploadURL, resp.StatusCode, duration)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return httpStatusError(resp.StatusCode, uploadURL, respBody)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// httpStatusError formats a non-2xx response into the same error shape the
// other video methods produce.
func httpStatusError(status int, reqURL string, body []byte) error {
	errMsg := strings.TrimSpace(string(body))
	if errMsg == "" {
		return fmt.Errorf("HTTP %d from %s", status, reqURL)
	}
	return fmt.Errorf("HTTP %d from %s: %s", status, reqURL, errMsg)
}
