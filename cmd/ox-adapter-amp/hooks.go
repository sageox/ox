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

// ampPrimeMarkerStart / End are the Amp-specific block markers. Each
// adapter uses a unique pair so installs/uninstalls don't collide across
// adapters that share AGENTS.md. See #527.
const ampPrimeMarkerStart = "<!-- ox:prime:amp:start -->"
const ampPrimeMarkerEnd = "<!-- ox:prime:amp:end -->"

// ampLegacyPrimeMarkerStart / End are the pre-#527 generic markers. Kept
// for backward-compat detection: check matches both; uninstall removes
// both. New installs only emit the unique markers.
const ampLegacyPrimeMarkerStart = "<!-- ox:prime:start -->"
const ampLegacyPrimeMarkerEnd = "<!-- ox:prime:end -->"

// ampPrimeBlock is the content injected into AGENTS.md for Amp CLI.
// Amp auto-loads AGENTS.md from the project root on every session.
//
// NOTE: the prime command is intentionally adapter-agnostic — no hardcoded
// AGENT_ENV=<adapter> prefix. AGENTS.md is often shared across agents, so
// any block that mis-routes AGENT_ENV poisons sessions running a different
// coding agent. Runtime detection in agentx.CurrentAgent handles agent
// identification correctly. See #527.
var ampPrimeBlock = ampPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	ampPrimeMarkerEnd

// ampBlockAlreadyPresent reports whether AGENTS.md already carries an Amp
// prime block under either the current or legacy marker pair. Legacy
// markers are recognized so pre-#527 installations are treated as
// "installed" and we don't stack a second block on top of them.
func ampBlockAlreadyPresent(content string) bool {
	return strings.Contains(content, ampPrimeMarkerStart) ||
		strings.Contains(content, ampLegacyPrimeMarkerStart)
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{
			Installed: false,
		}, fmt.Errorf("amp does not support user-level hooks (no user-level AGENTS.md)")
	}

	// Project: AGENTS.md marker block (idempotent).
	agentsPath := resolveAgentsMDPath(p.RepoRoot)
	existing, err := os.ReadFile(agentsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	content := string(existing)
	if !ampBlockAlreadyPresent(content) {
		var newContent string
		if content == "" {
			newContent = ampPrimeBlock + "\n"
		} else {
			newContent = strings.TrimRight(content, "\n") + "\n\n" + ampPrimeBlock + "\n"
		}
		if err := fileutil.AtomicWriteBytes(agentsPath, []byte(newContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write AGENTS.md: %w", err)
		}
	}

	// User-global: ox-bridge.ts plugin. Installed once per machine
	// regardless of which project triggered install-hooks — Amp loads
	// ~/.config/amp/plugins/ for every session, so we don't need (and
	// shouldn't) drop a plugin file into each repo.
	bridgePath, err := userBridgePluginPath()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve plugin path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(bridgePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin dir: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(bridgePath, oxBridgePluginSrc, 0644); err != nil {
		return nil, fmt.Errorf("failed to write ox-bridge plugin: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{agentsPath, bridgePath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope}, nil
	}

	agentsPath := resolveAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope}, nil
	}

	return &adapterprotocol.CheckHooksResponse{
		Installed: ampBlockAlreadyPresent(string(data)),
		Scope:     p.Scope,
		HookFiles: []string{agentsPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	// Only project-scope state is touched here. The user-global bridge
	// plugin is intentionally left in place — other repos on the same
	// machine may still be using it.
	agentsPath := resolveAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
		}
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	content := string(data)
	cleaned := removePrimeBlock(content, ampPrimeMarkerStart, ampPrimeMarkerEnd)
	cleaned = removePrimeBlock(cleaned, ampLegacyPrimeMarkerStart, ampLegacyPrimeMarkerEnd)

	if cleaned == content {
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
