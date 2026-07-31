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

// scopeRoot returns the absolute filesystem root for the given scope.
// The error return is load-bearing: callers that derive paths from this root
// (including os.RemoveAll) must not proceed with a relative or empty path.
func scopeRoot(repoRoot, scope string) (string, error) {
	if scope == scopeUser {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory for user scope: %w", err)
		}
		return home, nil
	}
	if repoRoot == "" {
		return "", fmt.Errorf("empty scope root for scope %q: refusing to resolve a relative plugin path", scope)
	}
	if !filepath.IsAbs(repoRoot) {
		return "", fmt.Errorf("scope root %q is not absolute", repoRoot)
	}
	return repoRoot, nil
}

// pluginDir resolves the plugin directory for a scope.
func pluginDir(repoRoot, scope string) (string, error) {
	root, err := scopeRoot(repoRoot, scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".agents", "plugins", pluginName), nil
}

// verifyNoSymlinks ensures no path component between scopeRoot and path is a
// symlink. A repo-controlled symlink could redirect hook file writes to
// locations outside the repository. Components that don't exist yet are
// skipped — os.MkdirAll will create them freshly without following symlinks.
func verifyNoSymlinks(scopeRoot, path string) error {
	rel, err := filepath.Rel(scopeRoot, path)
	if err != nil {
		return fmt.Errorf("cannot relativize %q from %q: %w", path, scopeRoot, err)
	}
	current := scopeRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // path doesn't exist yet — nothing to follow
			}
			return fmt.Errorf("lstat %q: %w", current, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symlink — refusing to write hook files to avoid redirecting outside the repository", current)
		}
	}
	return nil
}

func hooksFilePath(repoRoot, scope string) (string, error) {
	dir, err := pluginDir(repoRoot, scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks", "hooks.json"), nil
}

func manifestFilePath(repoRoot, scope string) (string, error) {
	dir, err := pluginDir(repoRoot, scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugin.json"), nil
}

// --- install / check / uninstall ---

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope != scopeUser && p.RepoRoot == "" {
		return nil, fmt.Errorf("--repo-root is required for project scope")
	}

	root, err := scopeRoot(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	hooksPath, err := hooksFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}
	manifestPath, err := manifestFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	// Symlink check: a repo-controlled symlink could redirect writes outside
	// the repository boundary.
	if err := verifyNoSymlinks(root, manifestPath); err != nil {
		return nil, fmt.Errorf("install-hooks: %w", err)
	}
	if err := verifyNoSymlinks(root, hooksPath); err != nil {
		return nil, fmt.Errorf("install-hooks: %w", err)
	}

	// Refuse to overwrite a manifest another tool created. Our uninstall uses
	// x-ox-managed to decide whether to RemoveAll the plugin directory; if we
	// stamp a foreign manifest it would later delete their plugin's assets too.
	if _, statErr := os.Stat(manifestPath); statErr == nil && !manifestIsOurs(manifestPath) {
		return nil, fmt.Errorf("plugin directory %q already contains a manifest ox did not create; remove it before installing", filepath.Dir(manifestPath))
	}

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
	hooksPath, err := hooksFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}
	manifestPath, err := manifestFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	files := []string{manifestPath, hooksPath}

	// Goose ignores a plugin directory with no manifest, so hooks.json alone is
	// not "installed" — the hooks would never fire.
	if _, err := os.Stat(manifestPath); err != nil {
		return &adapterprotocol.CheckHooksResponse{Installed: false, Scope: p.Scope, HookFiles: files}, nil
	}

	data, readErr := os.ReadFile(hooksPath) //nolint:gosec // path derived from repo root + fixed plugin name
	if readErr != nil {
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
	hooksPath, err := hooksFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}

	data, readErr := os.ReadFile(hooksPath) //nolint:gosec // path derived from repo root + fixed plugin name
	if readErr != nil {
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
	manifestPath, err := manifestFilePath(p.RepoRoot, p.Scope)
	if err != nil {
		return nil, err
	}
	if len(hf.Hooks) == 0 && manifestIsOurs(manifestPath) {
		dir, err := pluginDir(p.RepoRoot, p.Scope)
		if err != nil {
			return nil, err
		}
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
