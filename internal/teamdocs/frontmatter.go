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
	// Template marks an ox-shipped starter doc the team hasn't filled in yet.
	// Forward-compatible: when the cloud stamps starters with `template: true`,
	// discovery skips them so unedited scaffolds never surface as knowledge.
	Template bool
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
		} else if strings.HasPrefix(line, "template:") {
			fm.Template = strings.EqualFold(extractValue(line, "template:"), "true")
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
	val := stripYAMLComment(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
	val = strings.Trim(val, `"'`)
	return val
}

// stripYAMLComment removes a trailing ` #` comment from a scalar value.
//
// Without this, `globs: **/*.go,**/*.mod   # matches Cursor` parsed the comment
// as part of the value and produced glob entries named "Cursor" and "Copilot" —
// a rule scoped to files that do not exist, which silently never applies. The
// guide's own examples carried such comments, so anyone copying them got a rule
// that looked scoped and was not.
//
// A value that OPENS with a quote is returned through its closing quote, because
// inside quotes a `#` is literal YAML and truncating there would corrupt a
// description that legitimately contains one.
func stripYAMLComment(v string) string {
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if end := closingQuote(v); end > 0 {
			return v[:end+1]
		}
		return v // unterminated quote: leave it alone rather than guess
	}
	// A flow sequence has to be scanned to its matching bracket before any
	// comment can be found, because a # inside one of its quoted entries is
	// literal. Cutting at the first " #" turned globs: ["**/*.go # generated"]
	// into the unparseable ["**/*.go — telling the agent a scope that does not
	// match the files the author meant.
	if len(v) > 0 && v[0] == '[' {
		if end := closingBracket(v); end > 0 {
			return v[:end+1]
		}
		return v // unterminated sequence: leave it alone rather than guess
	}
	if idx := strings.Index(v, " #"); idx >= 0 {
		return strings.TrimSpace(v[:idx])
	}
	if strings.HasPrefix(v, "#") {
		return "" // the whole value is a comment
	}
	return v
}

// closingBracket returns the index of the ] that closes the flow sequence
// opened at v[0], or -1 when it is unterminated. Brackets inside quoted entries
// do not count, and nesting is tracked so a sequence of sequences survives.
func closingBracket(v string) int {
	depth := 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\'', '"':
			end := closingQuote(v[i:])
			if end < 0 {
				return -1 // unterminated quote inside the sequence
			}
			i += end
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// closingQuote returns the index of the quote that ends the scalar opened at
// v[0], or -1 when it is unterminated.
//
// Both escape forms have to be honored or the value is silently truncated at
// its first inner quote: YAML doubles a single quote ('It”s') and backslash-
// escapes a double quote ("say \"hi\""). Truncating there turns a description
// into a fragment, and the reader has no way to tell it happened.
func closingQuote(v string) int {
	quote := v[0]
	for i := 1; i < len(v); i++ {
		switch {
		case quote == '\'' && v[i] == '\'':
			if i+1 < len(v) && v[i+1] == '\'' {
				i++ // '' is one literal quote, not the end
				continue
			}
			return i
		case quote == '"' && v[i] == '\\':
			i++ // skip whatever this escapes, including \"
		case quote == '"' && v[i] == '"':
			return i
		}
	}
	return -1
}
