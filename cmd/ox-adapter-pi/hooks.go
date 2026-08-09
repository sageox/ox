package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// piPrimeMarkerStart / End are the Pi-specific block markers. Each adapter
// uses a unique pair so installs/uninstalls don't collide across adapters
// that share AGENTS.md. See #527.
const piPrimeMarkerStart = "<!-- ox:prime:pi:start -->"
const piPrimeMarkerEnd = "<!-- ox:prime:pi:end -->"

// piLegacyPrimeMarkerStart / End are the pre-#527 generic markers. Kept
// for backward-compat detection: check matches both; uninstall removes
// both. New installs only emit the unique markers.
const piLegacyPrimeMarkerStart = "<!-- ox:prime:start -->"
const piLegacyPrimeMarkerEnd = "<!-- ox:prime:end -->"

// piLegacyInProcessMarkerStart / End are the pre-#527 markers that only the
// in-process installer (cmd/ox/hooks_pi.go) ever emitted. This adapter did not
// recognize them, so a repo installed the old way got a SECOND block appended
// on upgrade and kept an orphan block after uninstall.
const piLegacyInProcessMarkerStart = "<!-- ox:pi-prime:start -->"
const piLegacyInProcessMarkerEnd = "<!-- ox:pi-prime:end -->"

// piPrimeBlock is the content injected into AGENTS.md for Pi coding agent.
// Pi auto-loads AGENTS.md from the project root and parent directories on every session.
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
// prime block under either the current or legacy marker pair. Legacy
// markers are recognized so pre-#527 installations are treated as
// "installed" and we don't stack a second block on top of them.
func piBlockAlreadyPresent(content string) bool {
	return strings.Contains(content, piPrimeMarkerStart) ||
		strings.Contains(content, piLegacyPrimeMarkerStart) ||
		strings.Contains(content, piLegacyInProcessMarkerStart)
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{
			Installed: false,
		}, fmt.Errorf("pi does not support user-level hooks (no user-level AGENTS.md)")
	}

	agentsPath := resolveAgentsMDPath(p.RepoRoot)

	existing, err := os.ReadFile(agentsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	content := string(existing)

	// already installed — idempotent (current or legacy markers)
	if piBlockAlreadyPresent(content) {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{agentsPath},
		}, nil
	}

	var newContent string
	if content == "" {
		newContent = piPrimeBlock + "\n"
	} else {
		newContent = strings.TrimRight(content, "\n") + "\n\n" + piPrimeBlock + "\n"
	}

	if err := fileutil.AtomicWriteBytes(agentsPath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write AGENTS.md: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{agentsPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	agentsPath := resolveAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	installed := piBlockAlreadyPresent(string(data))
	return &adapterprotocol.CheckHooksResponse{
		Installed: installed,
		Scope:     p.Scope,
		HookFiles: []string{agentsPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	agentsPath := resolveAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
		}
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	content := string(data)

	// remove both the current-marker block and any legacy-marker block —
	// a pre-#527 installation may carry the generic <!-- ox:prime:start -->
	// pair that we no longer emit.
	cleaned := removePrimeBlock(content, piPrimeMarkerStart, piPrimeMarkerEnd)
	cleaned = removePrimeBlock(cleaned, piLegacyPrimeMarkerStart, piLegacyPrimeMarkerEnd)
	cleaned = removePrimeBlock(cleaned, piLegacyInProcessMarkerStart, piLegacyInProcessMarkerEnd)

	if cleaned == content {
		// nothing to uninstall
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(agentsPath); err != nil {
			return nil, fmt.Errorf("failed to remove empty AGENTS.md: %w", err)
		}
	} else {
		if err := fileutil.AtomicWriteBytes(agentsPath, []byte(cleaned), 0644); err != nil {
			return nil, fmt.Errorf("failed to write AGENTS.md: %w", err)
		}
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{agentsPath},
	}, nil
}

func resolveAgentsMDPath(repoRoot string) string {
	if repoRoot == "" {
		log.Println("WARN: resolveAgentsMDPath called with empty repoRoot, falling back to cwd")
		repoRoot, _ = os.Getwd()
	}
	return filepath.Join(repoRoot, "AGENTS.md")
}

// removePrimeBlock strips one start...end block (inclusive) from content,
// collapsing surrounding blank lines so no orphan whitespace remains.
// Returns content unchanged if either marker is absent, or if no end
// marker appears AFTER the start marker (which would indicate an orphan
// end marker earlier in the file — hand-edits, partial pastes, merge
// accidents can all produce this; we refuse to operate rather than
// silently delete arbitrary content between an orphan end and a real
// start). CodeRabbit review on #543.
func removePrimeBlock(content, startMarker, endMarker string) string {
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
