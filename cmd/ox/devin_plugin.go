package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// devinPluginSource is the requiredPlugins entry that makes Devin load the
// SageOx recorder (devin-plugin/ in this repo) for every session that clones
// the repository. It tracks the default branch so recorder fixes reach repos
// without another `ox init`.
var devinPluginSource = map[string]any{
	"source": "git-subdir",
	"url":    "https://github.com/sageox/ox.git",
	"path":   "devin-plugin",
}

func devinConfigPath(gitRoot string) string {
	return filepath.Join(gitRoot, ".devin", "config.json")
}

// devinDetected reports whether Devin is in play: the repo already carries
// Devin config, or ox is running on a Devin machine (DEVIN_DIR is set there;
// DEVIN_TASK_ID is not, on cloud VMs).
func devinDetected(gitRoot string) bool {
	if info, err := os.Stat(filepath.Join(gitRoot, ".devin")); err == nil && info.IsDir() {
		return true
	}
	return os.Getenv("DEVIN_DIR") != ""
}

func isDevinPluginEntry(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["source"] == devinPluginSource["source"] &&
		m["url"] == devinPluginSource["url"] && m["path"] == devinPluginSource["path"]
}

// rejectDevinSymlinks refuses a symlinked .devin or .devin/config.json: a
// checked-in symlink would otherwise point init's write outside the checkout.
func rejectDevinSymlinks(gitRoot string) error {
	for _, p := range []string{filepath.Dir(devinConfigPath(gitRoot)), devinConfigPath(gitRoot)} {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink %s", p)
		}
	}
	return nil
}

func readDevinConfig(gitRoot string) (map[string]any, error) {
	if err := rejectDevinSymlinks(gitRoot); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	data, err := os.ReadFile(devinConfigPath(gitRoot))
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", devinConfigPath(gitRoot), err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", devinConfigPath(gitRoot), err)
	}
	return cfg, nil
}

// HasDevinPlugin reports whether .devin/config.json already requires the
// SageOx recorder plugin.
func HasDevinPlugin(gitRoot string) bool {
	cfg, err := readDevinConfig(gitRoot)
	if err != nil {
		return false
	}
	list, _ := cfg["requiredPlugins"].([]any)
	for _, v := range list {
		if isDevinPluginEntry(v) {
			return true
		}
	}
	return false
}

// InstallDevinPlugin adds the recorder to .devin/config.json requiredPlugins,
// keeping every other key and plugin. An unparseable file is left untouched
// and reported: rewriting it would silently drop the user's config.
func InstallDevinPlugin(gitRoot string) error {
	cfg, err := readDevinConfig(gitRoot)
	if err != nil {
		return err
	}
	var list []any
	if raw, ok := cfg["requiredPlugins"]; ok {
		if list, ok = raw.([]any); !ok {
			return fmt.Errorf("%s: requiredPlugins is not a list", devinConfigPath(gitRoot))
		}
	}
	for _, v := range list {
		if isDevinPluginEntry(v) {
			return nil
		}
	}
	cfg["requiredPlugins"] = append(list, devinPluginSource)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", devinConfigPath(gitRoot), err)
	}
	if err := os.MkdirAll(filepath.Dir(devinConfigPath(gitRoot)), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(devinConfigPath(gitRoot)), err)
	}
	if err := os.WriteFile(devinConfigPath(gitRoot), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", devinConfigPath(gitRoot), err)
	}
	return nil
}
