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

const (
	queryPath = "/api/v1/query"
)

// QueryRequest represents the POST /api/v1/query request body.
type QueryRequest struct {
	Query     string   `json:"query"`
	Mode      string   `json:"mode,omitempty"`       // "hybrid", "knn", "bm25" (default: hybrid)
	K         int      `json:"k,omitempty"`           // number of results (default: 10, max: 100)
	Teams     []string `json:"teams"`                 // team IDs to search team-context indexes
	Repos     []string `json:"repos"`                 // repo IDs to search ledger indexes
	AgentID   string   `json:"agent_id,omitempty"`    // querying agent instance (e.g. "Oxa7b3")
	AgentType string   `json:"agent_type,omitempty"`  // querying agent type (e.g. "claude-code")
}

// QueryResponse represents the POST /api/v1/query response.
type QueryResponse struct {
	Results   []QueryResult `json:"results"`
	LatencyMs QueryLatency  `json:"latency_ms"`
}

// QueryResult is a single search result.
type QueryResult struct {
	Score      float64 `json:"score"`
	Text       string  `json:"text"`
	DocType    string  `json:"doc_type"`
	FilePath   string  `json:"file_path"`
	SourceType string  `json:"source_type"`
	SourceID   string  `json:"source_id"`
	CreatedAt  string  `json:"created_at,omitempty"`
}

// QueryLatency tracks latency of sub-operations.
type QueryLatency struct {
	Embed  int64 `json:"embed"`
	Search int64 `json:"search"`
	Total  int64 `json:"total"`
}

// Query calls POST /api/v1/query to perform semantic search over team context and ledger data.
// Requires authentication. Returns search results with relevance scores.
func (c *RepoClient) Query(req *QueryRequest) (*QueryResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + queryPath

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

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		errMsg := strings.TrimSpace(string(responseBody))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	logger.LogHTTPResponseBody(string(responseBody))

	var queryResp QueryResponse
	if err := json.Unmarshal(responseBody, &queryResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &queryResp, nil
}
