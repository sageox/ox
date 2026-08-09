package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/ui"
)

// piPrimeMarkerStart / End are the Pi-specific block markers. Must match
// the unique marker the external ox-adapter-pi binary emits so that the
// in-process install path (ox integrate install --pi) and the external
// adapter-protocol path (ox init with pi selected) share one block
// instead of each writing their own. See #527.
const piPrimeMarkerStart = "<!-- ox:prime:pi:start -->"
const piPrimeMarkerEnd = "<!-- ox:prime:pi:end -->"

// piLegacyInProcessMarkerStart / End are the pre-#527 in-process markers
// that only ever appeared in hooks_pi.go (distinct from the generic
// <!-- ox:prime:start --> pair used by the external adapter before it
// was also unified). Kept for backward-compat detection.
const piLegacyInProcessMarkerStart = "<!-- ox:pi-prime:start -->"
const piLegacyInProcessMarkerEnd = "<!-- ox:pi-prime:end -->"

// piLegacyGenericMarkerStart / End are the pre-#527 generic markers the
// external adapter used to emit, which may exist in older repos.
const piLegacyGenericMarkerStart = "<!-- ox:prime:start -->"
const piLegacyGenericMarkerEnd = "<!-- ox:prime:end -->"

// piPrimeBlock is the content injected into AGENTS.md for Pi.
// Pi auto-loads AGENTS.md from the project root on every session.
//
// This must stay byte-identical to piPrimeBlock in cmd/ox-adapter-pi/hooks.go.
// The two paths (ox integrate install --pi vs ox init) write the same file, and
// when the text diverges an upgrade appends a second block instead of matching
// the first.
//
// NOTE: the prime command is intentionally adapter-agnostic — no hardcoded
// AGENT_ENV=<adapter> prefix. AGENTS.md is often shared across agents (e.g.
// via a CLAUDE.md symlink), so any block that mis-routes AGENT_ENV poisons
// sessions running a different coding agent. Runtime detection in
// agentx.CurrentAgent handles agent identification correctly. See #527.
var piPrimeBlock = piPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	piPrimeMarkerEnd

// piBlockAlreadyPresent reports whether AGENTS.md already carries a Pi
// prime block under any recognized marker pair: the current
// <!-- ox:prime:pi:* --> pair, the in-process legacy <!-- ox:pi-prime:* -->
// pair (that only hooks_pi.go ever emitted), or the generic legacy
// <!-- ox:prime:* --> pair the external adapter used to emit. All three
// are accepted so we don't stack a duplicate block on top of any prior
// installation style.
func piBlockAlreadyPresent(content string) bool {
	return strings.Contains(content, piPrimeMarkerStart) ||
		strings.Contains(content, piLegacyInProcessMarkerStart) ||
		strings.Contains(content, piLegacyGenericMarkerStart)
}

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

	return piBlockAlreadyPresent(string(content))
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

	// already installed (current or legacy markers)
	if piBlockAlreadyPresent(content) {
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

	if err := fileutil.AtomicWriteBytes(agentsPath, []byte(newContent), sharedSettingsPerm); err != nil {
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

	// remove every recognized Pi block (current + both legacy forms) so
	// a single uninstall fully cleans up any install era.
	cleaned := removePiPrimeBlock(content, piPrimeMarkerStart, piPrimeMarkerEnd)
	cleaned = removePiPrimeBlock(cleaned, piLegacyInProcessMarkerStart, piLegacyInProcessMarkerEnd)
	cleaned = removePiPrimeBlock(cleaned, piLegacyGenericMarkerStart, piLegacyGenericMarkerEnd)

	if cleaned == content {
		fmt.Println("Pi integration not found in " + agentsPath)
		return nil
	}

	// if file is empty after removal, delete it
	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(agentsPath); err != nil {
			return fmt.Errorf("failed to remove empty %s: %w", agentsMDFileName, err)
		}
		fmt.Println(ui.PassStyle.Render("✓") + " Pi integration removed (deleted empty " + agentsMDFileName + ")")
		return nil
	}

	if err := fileutil.AtomicWriteBytes(agentsPath, []byte(cleaned), sharedSettingsPerm); err != nil {
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

// removePiPrimeBlock strips one start...end block (inclusive) from content,
// collapsing surrounding blank lines so no orphan whitespace remains.
// Returns content unchanged if either marker is absent, or if no end
// marker appears AFTER the start marker (which would indicate an orphan
// end marker earlier in the file — refuses to operate rather than
// silently delete content between an orphan end and a real start).
// Named with the pi-prefix to avoid collision with similarly-named
// helpers in other cmd/ox files; logic matches the external-adapter
// helpers. CodeRabbit review on #543.
func removePiPrimeBlock(content, startMarker, endMarker string) string {
	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return content
	}
	// search for endMarker AFTER the start marker so an orphan end marker
	// earlier in the file can't form an inverted range
	rel := strings.Index(content[startIdx+len(startMarker):], endMarker)
	if rel == -1 {
		return content
	}
	endIdx := startIdx + len(startMarker) + rel + len(endMarker)

	before := strings.TrimRight(content[:startIdx], "\n")
	after := strings.TrimLeft(content[endIdx:], "\n")

	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after + "\n"
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}
