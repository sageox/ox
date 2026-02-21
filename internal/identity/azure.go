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

// getAzureDevOpsIdentity fetches the authenticated Azure DevOps user identity.
// It tries in order:
//  1. AZURE_DEVOPS_EXT_PAT environment variable (common for Azure DevOps)
//  2. SYSTEM_ACCESSTOKEN environment variable (Azure Pipelines)
//  3. Azure CLI config file (~/.azure/azureProfile.json)
//
// Privacy: Only called if a remote points to dev.azure.com or visualstudio.com.
func getAzureDevOpsIdentity() (*Identity, error) {
	token := getAzureDevOpsToken()
	if token == "" {
		return nil, fmt.Errorf("no Azure DevOps token found")
	}

	return fetchAzureDevOpsUser(token)
}

// getAzureDevOpsToken retrieves Azure DevOps token from environment or Azure CLI config.
func getAzureDevOpsToken() string {
	// 1. Environment variables (CI/CD friendly)
	if t := os.Getenv("AZURE_DEVOPS_EXT_PAT"); t != "" {
		return t
	}
	if t := os.Getenv("SYSTEM_ACCESSTOKEN"); t != "" {
		return t
	}

	// 2. Read Azure CLI config (no az binary needed)
	return readAzureCliConfig()
}

// azureCliConfig represents the structure of ~/.azure/config
type azureCliConfig struct {
	DevOps struct {
		Token string `yaml:"pat_token"`
	} `yaml:"devops"`
}

// readAzureCliConfig reads the Azure DevOps token from Azure CLI config file.
// The Azure CLI stores credentials at ~/.azure/config (YAML format).
func readAzureCliConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	configPath := filepath.Join(home, ".azure", "config")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var config azureCliConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return ""
	}

	return config.DevOps.Token
}

// azureDevOpsProfileResponse represents the Azure DevOps API profile response.
type azureDevOpsProfileResponse struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	PublicAlias  string `json:"publicAlias"`
}

// fetchAzureDevOpsUser calls the Azure DevOps API to get user information.
func fetchAzureDevOpsUser(token string) (*Identity, error) {
	url := "https://app.vssps.visualstudio.com/_apis/profile/profiles/me?api-version=7.0"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Azure DevOps uses Basic auth with empty username and PAT as password
	req.SetBasicAuth("", token)
	req.Header.Set("User-Agent", useragent.String())
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		return nil, fmt.Errorf("azure devops api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("azure devops api returned status %d: %s", resp.StatusCode, string(body))
	}

	var profile azureDevOpsProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("failed to decode azure devops response: %w", err)
	}

	return &Identity{
		UserID:   fmt.Sprintf("azure-devops:%s", profile.ID),
		Email:    profile.EmailAddress,
		Name:     profile.DisplayName,
		Username: profile.PublicAlias,
		Source:   "azure-devops",
		Verified: true,
	}, nil
}
