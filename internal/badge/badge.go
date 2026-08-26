// Package badge handles SageOx README badge detection and suggestion.
//
// FRAMING: Team Communication
// The badge is framed as helping your team and collaborators discover that
// this repo uses SageOx for team context - not as attribution
// or marketing. The prompt should emphasize team benefits:
// "Let your team know this repo uses SageOx for team context."
//
// PERMISSION REQUIRED
// The AI agent MUST ask for explicit user permission before modifying
// README.md. Never auto-inject the badge without user consent.
package badge

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// BadgeMarkdown is the markdown snippet for the SageOx badge.
	// Uses shields.io with SageOx brand sage green (#7AAA77).
	BadgeMarkdown = `[![powered by SageOx](https://img.shields.io/badge/powered%20by-SageOx-7AAA77?labelColor=555)](https://github.com/sageox/ox)`

	// StatusNotAsked indicates badge has not been suggested yet.
	StatusNotAsked = ""

	// StatusAdded indicates user accepted and badge was added.
	StatusAdded = "added"

	// StatusDeclined indicates user permanently declined the badge.
	StatusDeclined = "declined"
)

// badgePatterns are strings that indicate the SageOx badge is already present.
var badgePatterns = []string{
	"sageox",
	"SageOx",
	"powered%20by-SageOx",
	"sageox/ox",
}

// readmeCandidates are filenames to check for README.
var readmeCandidates = []string{
	"README.md",
	"Readme.md",
	"readme.md",
	"README",
	"README.markdown",
	"README.txt",
}

// FindReadme looks for a README file in the git root directory.
// Returns the path if found, empty string otherwise.
func FindReadme(gitRoot string) string {
	if gitRoot == "" {
		return ""
	}

	for _, name := range readmeCandidates {
		path := filepath.Join(gitRoot, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// HasBadge checks if the README already contains a SageOx badge.
// Returns true if any badge pattern is found (case-insensitive).
func HasBadge(readmePath string) bool {
	if readmePath == "" {
		return false
	}

	content, err := os.ReadFile(readmePath)
	if err != nil {
		return false
	}

	contentLower := strings.ToLower(string(content))
	for _, pattern := range badgePatterns {
		if strings.Contains(contentLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// InjectBadge adds the SageOx badge to the README.
// It inserts the badge after the first heading, alongside other badges.
// Returns an error if the file cannot be read or written.
func InjectBadge(readmePath string) error {
	content, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	insertAt := findBadgeInsertionPoint(lines)

	// build new content with badge inserted
	var newLines []string
	newLines = append(newLines, lines[:insertAt]...)

	// add blank line before badge if previous line is not empty
	if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
		newLines = append(newLines, "")
	}

	newLines = append(newLines, BadgeMarkdown)

	// add blank line after badge if next line exists and is not empty
	if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
		newLines = append(newLines, "")
	}

	newLines = append(newLines, lines[insertAt:]...)

	return os.WriteFile(readmePath, []byte(strings.Join(newLines, "\n")), 0644)
}

// findBadgeInsertionPoint finds the best line to insert the badge.
// Prefers: after first heading, after existing badges, or at start of file.
func findBadgeInsertionPoint(lines []string) int {
	headingLine := -1
	lastBadgeLine := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// find first heading
		if headingLine == -1 && strings.HasPrefix(trimmed, "#") {
			headingLine = i
		}

		// track badge lines (after heading)
		if headingLine != -1 && i > headingLine {
			// detect badge patterns: [![...] or ![...]
			if strings.HasPrefix(trimmed, "[![") || strings.HasPrefix(trimmed, "![") {
				lastBadgeLine = i
			} else if trimmed != "" && !strings.HasPrefix(trimmed, "[![") && !strings.HasPrefix(trimmed, "![") {
				// hit non-badge, non-empty line after heading - stop looking
				break
			}
		}
	}

	// prefer inserting after last existing badge
	if lastBadgeLine != -1 {
		return lastBadgeLine + 1
	}

	// otherwise insert after heading
	if headingLine != -1 {
		return headingLine + 1
	}

	// fallback to start of file
	return 0
}
