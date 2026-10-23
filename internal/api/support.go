package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"

	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

// MaxSupportRequestBytes is the support API's maximum encoded request size.
const MaxSupportRequestBytes = 32 << 10

const maxSupportResponseBytes = 4096

var supportCaseIDPattern = regexp.MustCompile(`^sup_[0-9a-f]{18}$`)

// SupportCaseRequest is the explicit feedback accepted by the support API.
type SupportCaseRequest struct {
	Description string         `json:"description"`
	Email       string         `json:"email,omitempty"`
	Context     SupportContext `json:"context"`
}

// SupportContext identifies the product surface without attaching diagnostics.
type SupportContext struct {
	Version        int    `json:"version"`
	Surface        string `json:"surface"`
	Intent         string `json:"intent,omitempty"`
	ProductVersion string `json:"productVersion,omitempty"`
	Platform       string `json:"platform,omitempty"`
	Category       string `json:"category,omitempty"`
}

// SubmitSupportCase sends the exact, already-validated JSON bytes to the support
// API. It never retries: the server's email delivery is not idempotent, and a
// lost response does not prove the submission failed. Neither report text nor
// server response text is included in logs or errors.
func (c *RepoClient) SubmitSupportCase(ctx context.Context, payload []byte) (string, error) {
	if len(payload) > MaxSupportRequestBytes {
		return "", fmt.Errorf("support request exceeds %d bytes", MaxSupportRequestBytes)
	}
	if !json.Valid(payload) {
		return "", errors.New("support request must contain valid JSON")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + "/api/v1/support/cases"
	req, err := useragent.NewRequest(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("could not create support request")
	}
	// explicit feedback carries only supplied content and product/platform
	// metadata; the standard API headers also attach ambient workspace identity.
	req.Header.Del(useragent.HeaderRepoID)
	req.Header.Del("X-Orchestrator")
	req.Header.Set("User-Agent", fmt.Sprintf("ox/%s (%s; %s)", version.Version, runtime.GOOS, runtime.GOARCH))
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.noRedirectClient().Do(req)
	if err != nil {
		// transport errors can carry server-controlled text or URLs, so expose
		// only safe classifications while preserving context cancellation.
		if ctx.Err() != nil {
			return "", fmt.Errorf("support submission interrupted: %w", ctx.Err())
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "", errors.New("support submission timed out")
		}
		return "", errors.New("could not reach the support API")
	}
	defer resp.Body.Close()

	if CheckVersionResponse(resp) {
		return "", ErrVersionUnsupported
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPStatusError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSupportResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("support submission interrupted: %w", ctx.Err())
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "", errors.New("support submission timed out")
		}
		return "", errors.New("could not read the support receipt")
	}
	if len(body) > maxSupportResponseBytes {
		return "", errors.New("support receipt exceeds the response limit")
	}
	return decodeSupportReceipt(body)
}

// decodeSupportReceipt accepts exactly one case_id field and no trailing JSON,
// so a proxy response or ambiguous receipt cannot be reported as submitted.
func decodeSupportReceipt(body []byte) (string, error) {
	invalid := errors.New("support API returned an invalid receipt")
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return "", invalid
	}
	key, err := decoder.Token()
	if err != nil || key != "case_id" {
		return "", invalid
	}
	var caseID string
	if err := decoder.Decode(&caseID); err != nil || !supportCaseIDPattern.MatchString(caseID) {
		return "", invalid
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return "", invalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", invalid
	}
	return caseID, nil
}
