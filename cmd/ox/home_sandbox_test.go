package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/paths"
)

// shadowedCLIs are third-party tools ox execs that keep per-user state under the
// home directory or in the OS keychain. `claude auth status` rewrote the
// developer's ~/.claude.json, and `gh` wrote ~/.local/state/gh/device-id; worse,
// a test's outcome depended on whether the developer happened to be logged in.
// Each is shadowed by a stub that fails the way an installed-but-logged-out CLI
// does. A test that needs one prepends its own fake to PATH, which still wins.
var shadowedCLIs = []string{"claude", "codex", "gemini", "gh"}

// developerEnv records each variable isolateHome changed, as the test binary
// started with it, so useDeveloperHome can put it back.
var developerEnv = map[string]*string{}

// isolateHome points every home-derived location at <sandbox>/home and puts
// stubs for shadowedCLIs at the front of PATH, then refuses to run if any of it
// did not take.
//
// `go test ./cmd/ox/` used to write into the developer's real home: it replaced
// ~/.local/share/sageox/cli/known-repos.json, left session caches and an auth
// lock under ~/.local/share/sageox/ and ~/.config/sageox/, and ran the real
// `claude auth status`. Fixing writers one at a time leaves the next one to be
// found the same way; with the home in the sandbox, a test that forgets to
// isolate itself writes into a directory that is removed after the run.
func isolateHome(sandbox string) error {
	// Pin the Go toolchain's directories BEFORE moving the home: on macOS
	// GOCACHE defaults under ~/Library/Caches, so tests that `go build` a binary
	// would otherwise compile from a cold cache, and GOENV holds the developer's
	// `go env -w` settings (GOPRIVATE, GOFLAGS).
	if err := pinGoEnv(); err != nil {
		return err
	}

	home := filepath.Join(sandbox, "home")
	bin := filepath.Join(sandbox, "bin")
	for _, dir := range []string{home, bin} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}

	// USERPROFILE is what os.UserHomeDir reads on Windows.
	setenv := map[string]string{"HOME": home, "USERPROFILE": home}
	// Unset rather than point into the sandbox, so the defaults derived from HOME
	// are what tests exercise. CLAUDE_CONFIG_DIR and CODEX_HOME relocate those
	// agents' state, which ox reads and imports.
	unset := []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"CLAUDE_CONFIG_DIR", "CODEX_HOME",
	}

	// The stubs are POSIX shell scripts. Windows would need a .bat port that no
	// CI job runs, and a shim nobody exercises is how the next fail-open gets
	// written, so the real CLIs stay reachable there; the home is still isolated.
	if runtime.GOOS != "windows" {
		for _, name := range shadowedCLIs {
			stub := fmt.Sprintf("#!/bin/sh\necho '%s is shadowed in cmd/ox tests; put a fake first on PATH' >&2\nexit 127\n", name)
			if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
				return err
			}
		}
		setenv["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	}

	for key, value := range setenv {
		recordDeveloperEnv(key)
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	for _, key := range unset {
		recordDeveloperEnv(key)
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}

	// Without the developer's ~/.gitconfig, git has no identity to commit with.
	// CI sets one globally, which the sandbox now hides too. A fixed identity
	// also keeps settings such as commit.gpgsign out of the tests.
	gitconfig := "[user]\n\tname = ox test\n\temail = ox-test@example.com\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		return err
	}

	return verifyHomeSandbox(home, bin)
}

func recordDeveloperEnv(key string) {
	if value, ok := os.LookupEnv(key); ok {
		developerEnv[key] = &value
	} else {
		developerEnv[key] = nil
	}
}

// verifyHomeSandbox checks the redirect from the outside, the way the code under
// test will see it. Calling into internal/paths here also pins its cached home
// directory to the sandbox for the rest of the run.
func verifyHomeSandbox(home, bin string) error {
	inside := func(path string) bool { return strings.HasPrefix(path, home+string(filepath.Separator)) }

	if got, err := os.UserHomeDir(); err != nil || got != home {
		return fmt.Errorf("os.UserHomeDir() = %q (err=%v), want the home sandbox %s", got, err, home)
	}
	for name, dir := range map[string]string{
		"paths.ConfigDir()": paths.ConfigDir(),
		"paths.DataDir()":   paths.DataDir(),
		"paths.CacheDir()":  paths.CacheDir(),
	} {
		if !inside(dir) {
			return fmt.Errorf("%s = %q, outside the home sandbox %s. internal/paths caches the "+
				"home directory on first use, so something resolved it during package init, "+
				"before TestMain ran: likely a package-level var that calls internal/paths. "+
				"Resolve that path when it is used instead", name, dir, home)
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range shadowedCLIs {
			got, err := exec.LookPath(name)
			if err != nil || filepath.Dir(got) != bin {
				return fmt.Errorf("%s resolves to %q (err=%v), not the stub in %s", name, got, err, bin)
			}
		}
	}
	return nil
}

// pinGoEnv fixes the Go toolchain's home-derived directories to the values they
// have right now, so they survive the move of HOME.
func pinGoEnv() error {
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	if _, err := exec.LookPath("go"); err != nil {
		return nil // no toolchain on PATH: no test can build a binary either
	}
	cmd := exec.Command("go", append([]string{"env"}, keys...)...)
	cmd.Dir = packageDir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("go env: %w: %s", err, exitErr.Stderr)
		}
		return fmt.Errorf("go env: %w", err)
	}
	values := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(values) != len(keys) {
		return fmt.Errorf("go env %s: got %d lines, want %d", strings.Join(keys, " "), len(values), len(keys))
	}
	for i, key := range keys {
		if value := strings.TrimRight(values[i], "\r"); value != "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// useDeveloperHome undoes isolateHome for the rest of the test: the developer's
// real home, agent config, and PATH. Only opt-in live tests that deliberately
// drive the developer's own logged-in CLIs may call it. Like t.Setenv, it
// cannot be used in a parallel test.
func useDeveloperHome(t *testing.T) {
	t.Helper()
	for key, value := range developerEnv {
		if value != nil {
			t.Setenv(key, *value)
			continue
		}
		t.Setenv(key, "") // registers the restore
		_ = os.Unsetenv(key)
	}
}
