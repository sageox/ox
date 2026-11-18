package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestInstalledOpenCodePluginPrimesOnSessionCreated(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js 22+ is needed to execute the generated TypeScript")
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
	cmd := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", run, filepath.Clean(result.FilesWritten[0]))
	cmd.Dir = root
	cmd.Env = testguard.MinimalEnv(nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute installed OpenCode plugin: %v\n%s", err, out)
	}
}
