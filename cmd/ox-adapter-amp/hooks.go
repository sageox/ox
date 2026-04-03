package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

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

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{
			Installed: false,
		}, fmt.Errorf("amp does not support user-level hooks (no user-level AGENTS.md)")
	}

	agentsPath := resolveAgentsMDPath(p.RepoRoot)

	existing, err := os.ReadFile(agentsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	content := string(existing)

	// already installed — idempotent
	if strings.Contains(content, ampPrimeMarkerStart) {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{agentsPath},
		}, nil
	}

	var newContent string
	if content == "" {
		newContent = ampPrimeBlock + "\n"
	} else {
		newContent = strings.TrimRight(content, "\n") + "\n\n" + ampPrimeBlock + "\n"
	}

	if err := os.WriteFile(agentsPath, []byte(newContent), 0644); err != nil {
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

	installed := strings.Contains(string(data), ampPrimeMarkerStart)
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

	startIdx := strings.Index(content, ampPrimeMarkerStart)
	if startIdx == -1 {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	endIdx := strings.Index(content, ampPrimeMarkerEnd)
	if endIdx == -1 {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}
	endIdx += len(ampPrimeMarkerEnd)

	before := strings.TrimRight(content[:startIdx], "\n")
	after := strings.TrimLeft(content[endIdx:], "\n")

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

	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(agentsPath); err != nil {
			return nil, fmt.Errorf("failed to remove empty AGENTS.md: %w", err)
		}
	} else {
		if err := os.WriteFile(agentsPath, []byte(cleaned), 0644); err != nil {
			return nil, fmt.Errorf("failed to write AGENTS.md: %w", err)
		}
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{agentsPath},
	}, nil
}

func resolveAgentsMDPath(repoRoot string) string {
	if repoRoot != "" {
		return filepath.Join(repoRoot, "AGENTS.md")
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "AGENTS.md")
}
