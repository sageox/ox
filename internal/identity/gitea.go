package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"gopkg.in/yaml.v3"
)

// getGiteaIdentity fetches the authenticated Gitea user identity.
// It tries in order:
//  1. GITEA_TOKEN environment variable
//  2. tea CLI config file (~/.config/tea/config.yml)
//
// Since Gitea is self-hosted, we need the instance URL to know which
// server to query. The function will look up the token for the given
// instance URL.
//
// Privacy: Only called if a remote points to a Gitea instance.
func getGiteaIdentity(instanceURL string) (*Identity, error) {
	token := getGiteaToken(instanceURL)
	if token == "" {
		return nil, fmt.Errorf("no Gitea token found for instance %s", instanceURL)
	}

	return fetchGiteaUser(instanceURL, token)
}

// getGiteaToken retrieves Gitea token from environment or tea CLI config.
// For environment variable, it will work for any instance.
// For tea CLI config, it looks up the token for the specific instance URL.
func getGiteaToken(instanceURL string) string {
	// 1. Environment variable (CI/CD friendly, works for any instance)
	if t := os.Getenv("GITEA_TOKEN"); t != "" {
		return t
	}

	// 2. Read tea CLI config (no tea binary needed)
	return readTeaConfig(instanceURL)
}

// teaConfig represents the structure of ~/.config/tea/config.yml
type teaConfig struct {
	Logins []struct {
		Name    string `yaml:"name"`
		URL     string `yaml:"url"`
		Token   string `yaml:"token"`
		Default bool   `yaml:"default"`
	} `yaml:"logins"`
}

// readTeaConfig reads the Gitea token from tea CLI config file.
// The tea CLI stores credentials at ~/.config/tea/config.yml.
// Since Gitea is self-hosted, we need to match the instance URL.
func readTeaConfig(instanceURL string) string {
	configPath := filepath.Join(xdgConfigHome(), "tea", "config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var config teaConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return ""
	}

	// normalize instance URL for comparison (remove trailing slash, handle http/https)
	normalizedInstance := normalizeGiteaURL(instanceURL)

	// look for matching instance URL
	for _, login := range config.Logins {
		if normalizeGiteaURL(login.URL) == normalizedInstance {
			return login.Token
		}
	}

	// if no match found, try to use default login (if there is one)
	for _, login := range config.Logins {
		if login.Default {
			return login.Token
		}
	}

	return ""
}

// normalizeGiteaURL normalizes a Gitea instance URL for comparison.
// It handles both http/https and removes trailing slashes.
func normalizeGiteaURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	// parse URL to normalize
	u, err := url.Parse(rawURL)
	if err != nil {
		// if parsing fails, just do basic cleanup
		rawURL = trimTrailingSlash(rawURL)
		return rawURL
	}

	// use host (with optional port) for comparison
	// this handles both http://gitea.example.com and https://gitea.example.com
	return u.Host
}

// trimTrailingSlash removes trailing slash from URL.
func trimTrailingSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}

// giteaUserResponse represents the Gitea API user response.
// Based on Gitea API v1 /user endpoint.
type giteaUserResponse struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

// fetchGiteaUser calls the Gitea API to get user information.
// The Gitea API is at {instance}/api/v1/user for the authenticated user.
func fetchGiteaUser(instanceURL, token string) (*Identity, error) {
	// ensure instance URL has scheme
	if instanceURL == "" {
		return nil, fmt.Errorf("instance URL cannot be empty")
	}

	// build API URL
	apiURL := fmt.Sprintf("%s/api/v1/user", trimTrailingSlash(instanceURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Gitea uses Authorization: token <token>
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("User-Agent", useragent.String())
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("GET", apiURL)
	start := time.Now()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", apiURL, err, duration)
		return nil, fmt.Errorf("gitea api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", apiURL, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gitea api returned status %d: %s", resp.StatusCode, string(body))
	}

	var user giteaUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode gitea response: %w", err)
	}

	// extract host from instance URL for UserID
	u, err := url.Parse(instanceURL)
	var instanceHost string
	if err == nil {
		instanceHost = u.Host
	} else {
		instanceHost = instanceURL
	}

	return &Identity{
		UserID:   fmt.Sprintf("gitea:%s:%d", instanceHost, user.ID),
		Email:    user.Email,
		Name:     user.FullName,
		Username: user.Login,
		Source:   "gitea",
		Verified: true,
	}, nil
}
