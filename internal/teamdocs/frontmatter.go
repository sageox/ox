package teamdocs

import (
	"bufio"
	"os"
	"strings"
)

// docFrontmatter holds the parsed YAML frontmatter fields from a team doc.
type docFrontmatter struct {
	Title       string
	Description string
	Visibility  string
	When        string
}

// parseFrontmatter extracts team doc metadata from YAML frontmatter.
//
// Follows the existing parsing pattern from internal/claude/agents.go
// (parseAgentFrontmatter) — simple line-by-line extraction without a
// full YAML library. This avoids pulling in gopkg.in/yaml.v3 for 4
// simple key extractions. Multi-line 'when' values (YAML >- folded
// scalar) are handled by reading indented continuation lines.
//
// Returns zero-value docFrontmatter if no frontmatter is found.
func parseFrontmatter(path string) docFrontmatter {
	file, err := os.Open(path)
	if err != nil {
		return docFrontmatter{}
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0
	var fm docFrontmatter

	// track which field is accumulating multi-line content
	var multiLineTarget *string

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// frontmatter must start on line 1 with ---
		if lineCount == 1 {
			if line == "---" {
				inFrontmatter = true
				continue
			}
			return docFrontmatter{} // no frontmatter
		}

		// closing delimiter
		if inFrontmatter && line == "---" {
			break
		}

		if !inFrontmatter {
			break
		}

		// multi-line continuation: indented lines append to current field
		if multiLineTarget != nil && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			val := strings.TrimSpace(line)
			if val != "" {
				if *multiLineTarget != "" {
					*multiLineTarget += " "
				}
				*multiLineTarget += val
			}
			if lineCount > 30 {
				break
			}
			continue
		}

		// new key: stop multi-line accumulation
		multiLineTarget = nil

		if strings.HasPrefix(line, "title:") {
			fm.Title = extractValue(line, "title:")
		} else if strings.HasPrefix(line, "description:") {
			fm.Description = extractValue(line, "description:")
		} else if strings.HasPrefix(line, "visibility:") {
			fm.Visibility = extractValue(line, "visibility:")
		} else if strings.HasPrefix(line, "when:") {
			val := extractValue(line, "when:")
			if val == ">-" || val == ">" || val == "|" || val == "|-" {
				// YAML block scalar — read continuation lines
				fm.When = ""
				multiLineTarget = &fm.When
			} else {
				fm.When = val
			}
		}

		if lineCount > 30 {
			break
		}
	}

	return fm
}

// extractValue gets the trimmed value after a "key:" prefix, stripping quotes.
func extractValue(line, prefix string) string {
	val := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	val = strings.Trim(val, `"'`)
	return val
}
