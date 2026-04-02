package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/ui"
)

const piPrimeMarkerStart = "<!-- ox:pi-prime:start -->"
const piPrimeMarkerEnd = "<!-- ox:pi-prime:end -->"

// piPrimeBlock is the content injected into AGENTS.md for Pi.
// Pi auto-loads AGENTS.md from the project root on every session.
var piPrimeBlock = piPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"AGENT_ENV=pi ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	"\n" +
	"For full lifecycle integration (whispers, session recording, auto-prime), install the SageOx Pi extension:\n" +
	"```bash\n" +
	"pi install npm:@sageox/pi-ox\n" +
	"```\n" +
	piPrimeMarkerEnd

// hasPiHooks checks if the Pi ox prime marker exists in AGENTS.md.
// user=true always returns false (no user-level AGENTS.md for Pi).
// user=false checks the project-level AGENTS.md for the marker.
func hasPiHooks(user bool) bool {
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

	return strings.Contains(string(content), piPrimeMarkerStart)
}

// installPiHooks installs the ox prime marker block into AGENTS.md for Pi.
// user=true is a no-op (Pi has no user-level config file for AGENTS.md).
// user=false creates or appends to the project-level AGENTS.md.
func installPiHooks(user bool) error {
	if user {
		fmt.Println("Pi does not support user-level integration (no user-level AGENTS.md)")
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
	if strings.Contains(content, piPrimeMarkerStart) {
		fmt.Println(ui.PassStyle.Render("✓") + " Pi integration already installed in " + agentsPath)
		return nil
	}

	var newContent string
	if content == "" {
		// create new file with just the marker block
		newContent = piPrimeBlock + "\n"
	} else {
		// append to existing content with separator
		newContent = strings.TrimRight(content, "\n") + "\n\n" + piPrimeBlock + "\n"
	}

	if err := os.WriteFile(agentsPath, []byte(newContent), sharedSettingsPerm); err != nil {
		return fmt.Errorf("failed to write %s: %w", agentsMDFileName, err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Pi integration installed in " + agentsPath)
	return nil
}

// uninstallPiHooks removes the ox prime marker block from AGENTS.md for Pi.
// user=true is a no-op.
// user=false removes the marker block from the project-level AGENTS.md.
func uninstallPiHooks(user bool) error {
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
			fmt.Println("Pi integration not found (no " + agentsMDFileName + ")")
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", agentsMDFileName, err)
	}

	content := string(data)

	startIdx := strings.Index(content, piPrimeMarkerStart)
	if startIdx == -1 {
		fmt.Println("Pi integration not found in " + agentsPath)
		return nil
	}

	endIdx := strings.Index(content, piPrimeMarkerEnd)
	if endIdx == -1 {
		fmt.Println("Pi integration not found in " + agentsPath)
		return nil
	}
	endIdx += len(piPrimeMarkerEnd)

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
		fmt.Println(ui.PassStyle.Render("✓") + " Pi integration removed (deleted empty " + agentsMDFileName + ")")
		return nil
	}

	if err := os.WriteFile(agentsPath, []byte(cleaned), sharedSettingsPerm); err != nil {
		return fmt.Errorf("failed to write %s: %w", agentsMDFileName, err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Pi integration removed from " + agentsPath)
	return nil
}

// listPiHooks returns the installation status of Pi hooks.
func listPiHooks() map[string]bool {
	return map[string]bool{
		"Project": hasPiHooks(false),
		"User":    false,
	}
}
