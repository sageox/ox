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
		// marker present: refresh the block in place if its wording drifted (e.g. the old
		// imperative "BLOCKING … NOW" text), preserving the original @../AGENTS.md import
		// decision and everything outside the markers. Skip read-only files.
		refreshed, changed := refreshOMPPrimeBlock(content)
		if !changed {
			return &adapterprotocol.InstallHooksResponse{Installed: true, FilesWritten: []string{agentsPath}}, nil
		}
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(agentsPath); statErr == nil {
			if info.Mode().Perm()&0o200 == 0 {
				return &adapterprotocol.InstallHooksResponse{Installed: true, FilesWritten: []string{agentsPath}}, nil
			}
			mode = info.Mode().Perm()
		}
		// guard against a lost update: if the file changed since we read it, skip the refresh
		// rather than clobber the newer content — the next reconcile retries.
		if cur, rerr := os.ReadFile(agentsPath); rerr != nil || string(cur) != content {
			return &adapterprotocol.InstallHooksResponse{Installed: true, FilesWritten: []string{agentsPath}}, nil
		}
		if err := fileutil.AtomicWriteBytes(agentsPath, []byte(refreshed), mode); err != nil {
			return nil, fmt.Errorf("failed to refresh .omp/AGENTS.md: %w", err)
		}
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

// ompLegacyBlockSignatures identify a prior (imperative) omp block that should be regenerated.
// Gating on a known-legacy signature — rather than "any drift" — avoids rewriting a block a
// NEWER binary wrote (version-skew flip-flop in mixed-version teams) or a user-customized
// block. It mirrors the legacy-allowlist the markdown/plaintext self-heal paths use.
var ompLegacyBlockSignatures = []string{"**BLOCKING**"}

// refreshOMPPrimeBlock regenerates the omp prime block between its markers when the on-disk
// content is a KNOWN-legacy block (e.g. the old imperative "BLOCKING … NOW" wording),
// preserving the original @../AGENTS.md import decision and all content outside the markers.
// The start marker must occupy a complete line (start of file or after a newline) so a quoted
// marker in user prose is never touched. An orphan start marker is left untouched. Returns the
// (possibly updated) content and whether a change was made.
func refreshOMPPrimeBlock(content string) (string, bool) {
	// locate the block by whole-line matching of BOTH markers — a marker embedded in a
	// longer line (prefix/suffix/quote) is never treated as the block boundary.
	lines := strings.SplitAfter(content, "\n")
	off, start, end := 0, -1, -1
	for i := range lines {
		line := strings.TrimRight(lines[i], "\r\n")
		if start < 0 {
			if line == ompPrimeMarkerStart {
				start = off
			}
		} else if line == ompPrimeMarkerEnd {
			end = off + len(ompPrimeMarkerEnd) // block ends at the marker, before its line terminator
			break
		}
		off += len(lines[i])
	}
	if start < 0 || end < 0 {
		return content, false // start/end markers not both present as complete lines
	}
	currentBlock := content[start:end]

	isLegacy := false
	for _, sig := range ompLegacyBlockSignatures {
		if strings.Contains(currentBlock, sig) {
			isLegacy = true
			break
		}
	}
	if !isLegacy {
		return content, false // current or user-customized block — leave it
	}

	importRootAgents := strings.Contains(currentBlock, "@../AGENTS.md")
	desired := ompPrimeBlock(importRootAgents)
	if currentBlock == desired {
		return content, false
	}
	return content[:start] + desired + content[end:], true
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
