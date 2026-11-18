package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

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

type openCodePlugin struct {
	path    string
	linked  bool
	info    os.FileInfo
	content string
}

// readOpenCodePlugin reads only enough bytes to recognize generated templates.
func readOpenCodePlugin(path string) (openCodePlugin, error) {
	plugin := openCodePlugin{path: path}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		plugin.linked = true
		plugin.path, err = filepath.EvalSymlinks(path)
		if err == nil {
			info, err = os.Lstat(plugin.path)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return plugin, nil
	}
	if err != nil {
		return plugin, err
	}
	plugin.info = info
	limit := int64(len(openCodePluginTemplate) + strings.Count(openCodePluginTemplate, "\n"))
	if !info.Mode().IsRegular() || info.Size() > limit {
		return plugin, nil
	}
	f, err := openPluginFile(plugin.path, os.O_RDONLY)
	if err != nil {
		return plugin, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return plugin, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return plugin, fmt.Errorf("OpenCode plugin changed while being checked: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return plugin, err
	}
	plugin.content = strings.ReplaceAll(string(data), "\r\n", "\n")
	return plugin, nil
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	plugin, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	if plugin.content != openCodePluginTemplate {
		if err := writeOpenCodePlugin(plugin); err != nil {
			return nil, fmt.Errorf("write OpenCode plugin: %w", err)
		}
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{pluginPath},
	}, nil
}

func writeOpenCodePlugin(plugin openCodePlugin) error {
	if plugin.linked && plugin.content != legacyOpenCodePlugin {
		return errors.New("linked OpenCode plugin is missing or is not a generated hook")
	}
	if plugin.info == nil {
		if err := os.MkdirAll(filepath.Dir(plugin.path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(plugin.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(openCodePluginTemplate)
		return errors.Join(err, f.Close())
	}
	if !plugin.info.Mode().IsRegular() || plugin.info.Mode().Perm()&0222 == 0 {
		return fmt.Errorf("expected a writable regular OpenCode plugin file at %s", plugin.path)
	}
	// Keep the existing inode so repairs retain file ownership and access.
	f, err := openPluginFile(plugin.path, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(plugin.info, opened) {
		return fmt.Errorf("OpenCode plugin changed while being checked: %s", plugin.path)
	}
	n, err := f.WriteString(openCodePluginTemplate)
	if err != nil {
		return err
	}
	return f.Truncate(int64(n))
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	pluginPath := resolvePluginPath(p.RepoRoot, p.Scope)

	plugin, err := readOpenCodePlugin(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	regular := plugin.info != nil && plugin.info.Mode().IsRegular()
	return &adapterprotocol.CheckHooksResponse{
		Scope:         p.Scope,
		HookFiles:     []string{pluginPath},
		HasOwnedHooks: regular || plugin.linked,
		Installed:     regular && plugin.content != legacyOpenCodePlugin,
	}, nil
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
