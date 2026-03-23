package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/useragent"
	ss "github.com/sageox/ox/pkg/sessionsummary"
)

const (
	summarizePath    = "/api/v1/session/summarize"
	summarizeTimeout = 30 * time.Second
)

// SummaryPromptGuidelines contains the shared guidelines for session summarization.
// Delegates to pkg/sessionsummary for the canonical prompt.
const SummaryPromptGuidelines = ss.SummaryPromptGuidelines

// Type aliases — all consumers continue using session.SummarizeResponse etc.
type (
	SummarizeResponse = ss.SummarizeResponse
	SummarizeRequest  = ss.SummarizeRequest
	SummarizeEntry    = ss.SummarizeEntry
	AhaMoment         = ss.AhaMoment
	SageoxInsight     = ss.SageoxInsight
	ChapterSummary    = ss.ChapterSummary
	FileSummary       = ss.FileSummary
	AgentSummary      = ss.AgentSummary
	Decision          = ss.Decision
	ActionItem        = ss.ActionItem
	OpenQuestion      = ss.OpenQuestion
	TechnicalContext  = ss.TechnicalContext
)

// Summarize calls the SageOx API to generate an LLM summary of a session.
// If endpointURL is non-empty, uses that endpoint for auth and API calls;
// otherwise falls back to the default endpoint.
// Returns nil if summarization fails (non-critical feature).
func Summarize(entries []Entry, agentID, agentType, model, endpointURL string) (*SummarizeResponse, error) {
	var token *auth.StoredToken
	var err error
	if endpointURL != "" {
		token, err = auth.EnsureValidTokenForEndpoint(endpointURL, 300)
	} else {
		token, err = auth.EnsureValidToken(300)
	}
	if err != nil || token == nil {
		return nil, fmt.Errorf("authentication required for summarization")
	}

	// prepare request with limited entries
	req := buildSummarizeRequest(entries, agentID, agentType, model)

	// make API call
	client := &http.Client{Timeout: summarizeTimeout}
	var reqURL string
	if endpointURL != "" {
		reqURL = endpointURL + summarizePath
	} else {
		reqURL = endpoint.Get() + summarizePath
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := useragent.NewRequest(context.Background(), "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result SummarizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// buildSummarizeRequest converts entries to the API request format.
// Delegates filtering and construction to pkg/sessionsummary.
func buildSummarizeRequest(entries []Entry, agentID, agentType, model string) *SummarizeRequest {
	return ss.BuildSummarizeRequest(entriesToPkg(entries), agentID, agentType, model)
}

// FilterForSummarization removes low-value tool entries from session data.
// Delegates to pkg/sessionsummary.
func FilterForSummarization(entries []Entry) []Entry {
	return pkgToEntries(ss.FilterForSummarization(entriesToPkg(entries)))
}

// BuildSummaryPrompt builds a prompt for the calling agent to generate a session summary.
// Delegates to pkg/sessionsummary.
func BuildSummaryPrompt(entries []Entry, rawPath, ledgerSessionDir string) string {
	return ss.BuildSummaryPrompt(entriesToPkg(entries), rawPath, ledgerSessionDir)
}

// LocalSummary generates a simple local summary without API call.
// Delegates to pkg/sessionsummary.
func LocalSummary(entries []Entry) string {
	return ss.LocalSummary(entriesToPkg(entries))
}

// entriesToPkg converts internal SessionEntry slice to pkg Entry slice.
func entriesToPkg(entries []Entry) []ss.Entry {
	out := make([]ss.Entry, len(entries))
	for i, e := range entries {
		out[i] = ss.Entry{
			Timestamp:  e.Timestamp,
			Type:       string(e.Type),
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
		}
	}
	return out
}

// pkgToEntries converts pkg Entry slice back to internal SessionEntry slice.
func pkgToEntries(entries []ss.Entry) []Entry {
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = Entry{
			Timestamp:  e.Timestamp,
			Type:       SessionEntryType(e.Type),
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
		}
	}
	return out
}
