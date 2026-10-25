package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/sageox/ox/internal/useragent"
)

// WalkthroughExtractionRequest authorizes bounded decoding, not semantic inference.
type WalkthroughExtractionRequest struct {
	Retry       bool     `json:"retry,omitempty"`
	MaxWidth    int      `json:"max_width,omitempty"`
	Revision    string   `json:"revision"`
	CueFirst    int      `json:"cue_first,omitempty"`
	CueLast     int      `json:"cue_last,omitempty"`
	FromSeconds *float64 `json:"from_seconds,omitempty"`
	ToSeconds   *float64 `json:"to_seconds,omitempty"`
	MaxFrames   int      `json:"max_frames"`
}

type WalkthroughJob struct {
	JobID        string `json:"job_id"`
	Status       string `json:"status"`
	Revision     string `json:"revision,omitempty"`
	SyncRequired bool   `json:"sync_required,omitempty"`
	Error        string `json:"error,omitempty"`
}

// WalkthroughExtraction uses the authenticated team's existing recording API.
// Redirects are disabled so even a compromised API response cannot forward a
// bearer credential to an unrelated object-store or link-provided hostname.
func (c *RepoClient) WalkthroughExtraction(ctx context.Context, team, recording, job string, request *WalkthroughExtractionRequest) (*WalkthroughJob, error) {
	for _, id := range []string{team, recording} {
		if !safeWalkthroughSegment(id) {
			return nil, fmt.Errorf("invalid walkthrough scope")
		}
	}
	if job != "" && !safeWalkthroughSegment(job) {
		return nil, fmt.Errorf("invalid extraction job ID")
	}
	base, err := recordingsBase(ContextTypeTeam, team)
	if err != nil {
		return nil, err
	}
	endpoint := c.baseURL + base + "/" + recording + "/walkthrough/extractions"
	method := http.MethodPost
	var body io.Reader
	if job != "" {
		method = http.MethodGet
		endpoint += "/" + job
	} else {
		if request == nil {
			return nil, fmt.Errorf("extraction request is required")
		}
		hash, e := hex.DecodeString(request.Revision)
		if e != nil || len(hash) != 32 {
			return nil, fmt.Errorf("revision must be a SHA256")
		}
		if request.MaxFrames < 1 || request.MaxFrames > 8 {
			return nil, fmt.Errorf("max_frames must be 1-8")
		}
		if request.MaxWidth < 320 || request.MaxWidth > 4096 {
			return nil, fmt.Errorf("max_width must be 320-4096")
		}
		cues := request.CueFirst != 0 || request.CueLast != 0
		times := request.FromSeconds != nil || request.ToSeconds != nil
		if cues == times {
			return nil, fmt.Errorf("provide exactly one cue or time range")
		}
		if cues && (request.CueFirst < 1 || request.CueLast < request.CueFirst || request.CueLast-request.CueFirst > 49) {
			return nil, fmt.Errorf("cue range must contain 1-50 cues")
		}
		if times {
			if request.FromSeconds == nil || request.ToSeconds == nil {
				return nil, fmt.Errorf("both time bounds required")
			}
			from, to := *request.FromSeconds, *request.ToSeconds
			if math.IsNaN(from) || math.IsNaN(to) || math.IsInf(from, 0) || math.IsInf(to, 0) || from < 0 || to <= from || to-from > 60 {
				return nil, fmt.Errorf("time range must span at most 60 seconds")
			}
		}
		raw, e := json.Marshal(request)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(raw)
	}
	return c.walkthroughJobRequest(ctx, method, endpoint, body)
}

// PrepareWalkthrough upgrades a legacy recording explicitly. Its server budget
// is shared with recovery; ordinary reads never call this paid-work endpoint.
func (c *RepoClient) PrepareWalkthrough(ctx context.Context, team, recording string, retry ...bool) (*WalkthroughJob, error) {
	if !safeWalkthroughSegment(team) || !safeWalkthroughSegment(recording) {
		return nil, fmt.Errorf("invalid walkthrough scope")
	}
	base, err := recordingsBase(ContextTypeTeam, team)
	if err != nil {
		return nil, err
	}
	body := "{}"
	if len(retry) > 0 && retry[0] {
		body = `{"retry":true}`
	}
	return c.walkthroughJobRequest(ctx, http.MethodPost, c.baseURL+base+"/"+recording+"/walkthrough/prepare", strings.NewReader(body))
}

func (c *RepoClient) walkthroughJobRequest(ctx context.Context, method, endpoint string, body io.Reader) (*WalkthroughJob, error) {
	req, err := useragent.NewRequest(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.noRedirectClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("walkthrough extraction: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("walkthrough extraction returned HTTP %d (no automatic retry; check access, revision or server budget)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("extraction receipt exceeds limit")
	}
	var result WalkthroughJob
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("invalid extraction receipt: %w", err)
	}
	if result.Status == "completed" {
		hash, e := hex.DecodeString(result.Revision)
		if e != nil || len(hash) != 32 {
			return nil, fmt.Errorf("completed job lacks an immutable evidence revision")
		}
	}
	if !safeWalkthroughSegment(result.JobID) {
		return nil, fmt.Errorf("invalid job identity in receipt")
	}
	switch result.Status {
	case "queued", "running", "completed", "failed":
	default:
		return nil, fmt.Errorf("invalid extraction status")
	}
	return &result, nil
}

func safeWalkthroughSegment(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", r)
	})
}
