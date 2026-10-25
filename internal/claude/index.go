package claude

import (
	"bufio"
	"os"
	"strings"
)

// ParseIndex reads an index.md file and returns a map of name -> description.
// The index.md format is expected to be a simple list:
//
//	# Agents
//
//	- **agent-name**: Brief token-optimized description
//	- **other-agent**: Another description
//
// Returns empty map if file doesn't exist or parsing fails.
func ParseIndex(path string) map[string]string {
	result := make(map[string]string)

	file, err := os.Open(path)
	if err != nil {
		return result
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// look for markdown list items with bold name: - **name**: description
		if !strings.HasPrefix(line, "- **") && !strings.HasPrefix(line, "* **") {
			continue
		}

		// extract name and description
		// format: - **name**: description
		// or: - **name** - description
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")

		// find the closing ** for the name
		if !strings.HasPrefix(line, "**") {
			continue
		}
		line = strings.TrimPrefix(line, "**")

		endBold := strings.Index(line, "**")
		if endBold == -1 {
			continue
		}

		name := line[:endBold]
		rest := strings.TrimSpace(line[endBold+2:])

		// remove leading separator (: or -)
		rest = strings.TrimPrefix(rest, ":")
		rest = strings.TrimPrefix(rest, "-")
		description := strings.TrimSpace(rest)

		if name != "" && description != "" {
			result[name] = description
		}
	}

	return result
}

// ParseIndexWithTable reads an index.md file that uses a markdown table format.
// Table format:
//
//	| Name | Description |
//	|------|-------------|
//	| agent-name | Brief description |
//
// Returns empty map if file doesn't exist or parsing fails.
func ParseIndexWithTable(path string) map[string]string {
	result := make(map[string]string)

	file, err := os.Open(path)
	if err != nil {
		return result
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	inTable := false
	headerParsed := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// detect table start
		if strings.HasPrefix(line, "|") && strings.Contains(line, "|") {
			inTable = true

			// skip header row and separator
			if strings.Contains(strings.ToLower(line), "name") || strings.Contains(line, "---") {
				headerParsed = true
				continue
			}

			if !headerParsed {
				continue
			}

			// parse table row: | name | description |
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				name := strings.TrimSpace(parts[1])
				description := strings.TrimSpace(parts[2])

				// remove backticks from name if present
				name = strings.Trim(name, "`")

				if name != "" && description != "" {
					result[name] = description
				}
			}
		} else if inTable && line == "" {
			// empty line ends table
			inTable = false
			headerParsed = false
		}
	}

	return result
}
