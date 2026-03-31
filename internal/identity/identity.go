// Package identity provides unified identity resolution for ox.
// It determines user identity from multiple sources with the following priority:
//
//  1. SageOx OAuth (verified via our auth system)
//  2. Git provider (GitHub/GitLab/Bitbucket/AWS/GCP based on repo remotes)
//  3. Git config (unverified, user-declared)
//
// Privacy-first: Only probes providers that match the repo's actual remotes.
// We never make unnecessary API calls or leak credential presence.
package identity

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/repotools"
	"gopkg.in/yaml.v3"
)

// ProviderType represents a git hosting provider.
type ProviderType string

const (
	ProviderGitHub      ProviderType = "github"
	ProviderGitLab      ProviderType = "gitlab"
	ProviderBitbucket   ProviderType = "bitbucket"
	ProviderAzureDevOps ProviderType = "azure-devops"
	ProviderAWS         ProviderType = "aws"
	ProviderGCP         ProviderType = "gcp"
	ProviderGitea       ProviderType = "gitea"
	ProviderNone        ProviderType = ""
)

// Identity represents a user identity from a specific source.
type Identity struct {
	UserID   string `json:"user_id,omitempty"`  // provider-specific ID (e.g., "github:1234567")
	Email    string `json:"email,omitempty"`    // email from provider
	Name     string `json:"name,omitempty"`     // display name from provider
	Username string `json:"username,omitempty"` // provider username (e.g., GitHub login)
	Source   string `json:"source"`             // where this identity came from (see below)
	Verified bool   `json:"-"`                  // internal only - server must verify, not trust client claims
}

// Source values (identity origin, NOT repo type):
// - "sageox"    : From SageOx OAuth (verified via our auth system)
// - "github"    : From GitHub API (verified via their token)
// - "gitlab"    : From GitLab API (verified via their token)
// - "bitbucket" : From Bitbucket API (verified via their token)
// - "aws"       : From AWS STS (verified via AWS credentials)
// - "gcp"       : From GCP/gcloud config (unverified, user-declared)
// - "git"       : From git config (unverified, user-declared)
//
// NOTE: Source="git" means identity from `git config user.name/email`
//       This is DIFFERENT from RepoType="git" which means the repo uses git VCS

// ResolvedIdentities contains all resolved identities for a user.
// Primary is the highest-priority verified identity found.
// Other fields contain identities from each source (if available).
type ResolvedIdentities struct {
	Primary     *Identity `json:"primary"`
	SageOx      *Identity `json:"sageox,omitempty"`
	GitHub      *Identity `json:"github,omitempty"`
	GitLab      *Identity `json:"gitlab,omitempty"`
	Bitbucket   *Identity `json:"bitbucket,omitempty"`
	AzureDevOps *Identity `json:"azure_devops,omitempty"`
	AWS         *Identity `json:"aws,omitempty"`
	GCP         *Identity `json:"gcp,omitempty"`
	Gitea       *Identity `json:"gitea,omitempty"`
	Git         *Identity `json:"git,omitempty"`
}

// Config controls identity collection behavior.
type Config struct {
	// Collection controls how many identities to collect:
	// - "all": Collect all available identities (default)
	// - "primary-only": Only determine and send the primary identity
	// - "none": Only use git config identity (no API calls)
	Collection string `yaml:"collection"`

	// Disable lists providers to skip (e.g., ["bitbucket"])
	Disable []string `yaml:"disable"`
}

// Resolve determines user identity from all available sources.
// It follows the priority: SageOx → Provider (from remotes) → Git config.
// Privacy-first: only probes providers matching the repo's actual remotes.
func Resolve() (*ResolvedIdentities, error) {
	return ResolveWithConfig(nil)
}

// ResolveWithConfig determines user identity with custom configuration.
func ResolveWithConfig(cfg *Config) (*ResolvedIdentities, error) {
	if cfg == nil {
		cfg = &Config{Collection: "all"}
	}

	result := &ResolvedIdentities{}

	// always get git config identity (local, no network)
	gitIdentity := getGitIdentity()
	result.Git = gitIdentity

	// if "none" mode, only use git config
	if cfg.Collection == "none" {
		result.Primary = gitIdentity
		return result, nil
	}

	// 1. Try SageOx identity (always checked - it's our primary auth)
	sageoxIdentity := getSageOxIdentity()
	if sageoxIdentity != nil {
		result.SageOx = sageoxIdentity
		result.Primary = sageoxIdentity
	}

	// if "primary-only" and we have SageOx, we're done
	if cfg.Collection == "primary-only" && result.Primary != nil {
		return result, nil
	}

	// 2. Detect providers from git remotes (privacy-first)
	providers, remoteURLs := detectProvidersFromRemotes()
	disabledSet := make(map[string]bool)
	for _, d := range cfg.Disable {
		disabledSet[strings.ToLower(d)] = true
	}

	for _, provider := range providers {
		if disabledSet[string(provider)] {
			continue
		}

		var identity *Identity
		var err error

		switch provider {
		case ProviderGitHub:
			identity, err = getGitHubIdentity()
			if err == nil && identity != nil {
				result.GitHub = identity
			}
		case ProviderGitLab:
			identity, err = getGitLabIdentity()
			if err == nil && identity != nil {
				result.GitLab = identity
			}
		case ProviderBitbucket:
			identity, err = getBitbucketIdentity()
			if err == nil && identity != nil {
				result.Bitbucket = identity
			}
		case ProviderAWS:
			identity, err = getAWSIdentity()
			if err == nil && identity != nil {
				result.AWS = identity
			}
		case ProviderGCP:
			identity, err = getGCPIdentity()
			if err == nil && identity != nil {
				result.GCP = identity
			}
		case ProviderAzureDevOps:
			identity, err = getAzureDevOpsIdentity()
			if err == nil && identity != nil {
				result.AzureDevOps = identity
			}
		case ProviderGitea:
			// Gitea needs the instance URL from the remote
			instanceURL := extractGiteaInstanceURL(remoteURLs)
			if instanceURL != "" {
				identity, err = getGiteaIdentity(instanceURL)
				if err == nil && identity != nil {
					result.Gitea = identity
				}
			}
		}

		if err != nil {
			slog.Debug("failed to get provider identity", "provider", provider, "error", err)
		}

		// set as primary if we don't have one yet
		if result.Primary == nil && identity != nil {
			result.Primary = identity
		}

		// if "primary-only" and we have a primary, we're done
		if cfg.Collection == "primary-only" && result.Primary != nil {
			return result, nil
		}
	}

	// 3. Fall back to git config if no primary yet
	if result.Primary == nil {
		result.Primary = gitIdentity
	}

	return result, nil
}

// detectProvidersFromRemotes returns all providers detected across all remotes.
// A repo may have multiple remotes (origin, upstream, fork, etc.) pointing to
// different providers. We only collect identity from providers actually in use.
//
// Privacy: We explicitly do NOT probe providers that aren't in the remote list.
// This means we never make unnecessary API calls or leak credential presence.
// offline-safe: returns nil for local-only repos; identity resolution falls back to git config
func detectProvidersFromRemotes() ([]ProviderType, []string) {
	urls, err := repotools.GetRemoteURLs()
	if err != nil {
		slog.Debug("failed to get git remotes", "error", err)
		return nil, nil
	}

	seen := make(map[ProviderType]bool)
	var providers []ProviderType

	for _, url := range urls {
		var p ProviderType

		switch {
		case strings.Contains(url, "github.com"):
			p = ProviderGitHub
		case strings.Contains(url, "gitlab.com"):
			p = ProviderGitLab
		case strings.Contains(url, "bitbucket.org"):
			p = ProviderBitbucket
		case strings.Contains(url, "dev.azure.com") || strings.Contains(url, "visualstudio.com"):
			p = ProviderAzureDevOps
		case strings.Contains(url, "codecommit"):
			p = ProviderAWS
		case strings.Contains(url, "source.developers.google.com"):
			p = ProviderGCP
		case isGiteaInstance(url):
			p = ProviderGitea
		default:
			continue // self-hosted or unknown - skip
		}

		if !seen[p] {
			seen[p] = true
			providers = append(providers, p)
		}
	}

	return providers, urls
}

// isGiteaInstance checks if a URL might be a Gitea instance.
// Since Gitea is self-hosted, we check for common patterns.
func isGiteaInstance(url string) bool {
	// check for gitea in the URL (common for self-hosted)
	if strings.Contains(strings.ToLower(url), "gitea") {
		return true
	}
	// check if we have a tea config with a matching host
	return hasTeaConfigForURL(url)
}

// hasTeaConfigForURL checks if tea CLI has a login for this URL's host.
func hasTeaConfigForURL(url string) bool {
	// extract host from URL
	host := extractHost(url)
	if host == "" {
		return false
	}
	// check if tea config has this host
	token := readTeaConfigToken(host)
	return token != ""
}

// extractHost extracts the host from a git remote URL.
func extractHost(url string) string {
	// handle SSH format: git@host:path
	if strings.HasPrefix(url, "git@") {
		parts := strings.SplitN(url[4:], ":", 2)
		if len(parts) > 0 {
			return parts[0]
		}
	}
	// handle HTTPS format: https://host/path
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	parts := strings.SplitN(url, "/", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// extractGiteaInstanceURL extracts the Gitea instance URL from remote URLs.
func extractGiteaInstanceURL(urls []string) string {
	for _, url := range urls {
		if isGiteaInstance(url) {
			host := extractHost(url)
			if host != "" {
				return "https://" + host
			}
		}
	}
	return ""
}

// readTeaConfigToken reads the Gitea token for a host from tea CLI config.
func readTeaConfigToken(host string) string {
	configPath := filepath.Join(xdgConfigHome(), "tea", "config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	// simple yaml parsing for tea config
	type teaLogin struct {
		URL   string `yaml:"url"`
		Token string `yaml:"token"`
	}
	type teaCfg struct {
		Logins []teaLogin `yaml:"logins"`
	}

	var config teaCfg
	if err := yaml.Unmarshal(data, &config); err != nil {
		return ""
	}

	// look for matching host
	for _, login := range config.Logins {
		loginHost := extractHost(login.URL)
		if loginHost == host {
			return login.Token
		}
	}

	return ""
}

// getGitIdentity returns identity from git config (user.name/email).
// This is always available locally with no network calls.
func getGitIdentity() *Identity {
	gitIdent, err := repotools.DetectGitIdentity()
	if err != nil || gitIdent == nil {
		return &Identity{
			Source:   "git",
			Verified: false,
		}
	}

	return &Identity{
		Email:    gitIdent.Email,
		Name:     gitIdent.Name,
		Source:   "git",
		Verified: false,
	}
}

// getSageOxIdentity returns identity from SageOx OAuth if authenticated.
func getSageOxIdentity() *Identity {
	token, err := auth.GetToken()
	if err != nil || token == nil || token.AccessToken == "" {
		return nil
	}

	// check if token has user info
	if token.UserInfo.Email == "" && token.UserInfo.Name == "" && token.UserInfo.UserID == "" {
		return nil
	}

	return &Identity{
		UserID:   fmt.Sprintf("sageox:%s", token.UserInfo.UserID),
		Email:    token.UserInfo.Email,
		Name:     token.UserInfo.Name,
		Source:   "sageox",
		Verified: true,
	}
}

// xdgConfigHome returns the XDG config directory.
func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}
