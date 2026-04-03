package main

import (
	"fmt"
	"os"
	"path/filepath"

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
          await ` + "`ox agent prime`" + `.cwd(directory).quiet()
        } catch {
          // ox not installed or failed - continue silently
        }
      }
    }
  }
}
`

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	// check if already installed with same content
	if existing, err := os.ReadFile(pluginPath); err == nil {
		if string(existing) == openCodePluginTemplate {
			return &adapterprotocol.InstallHooksResponse{
				Installed:    true,
				FilesWritten: []string{pluginPath},
			}, nil
		}
	}

	dir := filepath.Dir(pluginPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin directory: %w", err)
	}

	if err := os.WriteFile(pluginPath, []byte(openCodePluginTemplate), 0644); err != nil {
		return nil, fmt.Errorf("failed to write plugin file: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{pluginPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	_, err := os.Stat(pluginPath)
	installed := err == nil

	return &adapterprotocol.CheckHooksResponse{
		Installed: installed,
		Scope:     p.Scope,
		HookFiles: []string{pluginPath},
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
	if repoRoot != "" {
		return filepath.Join(repoRoot, openCodeProjectPath, openCodePluginFileName)
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, openCodeProjectPath, openCodePluginFileName)
}
