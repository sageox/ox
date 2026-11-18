package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

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
	legacy := strings.Replace(openCodePluginTemplate, "await $", "await ", 1)
	if legacy == openCodePluginTemplate {
		t.Fatal("fixture must contain the legacy invocation")
	}
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
	if _, err := handleInstallHooks(params); err != nil {
		t.Fatal(err)
	}
	status, err = handleCheckHooks(params)
	if err != nil || !status.Installed {
		t.Fatalf("replacement should be installed: %+v, %v", status, err)
	}
}
