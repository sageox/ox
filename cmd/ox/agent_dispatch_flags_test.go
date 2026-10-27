package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The agent dispatcher (`ox agent <id> <verb> ...`) hands arguments to manual
// flag parsers, but cobra parses flags BEFORE the dispatcher runs. A flag the
// manual parser accepts that agentCmd never registered is therefore rejected
// as "unknown flag" before any handler sees it. Unit tests that call a handler
// directly (runAgentSessionLog, runAgentQuery, ...) bypass cobra and cannot see
// this; the tests below drive the real root command.

const (
	dispatchAgentID    = "OxDsp1"
	dispatchOtherAgent = "OxDsp2"
)

// resetAgentDispatchFlags returns every flag on agentCmd to its default. pflag
// never resets a parsed value, so without this a flag set by one Execute leaks
// into the next one through the reinjection code.
func resetAgentDispatchFlags(t *testing.T) {
	t.Helper()
	reset := func() {
		agentCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		agentCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	reset()
	t.Cleanup(reset)
}

// dispatchTestEnv isolates the process from the developer's agent, daemon,
// telemetry and endpoint, and returns an initialized project root.
func dispatchTestEnv(t *testing.T) string {
	t.Helper()
	root := setupSessionTestProject(t)
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Setenv(config.EnvProjectRoot, resolved)

	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("SAGEOX_TELEMETRY", "false")
	isolateSessionMarkerDir(t)
	// the suite may run inside a coding agent: unset (not just blank) its
	// signals so detection cannot supply an identity the test did not.
	for _, name := range []string{
		"SAGEOX_AGENT_ID", "AGENT_ENV", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AMP_THREAD_URL",
		"CURSOR_SESSION_ID", "PI_CODING_AGENT", "PI_SESSION_ID", "PI_CODING_AGENT_DIR",
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	return resolved
}

// startDispatchRecording registers an AI coworker and starts a generic-adapter
// recording for it, returning the file `session log` appends to.
func startDispatchRecording(t *testing.T, projectRoot, agentID string) string {
	t.Helper()
	registerTestInstance(t, projectRoot, agentID)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "generic",
	})
	require.NoError(t, err)
	return sessionLogTargetFile(state)
}

// executeRootCLI runs args through the real root command, the same path a
// shell invocation takes: cobra flag parsing, PersistentPreRunE, then the
// agent dispatcher. Package state the run mutates is restored on cleanup.
//
// PersistentPreRunE resolves feature flags, reinitializes the logger, and
// triggers the first runtime capability probe, all process-global. runtime.Caps
// is cached with sync.Once, so a probe taken under this test's environment
// sticks for every later test (it disabled the KB source for
// TestRunAgentPrime_KeepsWarningsOffStderr), hence the Reset on both sides.
func executeRootCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetAgentDispatchFlags(t)
	runtime.Reset()
	t.Cleanup(runtime.Reset)
	savedCfg, savedCtx, savedPreRun := cfg, cliCtx, preRunReached
	savedFlags := flagsSnapshot{flags.Get()}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		cfg, cliCtx, preRunReached = savedCfg, savedCtx, savedPreRun
		flags.Init(context.Background(), savedFlags)
		logger.Init(false)
		resetCLIOutputModes()
	})
	err := rootCmd.Execute()
	return out.String(), err
}

// resetCLIOutputModes returns the output modes cli.NewContext sets from a
// run's flags to their process defaults. They are package globals read by
// every cli printer: a leaked --json mode silences cli.PrintHintTo for every
// later test, in whatever order the package happens to run.
func resetCLIOutputModes() {
	cli.SetJSONMode(false)
	cli.SetNoInteractive(false)
	cli.SetNoInput(false)
	cli.SetAssumeYes(false)
	updatenotice.SetMachineOutput(false)
}

// TestExecuteRootCLI_RestoresCLIOutputModes pins that a run through
// executeRootCLI leaves none of the output modes PersistentPreRunE sets
// (--json, --no-input, --yes) behind for the tests that run after it.
// Failure prevented: one `--json` root run silently drops every cli hint for
// the rest of the package, so a human-render test passes or fails depending on
// which tests ran first (TestRenderKBQueryResult_HitHintOnlyWithHits).
func TestExecuteRootCLI_RestoresCLIOutputModes(t *testing.T) {
	hintPrints := func() bool {
		var buf bytes.Buffer
		cli.PrintHintTo(&buf, "hint")
		return buf.Len() > 0
	}
	t.Run("run with output-mode flags", func(t *testing.T) {
		_, err := executeRootCLI(t, "version", "--json", "--no-input", "--yes")
		require.NoError(t, err)
		// The run must really set the modes, or the checks below pass
		// against state nothing ever changed.
		require.False(t, hintPrints(), "--json did not reach cli's JSON mode")
		require.True(t, cli.NoInput(), "--no-input did not reach cli")
		require.True(t, cli.AssumeYes(), "--yes did not reach cli")
	})
	assert.True(t, hintPrints(), "JSON mode leaked past executeRootCLI; later human-render tests lose their hints")
	assert.False(t, cli.NoInput(), "--no-input leaked past executeRootCLI")
	assert.False(t, cli.AssumeYes(), "--yes leaked past executeRootCLI")
}

// pipeStdin makes os.Stdin a pipe carrying content, as when an AI coworker
// runs `echo ... | ox agent <id> session log --stdin`.
func pipeStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	previous := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = previous
		_ = r.Close()
	})
	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()
}

// TestAgentSessionLog_RootCommandAcceptsEveryDocumentedForm is the regression
// test for `ox agent <id> session log --role user --content "..."` failing with
// "unknown flag: --role". ox's own session-start guidance tells AI coworkers to
// run exactly that command.
//
// Failure prevented: a coworker follows ox's next_actions verbatim and gets a
// cobra usage error, so no conversation turn is recorded. The customer-visible
// difference asserted here is the entry landing in the session file.
func TestAgentSessionLog_RootCommandAcceptsEveryDocumentedForm(t *testing.T) {
	tests := []struct {
		name  string
		args  func(agentID string) []string
		stdin string
		env   map[string]string
		want  map[string]any // fields of the single appended entry
	}{
		{
			name: "role and content",
			args: func(id string) []string {
				return []string{"agent", id, "session", "log", "--role", "user", "--content", "Fix the login bug"}
			},
			want: map[string]any{"type": "user", "content": "Fix the login bug"},
		},
		{
			name: "equals form",
			args: func(id string) []string {
				return []string{"agent", id, "session", "log", "--role=assistant", "--content=I will investigate"}
			},
			want: map[string]any{"type": "assistant", "content": "I will investigate"},
		},
		{
			name: "tool entry",
			args: func(id string) []string {
				return []string{
					"agent", id, "session", "log", "--role", "tool",
					"--tool-name", "bash", "--tool-input", "go test ./...", "--content", "PASS",
				}
			},
			want: map[string]any{"type": "tool", "tool_name": "bash", "tool_input": "go test ./...", "content": "PASS"},
		},
		{
			name: "stdin",
			args: func(id string) []string {
				return []string{"agent", id, "session", "log", "--role", "assistant", "--stdin"}
			},
			stdin: "multi-line\ncontent from stdin",
			want:  map[string]any{"type": "assistant", "content": "multi-line\ncontent from stdin"},
		},
		{
			name: "content that looks like flags",
			args: func(id string) []string {
				return []string{"agent", id, "session", "log", "--role", "user", "--content", "--role is a flag, - not a bullet"}
			},
			want: map[string]any{"type": "user", "content": "--role is a flag, - not a bullet"},
		},
		{
			name: "flags before the verb path",
			args: func(id string) []string {
				return []string{"agent", "--role", "system", "--content", "boot", id, "session", "log"}
			},
			want: map[string]any{"type": "system", "content": "boot"},
		},
		{
			name: "implicit agent id from environment",
			args: func(string) []string {
				return []string{"agent", "session", "log", "--role", "user", "--content", "implicit id"}
			},
			env:  map[string]string{"SAGEOX_AGENT_ID": dispatchAgentID},
			want: map[string]any{"type": "user", "content": "implicit id"},
		},
		{
			name: "explicit agent-id flag",
			args: func(string) []string {
				return []string{"agent", "session", "log", "--agent-id", dispatchAgentID, "--role", "user", "--content", "flag id"}
			},
			want: map[string]any{"type": "user", "content": "flag id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := dispatchTestEnv(t)
			file := startDispatchRecording(t, root, dispatchAgentID)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if tt.stdin != "" {
				pipeStdin(t, tt.stdin)
			}

			out, err := executeRootCLI(t, tt.args(dispatchAgentID)...)
			require.NoError(t, err, "the command ox's own guidance names must run")

			entries := readJSONLEntries(t, file)
			require.Len(t, entries, 1, "the entry must land in the session file")
			for field, want := range tt.want {
				assert.Equal(t, want, entries[0][field], "field %s", field)
			}

			var result sessionLogOutput
			require.NoError(t, json.Unmarshal([]byte(out), &result), "stdout must be one JSON document: %q", out)
			assert.True(t, result.Success)
			assert.Equal(t, 0, result.Seq)
		})
	}
}

// TestAgentSessionLog_RepeatedRunsAppendInOrder guards the reinjection path
// against stale flag values: pflag keeps the last parsed value, so a second
// invocation that omits --tool-name must not inherit the first one's.
func TestAgentSessionLog_RepeatedRunsAppendInOrder(t *testing.T) {
	root := dispatchTestEnv(t)
	file := startDispatchRecording(t, root, dispatchAgentID)

	_, err := executeRootCLI(t, "agent", dispatchAgentID, "session", "log",
		"--role", "tool", "--tool-name", "bash", "--tool-input", "ls", "--content", "out")
	require.NoError(t, err)
	_, err = executeRootCLI(t, "agent", dispatchAgentID, "session", "log", "--role", "user", "--content", "next")
	require.NoError(t, err)

	entries := readJSONLEntries(t, file)
	require.Len(t, entries, 2)
	assert.Equal(t, "bash", entries[0]["tool_name"])
	assert.Equal(t, "user", entries[1]["type"])
	assert.NotContains(t, entries[1], "tool_name", "a later entry must not inherit the earlier tool name")
	assert.Equal(t, float64(1), entries[1]["seq"])
}

// TestAgentSessionRecord_RootCommandAcceptsEntriesFlag covers `--entries`, the
// other session flag a manual parser reads that agentCmd never registered.
func TestAgentSessionRecord_RootCommandAcceptsEntriesFlag(t *testing.T) {
	root := dispatchTestEnv(t)
	logFile := startDispatchRecording(t, root, dispatchAgentID)
	// session record appends to the session's raw.jsonl, a sibling of the
	// generic adapter's input file
	rawFile := filepath.Join(filepath.Dir(logFile), ledgerFileRaw)

	// session record prints to os.Stdout rather than the cobra writer
	var runErr error
	captureStdoutForPlanCLI(t, func() {
		_, runErr = executeRootCLI(t, "agent", dispatchAgentID, "session", "record",
			"--entries", `[{"type":"user","content":"batched turn"}]`)
	})
	require.NoError(t, runErr)

	entries := readJSONLEntries(t, rawFile)
	require.NotEmpty(t, entries, "the batched entry must land in the session file")
	assert.Contains(t, strings.Join(entryContents(entries), "\n"), "batched turn")
}

func entryContents(entries []map[string]any) []string {
	var contents []string
	for _, entry := range entries {
		if content, ok := entry["content"].(string); ok {
			contents = append(contents, content)
		}
	}
	return contents
}

// TestAgentQuery_RootCommandForwardsFlagsToParser proves query's manual flags
// survive cobra. Each value is deliberately invalid: the parser's rejection
// message can only appear if the flag was accepted by cobra AND reinjected into
// the handler's args, and it fails before any network call.
func TestAgentQuery_RootCommandForwardsFlagsToParser(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"limit", []string{"--limit", "many"}, `invalid --limit value "many"`},
		{"k alias", []string{"--k", "many"}, `invalid --limit value "many"`},
		{"mode", []string{"--mode", "fuzzy"}, `invalid mode "fuzzy"`},
		{"source", []string{"--source", "elsewhere"}, `invalid source "elsewhere"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := dispatchTestEnv(t)
			registerTestInstance(t, root, dispatchAgentID)

			args := append([]string{"agent", dispatchAgentID, "query", "token refresh"}, tt.args...)
			_, err := executeRootCLI(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "unknown flag")
		})
	}
}

// TestAgentID_FlagSuppliesIdentityForShorthand covers `ox agent heartbeat
// --agent-id <id>`: AI coworkers generalize `ox murmur list --agent-id`.
func TestAgentID_FlagSuppliesIdentityForShorthand(t *testing.T) {
	t.Run("heartbeat resolves the named coworker", func(t *testing.T) {
		root := dispatchTestEnv(t)
		registerTestInstance(t, root, dispatchAgentID)

		_, err := executeRootCLI(t, "agent", "heartbeat", "--agent-id", dispatchAgentID)
		require.NoError(t, err)
	})

	t.Run("the flag is what supplies the identity", func(t *testing.T) {
		root := dispatchTestEnv(t)
		registerTestInstance(t, root, dispatchAgentID)

		_, err := executeRootCLI(t, "agent", "heartbeat")
		require.Error(t, err, "without the flag there is no identity to resolve")
		assert.Contains(t, err.Error(), "no agent ID")
	})

	t.Run("the flag outranks implicit resolution", func(t *testing.T) {
		root := dispatchTestEnv(t)
		// the environment names a coworker with no recording; only the flag's
		// coworker can accept a log entry.
		registerTestInstance(t, root, dispatchOtherAgent)
		file := startDispatchRecording(t, root, dispatchAgentID)
		t.Setenv("SAGEOX_AGENT_ID", dispatchOtherAgent)

		_, err := executeRootCLI(t, "agent", "session", "log", "--agent-id", dispatchAgentID,
			"--role", "user", "--content", "who am I")
		require.NoError(t, err)
		require.Len(t, readJSONLEntries(t, file), 1)
	})

	t.Run("a malformed id is an error not a fallback", func(t *testing.T) {
		root := dispatchTestEnv(t)
		registerTestInstance(t, root, dispatchOtherAgent)
		t.Setenv("SAGEOX_AGENT_ID", dispatchOtherAgent)

		_, err := executeRootCLI(t, "agent", "heartbeat", "--agent-id", "not-an-id")
		require.Error(t, err, "falling back to the environment would act on a different coworker")
		assert.Contains(t, err.Error(), "not an ox agent ID")
		assert.Equal(t, errkind.Usage, errkind.Of(err))
	})

	t.Run("a positional id wins over the flag", func(t *testing.T) {
		root := dispatchTestEnv(t)
		registerTestInstance(t, root, dispatchAgentID)

		_, err := executeRootCLI(t, "agent", dispatchAgentID, "heartbeat", "--agent-id", dispatchOtherAgent)
		require.NoError(t, err, "the unregistered flag value must not be consulted")
	})
}

func TestAgentSessionLog_ExplicitBlankIDRejectsEnvironmentFallback(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"whitespace only", " \t\n "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := dispatchTestEnv(t)
			file := startDispatchRecording(t, root, dispatchOtherAgent)
			t.Setenv("SAGEOX_AGENT_ID", dispatchOtherAgent)
			require.NoFileExists(t, file)

			_, err := executeRootCLI(t, "agent", "session", "log", "--agent-id", tt.value,
				"--role", "user", "--content", "must not reach the environment coworker")
			assert.Error(t, err, "an explicitly blank ID must not fall back to the environment")
			assert.Equal(t, errkind.Usage, errkind.Of(err))
			assert.NoFileExists(t, file, "the environment coworker's session must remain unchanged")
		})
	}
}

// manualAgentFlagSources are the files holding dispatched handlers that parse
// flags from a raw []string. A flag literal in any of them must be registered
// on agentCmd, or cobra rejects it before the handler runs.
var manualAgentFlagSources = []string{
	"agent_session.go",
	"agent_session_log.go",
	"agent_session_plan_history.go",
	"agent_session_capture_prior.go",
	"agent_session_subagent.go",
	"agent_tasks.go",
	"agent_query.go",
	"session_end_sync.go",
}

var flagLiteralPattern = regexp.MustCompile(`^--([a-z][a-z0-9-]*)=?$`)

// TestAgentDispatchedFlagsAreRegistered fails when a dispatched handler's
// manual parser accepts a flag agentCmd does not know. It scans the handler
// sources for flag-shaped string literals, so a flag added to a parser later is
// caught without anyone remembering this test exists.
func TestAgentDispatchedFlagsAreRegistered(t *testing.T) {
	// merge inherited flags so root-level --json counts as known
	known := map[string]bool{}
	agentCmd.LocalFlags().VisitAll(func(f *pflag.Flag) { known[f.Name] = true })
	agentCmd.InheritedFlags().VisitAll(func(f *pflag.Flag) { known[f.Name] = true })

	var found []string
	for _, name := range manualAgentFlagSources {
		path := repoPath(name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if m := flagLiteralPattern.FindStringSubmatch(value); m != nil {
				found = append(found, name+": --"+m[1])
				if !known[m[1]] {
					t.Errorf("%s parses --%s by hand but agentCmd does not register it: cobra rejects it as an unknown flag before the dispatcher runs; register it in agent.go init() and reinject it in runWithAgentID", name, m[1])
				}
			}
			return true
		})
	}
	// a scan that matches nothing would pass vacuously after a refactor
	require.GreaterOrEqual(t, len(found), 15, "flag literal scan found too few parser flags: %v", found)
}

// TestAgentDispatchedFlagsAreReinjected checks the second half of the contract:
// registering a flag is not enough, its value must be handed back to the
// handler. Every hidden dispatcher flag must be named in a reinjection list.
func TestAgentDispatchedFlagsAreReinjected(t *testing.T) {
	reinjected := map[string]bool{}
	for _, list := range [][]string{
		agentSessionStringFlags, agentSessionBoolFlags, agentTasksStringFlags,
		agentQueryStringFlags, agentQueryBoolFlags,
	} {
		for _, name := range list {
			reinjected[name] = true
		}
	}
	// consumed through cmd.Flags() directly rather than reinjected
	readDirectly := map[string]bool{
		"force": true, "release-quarantine": true, "no-optimize": true,
		"agent-ended": true, "agent-id": true,
	}

	var hidden []string
	checkHidden := func(f *pflag.Flag) {
		if !f.Hidden || readDirectly[f.Name] {
			return
		}
		hidden = append(hidden, f.Name)
		assert.True(t, reinjected[f.Name], "hidden agent flag --%s is registered but never reinjected: its value would be dropped", f.Name)
	}
	agentCmd.PersistentFlags().VisitAll(checkHidden)
	agentCmd.LocalNonPersistentFlags().VisitAll(checkHidden)
	for name := range reinjected {
		registered := agentCmd.PersistentFlags().Lookup(name) != nil || agentCmd.Flags().Lookup(name) != nil
		assert.True(t, registered, "reinjection list names --%s but agentCmd does not register it", name)
	}
	sort.Strings(hidden)
	assert.NotEmpty(t, hidden)
}

// TestAgentTasksAdd_OwnFlagsShadowDispatcherFlags guards the collision the
// dispatcher registrations create: `agent tasks add` is a real cobra command
// with its own --source (and --title), and agentCmd now also registers hidden
// persistent flags of those names. The subcommand's flag must win.
//
// Failure prevented: a producer's `--source daemon` silently stored as the
// default "cli" because the inherited dispatcher flag swallowed the value.
func TestAgentTasksAdd_OwnFlagsShadowDispatcherFlags(t *testing.T) {
	root := dispatchTestEnv(t)
	t.Cleanup(func() {
		agentTasksAddCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	})

	var runErr error
	captureStdoutForPlanCLI(t, func() {
		_, runErr = executeRootCLI(t, "agent", "tasks", "add", "--title", "shadow check", "--source", "daemon-test")
	})
	require.NoError(t, runErr)

	store, err := agenttask.NewStore(root)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	tasks, err := store.List(false)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "shadow check", tasks[0].Title)
	assert.Equal(t, "daemon-test", tasks[0].Source, "the subcommand's own --source must win over the dispatcher's")
}

// TestExplicitAgentIDFlag pins the three outcomes of the --agent-id helper.
func TestExplicitAgentIDFlag(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		supplied bool
		want     string
		wantErr  bool
	}{
		{"absent", "", false, "", false},
		{"empty", "", true, "", true},
		{"whitespace only", " \t\n ", true, "", true},
		{"valid", dispatchAgentID, true, dispatchAgentID, false},
		{"valid with padding", "  " + dispatchAgentID + " ", true, dispatchAgentID, false},
		{"malformed", "not-an-id", true, "", true},
		{"uuid is classified", "123e4567-e89b-12d3-a456-426614174000", true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetAgentDispatchFlags(t)
			_ = agentCmd.LocalFlags() // merge persistent flags into cmd.Flags(), as cobra does before RunE
			if tt.supplied {
				require.NoError(t, agentCmd.Flags().Set("agent-id", tt.value))
			}
			got, err := explicitAgentIDFlag(agentCmd)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, errkind.Usage, errkind.Of(err))
				assert.False(t, agentinstance.IsValidAgentID(tt.value))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
