package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func makeCursorTestExecutable(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write test executable: %v", err)
	}
	return path
}

func writeCursorHooksFixture(t *testing.T, repoRoot, content string, mode os.FileMode) string {
	t.Helper()
	directory := filepath.Join(repoRoot, ".cursor")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("mkdir .cursor: %v", err)
	}
	path := filepath.Join(directory, "hooks.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write hooks fixture: %v", err)
	}
	return path
}

func decodeCursorHooksFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks fixture: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode hooks fixture: %v", err)
	}
	return document
}

func cursorFixtureEntries(t *testing.T, document map[string]any, event string) []any {
	t.Helper()
	hooks, ok := document["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks = %#v, want object", document["hooks"])
	}
	entries, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("hooks.%s = %#v, want array", event, hooks[event])
	}
	return entries
}

func TestCursorHooksInstallCheckUninstallPreservesUnrelatedConfiguration(t *testing.T) {
	repoRoot := t.TempDir()
	executableDir := filepath.Join(t.TempDir(), "dir with space")
	if err := os.Mkdir(executableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := makeCursorTestExecutable(t, executableDir, "ox-adapter-'cursor")
	stale := cursorHookCommand("/old/location/ox-adapter-cursor", "sessionStart")
	seed := `{
  "version": 1,
  "futureTopLevel": {"largeNumber": 900719925474099312345, "enabled": true},
  "hooks": {
    "foreignEvent": [{"command": "echo foreign", "timeout": 2, "future": [1, 2]}],
    "sessionStart": [
      {"command": "echo user-session-start", "timeout": 3, "keep": "yes"},
      {"command": ` + mustJSON(t, stale) + `, "timeout": 5, "ownedExtra": {"preserveOnUpgrade": true}}
    ]
  }
}
`
	path := writeCursorHooksFixture(t, repoRoot, seed, 0o640)

	gotPath, changed, err := installCursorHooks(repoRoot, executable)
	if err != nil {
		t.Fatalf("installCursorHooks: %v", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != canonicalPath || !changed {
		t.Fatalf("install path/changed = %q/%v, want %q/true", gotPath, changed, canonicalPath)
	}
	if mode := fileMode(t, path); mode != 0o640 {
		t.Fatalf("installed config mode = %o, want 640", mode)
	}
	document := decodeCursorHooksFixture(t, path)
	future := document["futureTopLevel"].(map[string]any)
	if future["enabled"] != true {
		t.Fatalf("unknown top-level field changed: %#v", future)
	}
	installedBytes, _ := os.ReadFile(path)
	if !strings.Contains(string(installedBytes), "900719925474099312345") {
		t.Fatal("install changed an unknown large JSON number")
	}
	foreign := cursorFixtureEntries(t, document, "foreignEvent")
	if len(foreign) != 1 || foreign[0].(map[string]any)["command"] != "echo foreign" {
		t.Fatalf("foreign hook changed: %#v", foreign)
	}

	for _, event := range cursorHookEvents {
		entries := cursorFixtureEntries(t, document, event)
		wantCommand := cursorHookCommand(executable, event)
		owned := 0
		for _, rawEntry := range entries {
			entry := rawEntry.(map[string]any)
			if entry["command"] == wantCommand {
				owned++
				if entry["timeout"] != float64(cursorHookTimeout) {
					t.Fatalf("hooks.%s timeout = %#v, want %d", event, entry["timeout"], cursorHookTimeout)
				}
				if event == "sessionStart" {
					if _, ok := entry["ownedExtra"]; !ok {
						t.Fatal("upgrading the owned entry dropped its unknown field")
					}
				}
			}
		}
		if owned != 1 {
			t.Fatalf("hooks.%s has %d current entries, want 1: %#v", event, owned, entries)
		}
	}

	installedPath, installed, err := checkCursorHooks(repoRoot, executable)
	if err != nil || !installed || installedPath != canonicalPath {
		t.Fatalf("check = %q/%v/%v, want %q/true/nil", installedPath, installed, err, canonicalPath)
	}
	firstInstall, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err = installCursorHooks(repoRoot, executable)
	if err != nil || changed {
		t.Fatalf("second install changed/error = %v/%v, want false/nil", changed, err)
	}
	secondInstall, _ := os.ReadFile(path)
	if !reflect.DeepEqual(firstInstall, secondInstall) {
		t.Fatal("second install changed hooks.json bytes")
	}

	_, changed, err = uninstallCursorHooks(repoRoot, executable)
	if err != nil || !changed {
		t.Fatalf("uninstall changed/error = %v/%v, want true/nil", changed, err)
	}
	document = decodeCursorHooksFixture(t, path)
	if document["futureTopLevel"].(map[string]any)["enabled"] != true {
		t.Fatal("uninstall dropped unknown top-level data")
	}
	uninstalledBytes, _ := os.ReadFile(path)
	if !strings.Contains(string(uninstalledBytes), "900719925474099312345") {
		t.Fatal("uninstall changed an unknown large JSON number")
	}
	if got := cursorFixtureEntries(t, document, "foreignEvent"); len(got) != 1 {
		t.Fatalf("uninstall changed foreign event: %#v", got)
	}
	startEntries := cursorFixtureEntries(t, document, "sessionStart")
	if len(startEntries) != 1 || startEntries[0].(map[string]any)["command"] != "echo user-session-start" {
		t.Fatalf("uninstall changed unrelated sessionStart entry: %#v", startEntries)
	}
	for _, event := range cursorHookEvents[1:] {
		hooks := document["hooks"].(map[string]any)
		if _, present := hooks[event]; present {
			t.Fatalf("owned event %s remains after uninstall", event)
		}
	}
	firstUninstall, _ := os.ReadFile(path)
	_, changed, err = uninstallCursorHooks(repoRoot, executable)
	if err != nil || changed {
		t.Fatalf("second uninstall changed/error = %v/%v, want false/nil", changed, err)
	}
	secondUninstall, _ := os.ReadFile(path)
	if !reflect.DeepEqual(firstUninstall, secondUninstall) {
		t.Fatal("second uninstall changed hooks.json bytes")
	}
}

func TestCursorHookCommandQuotesAbsoluteExecutable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "space dir")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := makeCursorTestExecutable(t, directory, "ox-adapter-'cursor")
	command := cursorHookCommand(executable, "sessionStart")
	output, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("execute generated command %q: %v (%s)", command, err, output)
	}
	if string(output) != "hook\nsessionStart\n" {
		t.Fatalf("generated command argv output = %q", output)
	}
}

func TestCursorHooksRejectMalformedOrUnsupportedConfigurationWithoutMutation(t *testing.T) {
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"invalid JSON", `{`, "malformed hooks.json"},
		{"null root", `null`, "root must be an object"},
		{"missing version", `{"hooks":{}}`, "version is required"},
		{"unknown version", `{"version":2,"hooks":{}}`, "unsupported hooks.json version 2"},
		{"string version", `{"version":"1","hooks":{}}`, "version must be an integer"},
		{"null hooks", `{"version":1,"hooks":null}`, "hooks must be an object"},
		{"event object", `{"version":1,"hooks":{"sessionStart":{}}}`, "expected an array"},
		{"entry scalar", `{"version":1,"hooks":{"sessionStart":[1]}}`, "hook entry must be an object"},
		{"missing command", `{"version":1,"hooks":{"sessionStart":[{}]}}`, "command is required"},
		{"wrong command type", `{"version":1,"hooks":{"sessionStart":[{"command":1}]}}`, "command must be a nonempty string"},
		{"wrong hook type", `{"version":1,"hooks":{"sessionStart":[{"type":1,"command":"echo ok"}]}}`, "type must be a nonempty string"},
		{"unknown hook type", `{"version":1,"hooks":{"sessionStart":[{"type":"future","prompt":"text"}]}}`, "command is required"},
		{"prompt missing prompt", `{"version":1,"hooks":{"sessionStart":[{"type":"prompt"}]}}`, "prompt is required"},
		{"prompt wrong prompt type", `{"version":1,"hooks":{"sessionStart":[{"type":"prompt","prompt":1}]}}`, "prompt must be a nonempty string"},
		{"prompt with command", `{"version":1,"hooks":{"sessionStart":[{"type":"prompt","prompt":"text","command":"echo no"}]}}`, "command hook type must be command"},
		{"wrong timeout type", `{"version":1,"hooks":{"sessionStart":[{"command":"echo ok","timeout":"10"}]}}`, "timeout must be a positive number"},
		{"zero timeout", `{"version":1,"hooks":{"sessionStart":[{"command":"echo ok","timeout":0}]}}`, "timeout must be a positive number"},
	}
	operations := []struct {
		name string
		run  func(string, string) error
	}{
		{"install", func(root, exe string) error { _, _, err := installCursorHooks(root, exe); return err }},
		{"check", func(root, exe string) error { _, _, err := checkCursorHooks(root, exe); return err }},
		{"uninstall", func(root, exe string) error { _, _, err := uninstallCursorHooks(root, exe); return err }},
	}
	for _, test := range tests {
		for _, operation := range operations {
			t.Run(test.name+"/"+operation.name, func(t *testing.T) {
				repoRoot := t.TempDir()
				path := writeCursorHooksFixture(t, repoRoot, test.content, 0o600)
				before, _ := os.ReadFile(path)
				err := operation.run(repoRoot, executable)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want substring %q", err, test.want)
				}
				after, _ := os.ReadFile(path)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected operation changed malformed config")
				}
			})
		}
	}
}

func TestCursorHooksRejectSymlinkParentsAndLeaves(t *testing.T) {
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	tests := []struct {
		name  string
		setup func(*testing.T, string, string) string
	}{
		{
			name: "symlink parent",
			setup: func(t *testing.T, root, outside string) string {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(root, ".cursor")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return filepath.Join(outside, "hooks.json")
			},
		},
		{
			name: "dangling parent",
			setup: func(t *testing.T, root, outside string) string {
				t.Helper()
				if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(root, ".cursor")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return filepath.Join(outside, "missing", "hooks.json")
			},
		},
		{
			name: "symlink leaf",
			setup: func(t *testing.T, root, outside string) string {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, ".cursor"), 0o755); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(outside, "hooks.json")
				if err := os.WriteFile(target, []byte(`{"version":1,"hooks":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, ".cursor", "hooks.json")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return target
			},
		},
		{
			name: "dangling leaf",
			setup: func(t *testing.T, root, outside string) string {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, ".cursor"), 0o755); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(outside, "missing.json")
				if err := os.Symlink(target, filepath.Join(root, ".cursor", "hooks.json")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return target
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			outside := t.TempDir()
			target := test.setup(t, repoRoot, outside)
			before, _ := os.ReadFile(target)
			if _, _, err := installCursorHooks(repoRoot, executable); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("install error = %v, want symlink refusal", err)
			}
			if _, _, err := checkCursorHooks(repoRoot, executable); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("check error = %v, want symlink refusal", err)
			}
			if _, _, err := uninstallCursorHooks(repoRoot, executable); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("uninstall error = %v, want symlink refusal", err)
			}
			after, _ := os.ReadFile(target)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("symlink refusal changed the external target")
			}
		})
	}
}

func TestCursorHooksCheckIsReadOnly(t *testing.T) {
	repoRoot := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	path, _, err := installCursorHooks(repoRoot, executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fileutil.LockPath(path)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	beforeInfo, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	_, installed, err := checkCursorHooks(repoRoot, executable)
	if err != nil || !installed {
		t.Fatalf("read-only check installed/error = %v/%v", installed, err)
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	if !reflect.DeepEqual(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("check modified hooks.json")
	}
	if _, err := os.Stat(fileutil.LockPath(path)); !os.IsNotExist(err) {
		t.Fatalf("check created a lock file: %v", err)
	}
}

func TestCursorHooksMissingExecutableDoesNotCreateConfig(t *testing.T) {
	repoRoot := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing", "ox-adapter-cursor")
	if _, _, err := installCursorHooks(repoRoot, missing); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("install error = %v, want missing executable", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".cursor")); !os.IsNotExist(err) {
		t.Fatalf("missing executable created config directory: %v", err)
	}
}

func TestCursorHooksCheckDetectsAndInstallRepairsStaleMissingBridge(t *testing.T) {
	repoRoot := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	path, _, err := installCursorHooks(repoRoot, executable)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeCursorHooksFixture(t, path)
	entries := cursorFixtureEntries(t, document, "sessionStart")
	entry := entries[0].(map[string]any)
	entry["command"] = cursorHookCommand(
		filepath.Join(t.TempDir(), "missing", "ox-adapter-cursor"),
		"sessionStart",
	)
	entry["timeout"] = float64(9)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, installed, err := checkCursorHooks(repoRoot, executable)
	if err != nil || installed {
		t.Fatalf("stale check installed/error = %v/%v, want false/nil", installed, err)
	}
	_, changed, err := installCursorHooks(repoRoot, executable)
	if err != nil || !changed {
		t.Fatalf("repair install changed/error = %v/%v, want true/nil", changed, err)
	}
	_, installed, err = checkCursorHooks(repoRoot, executable)
	if err != nil || !installed {
		t.Fatalf("repaired check installed/error = %v/%v, want true/nil", installed, err)
	}
}

func TestCursorHooksRejectNonExecutableBridge(t *testing.T) {
	repoRoot := t.TempDir()
	executable := filepath.Join(t.TempDir(), "ox-adapter-cursor")
	if err := os.WriteFile(executable, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := installCursorHooks(repoRoot, executable); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("install error = %v, want non-executable refusal", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".cursor")); !os.IsNotExist(err) {
		t.Fatalf("non-executable bridge created config directory: %v", err)
	}
}

func TestCursorHooksDoNotTakeOverSimilarForeignCommands(t *testing.T) {
	repoRoot := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	foreign := []string{
		"echo '/tmp/ox-adapter-cursor' hook sessionStart",
		"/tmp/ox-adapter-cursor hook sessionStart",
		cursorHookCommand("/tmp/not-ox-adapter-cursor", "sessionStart"),
		cursorHookCommand("/tmp/ox-adapter-cursor", "stop"),
	}
	entries := make([]map[string]any, 0, len(foreign))
	for _, command := range foreign {
		entries = append(entries, map[string]any{"command": command, "timeout": 4})
	}
	seed, err := json.Marshal(map[string]any{
		"version": 1,
		"hooks":   map[string]any{"sessionStart": entries},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := writeCursorHooksFixture(t, repoRoot, string(seed), 0o600)
	if _, _, err := installCursorHooks(repoRoot, executable); err != nil {
		t.Fatal(err)
	}
	if _, _, err := uninstallCursorHooks(repoRoot, executable); err != nil {
		t.Fatal(err)
	}
	document := decodeCursorHooksFixture(t, path)
	gotEntries := cursorFixtureEntries(t, document, "sessionStart")
	var got []string
	for _, raw := range gotEntries {
		got = append(got, raw.(map[string]any)["command"].(string))
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign commands after lifecycle = %#v, want %#v", got, foreign)
	}
}

func TestCursorHooksPreserveNativePromptEntriesAsUnrelated(t *testing.T) {
	repoRoot := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	promptText := "Review '/tmp/ox-adapter-cursor' hook sessionStart without running it."
	seed := `{
  "version": 1,
  "hooks": {
    "sessionStart": [{
      "type": "prompt",
      "prompt": ` + mustJSON(t, promptText) + `,
      "timeout": 10,
      "model": "cursor-small",
      "opaque": {"large": 900719925474099312345, "escaped": "\\u003ckeep\\u003e"}
    }],
    "futurePromptEvent": [{
      "type": "prompt",
      "prompt": "Keep this future event",
      "timeout": 4,
      "model": {"future": true}
    }]
  }
}
`
	path := writeCursorHooksFixture(t, repoRoot, seed, 0o600)
	_, _, err := installCursorHooks(repoRoot, executable)
	if err != nil {
		t.Fatalf("install with native prompt hooks: %v", err)
	}
	assertPromptHooksPreserved(t, path, promptText)
	_, installed, err := checkCursorHooks(repoRoot, executable)
	if err != nil || !installed {
		t.Fatalf("check with native prompt hooks installed/error = %v/%v", installed, err)
	}
	_, _, err = uninstallCursorHooks(repoRoot, executable)
	if err != nil {
		t.Fatalf("uninstall with native prompt hooks: %v", err)
	}
	assertPromptHooksPreserved(t, path, promptText)
}

func assertPromptHooksPreserved(t *testing.T, path, promptText string) {
	t.Helper()
	document := decodeCursorHooksFixture(t, path)
	startEntries := cursorFixtureEntries(t, document, "sessionStart")
	if len(startEntries) < 1 {
		t.Fatal("sessionStart prompt hook was removed")
	}
	prompt := startEntries[0].(map[string]any)
	if prompt["type"] != "prompt" || prompt["prompt"] != promptText || prompt["model"] != "cursor-small" {
		t.Fatalf("sessionStart prompt hook changed: %#v", prompt)
	}
	opaque := prompt["opaque"].(map[string]any)
	if opaque["escaped"] != `\u003ckeep\u003e` {
		t.Fatalf("opaque prompt data changed: %#v", opaque)
	}
	future := cursorFixtureEntries(t, document, "futurePromptEvent")
	if len(future) != 1 || future[0].(map[string]any)["prompt"] != "Keep this future event" {
		t.Fatalf("future prompt event changed: %#v", future)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "900719925474099312345") {
		t.Fatal("prompt hook's opaque large-number bytes changed")
	}
}

func TestCursorHooksConcurrentInstallsSerialize(t *testing.T) {
	repoRoot := t.TempDir()
	executable := makeCursorTestExecutable(t, t.TempDir(), "cursor-test")
	const writers = 8
	var wait sync.WaitGroup
	errors := make(chan error, writers)
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := installCursorHooks(repoRoot, executable)
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent install: %v", err)
		}
	}
	_, installed, err := checkCursorHooks(repoRoot, executable)
	if err != nil || !installed {
		t.Fatalf("check after concurrent installs = %v/%v", installed, err)
	}
}

func TestCursorHookHandlersRejectUnsupportedScope(t *testing.T) {
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "user"}
	if response, err := handleInstallHooks(params); err == nil || response != nil {
		t.Fatalf("install user scope response/error = %#v/%v", response, err)
	}
	if response, err := handleCheckHooks(params); err == nil || response != nil {
		t.Fatalf("check user scope response/error = %#v/%v", response, err)
	}
	if response, err := handleUninstallHooks(params); err == nil || response != nil {
		t.Fatalf("uninstall user scope response/error = %#v/%v", response, err)
	}
}

func TestCursorHookHandlersProjectLifecycle(t *testing.T) {
	wantEvents := []string{
		"sessionStart",
		"beforeSubmitPrompt",
		"postToolUse",
		"postToolUseFailure",
		"afterAgentResponse",
		"stop",
		"sessionEnd",
		"preCompact",
	}
	if !reflect.DeepEqual(cursorHookEvents, wantEvents) {
		t.Fatalf("cursorHookEvents = %#v, want %#v", cursorHookEvents, wantEvents)
	}
	repoRoot := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"}

	installed, err := handleInstallHooks(params)
	if err != nil {
		t.Fatalf("handleInstallHooks: %v", err)
	}
	if !installed.Installed || !reflect.DeepEqual(installed.Hooks, wantEvents) || len(installed.FilesWritten) != 1 {
		t.Fatalf("install response = %#v", installed)
	}
	checked, err := handleCheckHooks(params)
	if err != nil || !checked.Installed || !checked.HasOwnedHooks || checked.Scope != "project" || len(checked.HookFiles) != 1 {
		t.Fatalf("check response/error = %#v/%v", checked, err)
	}
	installedAgain, err := handleInstallHooks(params)
	if err != nil || !installedAgain.Installed || len(installedAgain.FilesWritten) != 0 {
		t.Fatalf("second install response/error = %#v/%v", installedAgain, err)
	}

	uninstalled, err := handleUninstallHooks(params)
	if err != nil || !uninstalled.Uninstalled || len(uninstalled.FilesModified) != 1 {
		t.Fatalf("uninstall response/error = %#v/%v", uninstalled, err)
	}
	checked, err = handleCheckHooks(params)
	if err != nil || checked.Installed || checked.HasOwnedHooks {
		t.Fatalf("post-uninstall check response/error = %#v/%v", checked, err)
	}
	uninstalledAgain, err := handleUninstallHooks(params)
	if err != nil || !uninstalledAgain.Uninstalled || len(uninstalledAgain.FilesModified) != 0 {
		t.Fatalf("second uninstall response/error = %#v/%v", uninstalledAgain, err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
