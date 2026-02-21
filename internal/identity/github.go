package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"gopkg.in/yaml.v3"
)

// getGitHubIdentity fetches the authenticated GitHub user identity.
// It tries in order:
//  1. GITHUB_TOKEN environment variable
//  2. GH_TOKEN environment variable
//  3. gh CLI config file (~/.config/gh/hosts.yml)
//
// Privacy: Only called if a remote points to github.com.
func getGitHubIdentity() (*Identity, error) {
	token := getGitHubToken()
	if token == "" {
		return nil, fmt.Errorf("no GitHub token found")
	}

	return fetchGitHubUser(token)
}

// getGitHubToken retrieves GitHub token from environment or gh CLI config.
func getGitHubToken() string {
	// 1. Environment variables (CI/CD friendly)
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}

	// 2. Read gh CLI config (no gh binary needed)
	return readGHHostsConfig()
}

// ghHostsConfig represents the structure of ~/.config/gh/hosts.yml
type ghHostsConfig map[string]struct {
	User        string `yaml:"user"`
	OAuthToken  string `yaml:"oauth_token"`
	GitProtocol string `yaml:"git_protocol"`
}

// readGHHostsConfig reads the GitHub token from gh CLI config file.
// The gh CLI stores credentials at ~/.config/gh/hosts.yml (XDG-compliant).
func readGHHostsConfig() string {
	configPath := filepath.Join(xdgConfigHome(), "gh", "hosts.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var hosts ghHostsConfig
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return ""
	}

	// look for github.com entry
	if host, ok := hosts["github.com"]; ok {
		return host.OAuthToken
	}

	return ""
}

// gitHubUserResponse represents the GitHub API user response.
type gitHubUserResponse struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// fetchGitHubUser calls the GitHub API to get user information.
func fetchGitHubUser(token string) (*Identity, error) {
	url := "https://api.github.com/user"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", useragent.String())
	req.Header.Set("Accept", "application/vnd.github+json")

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return nil, fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api returned status %d: %s", resp.StatusCode, string(body))
	}

	var user gitHubUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode github response: %w", err)
	}

	return &Identity{
		UserID:   fmt.Sprintf("github:%d", user.ID),
		Email:    user.Email,
		Name:     user.Name,
		Username: user.Login,
		Source:   "github",
		Verified: true,
	}, nil
}
