package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// legacyPluginFixture is the plugin written by ox releases before the shell
// tag fix, kept verbatim so template changes cannot hide a missed migration.
const legacyPluginFixture = `import type { Plugin } from "@opencode-ai/plugin"

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

func TestInstallHooksKeepsReadOnlyPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions")
	}
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	const custom = "// coworker's plugin"
	if err := os.WriteFile(path, []byte(custom), 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := handleInstallHooks(params); err == nil {
		t.Error("installer replaced a read-only plugin")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != custom {
		t.Fatalf("read-only plugin changed: %q, %v", content, err)
	}
}

func TestInstallHooksUnderLinkedProjectRoot(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(project, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	params := adapterprotocol.HookParams{RepoRoot: link, Scope: "project"}
	if _, err := handleInstallHooks(params); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(resolvePluginPath(project, params.Scope))
	if err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("plugin missing in linked checkout: %v", err)
	}
}

func TestRepairHooksFromLinkedWorkingDirectory(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(project, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	path := resolvePluginPath(project, "project")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacyOpenCodePlugin), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("PWD", link)
	if _, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: ".", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("repair did not write the generated plugin: %v", err)
	}
}

func TestLinkedOpenCodePluginCanBeUninstalled(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	target := filepath.Join(t.TempDir(), "shared-plugin.ts")
	if err := os.WriteFile(target, []byte(openCodePluginTemplate), 0644); err != nil {
		t.Fatal(err)
	}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	status, err := handleCheckHooks(params)
	if err != nil || !status.Installed || !status.HasOwnedHooks {
		t.Fatalf("linked hook should be installed and removable: %+v, %v", status, err)
	}
	if _, err := handleUninstallHooks(params); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("plugin link was left behind: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestLinkedOpenCodePluginCanBeRepaired(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	target := filepath.Join(params.RepoRoot, "shared-plugin.ts")
	if err := os.WriteFile(target, []byte(legacyOpenCodePlugin), 0640); err != nil {
		t.Fatal(err)
	}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	status, err := handleCheckHooks(params)
	if err != nil || status.Installed || !status.HasOwnedHooks {
		t.Fatalf("linked legacy hook should require repair: %+v, %v", status, err)
	}
	if _, err := handleInstallHooks(params); err != nil {
		t.Fatal(err)
	}
	status, err = handleCheckHooks(params)
	if err != nil || !status.Installed {
		t.Fatalf("linked hook should be healthy after repair: %+v, %v", status, err)
	}
	if link, err := os.Readlink(path); err != nil || link != target {
		t.Fatalf("plugin link changed: %q, %v", link, err)
	}
}

func TestProjectOpenCodeRepairLeavesExternalPluginUnchanged(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	target := filepath.Join(t.TempDir(), "shared.ts")
	if err := os.WriteFile(target, []byte(legacyOpenCodePlugin), 0644); err != nil {
		t.Fatal(err)
	}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := handleInstallHooks(params); err == nil {
		t.Error("project hook repair accepted a linked plugin outside the checkout")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != legacyOpenCodePlugin {
		t.Fatalf("external plugin changed: %v", err)
	}
}

func TestOpenCodePluginCheckReportsLinkLookupFailure(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := handleCheckHooks(params); err == nil {
		t.Fatal("hook check hid a linked plugin lookup error")
	}
}

func TestInstalledOpenCodePluginPrimesOnSessionCreated(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js 22+ is needed to execute the generated TypeScript")
	}
	if err := exec.CommandContext(t.Context(), node, "--experimental-strip-types", "-e", "").Run(); err != nil {
		t.Skip("this Node.js does not support TypeScript type stripping")
	}
	root := t.TempDir()
	result, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: root, Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	const run = `import { strict as assert } from "node:assert";
import { pathToFileURL } from "node:url";
const { OxPrimePlugin } = await import(pathToFileURL(process.argv[1]));
const calls = [];
const plugin = await OxPrimePlugin({
  directory: process.cwd(),
  $: ([command]) => ({
    cwd: directory => ({ quiet: async () => calls.push([command, directory]) }),
  }),
});
await plugin.event({ event: { type: "session.updated" } });
assert.deepEqual(calls, []);
await plugin.event({ event: { type: "session.created" } });
assert.deepEqual(calls, [["ox agent prime", process.cwd()]]);
`
	cmd := exec.CommandContext(t.Context(), node, "--experimental-strip-types", "--input-type=module", "-e", run, filepath.Clean(result.FilesWritten[0]))
	cmd.Dir = root
	cmd.Env = testguard.MinimalEnv(nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute installed OpenCode plugin: %v\n%s", err, out)
	}
}

func TestCheckHooksDetectsAndRepairsLegacyPlugin(t *testing.T) {
	for _, newline := range []string{"lf", "crlf"} {
		t.Run(newline, func(t *testing.T) {
			checkLegacyPluginRepair(t, newline == "crlf")
		})
	}
}

func checkLegacyPluginRepair(t *testing.T, crlf bool) {
	t.Helper()
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	legacy := legacyPluginFixture
	if crlf {
		legacy = strings.ReplaceAll(legacy, "\n", "\r\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	status, err := handleCheckHooks(params)
	if err != nil || status.Installed {
		t.Fatalf("legacy hook should require repair: %+v, %v", status, err)
	}
	if !status.HasOwnedHooks {
		t.Fatal("legacy hook must remain visible to bulk uninstall")
	}
	var before *os.File
	if runtime.GOOS != "windows" {
		before, err = os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer before.Close()
	}
	if _, err := handleInstallHooks(params); err != nil {
		t.Fatal(err)
	}
	if before != nil {
		content, err := io.ReadAll(before)
		if err != nil || string(content) != legacy {
			t.Fatalf("an open reader should still see the complete original plugin: %v", err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("a new reader should see the complete replacement: %v", err)
	}
	status, err = handleCheckHooks(params)
	if err != nil || !status.Installed {
		t.Fatalf("replacement should be installed: %+v, %v", status, err)
	}
}

func writePluginAt(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCheckHooksDoesNotReadOversizedPlugin(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	writePluginAt(t, resolvePluginPath(params.RepoRoot, params.Scope), strings.Repeat("// custom\n", 1000), 0644)
	status, err := handleCheckHooks(params)
	if err != nil || !status.Installed || !status.HasOwnedHooks {
		t.Fatalf("oversized custom plugin should be reported as present: %+v, %v", status, err)
	}
}

func TestInstallHooksRejectsLinkToForeignPlugin(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	target := filepath.Join(params.RepoRoot, "mine.ts")
	const custom = "// coworker's plugin\n"
	writePluginAt(t, target, custom, 0644)
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := handleInstallHooks(params); err == nil || !strings.Contains(err.Error(), "not a generated hook") {
		t.Fatalf("expected linked foreign plugin to be refused, got %v", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != custom {
		t.Fatalf("linked foreign plugin changed: %q, %v", content, err)
	}
}

func TestInstallHooksReportsMissingProjectRoot(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: filepath.Join(t.TempDir(), "missing"), Scope: "project"}
	if _, err := handleInstallHooks(params); err == nil {
		t.Fatal("install succeeded for a project root that does not exist")
	}
}

func TestInstallHooksUserScopeWritesUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	result, err := handleInstallHooks(adapterprotocol.HookParams{Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, openCodeUserPath, openCodePluginFileName)
	if len(result.FilesWritten) != 1 || result.FilesWritten[0] != want {
		t.Fatalf("FilesWritten = %v, want %s", result.FilesWritten, want)
	}
	if content, err := os.ReadFile(want); err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("user plugin not written: %v", err)
	}
}

func TestInstallHooksWithoutRepoRootUsesWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if _, err := handleInstallHooks(adapterprotocol.HookParams{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(resolvePluginPath(cwd, "project")); err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("plugin not written under the working directory: %v", err)
	}
}
