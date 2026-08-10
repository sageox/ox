//go:build slow

// Package adapters_test proves every shipped adapter can actually do what its
// info response claims.
//
// The capability list is a promise the runtime never checked. Four adapters —
// codex, pi, droid and gemini — advertised incremental_reader and answered
// read-from-offset with "read-from-offset not implemented". The daemon's
// catch-up read is gated on that capability, so on every daemon restart the
// turns written since the last persisted offset were dropped, and the only
// trace was one Warn line.
//
// Unit tests could not catch this: each adapter's tests called its own
// functions directly, never the dispatch table that decides whether a
// subcommand exists. This test drives the real binaries the same way the
// daemon does.
package adapters_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// capabilityProbe maps a declared capability to a subcommand that must not
// answer "not implemented".
//
// The probes deliberately pass arguments that cannot succeed — a path that
// does not exist, an empty repo root. We are testing that the handler is
// WIRED, not that it works on real data; conformance tests in each adapter's
// own package cover the behavior. A real handler reports a real problem
// ("failed to open session file"); an unwired one reports that the subcommand
// itself is missing.
var capabilityProbe = map[string][]string{
	"incremental_reader": {"read-from-offset", "--session-file", "/nonexistent/ox-probe.jsonl", "--offset", "0"},
	"session_reader":     {"read", "--session-file", "/nonexistent/ox-probe.jsonl"},
	"session_importer":   {"import-session", "--session-id", "ox-probe-nonexistent"},
}

// notImplemented is how pkg/adapterruntime reports an unregistered handler.
const notImplemented = "not implemented"

type infoResponse struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

func TestEveryDeclaredCapabilityIsImplemented(t *testing.T) {
	repoRoot := repoRoot(t)
	binDir := t.TempDir()

	for _, adapter := range adapterDirs(t, repoRoot) {
		t.Run(adapter, func(t *testing.T) {
			bin := build(t, repoRoot, adapter, binDir)

			var info infoResponse
			out := run(t, bin, "info")
			if err := json.Unmarshal([]byte(out), &info); err != nil {
				t.Fatalf("info did not return JSON: %v\n%s", err, out)
			}
			if len(info.Capabilities) == 0 {
				t.Fatalf("%s declares no capabilities", adapter)
			}

			for _, capability := range info.Capabilities {
				args, probed := capabilityProbe[capability]
				if !probed {
					continue // no probe defined for this capability yet
				}
				t.Run(capability, func(t *testing.T) {
					got := run(t, bin, args...)
					if strings.Contains(got, notImplemented) {
						t.Errorf("%s advertises %q but %q reports it is not implemented.\n"+
							"  response: %s\n"+
							"  Anything gated on this capability — the daemon's catch-up read, for one — "+
							"fails silently and drops data.",
							adapter, capability, args[0], strings.TrimSpace(got))
					}
				})
			}
		})
	}
}

// adapterDirs lists every shipped adapter except the test double.
func adapterDirs(t *testing.T, repoRoot string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repoRoot, "cmd", "ox-adapter-*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range matches {
		name := strings.TrimPrefix(filepath.Base(m), "ox-adapter-")
		if name == "test" {
			continue // the protocol test double, not a shipped agent
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("found no adapters to check — the glob is wrong and this test proves nothing")
	}
	return out
}

func build(t *testing.T, repoRoot, adapter, binDir string) string {
	t.Helper()
	bin := filepath.Join(binDir, "ox-adapter-"+adapter)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ox-adapter-"+adapter)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s failed: %v\n%s", adapter, err, out)
	}
	return bin
}

// run returns combined output; a non-zero exit is expected and meaningful
// here, since every probe is designed to fail.
func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, _ := exec.Command(bin, args...).CombinedOutput()
	return string(out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repo root")
		}
		dir = parent
	}
}
