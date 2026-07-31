// hooks.go installs ox lifecycle hooks into a Goose plugin directory.
//
// Goose follows the Open Plugins hooks specification, so hooks live inside a
// plugin directory rather than in a single settings file:
//
//	<scope>/.agents/plugins/sageox/
//	├── plugin.json        manifest — Goose ignores a plugin without it
//	└── hooks/hooks.json   event → command rules
//
// Scope root is the repo root (project) or $HOME (user).
//
// Reference: https://block.github.io/goose/docs/guides/context-engineering/hooks
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

const (
	pluginName    = "sageox"
	pluginVersion = "1"

	scopeUser = "user"

	// oxHookMarker identifies a command this adapter installed. Uninstall
	// removes only rules matching it, because .agents/plugins/ is a shared
	// namespace that other tools legitimately write to.
	oxHookMarker = "ox agent hook"

	// ownershipKey marks the manifest as ours. Without it, uninstall leaves the
	// directory alone rather than deleting a plugin someone else authored.
	ownershipKey = "x-ox-managed"
)

// hookEvents are the Goose events ox acts on.
//
// Goose fires eleven events. The four omitted here — BeforeReadFile,
// AfterFileEdit, BeforeShellExecution, AfterShellExecution — are each a strict
// subset of PreToolUse or PostToolUse, since reading a file and running a shell
// command are both tool calls. Installing them would spawn `ox agent hook`
// twice per tool call for no signal ox does not already have.
//
// PostToolUseFailure is NOT redundant: Goose fires PostToolUse only on success,
// so without it a turn ending in a failed tool call produces no event until the
// next successful call or Stop.
var hookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
	"SessionEnd",
}

// hookTimeouts overrides the 30s Goose default per event. SessionStart shells
// out to a full `ox agent prime`, so it keeps the long timeout; everything else
// is a fast local call and should not stall a turn if ox wedges.
var hookTimeouts = map[string]int{
	"SessionStart": 30,
}

const defaultHookTimeout = 10

// --- hooks.json schema ---

type hooksFile struct {
	Hooks map[string][]hookRule `json:"hooks"`
}

// hookRule is one rule for an event. Matcher is deliberately never set: Goose
// treats it as a REGEX, and a bare "*" is an invalid regex that Goose silently
// skips — omitting the field is the unambiguous way to match everything.
type hookRule struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []hookAction `json:"hooks"`
}

type hookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// hookCommand builds the shell command for an event.
//
// Goose sends working_dir only on TOOL events, so SessionStart alone gives ox
// nothing to locate the repo with and it would fall back to walking up from
// whatever cwd Goose happens to use. For project scope we bake the absolute
// root in via OX_PROJECT_ROOT, which ox checks before the cwd walk. User scope
// has no single root to bake in and relies on the walk, like every other adapter.
func hookCommand(event, repoRoot, scope string) string {
	prefix := "AGENT_ENV=goose"
	if scope != scopeUser && repoRoot != "" {
		prefix = fmt.Sprintf("OX_PROJECT_ROOT=%s %s", shellQuote(repoRoot), prefix)
	}
	return fmt.Sprintf(
		`if command -v ox >/dev/null 2>&1; then %s ox agent hook %s 2>&1 || true; fi`,
		prefix, event,
	)
}

// shellQuote single-quotes a path for `sh -c`. Goose runs hook commands through
// a shell, so a repo path containing a space or a quote would otherwise split.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- paths ---

func pluginDir(repoRoot, scope string) string {
	root := repoRoot
	if scope == scopeUser {
		home, _ := os.UserHomeDir()
		root = home
	}
	return filepath.Join(root, ".agents", "plugins", pluginName)
}

func hooksFilePath(repoRoot, scope string) string {
	return filepath.Join(pluginDir(repoRoot, scope), "hooks", "hooks.json")
}

func manifestFilePath(repoRoot, scope string) string {
	return filepath.Join(pluginDir(repoRoot, scope), "plugin.json")
}

// --- install / check / uninstall ---

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope != scopeUser && p.RepoRoot == "" {
		return nil, fmt.Errorf("--repo-root is required for project scope")
	}

	hooksPath := hooksFilePath(p.RepoRoot, p.Scope)
	manifestPath := manifestFilePath(p.RepoRoot, p.Scope)

	hf := loadHooksFile(hooksPath)

	for _, event := range hookEvents {
		if eventHasOxHook(hf.Hooks[event]) {
			continue
		}
		hf.Hooks[event] = append(hf.Hooks[event], hookRule{
			Hooks: []hookAction{{
				Type:    "command",
				Command: hookCommand(event, p.RepoRoot, p.Scope),
				Timeout: timeoutFor(event),
			}},
		})
	}

	if err := writeManifest(manifestPath); err != nil {
		return nil, fmt.Errorf("failed to write plugin manifest: %w", err)
	}
	if err := writeHooksFile(hooksPath, hf); err != nil {
		return nil, fmt.Errorf("failed to write hooks.json: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{manifestPath, hooksPath},
		Hooks:        hookEvents,
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	hooksPath := hooksFilePath(p.RepoRoot, p.Scope)
	manifestPath := manifestFilePath(p.RepoRoot, p.Scope)

	files := []string{manifestPath, hooksPath}

	// Goose ignores a plugin directory with no manifest, so hooks.json alone is
	// not "installed" — the hooks would never fire.
	if _, err := os.Stat(manifestPath); err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope, HookFiles: files}, nil
	}

	data, err := os.ReadFile(hooksPath) //nolint:gosec // path derived from repo root + fixed plugin name
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope, HookFiles: files}, nil
	}

	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope, HookFiles: files}, nil
	}

	for _, event := range hookEvents {
		if !eventHasOxHook(hf.Hooks[event]) {
			return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope, HookFiles: files}, nil
		}
	}

	return &adapterprotocol.CheckHooksResponse{Installed: true, Scope: p.Scope, HookFiles: files}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	hooksPath := hooksFilePath(p.RepoRoot, p.Scope)

	data, err := os.ReadFile(hooksPath) //nolint:gosec // path derived from repo root + fixed plugin name
	if err != nil {
		// Nothing installed is a successful uninstall.
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	for event, rules := range hf.Hooks {
		kept := make([]hookRule, 0, len(rules))
		for _, r := range rules {
			if !ruleHasOxHook(r) {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(hf.Hooks, event)
		} else {
			hf.Hooks[event] = kept
		}
	}

	// If nothing of ours is left AND the manifest is ours, take the whole
	// directory. If the manifest is someone else's, leave their plugin intact
	// and only drop the hooks we added — .agents/plugins/ is shared ground.
	if len(hf.Hooks) == 0 && manifestIsOurs(manifestFilePath(p.RepoRoot, p.Scope)) {
		dir := pluginDir(p.RepoRoot, p.Scope)
		if err := os.RemoveAll(dir); err != nil {
			return nil, fmt.Errorf("failed to remove plugin dir: %w", err)
		}
		return &adapterprotocol.UninstallHooksResponse{
			Uninstalled:   true,
			FilesModified: []string{dir},
		}, nil
	}

	if err := writeHooksFile(hooksPath, hf); err != nil {
		return nil, fmt.Errorf("failed to write hooks.json: %w", err)
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{hooksPath},
	}, nil
}

// --- helpers ---

func timeoutFor(event string) int {
	if t, ok := hookTimeouts[event]; ok {
		return t
	}
	return defaultHookTimeout
}

func loadHooksFile(path string) hooksFile {
	hf := hooksFile{Hooks: map[string][]hookRule{}}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from repo root + fixed plugin name
	if err != nil {
		return hf
	}
	if err := json.Unmarshal(data, &hf); err != nil || hf.Hooks == nil {
		return hooksFile{Hooks: map[string][]hookRule{}}
	}
	return hf
}

func eventHasOxHook(rules []hookRule) bool {
	for _, r := range rules {
		if ruleHasOxHook(r) {
			return true
		}
	}
	return false
}

func ruleHasOxHook(r hookRule) bool {
	for _, a := range r.Hooks {
		if strings.Contains(a.Command, oxHookMarker) {
			return true
		}
	}
	return false
}

// manifestIsOurs reports whether plugin.json carries the ox ownership marker.
func manifestIsOurs(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // path derived from repo root + fixed plugin name
	if err != nil {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	owned, _ := m[ownershipKey].(bool)
	return owned
}

func writeManifest(path string) error {
	manifest := map[string]any{
		"name":        pluginName,
		"version":     pluginVersion,
		"description": "SageOx team context and session recording for Goose",
		ownershipKey:  true,
	}
	return writeJSON(path, manifest)
}

func writeHooksFile(path string, hf hooksFile) error {
	if hf.Hooks == nil {
		hf.Hooks = map[string][]hookRule{}
	}
	return writeJSON(path, hf)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644) //nolint:gosec // config file, not a secret
}
