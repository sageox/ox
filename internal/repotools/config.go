package repotools

import (
	"os/exec"
	"regexp"
	"strings"
)

// getGitConfig reads a git configuration value
func getGitConfig(key string) string {
	cmd := exec.Command("git", "config", "--get", key)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// Slugify converts a string to a filesystem-safe slug
// Replaces non-alphanumeric characters with hyphens and lowercases
func Slugify(s string) string {
	// replace non-alphanumeric chars with hyphens
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	slug := re.ReplaceAllString(s, "-")

	// trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// lowercase
	return strings.ToLower(slug)
}
