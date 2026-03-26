package status

import (
	"fmt"
	"strings"
	"time"
)

// InferSemantic auto-detects value semantic type from context.
// Returns one of: "success", "error", "highlight", "muted", "default".
func InferSemantic(label, value string) string {
	valueLower := strings.ToLower(value)
	labelLower := strings.ToLower(label)

	// success indicators
	if valueLower == "logged in" || valueLower == "yes" ||
		valueLower == "initialized" || valueLower == "enabled" ||
		valueLower == "true" {
		return "success"
	}

	// error/negative indicators
	if valueLower == "not logged in" || valueLower == "no" ||
		valueLower == "not initialized" || valueLower == "none" ||
		valueLower == "disabled" || valueLower == "false" {
		return "error"
	}

	// highlight important user identity data in gold
	if labelLower == "user" || labelLower == "email" {
		return "highlight"
	}

	// muted for technical details (IDs, paths, directories)
	if strings.Contains(labelLower, "id") ||
		strings.Contains(labelLower, "path") ||
		strings.Contains(labelLower, "directory") ||
		strings.Contains(labelLower, "file") ||
		strings.Contains(labelLower, "expires") {
		return "muted"
	}

	return "default"
}

// FormatTimeAgo formats a time as a human-readable relative time.
func FormatTimeAgo(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		weeks := int(diff.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}
}

// FormatEndpointDisplay returns a shorter display name for an endpoint URL.
// e.g., "https://api.test.sageox.ai" -> "api.test.sageox.ai"
func FormatEndpointDisplay(endpointURL string) string {
	if endpointURL == "" {
		return "(default)"
	}
	// strip protocol prefix for cleaner display
	endpointURL = strings.TrimPrefix(endpointURL, "https://")
	endpointURL = strings.TrimPrefix(endpointURL, "http://")
	return endpointURL
}

// ExtractGitHost extracts the hostname from a git clone URL.
// Handles both HTTPS (https://git.example.com/...) and SSH (git@git.example.com:...) URLs.
// Returns empty string if parsing fails.
func ExtractGitHost(cloneURL string) string {
	if cloneURL == "" {
		return ""
	}

	// handle SSH URLs (git@host:path)
	if strings.Contains(cloneURL, "@") && !strings.Contains(cloneURL, "://") {
		// git@git.example.com:user/repo.git -> git.example.com
		parts := strings.SplitN(cloneURL, "@", 2)
		if len(parts) == 2 {
			hostPart := strings.SplitN(parts[1], ":", 2)
			if len(hostPart) >= 1 {
				return hostPart[0]
			}
		}
		return ""
	}

	// handle HTTPS URLs
	cloneURL = strings.TrimPrefix(cloneURL, "https://")
	cloneURL = strings.TrimPrefix(cloneURL, "http://")

	// remove credentials if present (oauth2:token@host)
	if idx := strings.Index(cloneURL, "@"); idx != -1 {
		cloneURL = cloneURL[idx+1:]
	}

	// extract host (before first /)
	if idx := strings.Index(cloneURL, "/"); idx != -1 {
		return cloneURL[:idx]
	}
	return cloneURL
}

// FormatTokenCount formats a token count in human-readable form (e.g., "3.1K", "1.2M").
func FormatTokenCount(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
}

// FormatDurationShort formats a duration in a short human-readable form.
func FormatDurationShort(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// FormatGitRepoStatus formats the git repo status for display.
// Returns a human-readable status string and a semantic type for styling.
func FormatGitRepoStatus(s GitRepoStatus) (string, string) {
	if !s.Exists {
		return "not found", "error"
	}

	if s.Error != "" {
		return s.Error, "error"
	}

	var parts []string

	if s.UncommittedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted", s.UncommittedCount))
	} else {
		parts = append(parts, "synced")
	}

	result := strings.Join(parts, ", ")

	if s.HasLastSync {
		result += fmt.Sprintf(" (%s)", FormatTimeAgo(s.LastSync))
	}

	if s.UncommittedCount > 0 {
		return result, "warning"
	}
	return result, "success"
}

// EstimateTokens estimates token count from byte count (~4 bytes per token for English/code).
func EstimateTokens(bytes int64) int {
	return int(bytes / 4)
}
