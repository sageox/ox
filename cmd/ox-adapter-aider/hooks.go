package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

const aiderPrimeMarkerStart = "<!-- ox:prime:start -->"
const aiderPrimeMarkerEnd = "<!-- ox:prime:end -->"

// aiderPrimeBlock is the content injected into CONVENTIONS.md for Aider.
// Aider loads CONVENTIONS.md via the --read flag in .aider.conf.yml.
var aiderPrimeBlock = aiderPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"AGENT_ENV=aider ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	aiderPrimeMarkerEnd

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{
			Installed: false,
		}, fmt.Errorf("aider does not support user-level hooks (no user-level CONVENTIONS.md)")
	}

	convPath := resolveConventionsPath(p.RepoRoot)

	existing, err := os.ReadFile(convPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read CONVENTIONS.md: %w", err)
	}

	content := string(existing)

	// already installed — idempotent
	if strings.Contains(content, aiderPrimeMarkerStart) {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{convPath},
		}, nil
	}

	var newContent string
	if content == "" {
		newContent = aiderPrimeBlock + "\n"
	} else {
		newContent = strings.TrimRight(content, "\n") + "\n\n" + aiderPrimeBlock + "\n"
	}

	if err := os.WriteFile(convPath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write CONVENTIONS.md: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{convPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	convPath := resolveConventionsPath(p.RepoRoot)
	data, err := os.ReadFile(convPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	installed := strings.Contains(string(data), aiderPrimeMarkerStart)
	return &adapterprotocol.CheckHooksResponse{
		Installed: installed,
		Scope:     p.Scope,
		HookFiles: []string{convPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	convPath := resolveConventionsPath(p.RepoRoot)
	data, err := os.ReadFile(convPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
		}
		return nil, fmt.Errorf("failed to read CONVENTIONS.md: %w", err)
	}

	content := string(data)

	startIdx := strings.Index(content, aiderPrimeMarkerStart)
	if startIdx == -1 {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	endIdx := strings.Index(content, aiderPrimeMarkerEnd)
	if endIdx == -1 {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}
	endIdx += len(aiderPrimeMarkerEnd)

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
		if err := os.Remove(convPath); err != nil {
			return nil, fmt.Errorf("failed to remove empty CONVENTIONS.md: %w", err)
		}
	} else {
		if err := os.WriteFile(convPath, []byte(cleaned), 0644); err != nil {
			return nil, fmt.Errorf("failed to write CONVENTIONS.md: %w", err)
		}
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{convPath},
	}, nil
}

func resolveConventionsPath(repoRoot string) string {
	if repoRoot != "" {
		return filepath.Join(repoRoot, "CONVENTIONS.md")
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "CONVENTIONS.md")
}
