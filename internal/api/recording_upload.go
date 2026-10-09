package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// ErrRecordingUploadUnsupported means POST …/recordings/upload is not routed
// on this server: it predates the route, or runs without the upload handler
// mounted. Callers fall back to the team context document path so those
// servers keep accepting media imports. A deliberate 404 (team or recording
// not found) is an HTTPStatusError instead and must not fall back.
var ErrRecordingUploadUnsupported = errors.New("server has no recording upload route")

// maxErrorBodyBytes bounds how much of an error response is read. The server's
// error envelope is a few hundred bytes; anything larger is not a message a
// person should see verbatim.
const maxErrorBodyBytes = 4096

// RecordingUploadRequest is the POST /api/v1/teams/{team_id}/recordings/upload body.
type RecordingUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Title       string `json:"title,omitempty"`
	// RecordedAt dates the recording when the caller knows it (ox import --date).
	// Omitted otherwise so the server reads container metadata instead.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// ContentHash is the sha256 hex of the file, the server's client-side
	// dedupe key for "already ingested".
	ContentHash string `json:"content_hash,omitempty"`
	// ChecksumSHA256 is the base64 SHA-256 of the whole file. The server signs
	// it into a single PUT, so storage rejects bytes that differ from the ones
	// hashed; it is ignored when the server chooses multipart.
	ChecksumSHA256 string `json:"checksum_sha256,omitempty"`
	// Multipart says this client can upload in parts. The server still decides
	// by size; without it the answer is always a single PUT.
	Multipart bool `json:"multipart,omitempty"`
}

// RecordingUploadResponse tells the client where to put the bytes: either a
// single presigned PUT (UploadURL) or, for large files, one presigned PUT per
// part (Multipart). Servers that predate multipart send UploadURL only and no
// UploadID.
type RecordingUploadResponse struct {
	RecordingID string `json:"recording_id"`
	UploadURL   string `json:"upload_url,omitempty"`
	// UploadHeaders names every header the single PUT must carry — the ones
	// signed into UploadURL. Absent from servers that predate it.
	UploadHeaders map[string]string `json:"upload_headers,omitempty"`
	UploadID      string            `json:"upload_id,omitempty"`
	Multipart     *MultipartUpload  `json:"multipart,omitempty"`
}

// MultipartUpload is the server's part plan for a large file. Part n covers
// bytes [(n-1)*PartSize, min(n*PartSize, size)).
type MultipartUpload struct {
	PartSize  int64           `json:"part_size"`
	ExpiresAt time.Time       `json:"expires_at"`
	Parts     []MultipartPart `json:"parts"`
}

// MultipartPart is one presigned part URL and the headers signed into it.
type MultipartPart struct {
	PartNumber    int               `json:"part_number"`
	UploadURL     string            `json:"upload_url"`
	UploadHeaders map[string]string `json:"upload_headers,omitempty"`
}

// CompletedPart is a part the storage service accepted, identified by the
// ETag it returned. The server needs every one to complete the upload.
type CompletedPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// ConfirmRecordingUploadRequest is the optional POST …/recordings/{id}/confirm
// body. Parts is set only for a multipart upload.
type ConfirmRecordingUploadRequest struct {
	UploadID string          `json:"upload_id"`
	Parts    []CompletedPart `json:"parts,omitempty"`
}

// ConfirmRecordingUploadResponse holds the fields ox reads from a confirm. The
// server names the recording "id" on a plain upload and "recording_id" on a
// delivery into an existing recording; both are accepted.
type ConfirmRecordingUploadResponse struct {
	ID          string `json:"id,omitempty"`
	RecordingID string `json:"recording_id,omitempty"`
	Status      string `json:"status"`
}

// HTTPStatusError is a non-2xx answer from a SageOx API route. Code and
// Message are the server's own, unmodified, so a 403 or a 422
// upload_checksum_mismatch reaches the person as the server wrote it.
type HTTPStatusError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPStatusError) Error() string {
	prefix := fmt.Sprintf("HTTP %d", e.StatusCode)
	if e.Code != "" {
		prefix += " " + e.Code
	}
	if e.Message == "" {
		return prefix
	}
	return prefix + ": " + e.Message
}

// RequestRecordingUpload calls POST /api/v1/teams/{team_id}/recordings/upload,
// which creates the recording row and returns where to upload the file.
// Returns ErrRecordingUploadUnsupported on an unrouted 404.
//
// idempotencyKey is sent as the Idempotency-Key header. Repeating the call
// with the same key and the same req re-presigns: the server returns the same
// upload_id with fresh URLs instead of creating a second recording.
func (c *RepoClient) RequestRecordingUpload(ctx context.Context, contextType, contextID, idempotencyKey string, req *RecordingUploadRequest) (*RecordingUploadResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base + "/upload"

	status, body, err := c.postRecordingJSON(ctx, reqURL, idempotencyKey, req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound && !isAPIErrorEnvelope(body) {
		return nil, ErrRecordingUploadUnsupported
	}
	if status < 200 || status >= 300 {
		return nil, newHTTPStatusError(status, body)
	}

	var out RecordingUploadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode recording upload response: %w", err)
	}
	if out.RecordingID == "" {
		return nil, fmt.Errorf("recording upload response has no recording_id")
	}
	if out.UploadURL == "" && out.Multipart == nil {
		return nil, fmt.Errorf("recording upload response has neither upload_url nor multipart")
	}
	return &out, nil
}

// ConfirmRecordingUpload calls POST /api/v1/teams/{team_id}/recordings/{id}/confirm,
// which checks the stored object and starts processing. A nil req sends no
// body: a server that issued no upload_id predates the body and needs none.
func (c *RepoClient) ConfirmRecordingUpload(ctx context.Context, contextType, contextID, recordingID string, req *ConfirmRecordingUploadRequest) (*ConfirmRecordingUploadResponse, error) {
	base, err := recordingsBase(contextType, contextID)
	if err != nil {
		return nil, err
	}
	if recordingID == "" {
		return nil, fmt.Errorf("recording ID is required")
	}
	reqURL := strings.TrimSuffix(c.baseURL, "/") + base + "/" + url.PathEscape(recordingID) + "/confirm"

	var payload any
	if req != nil {
		payload = req
	}
	status, body, err := c.postRecordingJSON(ctx, reqURL, "", payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, newHTTPStatusError(status, body)
	}

	// the recording ID is already known, so an empty or unexpected body is not
	// a failure; status is informational
	var out ConfirmRecordingUploadResponse
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode confirm response: %w", err)
		}
	}
	return &out, nil
}

// postRecordingJSON sends an authenticated POST and returns the status and
// body. A nil payload sends an empty body; an empty idempotencyKey sends no
// Idempotency-Key. The body is never logged: the upload response carries
// presigned URLs, which are bearer credentials.
func (c *RepoClient) postRecordingJSON(ctx context.Context, reqURL, idempotencyKey string, payload any) (int, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	logger.LogHTTPRequest(http.MethodPost, reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, http.MethodPost, reqURL, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError(http.MethodPost, reqURL, err, duration)
		return 0, nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse(http.MethodPost, reqURL, resp.StatusCode, duration)

	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, nil, ErrUnauthorized
	}

	limit := int64(1 << 20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limit = maxErrorBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// isAPIErrorEnvelope reports whether body is api-go's handler error envelope,
// {"error":{"code":…,"message":…}}. Its handlers answer a deliberate 404 that
// way; an unmounted route gets the router's plain-text "404 page not found".
// That difference is the only signal separating "this team was not found"
// from "this server has no such route".
func isAPIErrorEnvelope(body []byte) bool {
	var env struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &env) != nil || env.Error == nil {
		return false
	}
	return env.Error.Code != "" || env.Error.Message != ""
}

// newHTTPStatusError extracts the server's message from either error envelope
// api-go emits ({"error":{"message":…}} from apierror, {"error":"…"} from older
// handlers), falling back to the raw body.
func newHTTPStatusError(status int, body []byte) *HTTPStatusError {
	trimmed := bytes.TrimSpace(body)
	var nested struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(trimmed, &nested) == nil && (nested.Error.Message != "" || nested.Error.Code != "") {
		return &HTTPStatusError{StatusCode: status, Code: nested.Error.Code, Message: nested.Error.Message}
	}
	var flat struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(trimmed, &flat) == nil && flat.Error != "" {
		return &HTTPStatusError{StatusCode: status, Message: flat.Error}
	}
	return &HTTPStatusError{StatusCode: status, Message: string(trimmed)}
}
