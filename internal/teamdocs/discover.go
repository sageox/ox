package teamdocs

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// DiscoverDocs scans the docs/ directory in a team context for markdown files
// and returns a catalog of documents visible to agents.
//
// Only .md files are processed. Rationale:
//   - Agents read markdown natively — no conversion tooling needed
//   - YAML frontmatter is a markdown convention with universal tooling support
//     (Jekyll, Hugo, Obsidian, VS Code, Agent Skills spec)
//   - Token estimation is trivial for text content
//   - Non-markdown assets (images, PDFs, data files) need entirely different
//     disclosure mechanisms and are out of scope for this catalog
//
// README.md is always excluded — it contains instructions about the directory
// itself, not content for agents. Server-side hardcodes this as hidden.
//
// Returns only docs with visibility != "hidden". Sorted by filename for
// stable, deterministic output.
func DiscoverDocs(teamPath string) ([]TeamDoc, error) {
	docsDir := filepath.Join(teamPath, "docs")

	entries, err := os.ReadDir(docsDir)
	if err != nil {
		// missing docs/ directory is not an error — team may not have docs yet
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var docs []TeamDoc

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		// only process markdown files
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}

		// README.md is always hidden — it's instructions about the directory,
		// not content for agents. This is hardcoded rather than relying on
		// frontmatter so new teams don't accidentally expose their README.
		if strings.EqualFold(name, "readme.md") {
			continue
		}

		fullPath := filepath.Join(docsDir, name)
		doc := buildDoc(name, fullPath)

		// filter out hidden docs
		if doc.Visibility == VisibilityHidden {
			continue
		}

		docs = append(docs, doc)
	}

	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Name < docs[j].Name
	})

	return docs, nil
}

// buildDoc constructs a TeamDoc from a markdown file, parsing frontmatter
// and falling back to content extraction for missing fields.
func buildDoc(name, path string) TeamDoc {
	fm := parseFrontmatter(path)

	doc := TeamDoc{
		Name:        name,
		Title:       fm.Title,
		Description: fm.Description,
		Path:        path,
		When:        fm.When,
		Visibility:  fm.Visibility,
	}

	// apply fallbacks for missing fields
	if doc.Title == "" {
		doc.Title = extractTitleFromContent(path)
	}
	if doc.Title == "" {
		doc.Title = titleFromFilename(name)
	}

	if doc.Description == "" {
		doc.Description = extractDescriptionFromContent(path)
	}

	if doc.Visibility == "" {
		doc.Visibility = DefaultVisibility
	}

	// treat "always" as "indexed" until auto-inlining is implemented
	// (visibility: always is a valid value, accepted now for forward compatibility)
	if doc.Visibility == VisibilityAlways {
		doc.Visibility = VisibilityIndexed
	}

	return doc
}

// extractTitleFromContent finds the first H1 heading (# Title) in markdown content.
// Skips frontmatter if present. Returns empty string if no heading found.
func extractTitleFromContent(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// skip frontmatter block
		if lineCount == 1 && line == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter {
			if line == "---" {
				inFrontmatter = false
			}
			continue
		}

		// look for # Heading (H1 only)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}

		// stop scanning after 50 lines — if no H1 by then, give up
		if lineCount > 50 {
			break
		}
	}

	return ""
}

// extractDescriptionFromContent extracts the first non-heading, non-empty paragraph
// from markdown content. Skips frontmatter. Truncates to 160 characters.
func extractDescriptionFromContent(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// skip frontmatter
		if lineCount == 1 && line == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter {
			if line == "---" {
				inFrontmatter = false
			}
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// skip headings, horizontal rules, and list items
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "| ") {
			// stop scanning after 50 lines to avoid reading entire large files
			if lineCount > 50 {
				break
			}
			continue
		}

		// found a paragraph line — use rune-safe truncation to avoid
		// splitting multi-byte UTF-8 characters (e.g., emoji, CJK)
		runes := []rune(trimmed)
		if len(runes) > 160 {
			return string(runes[:157]) + "..."
		}
		return trimmed
	}

	return ""
}

// titleFromFilename converts a filename to a human-readable title.
// "api-conventions.md" → "Api Conventions"
func titleFromFilename(name string) string {
	name = strings.TrimSuffix(name, ".md")
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")

	// title case: capitalize first letter of each word
	words := strings.Fields(name)
	for i, w := range words {
		if len(w) > 0 {
			runes := []rune(w)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}
