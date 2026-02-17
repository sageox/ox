package main

import (
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
)

// GetLedgerURLWithFallback fetches the ledger git URL from the cloud API,
// falling back to cached URL from the repo marker if API is unavailable.
// The sageoxDir parameter is optional - if empty, no fallback is attempted.
// The ep parameter is the endpoint to use for auth and API calls.
// Returns empty string if URL is not available from either source.
func GetLedgerURLWithFallback(sageoxDir, ep string) string {
	// try cloud API first
	url := getLedgerRemoteURL(ep)
	if url != "" {
		return url
	}

	// fall back to cached URL if we have a sageox directory
	if sageoxDir == "" {
		return ""
	}

	return GetCachedLedgerURL(sageoxDir, ep)
}

// GetTeamURLWithFallback fetches the team context git URL from the cloud API,
// falling back to cached URL from the repo marker if API is unavailable.
// The sageoxDir parameter is optional - if empty, no fallback is attempted.
// The ep parameter is the endpoint to use for auth and API calls.
// Returns empty string if URL is not available from either source.
func GetTeamURLWithFallback(sageoxDir, teamID, ep string) string {
	if teamID == "" {
		return ""
	}

	// try cloud API first
	url := getTeamContextRemoteURL(teamID, ep)
	if url != "" {
		return url
	}

	// fall back to cached URL if we have a sageox directory
	if sageoxDir == "" {
		return ""
	}

	return GetCachedTeamURL(sageoxDir, ep, teamID)
}

// GetLedgerURLWithFallbackFromRepoRoot is a convenience function that derives
// the sageox directory from the repo root and calls GetLedgerURLWithFallback.
// Resolves the endpoint from the project config.
func GetLedgerURLWithFallbackFromRepoRoot(repoRoot string) string {
	if repoRoot == "" {
		return getLedgerRemoteURL(endpoint.Get()) // no fallback possible
	}
	ep := endpoint.GetForProject(repoRoot)
	sageoxDir := filepath.Join(repoRoot, ".sageox")
	return GetLedgerURLWithFallback(sageoxDir, ep)
}

// GetTeamURLWithFallbackFromRepoRoot is a convenience function that derives
// the sageox directory from the repo root and calls GetTeamURLWithFallback.
// Resolves the endpoint from the project config.
func GetTeamURLWithFallbackFromRepoRoot(repoRoot, teamID string) string {
	if repoRoot == "" || teamID == "" {
		return getTeamContextRemoteURL(teamID, endpoint.Get()) // no fallback possible
	}
	ep := endpoint.GetForProject(repoRoot)
	sageoxDir := filepath.Join(repoRoot, ".sageox")
	return GetTeamURLWithFallback(sageoxDir, teamID, ep)
}

// FetchLedgerURLWithFallback fetches the ledger URL with detailed error info,
// falling back to cached URL if API fails. The sageoxDir is used for fallback.
// The ep parameter is the endpoint to use for auth and API calls.
// Returns (url, fromCache, error) where fromCache indicates if fallback was used.
func FetchLedgerURLWithFallback(sageoxDir, ep string) (url string, fromCache bool, err error) {
	// try API first
	token, tokenErr := auth.GetTokenForEndpoint(ep)
	if tokenErr == nil && token != nil && token.AccessToken != "" {
		client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
		repos, apiErr := client.GetRepos()
		if apiErr == nil && repos != nil {
			for _, repo := range repos.Repos {
				if repo.Type == "ledger" {
					if repo.URL != "" {
						return repo.URL, false, nil
					}
					// found ledger but empty URL - try fallback
					break
				}
			}
		}
	}

	// API failed or returned empty - try fallback
	if sageoxDir != "" {
		cachedURL := GetCachedLedgerURL(sageoxDir, ep)
		if cachedURL != "" {
			return cachedURL, true, nil
		}
	}

	// no URL available from any source
	if tokenErr != nil {
		return "", false, fmt.Errorf("get auth token for %s: %w", ep, tokenErr)
	}
	if token == nil || token.AccessToken == "" {
		return "", false, fmt.Errorf("not authenticated to %s - run 'ox login' first", ep)
	}
	return "", false, fmt.Errorf("no ledger URL available from API or cache")
}

// FetchTeamURLWithFallback fetches the team context URL with detailed error info,
// falling back to cached URL if API fails. The sageoxDir is used for fallback.
// The ep parameter is the endpoint to use for auth and API calls.
// Returns (url, fromCache, error) where fromCache indicates if fallback was used.
func FetchTeamURLWithFallback(sageoxDir, teamID, ep string) (url string, fromCache bool, err error) {
	if teamID == "" {
		return "", false, fmt.Errorf("team ID is empty")
	}

	// try API first
	token, tokenErr := auth.GetTokenForEndpoint(ep)
	if tokenErr == nil && token != nil && token.AccessToken != "" {
		client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
		teamInfo, apiErr := client.GetTeamInfo(teamID)
		if apiErr == nil && teamInfo != nil && teamInfo.RepoURL != "" {
			return teamInfo.RepoURL, false, nil
		}
	}

	// API failed or returned empty - try fallback
	if sageoxDir != "" {
		cachedURL := GetCachedTeamURL(sageoxDir, ep, teamID)
		if cachedURL != "" {
			return cachedURL, true, nil
		}
	}

	// no URL available from any source
	if tokenErr != nil {
		return "", false, fmt.Errorf("get auth token for %s: %w", ep, tokenErr)
	}
	if token == nil || token.AccessToken == "" {
		return "", false, fmt.Errorf("not authenticated to %s - run 'ox login' first", ep)
	}
	return "", false, fmt.Errorf("no team context URL available from API or cache for team %s", teamID)
}

// FetchAndCacheURLs fetches URLs from the cloud API and caches them in the repo marker.
// This is useful to call after successful authentication to ensure cached URLs are up-to-date.
// The ep parameter is the endpoint to use for auth and API calls.
// Returns true if URLs were successfully fetched and cached.
func FetchAndCacheURLs(sageoxDir, repoID, ep string) bool {
	if sageoxDir == "" || repoID == "" {
		return false
	}

	// check if we're authenticated
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return false
	}

	// fetch repos from API to validate we have access
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	repos, err := client.GetRepos()
	if err != nil || repos == nil {
		return false
	}

	// update marker with cached URLs (this reads from git credentials)
	if err := updateMarkerWithCachedURLs(sageoxDir, repoID, ep); err != nil {
		return false
	}

	return true
}

// ShouldRefreshCachedURLs checks if the cached URLs should be refreshed.
// The ep parameter is the endpoint to check cached URLs for.
// Returns true if cache is expired or missing.
func ShouldRefreshCachedURLs(sageoxDir, ep string) bool {
	if sageoxDir == "" {
		return false
	}

	urls, err := ReadMarkerURLs(sageoxDir, ep)
	if err != nil || urls == nil {
		return true // no cache = should refresh
	}

	return urls.IsExpired()
}
