package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// openCodePluginTemplate is the TypeScript plugin that runs ox agent prime
// on session start. OpenCode loads plugins from .opencode/plugin/ automatically.
const openCodePluginTemplate = `import type { Plugin } from "@opencode-ai/plugin"

// SageOx plugin for OpenCode
// runs 'ox agent prime' on session start to load team context
export const OxPrimePlugin: Plugin = async ({ $, directory }) => {
  return {
    event: async ({ event }) => {
      if (event.type === "session.created") {
        try {
          await $` + "`ox agent prime`" + `.cwd(directory).quiet()
        } catch {
          // ox not installed or failed - continue silently
        }
      }
    }
  }
}
`

// Older ox versions wrote the same plugin without OpenCode's shell tag.
var legacyOpenCodePlugin = strings.Replace(openCodePluginTemplate, "await $", "await ", 1)

// readOpenCodePlugin only needs enough bytes to compare generated templates.
// A larger regular file is a custom plugin and cannot equal either template.
func readOpenCodePlugin(path string) (content string, exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("expected a regular OpenCode plugin file at %s", path)
	}
	limit := int64(len(openCodePluginTemplate) + strings.Count(openCodePluginTemplate, "\n"))
	if info.Size() > limit {
		return "", true, nil
	}
	f, err := openPluginForCheck(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", false, fmt.Errorf("OpenCode plugin changed while being checked: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", false, err
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n"), true, nil
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	// check if already installed with same content
	existing, _, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	if existing == openCodePluginTemplate {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{pluginPath},
		}, nil
	}

	dir := filepath.Dir(pluginPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin directory: %w", err)
	}

	if err := fileutil.AtomicWriteBytes(pluginPath, []byte(openCodePluginTemplate), 0644); err != nil {
		return nil, fmt.Errorf("failed to write plugin file: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{pluginPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	content, exists, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	return &adapterprotocol.CheckHooksResponse{
		Installed:     exists && content != legacyOpenCodePlugin,
		HasOwnedHooks: exists,
		Scope:         p.Scope,
		HookFiles:     []string{pluginPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	if err := os.Remove(pluginPath); err != nil {
		return nil, fmt.Errorf("failed to remove plugin file: %w", err)
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{pluginPath},
	}, nil
}

func resolvePluginPath(repoRoot, scope string) string {
	if scope == "user" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, openCodeUserPath, openCodePluginFileName)
	}
	if repoRoot == "" {
		log.Println("WARN: resolvePluginPath called with empty repoRoot, falling back to cwd")
		repoRoot, _ = os.Getwd()
	}
	return filepath.Join(repoRoot, openCodeProjectPath, openCodePluginFileName)
}
