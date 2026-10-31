package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/useragent"
)

var githubSnapshotID = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

type GitHubCatalogEntry struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	Status string `json:"status"`
}
type GitHubCatalog struct {
	Entries  []GitHubCatalogEntry `json:"entries"`
	Complete bool                 `json:"complete"`
}

// GitHubRepoSnapshots reads the existing access-checked repo catalog and its selected snapshots.
func (c *RepoClient) GitHubRepoSnapshots(ctx context.Context, repoID string, known map[string]bool) ([]json.RawMessage, []GitHubCatalogEntry, error) {
	for attempt := 0; attempt < 2; attempt++ {
		bodies, entries, err := c.githubRepoSnapshotsOnce(ctx, repoID, known)
		var httpErr *githubRepoHTTPError
		if err != nil && errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound && attempt == 0 {
			continue
		}
		return bodies, entries, err
	}
	return nil, nil, errors.New("GitHub repository changed while reading")
}

func (c *RepoClient) githubRepoSnapshotsOnce(ctx context.Context, repoID string, known map[string]bool) ([]json.RawMessage, []GitHubCatalogEntry, error) {
	if repoID == "" || strings.ContainsAny(repoID, "/?#") {
		return nil, nil, errors.New("invalid repository ID")
	}
	base := strings.TrimSuffix(c.baseURL, "/") + "/api/v1/repos/" + url.PathEscape(repoID) + "/github"
	var cat GitHubCatalog
	// Each request can fill at most 120 uncached catalog entries; 45 covers the existing 5000-entry bound.
	for attempt := 0; attempt < 45; attempt++ {
		raw, err := c.githubRepoGET(ctx, base+"?view=catalog", 8<<20)
		if err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(raw, &cat); err != nil {
			return nil, nil, fmt.Errorf("decode GitHub catalog: %w", err)
		}
		if cat.Complete {
			break
		}
	}
	if !cat.Complete {
		return nil, nil, errors.New("GitHub catalog is incomplete; retry later")
	}
	out := make([]json.RawMessage, 0, len(cat.Entries))
	var selected []GitHubCatalogEntry
	for _, entry := range cat.Entries {
		if entry.Status != "ready" || !githubSnapshotID.MatchString(entry.ID) || (entry.Kind != "pr" && entry.Kind != "issue") || entry.Number <= 0 {
			return nil, nil, errors.New("GitHub catalog contains unavailable entries")
		}
		if known[entry.Kind+":"+entry.ID] {
			continue
		}
		raw, err := c.githubRepoGET(ctx, base+"/"+entry.ID, 1<<20)
		if err != nil {
			return nil, nil, err
		}
		if !json.Valid(raw) {
			return nil, nil, errors.New("invalid GitHub snapshot")
		}
		out = append(out, raw)
		selected = append(selected, entry)
	}
	return out, selected, nil
}

type githubRepoHTTPError struct{ status int }

func (e *githubRepoHTTPError) Error() string {
	return fmt.Sprintf("GitHub repository read: HTTP %d", e.status)
}

func (c *RepoClient) githubRepoGET(ctx context.Context, requestURL string, limit int64) ([]byte, error) {
	req, err := useragent.NewRequest(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub repository read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &githubRepoHTTPError{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("GitHub repository response too large")
	}
	return raw, nil
}
