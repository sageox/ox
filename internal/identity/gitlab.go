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

// getGitLabIdentity fetches the authenticated GitLab user identity.
// It tries in order:
//  1. GITLAB_TOKEN environment variable
//  2. GITLAB_PRIVATE_TOKEN environment variable
//  3. glab CLI config file (~/.config/glab-cli/config.yml)
//
// Privacy: Only called if a remote points to gitlab.com.
func getGitLabIdentity() (*Identity, error) {
	token := getGitLabToken()
	if token == "" {
		return nil, fmt.Errorf("no GitLab token found")
	}

	return fetchGitLabUser(token)
}

// getGitLabToken retrieves GitLab token from environment or glab CLI config.
func getGitLabToken() string {
	// 1. Environment variables (CI/CD friendly)
	if t := os.Getenv("GITLAB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GITLAB_PRIVATE_TOKEN"); t != "" {
		return t
	}

	// 2. Read glab CLI config (no glab binary needed)
	return readGLabConfig()
}

// glabConfig represents the structure of ~/.config/glab-cli/config.yml
type glabConfig struct {
	Hosts map[string]struct {
		Token   string `yaml:"token"`
		APIHost string `yaml:"api_host"`
		User    string `yaml:"user"`
	} `yaml:"hosts"`
}

// readGLabConfig reads the GitLab token from glab CLI config file.
// The glab CLI stores credentials at ~/.config/glab-cli/config.yml.
func readGLabConfig() string {
	configPath := filepath.Join(xdgConfigHome(), "glab-cli", "config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var config glabConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return ""
	}

	// look for gitlab.com entry
	if host, ok := config.Hosts["gitlab.com"]; ok {
		return host.Token
	}

	return ""
}

// gitLabUserResponse represents the GitLab API user response.
type gitLabUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// fetchGitLabUser calls the GitLab API to get user information.
func fetchGitLabUser(token string) (*Identity, error) {
	url := "https://gitlab.com/api/v4/user"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// GitLab uses PRIVATE-TOKEN header instead of Bearer
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return nil, fmt.Errorf("gitlab api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gitlab api returned status %d: %s", resp.StatusCode, string(body))
	}

	var user gitLabUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode gitlab response: %w", err)
	}

	return &Identity{
		UserID:   fmt.Sprintf("gitlab:%d", user.ID),
		Email:    user.Email,
		Name:     user.Name,
		Username: user.Username,
		Source:   "gitlab",
		Verified: true,
	}, nil
}
