package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/ui"
)

const agentsMDFileName = "AGENTS.md"

const ampPrimeMarkerStart = "<!-- ox:prime:start -->"
const ampPrimeMarkerEnd = "<!-- ox:prime:end -->"

// ampPrimeBlock is the content injected into AGENTS.md for Amp CLI.
// Amp auto-loads AGENTS.md from the project root on every session.
var ampPrimeBlock = ampPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"AGENT_ENV=amp ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	ampPrimeMarkerEnd

// resolveAgentsMDPath returns the path to AGENTS.md at the git root.
// Falls back to cwd if not in a git repo.
func resolveAgentsMDPath() (string, error) {
	root := findGitRoot()
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		root = cwd
	}
	return filepath.Join(root, agentsMDFileName), nil
}

// hasAmpHooks checks if the Amp CLI ox prime marker exists.
// user=true always returns false (no user-level AGENTS.md for Amp).
// user=false checks the project-level AGENTS.md for the marker.
func hasAmpHooks(user bool) bool {
	if user {
		return false
	}

	agentsPath, err := resolveAgentsMDPath()
	if err != nil {
		return false
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return false
	}

	return strings.Contains(string(content), ampPrimeMarkerStart)
}

// installAmpHooks installs the ox prime marker block into AGENTS.md.
// user=true is a no-op (Amp has no user-level config file).
// user=false creates or appends to the project-level AGENTS.md.
func installAmpHooks(user bool) error {
	if user {
		fmt.Println("Amp CLI does not support user-level integration (no user-level AGENTS.md)")
		return nil
	}

	agentsPath, err := resolveAgentsMDPath()
	if err != nil {
		return err
	}

	// read existing content if file exists
	existing, err := os.ReadFile(agentsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", agentsMDFileName, err)
	}

	content := string(existing)

	// already installed
	if strings.Contains(content, ampPrimeMarkerStart) {
		fmt.Println(ui.PassStyle.Render("✓") + " Amp CLI integration already installed in " + agentsPath)
		return nil
	}

	var newContent string
	if content == "" {
		// create new file with just the marker block
		newContent = ampPrimeBlock + "\n"
	} else {
		// append to existing content with separator
		newContent = strings.TrimRight(content, "\n") + "\n\n" + ampPrimeBlock + "\n"
	}

	if err := os.WriteFile(agentsPath, []byte(newContent), sharedSettingsPerm); err != nil {
		return fmt.Errorf("failed to write %s: %w", agentsMDFileName, err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Amp CLI integration installed in " + agentsPath)
	return nil
}

// uninstallAmpHooks removes the ox prime marker block from AGENTS.md.
// user=true is a no-op.
// user=false removes the marker block from the project-level AGENTS.md.
func uninstallAmpHooks(user bool) error {
	if user {
		return nil
	}

	agentsPath, err := resolveAgentsMDPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Amp CLI integration not found (no " + agentsMDFileName + ")")
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", agentsMDFileName, err)
	}

	content := string(data)

	startIdx := strings.Index(content, ampPrimeMarkerStart)
	if startIdx == -1 {
		fmt.Println("Amp CLI integration not found in " + agentsPath)
		return nil
	}

	endIdx := strings.Index(content, ampPrimeMarkerEnd)
	if endIdx == -1 {
		fmt.Println("Amp CLI integration not found in " + agentsPath)
		return nil
	}
	endIdx += len(ampPrimeMarkerEnd)

	// remove the block plus any surrounding blank lines
	before := content[:startIdx]
	after := content[endIdx:]

	// trim trailing newlines from before and leading newlines from after
	before = strings.TrimRight(before, "\n")
	after = strings.TrimLeft(after, "\n")

	var cleaned string
	if before == "" && after == "" {
		cleaned = ""
	} else if before == "" {
		cleaned = after + "\n"
	} else if after == "" {
		cleaned = before + "\n"
	} else {
		cleaned = before + "\n\n" + after + "\n"
	}

	// if file is empty after removal, delete it
	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(agentsPath); err != nil {
			return fmt.Errorf("failed to remove empty %s: %w", agentsMDFileName, err)
		}
		fmt.Println(ui.PassStyle.Render("✓") + " Amp CLI integration removed (deleted empty " + agentsMDFileName + ")")
		return nil
	}

	if err := os.WriteFile(agentsPath, []byte(cleaned), sharedSettingsPerm); err != nil {
		return fmt.Errorf("failed to write %s: %w", agentsMDFileName, err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Amp CLI integration removed from " + agentsPath)
	return nil
}

// listAmpHooks returns the installation status of Amp CLI hooks.
func listAmpHooks() map[string]bool {
	return map[string]bool{
		"Project": hasAmpHooks(false),
		"User":    false,
	}
}
