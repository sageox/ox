// Package endpoint provides centralized SageOx endpoint URL management.
package endpoint

import (
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
)

const (
	// Default is the default SageOx API endpoint.
	// All requests route through the main domain which proxies to internal services.
	Default = "https://sageox.ai"

	// EnvVar is the environment variable for the endpoint.
	EnvVar = "SAGEOX_ENDPOINT"

	// Production is an alias for Default, used in path functions to explicitly
	// indicate production endpoint paths should be used (e.g., ~/.sageox/data/teams/).
	// Using this constant makes code intent clearer than passing an empty string.
	Production = Default
)

// productionHosts are the hostnames that are considered production.
// These endpoints share the same data directory structure.
var productionHosts = map[string]bool{
	"api.sageox.ai": true,
	"app.sageox.ai": true,
	"www.sageox.ai": true,
	"sageox.ai":     true,
}

// stripPrefixes are common subdomain prefixes to strip from slugs.
// git. is included because git servers often use git.domain.com
var stripPrefixes = []string{"api.", "www.", "app.", "git."}

// Get returns the SageOx endpoint URL without trailing slash.
// Checks SAGEOX_ENDPOINT env var first, then falls back to default (production).
//
// ⚠️  WARNING: This function ignores project configuration!
//
// Using Get() when a project context exists will cause bugs where operations
// target production instead of the project's configured endpoint.
//
// ✅ CORRECT: Use GetForProject(projectRoot) for:
//   - All API calls within a repo context
//   - Path functions (paths.TeamsDataDir, paths.LedgersDataDir, etc.)
//   - Auth checks within a repo context
//   - Any operation where .sageox/config.json might exist
//
// Get returns the endpoint from SAGEOX_ENDPOINT env var or the default.
//
// ⚠️  WARNING: DO NOT USE THIS FUNCTION FOR PROJECT-SCOPED OPERATIONS ⚠️
//
// This function ignores project config and returns the global/env endpoint.
// Using this in project-scoped code causes endpoint mismatches where resources
// end up in wrong directories (e.g., localhost auth but sageox.ai/ directories).
//
// ✅ ALLOWED uses (require human review for each new call):
//   - ox login (before any project config exists)
//   - ox init (initial project setup, before config is saved)
//   - Operations explicitly outside any repo context
//
// ❌ FORBIDDEN uses:
//   - Any code that has access to gitRoot or projectRoot
//   - Doctor checks, agent operations, sync, status
//   - Anywhere ProjectContext or endpoint.GetForProject() could be used
//
// Before adding a call to Get(), you MUST:
//  1. Confirm no projectRoot is available in the call chain
//  2. Get explicit human approval in code review
//  3. Add a comment explaining why GetForProject() cannot be used
//
// If you have a projectRoot, use GetForProject(projectRoot) instead.
func Get() string {
	// 1. Env var always wins (explicit override)
	if url := os.Getenv(EnvVar); url != "" {
		return NormalizeEndpoint(url)
	}

	// 2. If logged into exactly one endpoint, use that
	if LoggedInEndpointsGetter != nil {
		endpoints := LoggedInEndpointsGetter()
		if len(endpoints) == 1 {
			return NormalizeEndpoint(endpoints[0])
		}
	}

	// 3. Default
	return Default
}

// ProjectEndpointGetter is a function that loads endpoint from project config.
// This is set by the config package to avoid circular imports.
var ProjectEndpointGetter func(projectRoot string) string

// LoggedInEndpointsGetter returns endpoints the user is logged into.
// This is set by the auth package to avoid circular imports.
var LoggedInEndpointsGetter func() []string

// GetForProject returns the endpoint for a specific project.
// Precedence: SAGEOX_ENDPOINT env var > project config > default
//
// Use this for repo-bound operations (doctor, init, agent, sync).
// Use Get() for global operations (login without --endpoint, status without repo).
func GetForProject(projectRoot string) string {
	// env var always wins - allows explicit override
	if url := os.Getenv(EnvVar); url != "" {
		return NormalizeEndpoint(url)
	}

	// check project config if getter is registered
	if ProjectEndpointGetter != nil && projectRoot != "" {
		if ep := ProjectEndpointGetter(projectRoot); ep != "" {
			return NormalizeEndpoint(ep)
		}
	}

	// warn when falling through to Default with no project root —
	// callers probably should use Get() instead, or guard against empty root
	if projectRoot == "" {
		pc, _, _, _ := runtime.Caller(1)
		caller := runtime.FuncForPC(pc).Name()
		slog.Debug("GetForProject called with empty projectRoot, falling back to Default",
			"caller", caller)
	}

	return Default
}

// IsProduction returns true if the endpoint is a production SageOx endpoint.
// Production endpoints share the same data directory structure, while non-production
// endpoints (dev, staging, localhost) are namespaced by their hostname.
func IsProduction(endpoint string) bool {
	if endpoint == "" {
		return true // empty defaults to production
	}

	// extract hostname from endpoint URL
	host := extractHost(endpoint)

	return productionHosts[host]
}

// extractHost extracts the hostname from an endpoint URL.
// Handles full URLs (https://api.sageox.ai), host:port (localhost:8080), and bare hosts.
func extractHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)

	// try parsing as URL first
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		// remove port from host if present
		host := u.Host
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			// check if this is actually a port (not IPv6)
			if !strings.Contains(host[idx:], "]") {
				host = host[:idx]
			}
		}
		return strings.ToLower(host)
	}

	// handle bare host or host:port
	host := endpoint
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// check if it looks like a port (numeric)
		port := host[idx+1:]
		isPort := true
		for _, c := range port {
			if c < '0' || c > '9' {
				isPort = false
				break
			}
		}
		if isPort {
			host = host[:idx]
		}
	}

	return strings.ToLower(host)
}

// NormalizeEndpoint strips common subdomain prefixes (api., www., app., git.)
// from the host portion of an endpoint URL. Preserves scheme, path, port, and
// removes trailing slashes.
//
// This is the canonical normalization function for endpoint URLs before they are
// stored, compared, or displayed. NormalizeSlug() calls this internally.
//
// Examples:
//
//	https://www.test.sageox.ai → https://test.sageox.ai
//	https://api.sageox.ai/v1  → https://sageox.ai/v1
//	https://app.sageox.ai     → https://sageox.ai
//	https://git.test.sageox.ai → https://test.sageox.ai
//	www.test.sageox.ai        → https://test.sageox.ai
//	test.sageox.ai            → https://test.sageox.ai
//	http://localhost:8080      → http://localhost:8080  (no prefix to strip)
func NormalizeEndpoint(ep string) string {
	if ep == "" {
		return ""
	}

	ep = strings.TrimSpace(ep)
	ep = strings.TrimSuffix(ep, "/")

	// try parsing as a full URL with scheme
	if u, err := url.Parse(ep); err == nil && u.Scheme != "" && u.Host != "" {
		host := u.Hostname() // strips port
		port := u.Port()

		normalized := stripSubdomainPrefix(host)
		if normalized != host {
			if port != "" {
				u.Host = normalized + ":" + port
			} else {
				u.Host = normalized
			}
			return strings.TrimSuffix(u.String(), "/")
		}
		return ep
	}

	// bare host or host:port — strip prefix, then add https:// scheme
	host := ep
	port := ""
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		candidate := host[idx+1:]
		isPort := len(candidate) > 0
		for _, c := range candidate {
			if c < '0' || c > '9' {
				isPort = false
				break
			}
		}
		if isPort {
			port = candidate
			host = host[:idx]
		}
	}

	normalized := stripSubdomainPrefix(host)
	if normalized == "" {
		return ""
	}
	if port != "" {
		return "https://" + normalized + ":" + port
	}
	return "https://" + normalized
}

// stripSubdomainPrefix removes a single common subdomain prefix from a hostname.
// Uses the shared stripPrefixes list as the single source of truth.
func stripSubdomainPrefix(host string) string {
	lower := strings.ToLower(host)
	for _, prefix := range stripPrefixes {
		if strings.HasPrefix(lower, prefix) {
			// preserve original casing of the remainder
			return host[len(prefix):]
		}
	}
	return host
}

// NormalizeSlug returns a normalized endpoint slug for use in filesystem paths.
// Calls NormalizeEndpoint() first, then extracts the host and removes port numbers.
// Normalizes 127.0.0.1 to localhost for consistency.
//
// Examples:
//
//	api.sageox.ai → sageox.ai
//	localhost:8080 → localhost
//	127.0.0.1:3000 → localhost
//	staging.sageox.ai:443 → staging.sageox.ai
//	https://app.sageox.ai/v1 → sageox.ai
//	https://git.test.sageox.ai → test.sageox.ai
func NormalizeSlug(endpoint string) string {
	if endpoint == "" {
		return ""
	}

	// normalize the full endpoint first (strips subdomain prefixes)
	normalized := NormalizeEndpoint(endpoint)

	// extract host (strips scheme, path, and port)
	host := extractHost(normalized)
	if host == "" {
		return "unknown"
	}

	// normalize 127.0.0.1 to localhost
	if host == "127.0.0.1" {
		host = "localhost"
	}

	return host
}

// SanitizeForPath returns a filesystem-safe version of the endpoint for use in paths.
// Uses NormalizeSlug() to strip common prefixes and remove port numbers.
func SanitizeForPath(endpoint string) string {
	return NormalizeSlug(endpoint)
}
