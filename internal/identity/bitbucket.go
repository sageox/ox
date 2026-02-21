package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// getBitbucketIdentity fetches the authenticated Bitbucket user identity.
// It tries in order:
//  1. BITBUCKET_TOKEN environment variable
//  2. BITBUCKET_ACCESS_TOKEN environment variable
//
// Note: There is no standard Bitbucket CLI like gh or glab, so we only
// check environment variables.
//
// Privacy: Only called if a remote points to bitbucket.org.
func getBitbucketIdentity() (*Identity, error) {
	token := getBitbucketToken()
	if token == "" {
		return nil, fmt.Errorf("no Bitbucket token found")
	}

	return fetchBitbucketUser(token)
}

// getBitbucketToken retrieves Bitbucket token from environment.
func getBitbucketToken() string {
	// Environment variables (CI/CD friendly)
	if t := os.Getenv("BITBUCKET_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("BITBUCKET_ACCESS_TOKEN"); t != "" {
		return t
	}

	return ""
}

// bitbucketUserResponse represents the Bitbucket API user response.
type bitbucketUserResponse struct {
	AccountID   string `json:"account_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	// Email requires a separate API call to /2.0/user/emails
}

// bitbucketEmailResponse represents the Bitbucket API emails response.
type bitbucketEmailResponse struct {
	Values []struct {
		Email     string `json:"email"`
		IsPrimary bool   `json:"is_primary"`
	} `json:"values"`
}

// fetchBitbucketUser calls the Bitbucket API to get user information.
func fetchBitbucketUser(token string) (*Identity, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// fetch user info
	url := "https://api.bitbucket.org/2.0/user"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return nil, fmt.Errorf("bitbucket api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bitbucket api returned status %d: %s", resp.StatusCode, string(body))
	}

	var user bitbucketUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode bitbucket response: %w", err)
	}

	// fetch primary email (requires separate API call)
	email := fetchBitbucketEmail(token, client)

	return &Identity{
		UserID:   fmt.Sprintf("bitbucket:%s", user.AccountID),
		Email:    email,
		Name:     user.DisplayName,
		Username: user.Username,
		Source:   "bitbucket",
		Verified: true,
	}, nil
}

// fetchBitbucketEmail fetches the primary email for the authenticated user.
// Returns empty string if email cannot be fetched (non-fatal).
func fetchBitbucketEmail(token string, client *http.Client) string {
	url := "https://api.bitbucket.org/2.0/user/emails"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)
		resp.Body.Close()
		return ""
	}
	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)
	defer resp.Body.Close()

	var emails bitbucketEmailResponse
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}

	// find primary email
	for _, e := range emails.Values {
		if e.IsPrimary {
			return e.Email
		}
	}

	// fall back to first email if no primary
	if len(emails.Values) > 0 {
		return emails.Values[0].Email
	}

	return ""
}
