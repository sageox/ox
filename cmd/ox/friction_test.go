package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	friction "github.com/sageox/frictionax"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendFrictionEvent_DeliversToSocket verifies that sendFrictionEventTo sends
// a friction event to the daemon socket via IPC. This is a regression test —
// the sendFrictionEvent call was silently dropped during the frictionax migration,
// breaking the CLI→daemon telemetry pipeline with zero test failures.
func TestSendFrictionEvent_DeliversToSocket(t *testing.T) {
	// not parallel: redaction walks the real rootCmd tree, and cobra sorts a
	// command's children in place on first read

	// set up a temp Unix socket (short path for macOS 104-char limit)
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-ftest-%d.sock", time.Now().UnixNano()%100000))
	t.Cleanup(func() { os.Remove(socketPath) })

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8192)
		if n, _ := conn.Read(buf); n > 0 {
			received <- buf[:n]
		}
	}()

	// build event matching what frictionEngine.Handle() returns
	event := &friction.FrictionEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Command:    "ox",
		Subcommand: "badcommand",
		Actor:      "human",
		Input:      "ox badcommand",
		ErrorMsg:   "unknown command \"badcommand\" for \"ox\"",
	}

	// exercise the real sendFrictionEventTo with injected socket
	sendFrictionEventTo(event, socketPath)

	select {
	case msg := <-received:
		msgStr := string(msg)
		assert.Contains(t, msgStr, `"type":"friction"`)
		assert.Contains(t, msgStr, `"unknown-command"`)
		// the mistyped word is user input, so it is redacted before it leaves
		assert.NotContains(t, msgStr, "badcommand")
	case <-time.After(1 * time.Second):
		t.Fatal("friction IPC event was not delivered to socket")
	}
}

type receivedFrictionMessage struct {
	raw     string
	payload daemon.FrictionPayload
}

// listenForFrictionPayload accepts one connection on a temp Unix socket and
// delivers the friction payload it carries. The raw line is returned too, so a
// test can assert on bytes as well as fields.
func listenForFrictionPayload(t *testing.T) (socketPath string, got <-chan receivedFrictionMessage) {
	t.Helper()

	// short path for macOS's 104-char socket limit
	socketPath = filepath.Join(os.TempDir(), fmt.Sprintf("ox-fred-%d.sock", time.Now().UnixNano()%1000000))
	t.Cleanup(func() { os.Remove(socketPath) })

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })

	out := make(chan receivedFrictionMessage, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var msg daemon.Message
		if json.Unmarshal(line, &msg) != nil {
			return
		}
		var payload daemon.FrictionPayload
		if json.Unmarshal(msg.Payload, &payload) != nil {
			return
		}
		out <- receivedFrictionMessage{raw: string(line), payload: payload}
	}()
	return socketPath, out
}

// hermeticFrictionEnv makes sendFrictionEventTo's opt-out checks deterministic:
// an inherited DO_NOT_TRACK or a real user config with telemetry off would
// suppress the send and every assertion below would be about nothing.
func hermeticFrictionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_FRICTION", "true")
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
}

// TestSendFrictionEvent_RedactsUserDataBeforeTheSocket drives the real send path
// with an event stuffed with synthetic user data and reads what arrives on the
// daemon socket. The daemon forwards these strings to the cloud, so the socket
// is the last point the CLI controls.
//
// Failure prevented: a coworker's home path (with OS username), team ID,
// conversation ID, and pasted free text shipped to the cloud inside a friction
// event, which is how a 30-day export came to contain them.
func TestSendFrictionEvent_RedactsUserDataBeforeTheSocket(t *testing.T) {
	hermeticFrictionEnv(t)
	socketPath, received := listenForFrictionPayload(t)

	event := &friction.FrictionEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-flag",
		Command:    "ox plan",
		Actor:      "human",
		PathBucket: "repo",
		Input: "plan list --limit 50 --json --file /Users/person-a/src/x.html " +
			"--team team_abc123 --id cnv_01a00000-0000-7000-8000-000000000000 fix the login bug",
		ErrorMsg: `unknown command "plan show /Users/person-a/src/x.html team_abc123 fix the login bug" for "ox"` +
			"\n\nRun 'ox --help' for usage",
		Suggestion: "plan view /Users/person-a/src/x.html team_abc123 cnv_01a00000-0000-7000-8000-000000000000 --json",
	}

	sendFrictionEventTo(event, socketPath)

	var msg receivedFrictionMessage
	select {
	case msg = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("friction event was not delivered to the socket")
	}

	for _, leak := range []string{
		"person-a", "/Users", "x.html", "team_abc123", "cnv_01a", "01a00000", "fix the login bug", "login",
	} {
		assert.NotContains(t, msg.raw, leak, "the socket payload must not carry %q", leak)
	}

	// the command path and flag names are the diagnostic signal and must survive
	assert.Equal(t, "ox plan", msg.payload.Command)
	assert.Equal(t, "unknown-flag", msg.payload.Kind)
	assert.Equal(t, "plan list --limit <arg> --json --file <arg> --team <arg> --id <arg>", msg.payload.Input)
	assert.Equal(t, `unknown command "plan <arg>" for "ox"`, msg.payload.ErrorMsg)
	assert.Equal(t, "plan view <arg> --json", msg.payload.Suggestion)
}

// TestFrictionIPC_CalledFromRecoveryPath is a source-level contract test.
// It verifies that executeWithFrictionRecovery calls sendFrictionEvent.
// This catches the exact regression from the frictionax migration where
// sendFrictionEvent was silently removed from the call site.
func TestFrictionIPC_CalledFromRecoveryPath(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(repoPath("main.go"))
	require.NoError(t, err, "should be able to read main.go")

	// check for an uncommented call (tab-indented, no leading //)
	found := false
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "sendFrictionEvent(result.Event)" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"executeWithFrictionRecovery must call sendFrictionEvent(result.Event) — "+
			"removing this breaks CLI→daemon friction telemetry")
}

// TestSendFrictionEvent_Suppressed verifies every path that must NOT reach the
// daemon, using an injected socket so "suppressed" means "nothing arrived"
// rather than "the call returned".
//
// Failure prevented: two of these are telemetry opt-outs. Asserting only that
// the call does not panic passes against a build that ignores them entirely and
// sends the event anyway.
func TestSendFrictionEvent_Suppressed(t *testing.T) {
	sample := &friction.FrictionEvent{Kind: "unknown-command", Input: "ox bad"}

	tests := []struct {
		name  string
		env   map[string]string
		event *friction.FrictionEvent
	}{
		{
			// The nil guard is the first line of sendFrictionEventTo. Without
			// it a nil event either panics or serializes to a null payload and
			// is delivered as a real event.
			name:  "a nil event",
			event: nil,
		},
		{
			name:  "DO_NOT_TRACK=1",
			env:   map[string]string{"DO_NOT_TRACK": "1"},
			event: sample,
		},
		{
			name:  "SAGEOX_FRICTION=false",
			env:   map[string]string{"SAGEOX_FRICTION": "false"},
			event: sample,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Neutralize both opt-outs before applying the case's own. An
			// inherited DO_NOT_TRACK=1 would suppress the SAGEOX_FRICTION case
			// for the wrong reason, and vice versa, so each case would pass
			// against a build that ignores the variable it is meant to prove.
			t.Setenv("DO_NOT_TRACK", "")
			t.Setenv("SAGEOX_FRICTION", "true")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-fsup-%d.sock", time.Now().UnixNano()%100000))
			t.Cleanup(func() { os.Remove(socketPath) })

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			received := make(chan struct{}, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				received <- struct{}{}
			}()

			sendFrictionEventTo(tt.event, socketPath)

			select {
			case <-received:
				t.Fatal("event reached the daemon but delivery should have been suppressed")
			case <-time.After(50 * time.Millisecond):
				// good — nothing sent
			}
		})
	}
}

// --- Friction must never break the usage-error path ---

// restoreRootCmdState undoes what executeWithFrictionRecovery leaves behind as
// the process entry point: it resets rootCmd's flags and registers new flag
// objects, sets root args, assigns the command context, and sets the cli output
// modes from the run's flags. Left in place, a later in-process command parses
// --json into the old flag object while readers look up the new one through
// cmd.Root().PersistentFlags(), a --json run leaves every cli printer in JSON
// mode, and a later rootCmd.Execute() reuses the run's context, which the
// entry point cancels on return.
func restoreRootCmdState(t *testing.T) {
	t.Helper()
	flags, persistent := rootCmd.Flags(), rootCmd.PersistentFlags()
	rootCtx := rootCmd.Context()
	savedCtx, savedPreRun := cliCtx, preRunReached
	t.Cleanup(func() {
		rootCmd.ResetFlags()
		rootCmd.Flags().AddFlagSet(flags)
		rootCmd.PersistentFlags().AddFlagSet(persistent)
		rootCmd.SetArgs(nil)
		rootCmd.SetContext(rootCtx)
		cliCtx, preRunReached = savedCtx, savedPreRun
		resetCLIOutputModes()
	})
}

// TestRestoreRootCmdState_NextCommandStartsWithALiveContext runs the entry
// point with a context that is canceled once it returns, as every
// executeWithSignalContext run's is, then runs another command in process.
// Failure prevented: cobra keeps the context root last executed with and a
// later rootCmd.Execute() reuses it, so the next in-process command started
// already canceled.
func TestRestoreRootCmdState_NextCommandStartsWithALiveContext(t *testing.T) {
	probe := func(use string) *cobra.Command {
		cmd := &cobra.Command{
			Use:    use,
			Hidden: true,
			RunE:   func(cmd *cobra.Command, _ []string) error { return cmd.Context().Err() },
		}
		rootCmd.AddCommand(cmd)
		t.Cleanup(func() { rootCmd.RemoveCommand(cmd) })
		return cmd
	}
	probe("ctx-probe-entry")
	probe("ctx-probe-next")

	t.Run("entry point", func(t *testing.T) {
		restoreRootCmdState(t)
		savedCfg := cfg
		t.Cleanup(func() { cfg = savedCfg })
		ctx, cancel := context.WithCancel(context.Background())
		require.Equal(t, 0, executeWithFrictionRecovery(ctx, []string{"ctx-probe-entry"}, 0))
		cancel()
	})

	_, err := executeRootCLI(t, "ctx-probe-next")
	require.NoError(t, err, "the next command inherited the entry point's canceled context")
}

// panickingFrictionAdapter panics from inside frictionax's Handle, the way
// frictionax v0.1.2's isGitRepo did in Windows directories.
type panickingFrictionAdapter struct{}

func (panickingFrictionAdapter) CommandNames() []string    { return nil }
func (panickingFrictionAdapter) FlagNames(string) []string { return nil }
func (panickingFrictionAdapter) ParseError(error) *friction.ParsedError {
	panic("runtime error: slice bounds out of range [:-1]")
}

// TestExecuteWithFrictionRecovery_FrictionPanicKeepsOriginalError drives
// mistyped invocations through the real recovery path with a frictionax engine
// that panics.
// Failure prevented: a bug inside frictionax replaces the coworker's error, and
// the exit code a script relies on, with a Go stack trace.
func TestExecuteWithFrictionRecovery_FrictionPanicKeepsOriginalError(t *testing.T) {
	restoreRootCmdState(t)
	prev := frictionEngine
	frictionEngine = friction.New(panickingFrictionAdapter{})
	t.Cleanup(func() { frictionEngine = prev })

	// Both invocations fail at the root, so no subcommand merges the flag
	// objects executeWithFrictionRecovery mints and restoreRootCmdState discards.
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"unknown command", []string{"statuss"}, `unknown command "statuss"`},
		{"unknown flag", []string{"--no-such-flag"}, "unknown flag: --no-such-flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := -1
			stderr := captureStderr(t, func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("friction panic reached the coworker: %v", r)
					}
				}()
				code = executeWithFrictionRecovery(context.Background(), tt.args, 0)
			})
			assert.Equal(t, 1, code, "a usage error must still exit 1")
			assert.Contains(t, stderr, tt.wantErr, "the original error must still reach the coworker")
		})
	}
}
