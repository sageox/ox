package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Environment-selected JSON must reach local, inherited, and root flag readers.
// Explicit flags must still override it, and AI coworker defaults must survive.
func TestJSONOutputModesCLI(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))
	for _, command := range []struct {
		name        string
		args        []string
		agentEnv    string
		jsonField   string
		text        string
		exitCode    int
		defaultJSON bool
	}{
		{"local flag", []string{"version"}, "", "version", "Built:", 0, false},
		{"inherited flag", []string{"sync"}, "", "transport", "Transport: failed", 1, false},
		{"root flag", []string{"session", "list"}, "", "sessions", "Not in a SageOx project", 0, false},
		{"AI coworker default", []string{"session", "list"}, "codex", "sessions", "Not in a SageOx project", 0, true},
	} {
		for _, mode := range []struct {
			name     string
			env      string
			flag     string
			wantJSON bool
		}{
			{"default", "", "", command.defaultJSON},
			{"environment", "1", "", true},
			{"flag enables", "0", "--json", true},
			{"flag disables", "1", "--json=false", false},
		} {
			t.Run(command.name+"/"+mode.name, func(t *testing.T) {
				env := append(noInputCLIEnv(t), "OX_NO_DAEMON=1", "FEATURE_CLOUD=false", "FEATURE_AUTH=false",
					"OX_JSON="+mode.env, "AGENT_ENV="+command.agentEnv)
				args := append([]string(nil), command.args...)
				if mode.flag != "" {
					args = append(args, mode.flag)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := testguard.OxCmdContext(t, ctx, oxBin, t.TempDir(), env, args...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				require.NoError(t, ctx.Err(), "command timed out: %s", stderr.String())
				if command.exitCode == 0 {
					require.NoError(t, err, "stderr: %s", stderr.String())
				} else {
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr, "stdout: %s; stderr: %s", stdout.String(), stderr.String())
					assert.Equal(t, command.exitCode, exitErr.ExitCode())
				}
				if mode.wantJSON {
					var result map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), "stdout must contain one JSON result: %s", stdout.String())
					assert.Contains(t, result, command.jsonField)
					if command.agentEnv != "" {
						assert.Contains(t, result, "guidance", "fixture must detect the AI coworker")
					}
					assert.NotContains(t, stderr.String(), "Error:")
				} else {
					assert.False(t, json.Valid(stdout.Bytes()))
					assert.Contains(t, stdout.String(), command.text)
				}
			})
		}
	}
}

// json_output_guard_test.go — keeps `--json` output going through one printer.
//
// ox had grown 100+ JSON emitters across cmd/ox, each choosing its own indent
// and none of them themed, because writing `json.NewEncoder(os.Stdout)` is
// always the shortest path at the call site. The cost only shows up in
// aggregate, which is exactly the kind of drift a review does not catch.
//
// See .claude/rules/json-output.md for the API and the never-colorize list.

// terminalJSONEncoder matches an encoder constructed directly over a terminal
// stream. Deliberately narrow: it does NOT try to catch every
// "marshal then fmt.Println" shape, because distinguishing those from a file
// write needs real dataflow analysis, and a guard with false positives gets
// disabled. This catches the shape that actually recurs.
var terminalJSONEncoder = regexp.MustCompile(`json\.NewEncoder\(\s*(os\.Stdout|cmd\.OutOrStdout\(\))\s*\)`)

// jsonEncoderExemptions are files allowed to build their own encoder over a
// terminal stream, each for a stated reason. Adding an entry here is a design
// decision; adding one without a reason is how the guard becomes decoration.
var jsonEncoderExemptions = map[string]string{}

// TestNoHandRolledTerminalJSONEncoders fails when a command encodes JSON
// straight to stdout instead of using cli.PrintJSONTo.
//
// Failure prevented: a new `--json` flag that renders plain for a human and
// picks its own indentation, silently reintroducing the inconsistency this
// printer exists to remove.
func TestNoHandRolledTerminalJSONEncoders(t *testing.T) {
	t.Parallel()

	// Resolve the source directory from this file's own path, NOT from the
	// working directory: this package's TestMain chdirs into a temp dir, so
	// os.ReadDir(".") returns zero entries and every file-scanning check
	// silently passes by scanning nothing.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	// An empty or unreadable listing must not read as "no violations" — that
	// is the bug this guard itself shipped with. cmd/ox has hundreds of files;
	// if the scan sees almost none, it is looking in the wrong place.
	scanned := 0
	defer func() {
		if scanned < 50 {
			t.Errorf("scanned only %d .go files in %s — the guard is not looking at the real source tree", scanned, dir)
		}
	}()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, exempt := jsonEncoderExemptions[name]; exempt {
			continue
		}
		scanned++
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			if terminalJSONEncoder.MatchString(line) {
				t.Errorf("%s:%d encodes JSON straight to a terminal stream: %s\n"+
					"\tUse cli.PrintJSONTo(w, payload) — see .claude/rules/json-output.md.\n"+
					"\tIf this genuinely must not be themed, add %s to jsonEncoderExemptions with the reason.",
					name, i+1, strings.TrimSpace(line), name)
			}
		}
	}
}
