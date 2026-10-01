package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/internal/testutil/slogquiet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// packageDir is the directory `go test` starts in — the cmd/ox source directory,
// inside the real ox repository. Captured before TestMain moves away from it, for
// the few tests that legitimately need to read fixtures from the source tree.
var packageDir string

// repoPath resolves a path relative to the ox source tree, for tests that read
// checked-in fixtures. Use it instead of a relative literal: the process working
// directory is deliberately NOT the source tree while tests run.
func repoPath(parts ...string) string {
	return filepath.Join(append([]string{packageDir}, parts...)...)
}

// TestMain moves the process OUT of the ox repository before any test runs.
//
// Doctor checks resolve their target repository with findGitRoot(), which walks
// up from the process working directory. `go test` starts inside cmd/ox, so a
// check invoked without chdir'ing into a fixture resolved to the DEVELOPER'S OWN
// CHECKOUT — and FixLevelAuto checks then reconciled it. `go test ./cmd/ox/ -run
// TestDoctor` deleted nine tracked skills from the working tree and staged the
// lockfile. The checks were always cwd-resolved; the ox-cli-* rename only made
// the consequence visible, because reconcile finally had old names to retire
// rather than identical content to rewrite.
//
// Starting in a directory that is not a git repository at all makes the failure
// mode structural rather than a rule every future test author has to remember: a
// check that does not chdir into a fixture now resolves NO repository and skips,
// instead of silently operating on the wrong one. Tests that need a repository
// still build one and chdir into it exactly as before.
func TestMain(m *testing.M) {
	// m.Run aliases stderr to stdout in JSON mode; retain both original pipes.
	stdout, stderr := os.Stdout, os.Stderr
	slogquiet.Silence()

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot resolve working directory: %v\n", err)
		os.Exit(1)
	}
	packageDir = wd

	sandbox, err := os.MkdirTemp("", "ox-cmd-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot create sandbox: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chdir(sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot enter sandbox: %v\n", err)
		os.Exit(1)
	}

	// Verify the sandbox is not itself inside a repository, BEFORE any test runs.
	//
	// os.MkdirTemp uses os.TempDir(), which honors $TMPDIR (or %TMP%/%TEMP%). If
	// that points under a checkout — a developer with TMPDIR set to a scratch dir
	// in a repo, or a CI image that does — findGitRoot() still resolves a
	// repository after the chdir, and a FixLevelAuto check reached before the
	// tripwire test would reconcile it. The tripwire alone is not enough: it runs
	// in test order, and TestRunDoctorChecks_WithFixFlag may run first.
	if root := findGitRoot(); root != "" {
		fmt.Fprintf(os.Stderr,
			"cmd/ox tests: sandbox %s is inside git repository %s.\n"+
				"A FixLevelAuto doctor check would reconcile that repository. "+
				"Set TMPDIR to a directory outside any checkout and re-run.\n",
			sandbox, root)
		_ = os.Chdir(packageDir)
		_ = os.RemoveAll(sandbox)
		os.Exit(1)
	}

	code := m.Run()

	// Leave the sandbox before removing it; some platforms refuse to unlink the
	// working directory out from under a live process.
	_ = os.Chdir(packageDir)
	_ = os.RemoveAll(sandbox)

	// init installs the same output pipes in the test binary. Drain them too,
	// or `go test -list` can drop the tail of the acceptance test inventory.
	stdout.Close()
	if stdoutDone != nil {
		if err := <-stdoutDone; err != nil {
			fmt.Fprintf(stderr, "cmd/ox tests: flush stdout: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	stderr.Close()
	stderrWg.Wait()
	os.Exit(code)
}

// A failed output destination must neither report success nor leave a large
// writer blocked after the color-stripping pipe stops copying.
func TestCLIOutputWriteFailures(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))
	require.Greater(t, len(releaseNotes), 64*1024, "large-output fixture must exceed a typical pipe buffer")
	// The output pipe is shared by all formats. Cover its distinct failure
	// modes here; TestJSONOutputModesCLI already checks text and JSON output.
	for _, tt := range []struct {
		name        string
		args        []string
		destination string
		exitCode    int
	}{
		{"flush full output", []string{"release-notes", "--raw"}, "writable", 0},
		{"unblock failed writer", []string{"release-notes", "--raw"}, "unwritable", 1},
		{"preserve exit code", []string{"sync", "--read-only", "--json", "unexpected"}, "unwritable", 2},
		{"quiet broken pipe", []string{"release-notes", "--raw"}, "closed_pipe", -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.destination == "closed_pipe" && runtime.GOOS == "windows" {
				t.Skip("Windows does not use Unix SIGPIPE semantics")
			}
			env := append(noInputCLIEnv(t), "FEATURE_CLOUD=false", "FEATURE_AUTH=false", "CLICOLOR_FORCE=0")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, t.TempDir(), env, tt.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			switch tt.destination {
			case "unwritable":
				path := filepath.Join(t.TempDir(), "output")
				require.NoError(t, os.WriteFile(path, nil, 0o600))
				file, err := os.Open(path)
				require.NoError(t, err)
				t.Cleanup(func() { _ = file.Close() })
				cmd.Stdout = file
			case "closed_pipe":
				reader, writer, err := os.Pipe()
				require.NoError(t, err)
				t.Cleanup(func() { _ = writer.Close() })
				require.NoError(t, reader.Close())
				cmd.Stdout = writer
			}
			err := cmd.Run()
			require.NoError(t, ctx.Err(), "output failure left the command blocked: %s", stderr.String())
			if tt.destination == "closed_pipe" {
				// Consumers such as head may close stdout early. Keep Go's
				// quiet Unix pipeline termination instead of printing an error.
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				status, ok := exitErr.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				assert.Equal(t, syscall.SIGPIPE, status.Signal())
				assert.Empty(t, stderr.String())
				return
			}
			if tt.exitCode == 0 {
				require.NoError(t, err, "stderr: %s", stderr.String())
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, "stdout: %s; stderr: %s", stdout.String(), stderr.String())
				assert.Equal(t, tt.exitCode, exitErr.ExitCode())
			}
			if tt.destination == "unwritable" {
				assert.Contains(t, stderr.String(), "write")
				assert.Equal(t, 1, strings.Count(stderr.String(), "Error:"), "report the output error once")
			} else {
				assert.Equal(t, releaseNotes+"\n", stdout.String(), "the full command result must be flushed")
				assert.Empty(t, stderr.String())
			}
		})
	}
}

// A misspelled subcommand must fail without printing successful help or running
// a suggested command. Exercise the real parser, output streams, and exit code.
func TestCLIInvalidSubcommands(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))
	// Root, empty group, nested group, and alias exercise different dispatch
	// paths. The typo cases below cover ordinary and runnable groups.
	for _, group := range []string{"", "agent hooks", "plan feedback", "bubble"} {
		t.Run(group, func(t *testing.T) {
			env := append(noInputCLIEnv(t), "FEATURE_CLOUD=false", "FEATURE_AUTH=false")
			args := append(strings.Fields(group), "unexpected-command", "--json")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, t.TempDir(), env, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			require.NoError(t, ctx.Err(), "stderr: %s", stderr.String())
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "stdout: %s; stderr: %s", stdout.String(), stderr.String())
			assert.Equal(t, 1, exitErr.ExitCode())
			assert.Empty(t, stdout.String(), "usage errors must not look like successful output")
			canonical := group
			if group == "bubble" {
				canonical = "kb"
			}
			commandPath := strings.TrimSpace("ox " + canonical)
			assert.Contains(t, stderr.String(), fmt.Sprintf("Error: unknown command %q for %q", "unexpected-command", commandPath))
			assert.Contains(t, stderr.String(), "Run '"+commandPath+" --help' for usage")
			assert.NotContains(t, stderr.String(), "\nUsage\n", "keep error guidance concise")
		})
	}

	for _, tt := range []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{"bare root", nil, "Usage", false},
		{"bare group", []string{"plan"}, "ox plan", false},
		{"explicit help", []string{"plan", "unexpected-command", "--help"}, "ox plan", false},
		{"help command", []string{"help", "plan"}, "ox plan", false},
		{"root version", []string{"--version"}, "ox version", false},
		{"nested help without loading config", []string{"plan", "feedback", "--config", "missing.yaml"}, "ox plan feedback", false},
		{"catalog correction", []string{"daemons"}, "ox daemon", false},
		{"JSON typo", []string{"--json", "plan", "lsit"}, "Did you mean 'ox plan list'?", true},
		{"runnable group typo", []string{"session", "lsit"}, "Did you mean 'ox session list'?", true},
		{"custom dispatcher", []string{"agent", "unexpected-command"}, "unknown command or invalid agent_id: unexpected-command", true},
		{"global flag value", []string{"--config", "unused.yaml", "plan", "foo"}, `unknown command "foo" for "ox plan"`, true},
		{"literal help argument", []string{"plan", "--", "--help"}, `unknown command "--help" for "ox plan"`, true},
		{"invalid flag", []string{"plan", "--invalid-flag"}, "unknown flag: --invalid-flag", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := append(noInputCLIEnv(t), "FEATURE_CLOUD=false", "FEATURE_AUTH=false")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, t.TempDir(), env, tt.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			require.NoError(t, ctx.Err(), "stderr: %s", stderr.String())
			if tt.fail {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, "stdout: %s; stderr: %s", stdout.String(), stderr.String())
				assert.Equal(t, 1, exitErr.ExitCode())
				assert.Empty(t, stdout.String())
				assert.Contains(t, stderr.String(), tt.want)
				assert.True(t, strings.HasPrefix(stderr.String(), "Error:"), "show the error before suggestions: %s", stderr.String())
			} else {
				require.NoError(t, err, "stderr: %s", stderr.String())
				assert.Contains(t, stdout.String(), tt.want)
				if tt.name == "catalog correction" {
					assert.Contains(t, stderr.String(), "Correcting to: daemon")
				} else {
					assert.Empty(t, stderr.String())
				}
			}
		})
	}
}
