package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/version"
)

// URLCacheExpiry is the duration after which cached URLs are considered stale
// and should be refreshed from the API
const URLCacheExpiry = 7 * 24 * time.Hour // 7 days

// repoMarkerURLs holds cached URLs for the marker file
type repoMarkerURLs struct {
	LedgerURL    string
	TeamURLs     map[string]string // team_id -> git URL
	URLsCachedAt time.Time         // when URLs were cached
}

// IsExpired returns true if the cached URLs are older than URLCacheExpiry
func (u *repoMarkerURLs) IsExpired() bool {
	if u.URLsCachedAt.IsZero() {
		return true // no timestamp = expired (legacy markers)
	}
	return time.Since(u.URLsCachedAt) > URLCacheExpiry
}

// updateMarkerWithCachedURLs updates an existing marker file with cached URLs from git credentials.
// This is called after successful API registration and credential fetch.
func updateMarkerWithCachedURLs(sageoxDir, repoID, endpointURL string) error {
	// load git credentials to get URLs
	creds, err := gitserver.LoadCredentialsForEndpoint(endpointURL)
	if err != nil || creds == nil {
		return fmt.Errorf("no git credentials available")
	}

	// extract URLs from credentials
	urls := &repoMarkerURLs{
		TeamURLs: make(map[string]string),
	}

	for _, repo := range creds.Repos {
		switch repo.Type {
		case "ledger":
			urls.LedgerURL = repo.URL
		case "team-context":
			// extract team ID from repo name (e.g., "team-abc123" -> "abc123")
			teamID := strings.TrimPrefix(repo.Name, "team-")
			if teamID != "" {
				urls.TeamURLs[teamID] = repo.URL
			}
		}
	}

	// if no URLs found, nothing to update
	if urls.LedgerURL == "" && len(urls.TeamURLs) == 0 {
		return nil
	}

	// find and update the marker file
	uuidPart := extractUUIDSuffix(repoID)
	markerPath := filepath.Join(sageoxDir, ".repo_"+uuidPart)

	// read existing marker
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("failed to read marker: %w", err)
	}

	var marker repoMarkerWithURLs
	if err := json.Unmarshal(data, &marker); err != nil {
		return fmt.Errorf("failed to parse marker: %w", err)
	}

	// update URLs and timestamp
	marker.LedgerURL = urls.LedgerURL
	if marker.TeamURLs == nil {
		marker.TeamURLs = make(map[string]string)
	}
	for teamID, url := range urls.TeamURLs {
		marker.TeamURLs[teamID] = url
	}
	now := time.Now().UTC()
	marker.URLsCachedAt = now.Format(time.RFC3339)
	marker.UpdatedAt = now.Format(time.RFC3339)
	marker.Version = version.Version

	// write updated marker
	updatedData, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal marker: %w", err)
	}

	if err := os.WriteFile(markerPath, updatedData, 0644); err != nil {
		return fmt.Errorf("failed to write marker: %w", err)
	}

	return nil
}

// UpdateMarkerTeamURL updates the marker file with a new team context URL.
// This is called when a team context is added to the repository.
// Exported for use by other commands (e.g., team context add).
func UpdateMarkerTeamURL(sageoxDir, repoID, teamID, gitURL string) error {
	if repoID == "" || teamID == "" || gitURL == "" {
		return fmt.Errorf("missing required parameters")
	}

	uuidPart := extractUUIDSuffix(repoID)
	markerPath := filepath.Join(sageoxDir, ".repo_"+uuidPart)

	// read existing marker
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("failed to read marker: %w", err)
	}

	var marker repoMarkerWithURLs
	if err := json.Unmarshal(data, &marker); err != nil {
		return fmt.Errorf("failed to parse marker: %w", err)
	}

	// update team URL and timestamp
	if marker.TeamURLs == nil {
		marker.TeamURLs = make(map[string]string)
	}
	marker.TeamURLs[teamID] = gitURL
	now := time.Now().UTC()
	marker.URLsCachedAt = now.Format(time.RFC3339)
	marker.UpdatedAt = now.Format(time.RFC3339)
	marker.Version = version.Version

	// write updated marker
	updatedData, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal marker: %w", err)
	}

	if err := os.WriteFile(markerPath, updatedData, 0644); err != nil {
		return fmt.Errorf("failed to write marker: %w", err)
	}

	return nil
}

// ReadMarkerURLs reads cached URLs from the marker file for the given endpoint.
// Returns nil if no marker found or no URLs cached.
// Does NOT check cache expiry - use ReadMarkerURLsWithExpiry for that.
func ReadMarkerURLs(sageoxDir, targetEndpoint string) (*repoMarkerURLs, error) {
	return readMarkerURLsInternal(sageoxDir, targetEndpoint, false)
}

// ReadMarkerURLsWithExpiry reads cached URLs and returns nil if expired.
// Use this when you want to respect cache expiry (e.g., for fallback logic).
func ReadMarkerURLsWithExpiry(sageoxDir, targetEndpoint string) (*repoMarkerURLs, error) {
	return readMarkerURLsInternal(sageoxDir, targetEndpoint, true)
}

// readMarkerURLsInternal is the shared implementation for reading marker URLs
func readMarkerURLsInternal(sageoxDir, targetEndpoint string, checkExpiry bool) (*repoMarkerURLs, error) {
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// normalize target endpoint for comparison
	targetNorm := endpoint.NormalizeEndpoint(targetEndpoint)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}

		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}

		var marker repoMarkerWithURLs
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}

		// filter by endpoint
		markerEndpoint := endpoint.NormalizeEndpoint(marker.GetEndpoint())
		if markerEndpoint != targetNorm {
			continue
		}

		// found matching marker - return URLs if present
		if marker.LedgerURL != "" || len(marker.TeamURLs) > 0 {
			urls := &repoMarkerURLs{
				LedgerURL: marker.LedgerURL,
				TeamURLs:  marker.TeamURLs,
			}

			// parse cached timestamp
			if marker.URLsCachedAt != "" {
				if t, err := time.Parse(time.RFC3339, marker.URLsCachedAt); err == nil {
					urls.URLsCachedAt = t
				}
			}

			// check expiry if requested
			if checkExpiry && urls.IsExpired() {
				return nil, nil // expired, treat as no cache
			}

			return urls, nil
		}
	}

	return nil, nil
}

// GetCachedLedgerURL returns the cached ledger URL from the marker file.
// Returns empty string if not cached or cache is expired.
// This is useful for fallback when cloud API is unavailable.
func GetCachedLedgerURL(sageoxDir, targetEndpoint string) string {
	urls, err := ReadMarkerURLsWithExpiry(sageoxDir, targetEndpoint)
	if err != nil || urls == nil {
		return ""
	}
	return urls.LedgerURL
}

// GetCachedTeamURL returns the cached team context URL from the marker file.
// Returns empty string if not cached or cache is expired.
// This is useful for fallback when cloud API is unavailable.
func GetCachedTeamURL(sageoxDir, targetEndpoint, teamID string) string {
	urls, err := ReadMarkerURLsWithExpiry(sageoxDir, targetEndpoint)
	if err != nil || urls == nil {
		return ""
	}
	if urls.TeamURLs == nil {
		return ""
	}
	return urls.TeamURLs[teamID]
}

// repoMarkerWithURLs extends repoMarker with URL caching fields for JSON parsing
// This struct is used for reading/writing markers that may contain cached URLs
type repoMarkerWithURLs struct {
	RepoID       string            `json:"repo_id"`
	Type         string            `json:"type"`
	InitAt       string            `json:"init_at"`
	InitByEmail  string            `json:"init_by_email,omitempty"`
	InitByName   string            `json:"init_by_name,omitempty"`
	RepoSalt     string            `json:"repo_salt"`
	Endpoint     string            `json:"endpoint"`
	Fingerprint  json.RawMessage   `json:"fingerprint,omitempty"`
	LedgerURL    string            `json:"ledger_url,omitempty"`
	TeamURLs     map[string]string `json:"team_urls,omitempty"`
	URLsCachedAt string            `json:"urls_cached_at,omitempty"` // ISO8601 timestamp when URLs were cached
	Version      string            `json:"version,omitempty"`
	CreatedAt    string            `json:"created_at,omitempty"`
	UpdatedAt    string            `json:"updated_at,omitempty"`
	APIEndpoint  string            `json:"api_endpoint,omitempty"`
}

// GetEndpoint returns the endpoint, preferring new field over legacy
func (m *repoMarkerWithURLs) GetEndpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return m.APIEndpoint
}
