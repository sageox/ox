package gitserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/zalando/go-keyring"
)

const (
	// keyringService is the service name used in the OS keychain
	keyringService = "sageox-git"
	// keyringUser is the user identifier for the keychain entry
	keyringUser = "git-credentials"
)

// RepoEntry represents a single git repository from the credentials API.
// NOTE: GET /api/v1/cli/repos only returns team-context repos, not ledgers.
// Ledger URLs come from GET /api/v1/repos/{repo_id}/ledger-status separately.
type RepoEntry struct {
	Name   string `json:"name"`              // display name (e.g., "Ox CLI Test")
	Type   string `json:"type"`              // "team-context"
	URL    string `json:"url"`               // git clone URL
	TeamID string `json:"team_id,omitempty"` // stable team identifier (e.g., "team_jij1bg2btu")
	Slug   string `json:"slug,omitempty"`    // kebab-case team slug (server-provided)
}

// StableID returns the stable team identifier (team_xxx) for path construction and lookups.
func (r RepoEntry) StableID() string {
	return r.TeamID
}

// GitCredentials holds git server authentication credentials and repo URLs.
// Repos contains team-context repos indexed by team ID. Ledger URLs are NOT stored here.
type GitCredentials struct {
	Token     string               `json:"token"`
	ServerURL string               `json:"server_url"`
	Username  string               `json:"username"`
	ExpiresAt time.Time            `json:"expires_at"`
	Repos     map[string]RepoEntry `json:"repos,omitempty"` // indexed by team ID
}

// configDirOverride allows tests to override the config directory
var configDirOverride string

// forceFileStorage forces file-based storage instead of keychain (for testing)
var forceFileStorage bool

// TestSetConfigDirOverride sets the config directory override for testing.
// Returns the previous value so it can be restored.
// This function should only be called from tests.
func TestSetConfigDirOverride(dir string) string {
	prev := configDirOverride
	configDirOverride = dir
	return prev
}

// TestSetForceFileStorage forces file-based storage for testing.
// Returns the previous value so it can be restored.
// This function should only be called from tests.
func TestSetForceFileStorage(force bool) bool {
	prev := forceFileStorage
	forceFileStorage = force
	return prev
}

// getCredentialsFilePath returns the path to the git credentials file.
// Uses OX_GIT_CREDENTIALS_FILE env var if set, otherwise uses centralized paths package.
func getCredentialsFilePath() (string, error) {
	// check env var override first (for CI/headless environments)
	if envPath := os.Getenv("OX_GIT_CREDENTIALS_FILE"); envPath != "" {
		return envPath, nil
	}

	// allow tests to override config directory
	if configDirOverride != "" {
		return filepath.Join(configDirOverride, "sageox", "git-credentials.json"), nil
	}

	// use centralized paths package
	path := paths.GitCredentialsFile()
	if path == "" {
		return "", fmt.Errorf("failed to get git credentials path")
	}
	return path, nil
}

// isKeyringAvailable checks if OS keychain is available and functional.
// Returns false for CI environments, headless servers, or when keyring fails.
func isKeyringAvailable() bool {
	// respect test override
	if forceFileStorage {
		return false
	}

	// check env var to force file storage (useful for CI)
	if os.Getenv("OX_GIT_CREDENTIALS_FILE") != "" {
		return false
	}

	// check common CI environment indicators
	ciEnvVars := []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "JENKINS_URL", "BUILDKITE"}
	for _, envVar := range ciEnvVars {
		if os.Getenv(envVar) != "" {
			return false
		}
	}

	// test if keyring is actually functional by attempting a probe operation
	// use a unique test key that won't conflict with real data
	testKey := "sageox-keyring-probe"
	testValue := "probe"

	// try to set and get a value
	if err := keyring.Set(keyringService, testKey, testValue); err != nil {
		return false
	}

	// clean up probe value
	_ = keyring.Delete(keyringService, testKey)

	return true
}

// RemoveCredentials deletes git server credentials from all storage locations.
// Removes from both keychain and file storage to ensure complete cleanup.
func RemoveCredentials() error {
	var errs []error

	// remove from keychain
	if isKeyringAvailable() {
		if err := keyring.Delete(keyringService, keyringUser); err != nil {
			// only track error if it's not "not found"
			if !errors.Is(err, keyring.ErrNotFound) {
				errs = append(errs, fmt.Errorf("keychain: %w", err))
			}
		}
	}

	// remove from file storage
	if err := removeCredentialsFromFile(); err != nil {
		errs = append(errs, fmt.Errorf("file: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to remove credentials: %v", errs)
	}

	return nil
}

// removeCredentialsFromFile removes credentials from file storage
func removeCredentialsFromFile() error {
	credsPath, err := getCredentialsFilePath()
	if err != nil {
		return err
	}

	err = os.Remove(credsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove git credentials file: %w", err)
	}

	return nil
}

// RemoveCredentialsForEndpoint deletes git credentials for a specific endpoint.
// Removes from both keychain and file storage to ensure complete cleanup.
func RemoveCredentialsForEndpoint(endpointURL string) error {
	var errs []error

	// remove from keychain with endpoint-specific key
	if isKeyringAvailable() {
		keyUser := keyringUserForEndpoint(endpointURL)
		if err := keyring.Delete(keyringService, keyUser); err != nil {
			// only track error if it's not "not found"
			if !errors.Is(err, keyring.ErrNotFound) {
				errs = append(errs, fmt.Errorf("keychain: %w", err))
			}
		}
	}

	// remove from file storage
	credsPath, err := getEndpointCredentialsPath(endpointURL)
	if err != nil {
		errs = append(errs, fmt.Errorf("file path: %w", err))
	} else {
		if err := os.Remove(credsPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("file: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to remove credentials for endpoint: %v", errs)
	}

	return nil
}

// IsExpired checks if the credentials are expired
func (c *GitCredentials) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false // no expiration set
	}
	return time.Now().After(c.ExpiresAt)
}

// GetRepo returns a repo entry by name, or nil if not found
func (c *GitCredentials) GetRepo(name string) *RepoEntry {
	if c.Repos == nil {
		return nil
	}
	if entry, ok := c.Repos[name]; ok {
		return &entry
	}
	return nil
}

// AddRepo adds or updates a repo entry
func (c *GitCredentials) AddRepo(entry RepoEntry) {
	if c.Repos == nil {
		c.Repos = make(map[string]RepoEntry)
	}
	c.Repos[entry.StableID()] = entry
}

// GetStorageBackend returns the currently active storage backend.
// Returns "keychain" or "file" depending on what's available.
func GetStorageBackend() string {
	if isKeyringAvailable() {
		return "keychain"
	}
	return "file"
}

// CredentialStatus represents the current state of git credentials
type CredentialStatus struct {
	// Valid is true if credentials exist and are not expired
	Valid bool
	// Reason describes why credentials are invalid (empty if valid)
	Reason string
	// RepoCount is the number of repos in credentials (0 if invalid)
	RepoCount int
	// ExpiresAt is when credentials expire (zero if unknown)
	ExpiresAt time.Time
	// TimeUntilExpiry is the duration until expiry (negative if expired)
	TimeUntilExpiry time.Duration
}

// NearExpiryThreshold is how close to expiry credentials must be to trigger refresh
const NearExpiryThreshold = 1 * time.Hour

// NeedsRefresh returns true if credentials need to be refreshed
func (s CredentialStatus) NeedsRefresh() bool {
	return !s.Valid
}

// FormatExpiry returns a human-readable expiry string
func (s CredentialStatus) FormatExpiry() string {
	if s.TimeUntilExpiry < 0 {
		return "expired"
	}
	d := s.TimeUntilExpiry
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// RefreshResult describes the outcome of a credential refresh attempt
type RefreshResult struct {
	// Refreshed is true if new credentials were fetched and saved
	Refreshed bool
	// Skipped is true if refresh was skipped (credentials still valid)
	Skipped bool
	// Error is set if refresh failed (old credentials preserved)
	Error error
	// Status is the credential status after refresh attempt
	Status CredentialStatus
}

// CredentialFetcher is a function that fetches new credentials from the API.
// Returns the new credentials or an error. The caller is responsible for
// providing authentication context.
type CredentialFetcher func() (*GitCredentials, error)


// --- Per-Endpoint Credential Storage ---
// These functions support multi-endpoint setups where each endpoint has its own
// GitLab server and credentials. Credentials are stored in separate files per endpoint.

// getEndpointCredentialsPath returns the path to credentials file for a specific endpoint.
// Uses a slug derived from the endpoint URL to create unique file names.
func getEndpointCredentialsPath(endpointURL string) (string, error) {
	if endpointURL == "" {
		return getCredentialsFilePath() // fall back to default
	}

	// create endpoint slug for file name
	slug := endpointSlug(endpointURL)
	if slug == "" {
		return getCredentialsFilePath() // fall back to default
	}

	// allow tests to override config directory
	if configDirOverride != "" {
		return filepath.Join(configDirOverride, "sageox", fmt.Sprintf("git-credentials-%s.json", slug)), nil
	}

	// use centralized paths package
	basePath := paths.GitCredentialsFile()
	if basePath == "" {
		return "", fmt.Errorf("failed to get git credentials path")
	}

	// insert endpoint slug before .json extension
	dir := filepath.Dir(basePath)
	return filepath.Join(dir, fmt.Sprintf("git-credentials-%s.json", slug)), nil
}

// endpointSlug creates a safe file name slug from an endpoint URL.
// Delegates to endpoint.NormalizeSlug for consistent normalization across the codebase.
//
// Examples:
//   - http://localhost:3000 → localhost
//   - https://test.sageox.ai → test.sageox.ai
//   - https://www.test.sageox.ai → test.sageox.ai
//   - https://git.test.sageox.ai → test.sageox.ai
//   - https://api.test.sageox.ai → test.sageox.ai
//   - https://sageox.ai → sageox.ai
func endpointSlug(endpointURL string) string {
	return endpoint.NormalizeSlug(endpointURL)
}

// keyringUserForEndpoint returns the keyring user key for a specific endpoint.
// Uses endpoint slug to create unique keychain entries per endpoint.
func keyringUserForEndpoint(endpointURL string) string {
	slug := endpointSlug(endpointURL)
	if slug == "" {
		return keyringUser // default key for empty/invalid endpoint
	}
	return keyringUser + ":" + slug
}

// LoadCredentialsForEndpoint loads git credentials for a specific endpoint.
// Tries OS keychain first (with endpoint-specific key), falls back to file storage.
// Falls back to default credentials if endpoint-specific ones don't exist.
func LoadCredentialsForEndpoint(endpointURL string) (*GitCredentials, error) {
	// try keychain first with endpoint-specific key
	if isKeyringAvailable() {
		keyUser := keyringUserForEndpoint(endpointURL)
		data, err := keyring.Get(keyringService, keyUser)
		if err == nil && data != "" {
			var creds GitCredentials
			if err := json.Unmarshal([]byte(data), &creds); err == nil {
				return &creds, nil
			}
			// JSON parse failed, fall through to file storage
		}
		// keyring not found or error, fall through to file storage
	}

	// try endpoint-specific file
	credsPath, err := getEndpointCredentialsPath(endpointURL)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(credsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no credentials for this endpoint
		}
		return nil, fmt.Errorf("failed to read git credentials file: %w", err)
	}

	var creds GitCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse git credentials file: %w", err)
	}

	return &creds, nil
}

// SaveCredentialsForEndpoint saves git credentials for a specific endpoint.
// Uses OS keychain as primary storage (with endpoint-specific key).
// Falls back to file storage for CI/headless environments.
func SaveCredentialsForEndpoint(endpointURL string, creds GitCredentials) error {
	// marshal credentials to JSON for storage
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal git credentials: %w", err)
	}

	// try keychain first with endpoint-specific key
	if isKeyringAvailable() {
		keyUser := keyringUserForEndpoint(endpointURL)
		if err := keyring.Set(keyringService, keyUser, string(data)); err == nil {
			// keychain succeeded, also update file for backward compatibility
			// (ignore file write errors since keychain is primary)
			_ = saveCredentialsToFileForEndpoint(endpointURL, creds)
			return nil
		}
		// keychain failed, fall through to file storage
	}

	// fallback to file storage
	return saveCredentialsToFileForEndpoint(endpointURL, creds)
}

// saveCredentialsToFileForEndpoint saves credentials to endpoint-specific file storage
func saveCredentialsToFileForEndpoint(endpointURL string, creds GitCredentials) error {
	credsPath, err := getEndpointCredentialsPath(endpointURL)
	if err != nil {
		return err
	}

	// ensure directory exists with secure permissions
	configDir := filepath.Dir(credsPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// marshal credentials to JSON with indentation for readability
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal git credentials: %w", err)
	}

	// atomic write pattern
	tempPath := credsPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp git credentials file: %w", err)
	}

	if err := os.Rename(tempPath, credsPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename temp git credentials file: %w", err)
	}

	return nil
}

// RefreshCredentialsForEndpoint checks and refreshes credentials for a specific endpoint.
// This is the endpoint-aware version that saves credentials to the correct per-endpoint file.
func RefreshCredentialsForEndpoint(endpointURL string, fetcher CredentialFetcher, force bool) RefreshResult {
	status := CheckCredentialStatusForEndpoint(endpointURL)

	// check if refresh is needed
	if !force && !status.NeedsRefresh() {
		return RefreshResult{
			Skipped: true,
			Status:  status,
		}
	}

	// attempt to fetch new credentials
	newCreds, err := fetcher()
	if err != nil {
		// fetch failed - old credentials preserved
		return RefreshResult{
			Error:  err,
			Status: status, // return old status
		}
	}

	if newCreds == nil {
		return RefreshResult{
			Error:  fmt.Errorf("fetcher returned nil credentials"),
			Status: status,
		}
	}

	// save new credentials to endpoint-specific file
	if err := SaveCredentialsForEndpoint(endpointURL, *newCreds); err != nil {
		return RefreshResult{
			Error:  fmt.Errorf("save credentials: %w", err),
			Status: status,
		}
	}

	// return new status
	return RefreshResult{
		Refreshed: true,
		Status:    CheckCredentialStatusForEndpoint(endpointURL),
	}
}

// EnsureValidCredentialsForEndpoint checks credentials for a specific endpoint and refreshes if needed.
// This is the endpoint-aware version that should be used in multi-endpoint setups.
func EnsureValidCredentialsForEndpoint(endpointURL string, fetcher CredentialFetcher) (CredentialStatus, error) {
	result := RefreshCredentialsForEndpoint(endpointURL, fetcher, false)
	if result.Error != nil {
		return result.Status, result.Error
	}
	return result.Status, nil
}

// CheckCredentialStatusForEndpoint checks the status of credentials for a specific endpoint.
func CheckCredentialStatusForEndpoint(endpointURL string) CredentialStatus {
	creds, err := LoadCredentialsForEndpoint(endpointURL)
	if err != nil {
		return CredentialStatus{
			Valid:  false,
			Reason: "load error: " + err.Error(),
		}
	}
	if creds == nil {
		return CredentialStatus{
			Valid:  false,
			Reason: "missing",
		}
	}

	if creds.IsExpired() {
		return CredentialStatus{
			Valid:           false,
			Reason:          "expired",
			RepoCount:       len(creds.Repos),
			ExpiresAt:       creds.ExpiresAt,
			TimeUntilExpiry: time.Until(creds.ExpiresAt),
		}
	}

	timeUntilExpiry := time.Until(creds.ExpiresAt)
	if timeUntilExpiry < NearExpiryThreshold {
		return CredentialStatus{
			Valid:           false,
			Reason:          "expiring soon",
			RepoCount:       len(creds.Repos),
			ExpiresAt:       creds.ExpiresAt,
			TimeUntilExpiry: timeUntilExpiry,
		}
	}

	return CredentialStatus{
		Valid:           true,
		RepoCount:       len(creds.Repos),
		ExpiresAt:       creds.ExpiresAt,
		TimeUntilExpiry: timeUntilExpiry,
	}
}
