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

const memoryDistillPath = "/api/v1/teams/%s/memory/distill"

// DistillRequest is the request body for POST /api/v1/teams/<teamID>/memory/distill.
type DistillRequest struct {
	Observations []DistillObservation `json:"observations"`
	DateRange    [2]string            `json:"date_range"` // [start, end] RFC3339
}

// DistillObservation is a single observation in a distill request.
type DistillObservation struct {
	Content    string `json:"content"`
	RecordedAt string `json:"recorded_at"` // RFC3339
}

// DistillResponse is the response from POST /api/v1/teams/<teamID>/memory/distill.
type DistillResponse struct {
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"`
}

// DistillMemory calls POST /api/v1/teams/<teamID>/memory/distill to run
// server-side LLM distillation of accumulated observations.
func (c *RepoClient) DistillMemory(teamID string, req *DistillRequest) (*DistillResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(memoryDistillPath, teamID)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	logger.LogHTTPResponseBody(string(bodyBytes))

	var distillResp DistillResponse
	if err := json.Unmarshal(bodyBytes, &distillResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &distillResp, nil
}
