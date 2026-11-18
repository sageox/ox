package main

import (
	"crypto/rand"
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
		if err := writeOpenCodePlugin(openCodeHookRoot(p), plugin); err != nil {
			return nil, fmt.Errorf("write OpenCode plugin: %w", err)
		}
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{pluginPath},
	}, nil
}

func writeOpenCodePlugin(rootPath string, plugin openCodePlugin) error {
	if plugin.linked && plugin.content != legacyOpenCodePlugin {
		return errors.New("linked OpenCode plugin is missing or is not a generated hook")
	}
	rootPath, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return err
	}
	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return err
	}
	target := plugin.path
	if plugin.info != nil {
		target, err = filepath.EvalSymlinks(target)
		if err != nil {
			return err
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootPath, target)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("OpenCode plugin is outside the hook scope: %s", plugin.path)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, name := filepath.Dir(rel), filepath.Base(rel)
	if plugin.info == nil {
		if err := root.MkdirAll(dir, 0755); err != nil {
			return err
		}
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(openCodePluginTemplate)
		return errors.Join(err, f.Close())
	}
	if !plugin.info.Mode().IsRegular() || plugin.info.Mode().Perm()&0222 == 0 {
		return fmt.Errorf("expected a writable regular OpenCode plugin file at %s", plugin.path)
	}
	parent, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := openRootPluginFile(parent, name, os.O_WRONLY)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if !os.SameFile(plugin.info, opened) {
		return fmt.Errorf("OpenCode plugin changed while being checked: %s", plugin.path)
	}
	return replaceOpenCodePlugin(parent, name, opened)
}

func replaceOpenCodePlugin(parent *os.Root, name string, existing os.FileInfo) error {
	tempName := ".ox-plugin-" + rand.Text()
	tmp, err := parent.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Remove(tempName) }()
	defer tmp.Close()
	if err := preservePluginOwner(tmp, existing); err != nil {
		return err
	}
	if _, err := tmp.WriteString(openCodePluginTemplate); err != nil {
		return err
	}
	if err := tmp.Chmod(existing.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(existing, current) {
		return fmt.Errorf("OpenCode plugin changed during repair: %s", name)
	}
	return parent.Rename(tempName, name)
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
	root := openCodeHookRoot(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: scope})
	if scope == "user" {
		return filepath.Join(root, openCodeUserPath, openCodePluginFileName)
	}
	return filepath.Join(root, openCodeProjectPath, openCodePluginFileName)
}

func openCodeHookRoot(p adapterprotocol.HookParams) string {
	if p.Scope == "user" {
		home, _ := os.UserHomeDir()
		return home
	}
	if p.RepoRoot == "" {
		log.Println("WARN: resolvePluginPath called with empty repoRoot, falling back to cwd")
		cwd, _ := os.Getwd()
		return cwd
	}
	return p.RepoRoot
}
