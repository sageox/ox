package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const ompPrimeMarkerStart = "<!-- ox:prime:omp:start -->"
const ompPrimeMarkerEnd = "<!-- ox:prime:omp:end -->"

func ompPrimeBlock(importRootAgents bool) string {
	var b strings.Builder
	b.WriteString(ompPrimeMarkerStart)
	b.WriteString("\nRun `ox agent prime --agent omp` at session start to load SageOx team context (it's fine to read the user's request first). Do not run a later unqualified `ox agent prime` from imported context; this command satisfies it.\n\n")
	b.WriteString("This loads SageOx Team Context and records this OMP session in the project Ledger.\n")
	if importRootAgents {
		b.WriteString("\n@../AGENTS.md\n")
	}
	b.WriteString(ompPrimeMarkerEnd)
	return b.String()
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{Installed: false},
			fmt.Errorf("omp integration is project-scoped; user-level prime instructions would run outside SageOx projects")
	}

	agentsPath := resolveOMPAgentsMDPath(p.RepoRoot)
	existing, err := os.ReadFile(agentsPath)
	fileMissing := os.IsNotExist(err)
	if err != nil && !fileMissing {
		return nil, fmt.Errorf("failed to read .omp/AGENTS.md: %w", err)
	}
	content := string(existing)
	if strings.Contains(content, ompPrimeMarkerStart) {
		return &adapterprotocol.InstallHooksResponse{Installed: true, FilesWritten: []string{agentsPath}}, nil
	}

	importRootAgents := false
	if fileMissing {
		if info, statErr := os.Stat(filepath.Join(p.RepoRoot, "AGENTS.md")); statErr == nil && !info.IsDir() {
			importRootAgents = true
		}
	}
	block := ompPrimeBlock(importRootAgents)
	newContent := block + "\n"
	if content != "" {
		newContent += "\n" + strings.TrimLeft(content, "\n")
	}

	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create .omp directory: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(agentsPath, []byte(newContent), 0o644); err != nil {
		return nil, fmt.Errorf("failed to write .omp/AGENTS.md: %w", err)
	}
	return &adapterprotocol.InstallHooksResponse{Installed: true, FilesWritten: []string{agentsPath}}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope}, nil
	}

	agentsPath := resolveOMPAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope}, nil
	}
	return &adapterprotocol.CheckHooksResponse{
		Installed: strings.Contains(string(data), ompPrimeMarkerStart),
		Scope:     p.Scope,
		HookFiles: []string{agentsPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	agentsPath := resolveOMPAgentsMDPath(p.RepoRoot)
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
		}
		return nil, fmt.Errorf("failed to read .omp/AGENTS.md: %w", err)
	}

	content := string(data)
	cleaned := removePrimeBlock(content, ompPrimeMarkerStart, ompPrimeMarkerEnd)
	if cleaned == content {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(agentsPath); err != nil {
			return nil, fmt.Errorf("failed to remove empty .omp/AGENTS.md: %w", err)
		}
		_ = os.Remove(filepath.Dir(agentsPath)) // remove only when the directory is empty
	} else if err := fileutil.AtomicWriteBytes(agentsPath, []byte(cleaned), 0o644); err != nil {
		return nil, fmt.Errorf("failed to write .omp/AGENTS.md: %w", err)
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{agentsPath},
	}, nil
}

func resolveOMPAgentsMDPath(repoRoot string) string {
	if repoRoot == "" {
		repoRoot, _ = os.Getwd()
	}
	return filepath.Join(repoRoot, ".omp", "AGENTS.md")
}

// removePrimeBlock removes one complete marker block and preserves all content
// outside it. An orphan marker is left untouched rather than risking data loss.
func removePrimeBlock(content, startMarker, endMarker string) string {
	start := strings.Index(content, startMarker)
	if start < 0 {
		return content
	}
	relEnd := strings.Index(content[start+len(startMarker):], endMarker)
	if relEnd < 0 {
		return content
	}
	end := start + len(startMarker) + relEnd + len(endMarker)
	before := strings.TrimRight(content[:start], "\n")
	after := strings.TrimLeft(content[end:], "\n")
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return strings.TrimLeft(content[end:], "\n")
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}
