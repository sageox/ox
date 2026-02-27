package docs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skippedPrefixes lists command prefixes that should not appear in
// public reference docs. These are internal/advanced commands that
// would overwhelm users at launch.
var skippedPrefixes = []string{
	"ox_agent",
	"ox_coworker",
	"ox_daemon",
	"ox_integrate",
	"ox_config",
	"ox_view",
	"ox_session_commit",
	"ox_session_download",
	"ox_session_export",
	"ox_session_push-summary",
	"ox_session_redaction",
	"ox_session_remove",
	"ox_session_upload",
	"ox_version",
}

// shouldSkipFile returns true if the file should be excluded from generated docs.
func shouldSkipFile(filename string) bool {
	base := strings.TrimSuffix(filename, ".md")
	for _, prefix := range skippedPrefixes {
		if base == prefix || strings.HasPrefix(base, prefix+"_") {
			return true
		}
	}
	return false
}

// isSkippedSeeAlsoLink returns true if the line is a SEE ALSO bullet that
// links to a skipped command page (e.g. "* [ox agent](ox_agent.md) ...").
func isSkippedSeeAlsoLink(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "* [") {
		return false
	}
	// Extract the link target: "* [ox agent](ox_agent.md)..." → "ox_agent.md"
	start := strings.Index(trimmed, "](")
	end := strings.Index(trimmed, ")")
	if start == -1 || end == -1 || end <= start+2 {
		return false
	}
	target := trimmed[start+2 : end] // e.g. "ox_agent.md"
	// If it doesn't end in .md, it's already transformed — convert back
	if !strings.HasSuffix(target, ".md") {
		target = strings.TrimPrefix(target, "/")
		target = "ox_" + strings.ReplaceAll(target, "/", "_") + ".md"
	}
	return shouldSkipFile(target)
}

// PostProcess transforms flat Cobra markdown output into hierarchical MDX structure
func PostProcess(srcDir, destDir string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Read all markdown files from source directory
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	// Collect all files and sort them to determine sidebar positions
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			if shouldSkipFile(entry.Name()) {
				continue
			}
			files = append(files, entry.Name())
		}
	}

	// Process each file
	for i, filename := range files {
		srcPath := filepath.Join(srcDir, filename)
		destPath, err := computeDestPath(filename, files)
		if err != nil {
			return fmt.Errorf("failed to compute destination path for %s: %w", filename, err)
		}

		// Create full destination path
		fullDestPath := filepath.Join(destDir, destPath)

		// Create parent directories
		destParent := filepath.Dir(fullDestPath)
		if err := os.MkdirAll(destParent, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", destParent, err)
		}

		// Transform the file
		if err := transformFile(srcPath, fullDestPath, i+1); err != nil {
			return fmt.Errorf("failed to transform %s: %w", filename, err)
		}
	}

	return nil
}

// computeDestPath determines the hierarchical path for a given filename
func computeDestPath(filename string, allFiles []string) (string, error) {
	// Remove .md extension
	base := strings.TrimSuffix(filename, ".md")

	// Split by underscore: ox_agent_prime -> [ox, agent, prime]
	parts := strings.Split(base, "_")

	// First part should always be "ox"
	if len(parts) == 0 || parts[0] != "ox" {
		return "", fmt.Errorf("unexpected filename format: %s", filename)
	}

	// Special case: ox.md -> index.mdx
	if len(parts) == 1 {
		return "index.mdx", nil
	}

	// Remove "ox" prefix
	parts = parts[1:]

	// Check if the last part represents a parent command
	// by seeing if there are any files that extend this command
	isParentCommand := false
	prefix := "ox_" + strings.Join(parts, "_") + "_"
	for _, f := range allFiles {
		if strings.HasPrefix(f, prefix) {
			isParentCommand = true
			break
		}
	}

	// Build the path
	var pathParts []string
	if isParentCommand {
		// This is a parent command, create index.mdx in its directory
		pathParts = append(parts, "index.mdx")
	} else {
		// This is a leaf command
		if len(parts) == 1 {
			// Direct child of ox: agent.mdx
			pathParts = []string{parts[0] + ".mdx"}
		} else {
			// Nested command: infra/tofu/upload.mdx
			pathParts = make([]string, len(parts)-1)
			copy(pathParts, parts[:len(parts)-1])
			pathParts = append(pathParts, parts[len(parts)-1]+".mdx")
		}
	}

	return filepath.Join(pathParts...), nil
}

// transformFile reads the source file, updates frontmatter, and writes to destination
func transformFile(srcPath, destPath string, sidebarPosition int) error {
	// Read source file
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Create destination file
	destFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()

	scanner := bufio.NewScanner(srcFile)
	writer := bufio.NewWriter(destFile)
	defer writer.Flush()

	// Track frontmatter processing
	inFrontmatter := false
	frontmatterEnded := false
	lineNum := 0

	// Regex to match markdown links that need to be converted
	// [text](ox_agent_prime.md) -> [text](./prime)
	linkRegex := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+\.md)\)`)

	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		// Detect frontmatter boundaries
		if lineNum == 1 && strings.TrimSpace(line) == "---" {
			inFrontmatter = true
			writer.WriteString(line + "\n")
			continue
		}

		if inFrontmatter && strings.TrimSpace(line) == "---" {
			// End of frontmatter, add sidebar_position before closing
			fmt.Fprintf(writer, "sidebar_position: %d\n", sidebarPosition)
			writer.WriteString(line + "\n")
			inFrontmatter = false
			frontmatterEnded = true
			continue
		}

		// If we're in frontmatter, write line as-is
		if inFrontmatter {
			writer.WriteString(line + "\n")
			continue
		}

		// Transform links in content
		if frontmatterEnded {
			// Remove SEE ALSO entries that link to skipped pages
			if isSkippedSeeAlsoLink(line) {
				continue
			}
			line = linkRegex.ReplaceAllStringFunc(line, func(match string) string {
				return transformLink(match, destPath)
			})
		}

		writer.WriteString(line + "\n")
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading source file: %w", err)
	}

	return nil
}

// transformLink converts markdown links from flat structure to hierarchical
func transformLink(link, currentFilePath string) string {
	// Extract link text and target
	linkRegex := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+\.md)\)`)
	matches := linkRegex.FindStringSubmatch(link)
	if len(matches) != 3 {
		return link
	}

	text := matches[1]
	target := matches[2]

	// Convert target from ox_agent_prime.md to hierarchical path
	base := strings.TrimSuffix(target, ".md")
	parts := strings.Split(base, "_")

	if len(parts) == 0 || parts[0] != "ox" {
		return link
	}

	// Build relative path based on current file location
	var targetPath string
	if len(parts) == 1 {
		// Link to ox.md -> root index
		targetPath = "/"
	} else {
		// Build hierarchical path
		targetPath = "/" + strings.Join(parts[1:], "/")
	}

	return fmt.Sprintf("[%s](%s)", text, targetPath)
}
