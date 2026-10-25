package main

import (
	"errors"
	"strings"
	"testing"

	friction "github.com/sageox/frictionax"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFrictionRedactTree is a small stand-in for ox's command tree: groups, a
// leaf with a value flag, a bool persistent flag, an alias, and an `agent`
// dispatcher with a registered value flag. Synthetic on purpose, so the tests
// do not shift when real commands are added.
func newFrictionRedactTree() *cobra.Command {
	noop := func(*cobra.Command, []string) {}
	root := &cobra.Command{Use: "ox"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")

	plan := &cobra.Command{Use: "plan"}
	planList := &cobra.Command{Use: "list", Run: noop}
	planList.Flags().Int("limit", 0, "")
	plan.AddCommand(planList, &cobra.Command{Use: "view", Run: noop})

	session := &cobra.Command{Use: "session"}
	sessionLog := &cobra.Command{Use: "log", Run: noop}
	sessionLog.Flags().String("content", "", "")
	session.AddCommand(&cobra.Command{Use: "list", Run: noop}, sessionLog)

	agent := &cobra.Command{Use: "agent", Run: noop}
	agent.PersistentFlags().String("title", "", "")
	agent.AddCommand(&cobra.Command{Use: "prime", Run: noop})

	root.AddCommand(plan, session, agent,
		&cobra.Command{Use: "status", Aliases: []string{"st"}, Run: noop},
		&cobra.Command{Use: "conversation", Run: noop},
	)
	return root
}

// TestRedactCommandLine pins what survives of a command line. Every case with
// a "leaked" synthetic value must come out without it; every case with a real
// command path must come out with the path intact.
//
// Failure prevented: a friction event carrying a coworker's file paths, org and
// repo names, IDs, search text, or pasted prompt to the cloud verbatim.
func TestRedactCommandLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"command path and bool flag survive", "plan list --json", "plan list --json"},
		{"leading binary name is kept", "ox plan list", "ox plan list"},
		{"alias is kept as typed", "st --json", "st --json"},
		{"value flag: value becomes arg", "plan list --limit 50 --json", "plan list --limit <arg> --json"},
		{"unknown flag keeps its name, drops its value", "plan list --window 365d", "plan list --window <arg>"},
		{"=value suffix is dropped and consumes nothing", "plan list --limit=50 --json", "plan list --limit --json"},
		{"short bool flag is kept", "plan list -v", "plan list -v"},
		{"unknown short flag keeps its name", "plan list -x person-a", "plan list -x <arg>"},
		{"flag may sit between command words", "plan --json list", "plan --json list"},
		{"free-text positionals collapse into one arg", "plan view fix the login bug", "plan view <arg>"},
		{"free text after a value flag collapses with the value", "session log --content fix the login bug --json", "session log --content <arg> --json"},
		{"slash-prefixed home path is an arg", "plan view /Users/person-a/src/x.html", "plan view <arg>"},
		{"team id is an arg", "plan view team_abc123", "plan view <arg>"},
		{"conversation id is an arg", "plan view cnv_01a00000-0000-7000-8000-000000000000", "plan view <arg>"},
		{"unresolved first word is not kept", "fix the login bug", "<arg>"},
		{"path as the first word is not kept", "/Users/person-a/src/x.html", "<arg>"},
		{"word after a leaf is an arg even when it names a command", "plan view list", "plan view <arg>"},
		{"negative-number-looking text is not a flag", "plan view -5", "plan view <arg>"},
		{"dash-prefixed prose is not a flag name", "plan view --this-is-not-a-flag-name-because-it-is-far-too-long-for-one", "plan view <arg>"},
		{"after -- everything is an arg", "plan view -- --json now", "plan view -- <arg>"},
		{"newlines in a pasted prompt are folded away", "plan view line one\nline two\n\nline three", "plan view <arg>"},
		{"truncated mid-token", "plan view cnv_01a0000", "plan view <arg>"},
		{"unknown flag stops command resolution", "--nope plan list", "--nope <arg>"},

		// the agent dispatcher: cobra cannot resolve what comes after the id
		{"agent id becomes <id>", "agent OxAbCd status", "agent <id> status"},
		{"dispatcher router keeps its sub-word", "agent OxAbCd session log --content hello world", "agent <id> session log --content <arg>"},
		{"dispatcher command drops its arguments", "agent OxAbCd query fix the login bug", "agent <id> query <arg>"},
		{"dispatch word is kept, the free text behind it is not", "agent OxAbCd fix the login bug", "agent <id> fix <arg>"},
		{"capitalized dispatch word is free text", "agent OxAbCd Fix", "agent <id> <arg>"},
		{"dispatch word with a digit is free text", "agent OxAbCd step2", "agent <id> <arg>"},
		{"flags registered on agent survive after the id", "agent OxAbCd --title my-secret-project session log", "agent <id> --title <arg> session log"},
		{"known dispatcher word works without an id", "agent session start", "agent session start"},
		{"unknown word without an id is an arg", "agent skills list", "agent <arg>"},
		{"a real agent subcommand resolves in the tree", "agent prime --json", "agent prime --json"},
		{"an invalid id is an arg", "agent Ox12#4 status", "agent <arg>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// one tree per case: cobra sorts a command's children in place on
			// first read, so a shared tree is a data race under t.Parallel
			assert.Equal(t, tt.want, redactCommandLine(newFrictionRedactTree(), tt.input))
		})
	}
}

// TestRedactFrictionError pins the cobra error shapes that survive and the
// user-typed text each one must lose.
//
// Failure prevented: the error message, which quotes what was typed, carrying
// the same free text and IDs the input redaction removed.
func TestRedactFrictionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"empty", "", ""},
		{
			"unknown command under a group",
			`unknown command "show" for "ox plan"`,
			`unknown command "<arg>" for "ox plan"`,
		},
		{
			"unknown command quoting a whole command line keeps only the resolved path",
			`unknown command "conversation show cnv_01a00000-0000-7000-8000-000000000000" for "ox"`,
			`unknown command "conversation <arg>" for "ox"`,
		},
		{
			"unknown command carrying a pasted prompt",
			`unknown command "fix the login bug in /Users/person-a/src" for "ox"`,
			`unknown command "<arg>" for "ox"`,
		},
		{
			"cut off inside the quoted command",
			`unknown command "conversation show cnv_01a00000-0000-`,
			`unknown command "conversation <arg>"`,
		},
		{
			"cut off inside the parent",
			`unknown command "show" for "ox pla`,
			`unknown command "<arg>" for "ox <arg>"`,
		},
		{
			"only the first line is kept",
			"unknown command \"show\" for \"ox plan\"\n\nRun 'ox plan --help' for usage",
			`unknown command "<arg>" for "ox plan"`,
		},
		{"unknown flag is kept", "unknown flag: --window", "unknown flag: --window"},
		{"unknown flag drops =value", "unknown flag: --window=person-a", "unknown flag: --window"},
		{"unknown short flag is kept", "unknown flag: -x", "unknown flag: -x"},
		{"unknown flag that is not shaped like a flag", "unknown flag: /Users/person-a/x", "unknown flag: <arg>"},
		{
			"shorthand error drops the text it was found in",
			"unknown shorthand flag: 'x' in -x fix the login bug",
			"unknown shorthand flag: 'x' in",
		},
		{
			"shorthand error whose char is punctuation",
			"unknown shorthand flag: ' ' in - fix the login bug",
			"unknown shorthand flag: '<arg>' in",
		},
		{"required flags are names", `required flag(s) "file", "title" not set`, `required flag(s) "file", "title" not set`},
		{
			"required flag that is not a name",
			`required flag(s) "fix the login bug" not set`,
			`required flag(s) "<arg>" not set`,
		},
		{
			"invalid argument drops the value and pflag's repeat of it",
			`invalid argument "/Users/person-a/src/x.html" for "--limit" flag: strconv.ParseInt: parsing "/Users/person-a/src/x.html": invalid syntax`,
			`invalid argument "<arg>" for "--limit"`,
		},
		{
			"invalid argument in a shape that does not parse sends the phrase only",
			`invalid argument "fix the login bug" for flag --limit`,
			"invalid argument",
		},
		{
			"ox-authored error that merely contains a trigger phrase",
			`invalid argument: expected a number, got "fix the login bug" in /Users/person-a/src`,
			"invalid argument",
		},
		{"dispatcher word kept when command-shaped", "unknown command or invalid agent_id: skills", "unknown command or invalid agent_id: skills"},
		{"dispatcher text dropped when not", "unknown command or invalid agent_id: fix the login bug", "unknown command or invalid agent_id: <arg>"},
		{"agent dispatcher unknown command", "unknown command: status\nAvailable: doctor, query", "unknown command: status"},
		{"no trigger phrase: quoted text is dropped", `failed to read "fix the login bug"`, "failed to read <arg>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, redactFrictionError(newFrictionRedactTree(), tt.msg))
		})
	}
}

// TestRedactFrictionEvent_LeavesNoSyntheticUserData runs one event that stuffs
// every kind of user data into every field through the full redaction and
// asserts none of it survives, regardless of which rule caught it.
//
// Failure prevented: a rule change that fixes one shape and reopens another.
func TestRedactFrictionEvent_LeavesNoSyntheticUserData(t *testing.T) {
	t.Parallel()
	root := newFrictionRedactTree()

	leaks := []string{
		"person-a", "/Users", "team_abc123", "cnv_01a", "fix the login bug", "login",
		"acme-corp", "person@example.com", "https://", "x.html",
	}
	event := &friction.FrictionEvent{
		Command:    "ox",
		Subcommand: "acme-corp",
		Input: "plan view /Users/person-a/src/x.html team_abc123 cnv_01a00000-0000-7000-8000-000000000000 " +
			"fix the login bug acme-corp/secret-repo person@example.com https://example.com/acme-corp --limit=9",
		ErrorMsg: `unknown command "plan view /Users/person-a/src/x.html fix the login bug team_abc123" for "ox"`,
		Suggestion: "plan view /Users/person-a/src/x.html team_abc123 fix the login bug " +
			"acme-corp/secret-repo person@example.com",
	}

	got, ok := redactFrictionEvent(root, event)
	require.True(t, ok)

	for field, value := range map[string]string{"input": got.Input, "error": got.ErrorMsg, "suggestion": got.Suggestion} {
		for _, leak := range leaks {
			assert.NotContains(t, value, leak, "%s must not carry %q", field, leak)
		}
	}
	assert.Equal(t, "ox", got.Command)
	assert.Equal(t, "<arg>", got.Subcommand)
	assert.Equal(t, "plan view <arg> --limit", got.Input)
	assert.Equal(t, `unknown command "plan view <arg>" for "ox"`, got.ErrorMsg)
	assert.Equal(t, "plan view <arg>", got.Suggestion)
}

func TestRedactFrictionEvent_NilTreeStillRedacts(t *testing.T) {
	t.Parallel()
	got, ok := redactFrictionEvent(nil, &friction.FrictionEvent{Input: "ox plan view /Users/person-a/x --json"})
	require.True(t, ok)
	assert.NotContains(t, got.Input, "person-a")
	assert.NotContains(t, got.Input, "plan")
}

// TestRedactFrictionEvent_PanicDropsTheEvent pins the fail-closed contract: if
// redaction itself blows up, the caller gets ok=false and sends nothing, rather
// than a panic on the exit path or an unredacted event.
func TestRedactFrictionEvent_PanicDropsTheEvent(t *testing.T) {
	t.Parallel()
	got, ok := redactFrictionEvent(newFrictionRedactTree(), nil)
	assert.False(t, ok)
	assert.Equal(t, redactedFrictionFields{}, got)
}

// TestRedactCommandLine_EveryRealCommandPathSurvives walks ox's real command
// tree. It is the guard on "tree-aware": a rule that quietly redacted real
// commands would pass the synthetic tests above and destroy the telemetry.
func TestRedactCommandLine_EveryRealCommandPathSurvives(t *testing.T) {
	var visit func(cmd *cobra.Command, path []string)
	visited := 0
	visit = func(cmd *cobra.Command, path []string) {
		for _, child := range cmd.Commands() {
			childPath := append(append([]string(nil), path...), child.Name())
			line := strings.Join(childPath, " ")
			assert.Equal(t, line, redactCommandLine(rootCmd, line), "command path %q must survive redaction", line)
			visited++
			visit(child, childPath)
		}
	}
	visit(rootCmd, nil)
	require.Greater(t, visited, 20, "the real tree should have many commands; is rootCmd populated?")
}

// TestRedactCommandLine_RealTreeShapes replays the shapes of real friction
// events (synthetic values) against the real tree.
func TestRedactCommandLine_RealTreeShapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"unknown flag on a real command", "session list --limit 50 --json --window 365d", "session list --limit <arg> --json --window <arg>"},
		{"agent session log with content", "agent OxAbCd session log --content fix the login bug --role user", "agent <id> session log --content <arg> --role <arg>"},
		{"agent status", "agent OxAbCd status", "agent <id> status"},
		{"plan view with a slug", "plan view some-slug --json", "plan view <arg> --json"},
		{"conversation transcript with an id", "conversation transcript cnv_01a00000-0000-7000-8000-000000000000 --limit 3", "conversation transcript <arg> --limit <arg>"},
		{"query text", "agent OxAbCd query how does the login flow work", "agent <id> query <arg>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redactCommandLine(rootCmd, tt.input))
		})
	}
}

// TestFrictionEngine_RedactsSecretsWhenBuildingTheEvent proves initFriction
// wired the secrets redactor into the engine: without it frictionax's default
// no-op redactor sends a pasted credential through untouched.
//
// Failure prevented: a token typed on the command line reaching the cloud, or
// being cut in half by truncation so no pattern recognizes it later.
func TestFrictionEngine_RedactsSecretsWhenBuildingTheEvent(t *testing.T) {
	require.NotNil(t, frictionEngine, "initFriction runs in package init")

	token := "ghp_" + strings.Repeat("a1B2", 9)
	result := handleFriction(frictionEngine,
		[]string{"plan", "list", "--token", token},
		errors.New("unknown flag: --token"))

	require.NotNil(t, result)
	require.NotNil(t, result.Event)
	assert.NotContains(t, result.Event.Input, token)
	assert.Contains(t, result.Event.Input, "[REDACTED_GITHUB_TOKEN]")
}
