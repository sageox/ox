package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func projectParams(repoRoot string) adapterprotocol.HookParams {
	return adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"}
}

func hooksPathFor(t *testing.T, root string) string {
	t.Helper()
	p, err := hooksFilePath(root, "project")
	if err != nil {
		t.Fatalf("hooksFilePath: %v", err)
	}
	return p
}

func readHooks(t *testing.T, path string) hooksFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return hf
}

func TestInstallHooks_WritesPluginDirectory(t *testing.T) {
	root := t.TempDir()

	resp, err := handleInstallHooks(projectParams(root))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !resp.Installed {
		t.Fatal("Installed = false")
	}

	// Goose ignores a plugin directory with no manifest, so both files are
	// required — hooks.json alone would never fire.
	manifest := filepath.Join(root, ".agents", "plugins", "sageox", "plugin.json")
	hooks := filepath.Join(root, ".agents", "plugins", "sageox", "hooks", "hooks.json")

	for _, p := range []string{manifest, hooks} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	hf := readHooks(t, hooks)
	for _, event := range hookEvents {
		if !eventHasOxHook(hf.Hooks[event]) {
			t.Errorf("event %s has no ox hook", event)
		}
	}
}

// TestInstallHooks_NeverEmitsMatcher guards a Goose-specific trap: matcher is a
// REGEX, and a bare "*" is invalid, so Goose logs a warning and SILENTLY SKIPS
// the whole rule. Omitting the field is the only unambiguous "match everything".
func TestInstallHooks_NeverEmitsMatcher(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".agents", "plugins", "sageox", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if strings.Contains(string(raw), `"matcher"`) {
		t.Error("hooks.json must not contain a matcher field — a bad regex silently disables the rule")
	}
}

// TestInstallHooks_ProjectScopeBakesInRepoRoot covers the cwd problem: Goose
// sends working_dir only on TOOL events, so SessionStart gives ox nothing to
// locate the repo with and it would walk up from an arbitrary cwd.
func TestInstallHooks_ProjectScopeBakesInRepoRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	hf := readHooks(t, filepath.Join(root, ".agents", "plugins", "sageox", "hooks", "hooks.json"))
	cmd := hf.Hooks["SessionStart"][0].Hooks[0].Command

	if !strings.Contains(cmd, "OX_PROJECT_ROOT=") {
		t.Errorf("project-scope command must pin OX_PROJECT_ROOT, got: %s", cmd)
	}
	if !strings.Contains(cmd, root) {
		t.Errorf("command must name the repo root, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AGENT_ENV=goose") {
		t.Errorf("command must set AGENT_ENV=goose, got: %s", cmd)
	}
}

// TestHookCommand_QuotesPathsWithSpaces — Goose runs commands via `sh -c`, so an
// unquoted repo path containing a space would split into two arguments and the
// hook would silently target the wrong directory.
func TestHookCommand_QuotesPathsWithSpaces(t *testing.T) {
	cmd := hookCommand("SessionStart", "/Users/me/My Repos/ox", "project")
	if !strings.Contains(cmd, `'/Users/me/My Repos/ox'`) {
		t.Errorf("path with a space must be quoted, got: %s", cmd)
	}

	withQuote := hookCommand("SessionStart", `/tmp/it's`, "project")
	if !strings.Contains(withQuote, `'\''`) {
		t.Errorf("embedded single quote must be escaped, got: %s", withQuote)
	}
}

// TestHookCommand_UserScopeOmitsRepoRoot — user scope has no single repo to pin,
// so it must fall back to the cwd walk like every other adapter.
func TestHookCommand_UserScopeOmitsRepoRoot(t *testing.T) {
	cmd := hookCommand("SessionStart", "/repo", scopeUser)
	if strings.Contains(cmd, "OX_PROJECT_ROOT") {
		t.Errorf("user-scope command must not pin a repo root, got: %s", cmd)
	}
}

// TestInstallHooks_EventCoverage documents which Goose events ox installs and
// which it deliberately skips. The four skipped events are strict subsets of
// PreToolUse/PostToolUse — installing them would spawn `ox agent hook` twice per
// tool call for no signal ox does not already have.
func TestInstallHooks_EventCoverage(t *testing.T) {
	installed := map[string]bool{}
	for _, e := range hookEvents {
		installed[e] = true
	}

	mustInstall := []string{
		"SessionStart", "SessionEnd", "Stop",
		"UserPromptSubmit", "PreToolUse", "PostToolUse",
		// Not redundant: Goose fires PostToolUse only on SUCCESS, so without
		// this a failed turn produces no event until the next success or Stop.
		"PostToolUseFailure",
	}
	for _, e := range mustInstall {
		if !installed[e] {
			t.Errorf("event %s must be installed", e)
		}
	}

	mustSkip := []string{"BeforeReadFile", "AfterFileEdit", "BeforeShellExecution", "AfterShellExecution"}
	for _, e := range mustSkip {
		if installed[e] {
			t.Errorf("event %s is a strict subset of Pre/PostToolUse — installing it doubles hook spawns per tool call", e)
		}
	}
}

func TestInstallHooks_Idempotent(t *testing.T) {
	root := t.TempDir()
	hooksPath := hooksPathFor(t, root)

	for i := 0; i < 3; i++ {
		if _, err := handleInstallHooks(projectParams(root)); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}

	hf := readHooks(t, hooksPath)
	for _, event := range hookEvents {
		if got := len(hf.Hooks[event]); got != 1 {
			t.Errorf("event %s has %d rules after 3 installs, want 1", event, got)
		}
	}
}

func TestCheckHooks_RoundTrip(t *testing.T) {
	root := t.TempDir()

	resp, err := handleCheckHooks(projectParams(root))
	if err != nil {
		t.Fatalf("check before install: %v", err)
	}
	if resp.Installed {
		t.Error("check reported installed before install")
	}

	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	resp, err = handleCheckHooks(projectParams(root))
	if err != nil {
		t.Fatalf("check after install: %v", err)
	}
	if !resp.Installed {
		t.Error("check reported not installed after install")
	}
}

// TestCheckHooks_ManifestMissingIsNotInstalled — a hooks.json with no sibling
// plugin.json looks installed on disk but Goose never loads it, so the hooks
// never fire. Reporting "installed" there would hide a fully broken setup.
func TestCheckHooks_ManifestMissingIsNotInstalled(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := os.Remove(filepath.Join(root, ".agents", "plugins", "sageox", "plugin.json")); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}

	resp, err := handleCheckHooks(projectParams(root))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.Installed {
		t.Error("hooks.json without plugin.json must not count as installed — Goose ignores the plugin")
	}
}

func TestUninstallHooks_RemovesOwnedPluginDir(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	if _, err := handleUninstallHooks(projectParams(root)); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	dir := filepath.Join(root, ".agents", "plugins", "sageox")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed, stat err = %v", dir, err)
	}

	// .agents/plugins/ itself is shared ground and must survive.
	if _, err := os.Stat(filepath.Join(root, ".agents", "plugins")); err != nil {
		t.Errorf("shared plugins dir must not be removed: %v", err)
	}
}

// TestUninstallHooks_PreservesForeignRules is the safety property that matters
// most: .agents/plugins/ is a shared namespace, so uninstall must remove only
// commands this adapter wrote.
func TestUninstallHooks_PreservesForeignRules(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	hooksPath := hooksPathFor(t, root)
	hf := readHooks(t, hooksPath)
	hf.Hooks["SessionStart"] = append(hf.Hooks["SessionStart"], hookRule{
		Hooks: []hookAction{{Type: "command", Command: "echo someone-elses-hook"}},
	})
	if err := writeHooksFile(hooksPath, hf); err != nil {
		t.Fatalf("write foreign rule: %v", err)
	}

	if _, err := handleUninstallHooks(projectParams(root)); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	after := readHooks(t, hooksPath)
	rules := after.Hooks["SessionStart"]
	if len(rules) != 1 {
		t.Fatalf("SessionStart has %d rules, want the 1 foreign rule preserved", len(rules))
	}
	if !strings.Contains(rules[0].Hooks[0].Command, "someone-elses-hook") {
		t.Errorf("surviving rule = %q, want the foreign one", rules[0].Hooks[0].Command)
	}
}

// TestUninstallHooks_LeavesForeignPluginDirAlone — if plugin.json is not ours,
// never delete the directory, even when no ox rules remain.
func TestUninstallHooks_LeavesForeignPluginDirAlone(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	manifestPath := filepath.Join(root, ".agents", "plugins", "sageox", "plugin.json")
	foreign := map[string]any{"name": "sageox", "version": "9", "description": "not ours"}
	if err := writeJSON(manifestPath, foreign); err != nil {
		t.Fatalf("write foreign manifest: %v", err)
	}

	if _, err := handleUninstallHooks(projectParams(root)); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("a plugin.json we do not own must survive uninstall: %v", err)
	}
}

func TestUninstallHooks_NoInstallIsSuccess(t *testing.T) {
	resp, err := handleUninstallHooks(projectParams(t.TempDir()))
	if err != nil {
		t.Fatalf("uninstall with nothing installed: %v", err)
	}
	if !resp.Uninstalled {
		t.Error("uninstalling nothing should report success")
	}
}

func TestInstallHooks_ProjectScopeRequiresRepoRoot(t *testing.T) {
	if _, err := handleInstallHooks(adapterprotocol.HookParams{Scope: "project"}); err == nil {
		t.Error("project scope without a repo root must error rather than write to the filesystem root")
	}
}

// TestPluginDir_RefusesRelativePaths is the guard on a destructive operation.
// Uninstall calls os.RemoveAll on this path. If an unresolvable scope root ever
// produced a relative ".agents/plugins/sageox", uninstall would delete whatever
// happened to sit under the adapter's current working directory.
func TestPluginDir_RefusesRelativePaths(t *testing.T) {
	tests := []struct {
		name     string
		repoRoot string
		scope    string
	}{
		{"empty project root", "", "project"},
		{"relative project root", "some/relative/path", "project"},
		{"dot project root", ".", "project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pluginDir(tt.repoRoot, tt.scope)
			if err == nil {
				t.Fatalf("pluginDir(%q, %q) = %q, want an error — os.RemoveAll would target a relative path",
					tt.repoRoot, tt.scope, got)
			}
			if got != "" {
				t.Errorf("pluginDir returned %q alongside an error; callers must not receive a usable path", got)
			}
		})
	}
}

// TestHandlers_RejectUnanchoredScopeRoot proves the guard is wired into every
// handler, not just the helper. check and uninstall previously had no
// repo-root requirement at all.
func TestHandlers_RejectUnanchoredScopeRoot(t *testing.T) {
	bad := adapterprotocol.HookParams{RepoRoot: "", Scope: "project"}

	if _, err := handleInstallHooks(bad); err == nil {
		t.Error("install must reject an unanchored scope root")
	}
	if _, err := handleCheckHooks(bad); err == nil {
		t.Error("check must reject an unanchored scope root")
	}
	if _, err := handleUninstallHooks(bad); err == nil {
		t.Error("uninstall must reject an unanchored scope root — it calls os.RemoveAll")
	}
}

// TestUninstallHooks_DoesNotTouchCwd is the end-to-end version of the same
// property: run uninstall with a bad scope root from inside a directory that
// contains a real .agents/plugins/sageox, and confirm it survives.
func TestUninstallHooks_DoesNotTouchCwd(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Empty RepoRoot: the pre-fix code would have joined to a relative path and
	// removed the plugin directory sitting in the cwd.
	if _, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: "", Scope: "project"}); err == nil {
		t.Error("expected an error for an unanchored scope root")
	}

	if _, err := os.Stat(filepath.Join(root, ".agents", "plugins", "sageox", "plugin.json")); err != nil {
		t.Errorf("cwd plugin directory was destroyed by an unanchored uninstall: %v", err)
	}
}

// TestInstallHooks_RefusesForeignManifest guards the destructive arm of
// uninstall: install stamps x-ox-managed=true, which uninstall uses to decide
// whether to RemoveAll the plugin directory. Overwriting a foreign manifest
// would cause a later uninstall to delete that tool's assets.
func TestInstallHooks_RefusesForeignManifest(t *testing.T) {
	root := t.TempDir()

	manifestPath := filepath.Join(root, ".agents", "plugins", "sageox", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	foreign := map[string]any{"name": "sageox", "description": "not ours"}
	data, _ := json.Marshal(foreign)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("write foreign manifest: %v", err)
	}

	_, err := handleInstallHooks(projectParams(root))
	if err == nil {
		t.Fatal("install must refuse to overwrite a foreign manifest")
	}

	// Foreign manifest must be untouched.
	got, _ := os.ReadFile(manifestPath)
	if !strings.Contains(string(got), `"not ours"`) {
		t.Error("install overwrote the foreign manifest")
	}
}

// TestInstallHooks_RefusesSymlinkedDir verifies that a symlinked .agents
// directory causes install to error rather than redirect writes outside the repo.
func TestInstallHooks_RefusesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()

	agentsDir := filepath.Join(root, ".agents")
	if err := os.Symlink(external, agentsDir); err != nil {
		t.Skipf("cannot create symlink (OS limitation): %v", err)
	}

	_, err := handleInstallHooks(projectParams(root))
	if err == nil {
		t.Fatal("install must refuse to write through a symlinked .agents directory")
	}
}

// TestInstallHooks_RefusesSymlinkedFile verifies that a symlinked plugin.json
// causes install to error rather than overwrite the symlink target.
func TestInstallHooks_RefusesSymlinkedFile(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()

	plugDir := filepath.Join(root, ".agents", "plugins", "sageox")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	target := filepath.Join(external, "plugin.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(plugDir, "plugin.json")); err != nil {
		t.Skipf("cannot create symlink (OS limitation): %v", err)
	}

	_, err := handleInstallHooks(projectParams(root))
	if err == nil {
		t.Fatal("install must refuse to write through a symlinked plugin.json")
	}
}

// TestInstallHooks_PreservesUnrelatedEvents — a user may add their own rules for
// events ox does not manage; install must merge, not overwrite.
func TestInstallHooks_PreservesUnrelatedEvents(t *testing.T) {
	root := t.TempDir()
	hooksPath := hooksPathFor(t, root)

	seed := hooksFile{Hooks: map[string][]hookRule{
		"AfterFileEdit": {{Hooks: []hookAction{{Type: "command", Command: "gofmt -w ."}}}},
	}}
	if err := writeHooksFile(hooksPath, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	hf := readHooks(t, hooksPath)
	if len(hf.Hooks["AfterFileEdit"]) != 1 {
		t.Error("install must not drop an event ox does not manage")
	}
}

func TestInstallHooks_SessionStartGetsLongerTimeout(t *testing.T) {
	root := t.TempDir()
	if _, err := handleInstallHooks(projectParams(root)); err != nil {
		t.Fatalf("install: %v", err)
	}

	hf := readHooks(t, filepath.Join(root, ".agents", "plugins", "sageox", "hooks", "hooks.json"))

	// SessionStart shells out to a full `ox agent prime`; the rest are fast
	// local calls and must not be able to stall a turn for 30s.
	if got := hf.Hooks["SessionStart"][0].Hooks[0].Timeout; got != 30 {
		t.Errorf("SessionStart timeout = %d, want 30", got)
	}
	if got := hf.Hooks["PostToolUse"][0].Hooks[0].Timeout; got != defaultHookTimeout {
		t.Errorf("PostToolUse timeout = %d, want %d", got, defaultHookTimeout)
	}
}
