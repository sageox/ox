// NOTE: This client-side discovery could potentially be moved server-side
// in the future. The server could provide command metadata via API, which would
// enable centralized management and reduce client complexity. For now, we
// discover commands from the local team context filesystem.

package claude

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverTeamCommands finds all slash commands in a team context.
// It checks index.md first for token-optimized descriptions, then falls back
// to parsing individual command files for frontmatter.
//
// Expected directory structure:
//
//	<team_context>/coworkers/commands/
//	├── index.md          # Optional: token-optimized descriptions
//	├── deploy.md         # /deploy command
//	└── review-pr.md      # /review-pr command
func DiscoverTeamCommands(teamPath string) ([]Command, error) {
	commandsDir := filepath.Join(teamPath, CommandsDir)

	if _, err := os.Stat(commandsDir); os.IsNotExist(err) {
		return nil, nil
	}

	// load index.md for optimized descriptions (preferred)
	indexDescriptions := ParseIndex(filepath.Join(commandsDir, "index.md"))

	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		return nil, err
	}

	var commands []Command
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "index.md" {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		cmdPath := filepath.Join(commandsDir, entry.Name())

		cmd := Command{
			Name:    name,
			Trigger: "/" + name,
			Path:    cmdPath,
		}

		// prefer index.md description (token-optimized)
		if desc, ok := indexDescriptions[name]; ok {
			cmd.Description = desc
			cmd.FromIndex = true
		} else {
			// fallback to parsing file frontmatter
			fm := parseCommandFrontmatter(cmdPath)
			if fm.name != "" {
				cmd.Name = fm.name
			}
			if fm.trigger != "" {
				cmd.Trigger = fm.trigger
			}
			if fm.description != "" {
				cmd.Description = fm.description
			}
		}

		commands = append(commands, cmd)
	}

	return commands, nil
}

// commandFrontmatter holds parsed frontmatter fields from a command file.
type commandFrontmatter struct {
	name        string
	description string
	trigger     string
}

// parseCommandFrontmatter extracts command metadata from YAML frontmatter.
// Expected format:
//
//	---
//	name: deploy
//	description: Deploy to production environment
//	trigger: /deploy
//	---
func parseCommandFrontmatter(path string) commandFrontmatter {
	var fm commandFrontmatter

	file, err := os.Open(path)
	if err != nil {
		return fm
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// frontmatter starts with ---
		if lineCount == 1 && line == "---" {
			inFrontmatter = true
			continue
		}

		// frontmatter ends with ---
		if inFrontmatter && line == "---" {
			break
		}

		if inFrontmatter {
			if strings.HasPrefix(line, "name:") {
				fm.name = extractFrontmatterValue(line, "name:")
			}
			if strings.HasPrefix(line, "description:") {
				fm.description = extractFrontmatterValue(line, "description:")
			}
			if strings.HasPrefix(line, "trigger:") {
				fm.trigger = extractFrontmatterValue(line, "trigger:")
			}
		}

		// stop after 20 lines to avoid scanning entire file
		if lineCount > 20 {
			break
		}
	}

	return fm
}

// extractFrontmatterValue extracts the value from a "key: value" line.
func extractFrontmatterValue(line, prefix string) string {
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	return strings.Trim(value, `"'`)
}
