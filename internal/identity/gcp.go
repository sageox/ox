package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// getGCPIdentity fetches the authenticated GCP user identity.
// It tries in order:
//  1. GOOGLE_APPLICATION_CREDENTIALS environment variable (service account)
//  2. gcloud CLI config file (~/.config/gcloud/configurations/config_default)
//  3. Application default credentials (~/.config/gcloud/application_default_credentials.json)
//
// We use a simpler approach: just read the account email from gcloud config
// without making API calls to avoid unnecessary network traffic.
//
// Privacy: Only called if a remote points to source.developers.google.com.
func getGCPIdentity() (*Identity, error) {
	// 1. Check for service account credentials file
	if credsPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credsPath != "" {
		if identity, err := readServiceAccountCredentials(credsPath); err == nil && identity != nil {
			return identity, nil
		}
	}

	// 2. Try gcloud CLI config (most common for developers)
	if identity := readGCloudConfig(); identity != nil {
		return identity, nil
	}

	// 3. Try application default credentials
	if identity := readApplicationDefaultCredentials(); identity != nil {
		return identity, nil
	}

	return nil, fmt.Errorf("no GCP credentials found")
}

// readGCloudConfig reads the GCP account email from gcloud CLI config.
// The gcloud CLI stores active config at ~/.config/gcloud/configurations/config_default.
func readGCloudConfig() *Identity {
	configPath := filepath.Join(xdgConfigHome(), "gcloud", "configurations", "config_default")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	// parse simple INI-style format
	account := parseGCloudConfigAccount(string(data))
	if account == "" {
		return nil
	}

	return &Identity{
		UserID:   fmt.Sprintf("gcp:%s", account),
		Email:    account,
		Source:   "gcp",
		Verified: false, // gcloud config is local, not verified by API
	}
}

// parseGCloudConfigAccount extracts the account email from gcloud config text.
// The config format is:
//
//	[core]
//	account = user@example.com
//	project = my-project
func parseGCloudConfigAccount(content string) string {
	inCoreSection := false
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// detect [core] section
		if line == "[core]" {
			inCoreSection = true
			continue
		}

		// detect start of new section
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inCoreSection = false
			continue
		}

		// look for account = value in core section
		if inCoreSection && strings.HasPrefix(line, "account") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}

	return ""
}

// applicationDefaultCredentials represents the structure of ADC JSON file.
type applicationDefaultCredentials struct {
	ClientID    string `json:"client_id"`
	ClientEmail string `json:"client_email"` // for service accounts
	Type        string `json:"type"`         // "service_account" or "authorized_user"
}

// readApplicationDefaultCredentials reads the GCP ADC file.
// The ADC file is stored at ~/.config/gcloud/application_default_credentials.json.
func readApplicationDefaultCredentials() *Identity {
	adcPath := filepath.Join(xdgConfigHome(), "gcloud", "application_default_credentials.json")

	data, err := os.ReadFile(adcPath)
	if err != nil {
		return nil
	}

	var creds applicationDefaultCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil
	}

	// for service accounts, use client_email
	if creds.Type == "service_account" && creds.ClientEmail != "" {
		return &Identity{
			UserID:   fmt.Sprintf("gcp:%s", creds.ClientEmail),
			Email:    creds.ClientEmail,
			Source:   "gcp",
			Verified: false,
		}
	}

	// for authorized_user type, we'd need to call the API
	// but we're avoiding that, so just skip
	return nil
}

// serviceAccountCredentials represents the structure of service account JSON key.
type serviceAccountCredentials struct {
	Type                string `json:"type"`
	ProjectID           string `json:"project_id"`
	PrivateKeyID        string `json:"private_key_id"`
	ClientEmail         string `json:"client_email"`
	ClientID            string `json:"client_id"`
	AuthURI             string `json:"auth_uri"`
	TokenURI            string `json:"token_uri"`
	AuthProviderCertURL string `json:"auth_provider_x509_cert_url"`
	ClientCertURL       string `json:"client_x509_cert_url"`
}

// readServiceAccountCredentials reads a service account JSON key file.
func readServiceAccountCredentials(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds serviceAccountCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse service account credentials: %w", err)
	}

	if creds.Type != "service_account" {
		return nil, fmt.Errorf("credentials file is not a service account")
	}

	if creds.ClientEmail == "" {
		return nil, fmt.Errorf("service account missing client_email")
	}

	return &Identity{
		UserID:   fmt.Sprintf("gcp:%s", creds.ClientEmail),
		Email:    creds.ClientEmail,
		Source:   "gcp",
		Verified: false, // local file, not API-verified
	}, nil
}

// parseGCloudProperties parses gcloud properties file.
// This is an alternative location where gcloud stores configuration.
func parseGCloudProperties() *Identity {
	propsPath := filepath.Join(xdgConfigHome(), "gcloud", "properties")

	data, err := os.ReadFile(propsPath)
	if err != nil {
		return nil
	}

	account := parseGCloudConfigAccount(string(data))
	if account == "" {
		return nil
	}

	return &Identity{
		UserID:   fmt.Sprintf("gcp:%s", account),
		Email:    account,
		Source:   "gcp",
		Verified: false,
	}
}
