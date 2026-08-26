package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// Recording context types. Recordings are conversation content and live in
// team context only — Knowledge Bubbles are curated syntheses and take no
// recordings (ox ADR-028; the /api/v1/kb/{kb_id}/recordings client path was
// removed under epic ox-nsf7). The contextType parameter survives on the
// client methods so call sites stay explicit about what they target.
const (
	ContextTypeTeam = "team"
)

// recordingsBase returns the recordings collection path for a context
// ("/api/v1/teams/{id}/recordings"). Validated here, once, so callers can't
// accidentally fabricate a path for an unknown context type.
func recordingsBase(contextType, contextID string) (string, error) {
	if contextID == "" {
		return "", fmt.Errorf("context ID is required")
	}
	switch contextType {
	case ContextTypeTeam:
		return fmt.Sprintf("/api/v1/teams/%s/recordings", contextID), nil
	default:
		return "", fmt.Errorf("invalid context type %q (want %q)", contextType, ContextTypeTeam)
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
