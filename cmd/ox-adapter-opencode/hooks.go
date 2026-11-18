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
func readOpenCodePlugin(path string) (string, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", info, nil
	}
	limit := int64(len(openCodePluginTemplate) + strings.Count(openCodePluginTemplate, "\n"))
	if info.Size() > limit {
		return "", info, nil
	}
	f, err := openPluginFile(path, os.O_RDONLY)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", nil, fmt.Errorf("OpenCode plugin changed while being checked: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", nil, err
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n"), info, nil
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	// check if already installed with same content
	existing, info, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	if existing == openCodePluginTemplate {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{pluginPath},
		}, nil
	}

	perm := os.FileMode(0644)
	if info != nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("expected a regular OpenCode plugin file at %s", pluginPath)
		}
		perm = info.Mode().Perm()
		if perm&0222 == 0 {
			return nil, fmt.Errorf("OpenCode plugin is read-only: %s", pluginPath)
		}
		// Check write access before atomic replacement. Rename alone only
		// checks directory permissions and can bypass the file's permissions.
		f, err := openPluginFile(pluginPath, os.O_WRONLY)
		if err != nil {
			return nil, fmt.Errorf("open existing OpenCode plugin for writing: %w", err)
		}
		opened, err := f.Stat()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("stat existing OpenCode plugin: %w", err)
		}
		if !os.SameFile(info, opened) {
			return nil, fmt.Errorf("OpenCode plugin changed while being checked: %s", pluginPath)
		}
	}

	dir := filepath.Dir(pluginPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin directory: %w", err)
	}

	if err := fileutil.AtomicWriteBytes(pluginPath, []byte(openCodePluginTemplate), perm); err != nil {
		return nil, fmt.Errorf("failed to write plugin file: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{pluginPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	content, info, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	status := &adapterprotocol.CheckHooksResponse{Scope: p.Scope, HookFiles: []string{pluginPath}}
	if info == nil {
		return status, nil
	}
	switch {
	case info.Mode().IsRegular():
		status.HasOwnedHooks = true
		status.Installed = content != legacyOpenCodePlugin
	case info.Mode()&os.ModeSymlink != 0:
		// A linked plugin is user-managed. Inspect only target metadata and
		// report the link as removable even if its target is missing.
		target, err := os.Stat(pluginPath)
		status.HasOwnedHooks = true
		status.Installed = err == nil && target.Mode().IsRegular()
	}
	return status, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	if _, err := os.Lstat(pluginPath); os.IsNotExist(err) {
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
