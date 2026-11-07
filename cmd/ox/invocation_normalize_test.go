package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	friction "github.com/sageox/frictionax"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Everything here exercises the planners without running a command. The
// behavior through the real recovery path lives in friction_recovery_test.go.

// TestWordSplitArgs covers the zsh trap: an unquoted $var is not split, so a
// loop over command names passes "plan list" as one argv element.
// Failure prevented: `for c in "plan list" "daemon list"; do ox $c --help; done`
// failing with `unknown command "plan list" for "ox"` in zsh.
func TestWordSplitArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"two words become two args", []string{"plan list", "--help"}, []string{"plan", "list", "--help"}, true},
		{"trailing args are kept in order", []string{"plan view slug", "--json"}, []string{"plan", "view", "slug", "--json"}, true},
		{"a top-level command with a space", []string{"login "}, []string{"login"}, true},
		{"words that name no command are left alone", []string{"no such thing", "--help"}, nil, false},
		{"a lone word with no whitespace is untouched", []string{"plan"}, nil, false},
		{"whitespace in a later argument is a value, not a command", []string{"query", "what is x"}, nil, false},
		{"a leading flag is not a command line", []string{"--json plan list"}, nil, false},
		{"empty argv", nil, nil, false},
		{"root alone resolves to nothing", []string{" "}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := wordSplitArgs(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestFlagAsVerbArgs covers a subcommand typed as a flag.
// Failure prevented: `ox daemon --list` failing with `unknown flag: --list`
// when `ox daemon list` is exactly what was meant, and the opposite error:
// rewriting `ox guide --list` to `ox guide list`, which asks for a guide
// called "list".
func TestFlagAsVerbArgs(t *testing.T) {
	tests := []struct {
		name     string
		in       []string
		want     []string
		wantVerb string
	}{
		{"group with that subcommand", []string{"daemon", "--list"}, []string{"daemon", "list"}, "list"},
		{"nested group", []string{"plan", "--list"}, []string{"plan", "list"}, "list"},
		{"trailing flags are kept", []string{"daemon", "--list", "--json"}, []string{"daemon", "list", "--json"}, "list"},
		{"guide reads list as a topic name, not a subcommand", []string{"guide", "--list"}, nil, ""},
		{"a flag the command owns is left alone", []string{"daemon", "list", "--json"}, nil, ""},
		{"help is a flag everywhere", []string{"daemon", "--help"}, nil, ""},
		{"a joined value is not a verb", []string{"daemon", "--list=x"}, nil, ""},
		{"no such subcommand", []string{"daemon", "--frobnicate"}, nil, ""},
		{"the flag must sit after the command, not before it", []string{"--list", "daemon"}, nil, ""},
		{"after -- everything is literal", []string{"daemon", "--", "--list"}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, verb, ok := flagAsVerbArgs(tt.in)
			assert.Equal(t, tt.want != nil, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantVerb, verb)
		})
	}
}

// TestPlanCorrection_PreCobra covers the plan before cobra has run (err == nil):
// only the rewrites that need no failure to justify them apply.
func TestPlanCorrection_PreCobra(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		wantSource correctionSource
		wantArgs   []string
		wantAuto   bool
	}{
		{"word-split runs", []string{"plan list", "--help"}, correctionWordSplit, []string{"plan", "list", "--help"}, true},
		{"flag-as-verb runs", []string{"daemon", "--list"}, correctionFlagAsVerb, []string{"daemon", "list"}, true},
		{"a destructive verb is only suggested", []string{"daemon", "--kill"}, correctionFlagAsVerb, []string{"daemon", "kill"}, false},
		{"a word-split that would not parse is dropped", []string{"plan list", "--no-such-flag"}, correctionNone, nil, false},
		{"a word-split onto a group with a typo is dropped", []string{"plan lst"}, correctionNone, nil, false},
		{"guide --list is left to fail", []string{"guide", "--list"}, correctionNone, nil, false},
		{"a valid invocation is untouched", []string{"plan", "list"}, correctionNone, nil, false},
		{"the catalog is not consulted without an error", []string{"auth", "--help"}, correctionNone, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planCorrection(tt.in, nil)
			assert.Equal(t, tt.wantSource, plan.Source)
			assert.Equal(t, tt.wantArgs, plan.Corrected)
			assert.Equal(t, tt.wantAuto, plan.Auto)
		})
	}
}

// TestPlanInvocation_RecoveryPlans feeds each shape through the whole planner
// (the error cobra would raise, then the catalog) and states what ox would do.
// Failure prevented: the 282 real failed invocations this was written from
// staying failures, and the opposite: an auto-executed guess that changes the
// verb or runs something destructive.
func TestPlanInvocation_RecoveryPlans(t *testing.T) {
	registerGatedCommandsForTest(t)
	require.NotNil(t, frictionEngine, "the friction engine is wired by init")

	tests := []struct {
		name       string
		in         []string
		wantSource correctionSource
		wantArgs   []string
		wantAuto   bool
	}{
		// the whole input matches an entry
		{"auth maps to login", []string{"auth"}, correctionCatalog, []string{"login"}, true},
		{"auth status maps to status", []string{"auth", "status"}, correctionCatalog, []string{"status"}, true},
		{"skill is skills", []string{"skill"}, correctionCatalog, []string{"skills"}, true},
		{"update is only suggested", []string{"update"}, correctionCatalog, []string{"upgrade"}, false},
		{"signout is only suggested: logout mutates", []string{"signout"}, correctionCatalog, []string{"logout"}, false},
		{"mural is only suggested", []string{"mural"}, correctionCatalog, []string{"murmur"}, false},
		{"ledger maps to status", []string{"ledger"}, correctionCatalog, []string{"status"}, true},
		{"ledgers maps to status", []string{"ledgers"}, correctionCatalog, []string{"status"}, true},
		{"the help convention", []string{"plan", "help"}, correctionCatalog, []string{"plan", "--help"}, true},

		// the catalog matches only the whole input, so flags defeated it
		{"flags after a mapped command", []string{"auth", "--help"}, correctionCatalogPrefix, []string{"login", "--help"}, true},
		{"whoami with flags", []string{"whoami", "--help"}, correctionCatalogPrefix, []string{"status", "--help"}, true},
		{"trailing positional carried across", []string{"plan", "show", "x"}, correctionCatalogPrefix, []string{"plan", "view", "x"}, true},
		{"flag after the carried positional", []string{"plan", "show", "x", "--json"}, correctionCatalogPrefix, []string{"plan", "view", "x", "--json"}, true},
		{"ledger sync keeps its flags", []string{"ledger", "sync", "--json"}, correctionCatalogPrefix, []string{"sync", "--json"}, true},
		{"login takes no arguments, so auth token is only suggested", []string{"auth", "token"}, correctionCatalogPrefix, []string{"login", "token"}, false},
		{"upgrade keeps its flags but still is not run", []string{"update", "--help"}, correctionCatalogPrefix, []string{"upgrade", "--help"}, false},

		// the shell passed a command line as one word
		{"word-split with help", []string{"plan list", "--help"}, correctionWordSplit, []string{"plan", "list", "--help"}, true},
		{"word-split of a mapped command is not a catalog guess", []string{"agent prime", "--help"}, correctionWordSplit, []string{"agent", "prime", "--help"}, true},

		// a subcommand typed as a flag
		{"daemon --list", []string{"daemon", "--list"}, correctionFlagAsVerb, []string{"daemon", "list"}, true},
		{"daemon --kill is only suggested", []string{"daemon", "--kill"}, correctionFlagAsVerb, []string{"daemon", "kill"}, false},

		// nothing sensible to offer
		{"mcp is not gc", []string{"mcp"}, correctionNone, nil, false},
		{"doc is not gc", []string{"doc"}, correctionNone, nil, false},
		{"api is not pr", []string{"api"}, correctionNone, nil, false},
		{"guide --list stays an error", []string{"guide", "--list"}, correctionNone, nil, false},
		{"unmapped group word", []string{"repo", "list"}, correctionNone, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planInvocation(tt.in)
			assert.Equal(t, tt.wantSource, plan.Source, "source")
			assert.Equal(t, tt.wantArgs, plan.Corrected, "corrected argv")
			assert.Equal(t, tt.wantAuto, plan.Auto, "auto")
			if plan.Source == correctionNone {
				if plan.Result != nil {
					assert.Nil(t, plan.Result.Suggestion, "nothing to offer means nothing printed")
				}
				return
			}
			require.NotNil(t, plan.Result)
			require.NotNil(t, plan.Result.Suggestion)
			assert.Equal(t, plan.Auto, plan.Result.AutoExecute, "the result frictionax emits must agree with the plan")
			assert.NotEmpty(t, plan.Suggestion)
		})
	}
}

// TestPlanCorrection_DispatcherErrors covers the agent dispatcher, which raises
// its own "unknown command" errors after cobra has accepted the argv.
// Failure prevented: `ox agent <id> status` (23 real events in one month for
// the `agent ... session log` family alone) dead-ending in an error that names
// no fix.
func TestPlanCorrection_DispatcherErrors(t *testing.T) {
	tests := []struct {
		name     string
		in       []string
		err      string
		wantArgs []string
	}{
		{"agent status", []string{"agent", "status"}, "unknown command or invalid agent_id: status", []string{"session", "status"}},
		{"agent id status", []string{"agent", "Oxa7b3", "status"}, "unknown command: status", []string{"session", "status"}},
		{"agent id status keeps flags", []string{"agent", "Oxa7b3", "status", "--json"}, "unknown command: status", []string{"session", "status", "--json"}},
		{"agent id session score", []string{"agent", "Oxa7b3", "session", "score"}, "unknown command: score", []string{"session", "score"}},
		{"agent score", []string{"agent", "score", "minor"}, "unknown command or invalid agent_id: score", []string{"session", "score", "minor"}},
		{"agent session-list", []string{"agent", "session-list"}, "unknown command or invalid agent_id: session-list", []string{"session", "list"}},
		{"agent ls", []string{"agent", "ls"}, "unknown command or invalid agent_id: ls", []string{"agent", "list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planCorrection(tt.in, errors.New(tt.err))
			assert.Equal(t, tt.wantArgs, plan.Corrected)
			assert.NotEqual(t, correctionNone, plan.Source)
		})
	}
}

// TestPlanCorrection_UnparseableErrorsOfferNothing proves a failure frictionax
// cannot classify (a command's own runtime error) is never "corrected".
// Failure prevented: re-running a command that already ran and failed.
func TestPlanCorrection_UnparseableErrorsOfferNothing(t *testing.T) {
	plan := planCorrection([]string{"auth", "--help"}, errors.New("connection refused"))
	assert.Equal(t, correctionNone, plan.Source)
	assert.Nil(t, plan.Corrected)
	assert.False(t, plan.Auto)
	assert.Nil(t, plan.Result)
}

// TestPlanCorrection_PrefixOnlyAfterUsageRejection proves the leading-words
// fallback is limited to unknown command / unknown flag failures, so a command
// that ran and failed with a different usage error is not re-dispatched.
func TestPlanCorrection_PrefixOnlyAfterUsageRejection(t *testing.T) {
	plan := planCorrection([]string{"whoami", "--help"}, errors.New(`invalid argument "x" for "--help"`))
	assert.NotEqual(t, correctionCatalogPrefix, plan.Source)
	assert.False(t, plan.Auto)
}

// TestPlausibleFuzzyMatch pins the guard on edit-distance guesses.
// Failure prevented: `ox mcp` offering `gc`, which re-clones repositories.
func TestPlausibleFuzzyMatch(t *testing.T) {
	tests := []struct {
		bad, good string
		want      bool
	}{
		{"mcp", "gc", false},
		{"doc", "gc", false},
		{"api", "pr", false},
		{"stat", "status", false},
		{"sesion", "session", true},
		{"statuss", "status", true},
		{"agnt", "agent", true},
		{"xyzzy", "agent", false},
		{"--limt", "--limit", true},
		{"--zz", "--json", false},
		{"", "agent", false},
	}
	for _, tt := range tests {
		t.Run(tt.bad+"->"+tt.good, func(t *testing.T) {
			got := plausibleFuzzyMatch(&friction.Suggestion{Type: friction.SuggestionLevenshtein, Original: tt.bad, Corrected: tt.good})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"mcp", "gc", 2},
		{"kitten", "sitting", 3},
		{"café", "cafe", 1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, editDistance(tt.a, tt.b), "%q vs %q", tt.a, tt.b)
		assert.Equal(t, tt.want, editDistance(tt.b, tt.a), "%q vs %q", tt.b, tt.a)
	}
}

// TestArgvParsesCleanly is the safety net itself.
// Failure prevented: auto-executing a correction cobra will reject, which turns
// one error into two.
func TestArgvParsesCleanly(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want bool
	}{
		{"a leaf command", []string{"login"}, true},
		{"a leaf given an argument it rejects", []string{"login", "token"}, false},
		{"help on a group", []string{"plan", "--help"}, true},
		{"a nested command with help", []string{"plan", "list", "--help"}, true},
		{"an unknown flag", []string{"plan", "list", "--no-such-flag"}, false},
		{"a group given a stray word", []string{"plan", "bogus"}, false},
		{"a command missing its required argument", []string{"plan", "view"}, false},
		{"a command given its argument", []string{"plan", "view", "some-slug"}, true},
		{"the dispatcher takes free-form words", []string{"agent", "Oxa7b3", "session", "stop"}, true},
		{"the root alone is not a correction", nil, false},
		{"an unknown command", []string{"no-such-command"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, argvParsesCleanly(tt.in))
		})
	}
}

// TestPredictCobraError proves the helper returns the error cobra would raise
// for an argv, so a corpus can feed realistic failures to the planner.
func TestPredictCobraError(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string // substring of the error; empty means no error
	}{
		{"one-word command line", []string{"plan list", "--help"}, `unknown command "plan list" for "ox"`},
		{"unknown root command", []string{"auth", "--help"}, `unknown command "auth" for "ox"`},
		{"unknown subcommand of a group", []string{"plan", "show", "x"}, `unknown command "show" for "ox plan"`},
		{"unknown flag on a leaf", []string{"daemon", "list", "--nope"}, "unknown flag: --nope"},
		{"unknown flag on a group", []string{"daemon", "--list"}, "unknown flag: --list"},
		{"wrong argument count", []string{"login", "token"}, "unknown command"},
		{"a valid invocation", []string{"plan", "list"}, ""},
		{"help is valid", []string{"plan", "list", "--help"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := predictCobraError(tt.in)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestDryParseFlags proves the safety net's parse is invisible to the command
// it checks, and still rejects what cobra would reject.
// Failure prevented: `--tag a --tag b` reaching a command as a, b, a, b after
// the safety net parsed it once to check it. pflag keeps that append state
// private, so the only safe dry run is one that never touches the real flags.
func TestDryParseFlags(t *testing.T) {
	newCommand := func() (*cobra.Command, *[]string, *string, *int) {
		cmd := &cobra.Command{Use: "dry"}
		tags := cmd.Flags().StringSlice("tag", []string{"seed"}, "")
		name := cmd.Flags().String("name", "keep", "")
		verbose := cmd.Flags().CountP("verbose", "v", "")
		return cmd, tags, name, verbose
	}

	t.Run("the command's own flags are left as found", func(t *testing.T) {
		cmd, tags, name, verbose := newCommand()
		in := []string{"--tag", "a", "--tag", "b", "--name", "x", "-vv", "word"}

		positional, help, err := dryParseFlags(cmd, in)
		require.NoError(t, err)
		_, _, err = dryParseFlags(cmd, in) // checking twice must not matter either
		require.NoError(t, err)
		assert.Equal(t, []string{"word"}, positional)
		assert.False(t, help)

		assert.Equal(t, []string{"seed"}, *tags)
		assert.Equal(t, "keep", *name)
		assert.Equal(t, 0, *verbose)
		assert.False(t, cmd.Flags().Lookup("tag").Changed)

		// the real parse that follows sees a clean slate
		require.NoError(t, cmd.ParseFlags(in))
		assert.Equal(t, []string{"a", "b"}, *tags)
		assert.Equal(t, "x", *name)
		assert.Equal(t, 2, *verbose)
	})

	t.Run("what cobra rejects is rejected", func(t *testing.T) {
		cmd, _, _, _ := newCommand()
		for _, tt := range []struct {
			name, want string
			in         []string
		}{
			{"unknown flag", "unknown flag: --nope", []string{"--nope"}},
			{"unknown shorthand", "unknown shorthand flag: 'z' in -z", []string{"-z"}},
			{"missing value", "flag needs an argument: --name", []string{"--name"}},
		} {
			_, _, err := dryParseFlags(cmd, tt.in)
			require.Error(t, err, tt.name)
			assert.Contains(t, err.Error(), tt.want, tt.name)
		}
	})

	t.Run("help is reported, not obeyed", func(t *testing.T) {
		cmd, _, _, _ := newCommand()
		_, help, err := dryParseFlags(cmd, []string{"--help"})
		require.NoError(t, err)
		assert.True(t, help)
	})
}

func TestQuoteArgs(t *testing.T) {
	assert.Equal(t, "plan list --help", quoteArgs([]string{"plan", "list", "--help"}))
	assert.Equal(t, "'plan list' --help", quoteArgs([]string{"plan list", "--help"}))
	assert.Equal(t, `'it'\''s' ''`, quoteArgs([]string{"it's", ""}))
}

func TestArgsRequestJSON(t *testing.T) {
	tests := []struct {
		in   []string
		want bool
	}{
		{[]string{"status", "--json"}, true},
		{[]string{"status", "--json=true"}, true},
		{[]string{"status", "--json=false"}, false},
		{[]string{"status", "--json=bogus"}, false},
		{[]string{"status"}, false},
		{[]string{"echo", "--", "--json"}, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, argsRequestJSON(tt.in), "%v", tt.in)
	}
}

// TestEmitCorrection pins what a coworker or agent reads when ox changes or
// withholds a command: one JSON line on stdout for agents, one short line on
// stderr for people, naming the cause so it is not repeated.
func TestEmitCorrection(t *testing.T) {
	autoWordSplit := planCorrection([]string{"plan list", "--help"}, nil)
	require.Equal(t, correctionWordSplit, autoWordSplit.Source)
	suggestOnly := planCorrection([]string{"daemon", "--kill"}, nil)
	require.False(t, suggestOnly.Auto)

	t.Run("agent gets one JSON line naming what changed and why", func(t *testing.T) {
		out := captureStdoutForPlanCLI(t, func() { emitCorrection(autoWordSplit, true) })
		require.Equal(t, 1, strings.Count(out, "\n"), "one line: %q", out)
		var got struct {
			Corrected struct{ Was, Now, Note string } `json:"_corrected"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &got), out)
		assert.Equal(t, "'plan list' --help", got.Corrected.Was)
		assert.Equal(t, "plan list --help", got.Corrected.Now)
		assert.Contains(t, got.Corrected.Note, "zsh does not split")
		assert.Contains(t, got.Corrected.Note, "${=var}")
	})

	t.Run("a suggestion is labeled as one", func(t *testing.T) {
		out := captureStdoutForPlanCLI(t, func() { emitCorrection(suggestOnly, true) })
		var got struct {
			Suggestion struct{ Try, Note string } `json:"_suggestion"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &got), out)
		assert.Equal(t, "daemon kill", got.Suggestion.Try)
	})

	t.Run("a person gets a stderr line and a quiet stdout", func(t *testing.T) {
		var stdout string
		stderr := captureStderr(t, func() {
			stdout = captureStdoutForPlanCLI(t, func() { emitCorrection(autoWordSplit, false) })
		})
		assert.Empty(t, stdout)
		assert.Equal(t, 1, strings.Count(stderr, "\n"), "one line: %q", stderr)
		assert.Contains(t, stderr, "-> Correcting to: plan list --help")
		assert.Contains(t, stderr, "zsh does not split")
	})

	t.Run("plans without their own note keep the frictionax wording", func(t *testing.T) {
		catalogPlan := planInvocation([]string{"auth"})
		require.Equal(t, correctionCatalog, catalogPlan.Source)
		stderr := captureStderr(t, func() { emitCorrection(catalogPlan, false) })
		assert.Equal(t, "-> Correcting to: login\n", stderr)
	})

	t.Run("nothing to say prints nothing", func(t *testing.T) {
		stderr := captureStderr(t, func() {
			assert.Empty(t, captureStdoutForPlanCLI(t, func() { emitCorrection(correctionPlan{}, true) }))
		})
		assert.Empty(t, stderr)
	})
}

// TestAttachSuggestion proves the telemetry event carries the final corrected
// command, not just what was typed.
// Failure prevented: friction analytics that record the failures but not the
// fix, so the catalog cannot tell which corrections agents needed.
func TestAttachSuggestion(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		err  error
		want string
	}{
		{"exact catalog match", []string{"auth"}, errors.New(`unknown command "auth" for "ox"`), "login"},
		{"prefix match carries the trailing arguments", []string{"auth", "--help"}, errors.New(`unknown command "auth" for "ox"`), "login --help"},
		{"a dropped fuzzy guess leaves the event bare", []string{"mcp"}, errors.New(`unknown command "mcp" for "ox"`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planCorrection(tt.in, tt.err)
			require.NotNil(t, plan.Result)
			require.NotNil(t, plan.Result.Event)
			attachSuggestion(plan.Result)
			assert.Equal(t, tt.want, plan.Result.Event.Suggestion)
		})
	}

	assert.NotPanics(t, func() { attachSuggestion(nil) })
	assert.NotPanics(t, func() { attachSuggestion(&friction.Result{Suggestion: &friction.Suggestion{Corrected: "x"}}) })
}

// TestNormalizeInvocation covers the pre-cobra wrapper main() calls.
func TestNormalizeInvocation(t *testing.T) {
	withoutAgentContext(t)

	t.Run("an auto rewrite is applied and announced", func(t *testing.T) {
		var got []string
		stderr := captureStderr(t, func() {
			got = normalizeInvocation([]string{"plan list", "--help"})
		})
		assert.Equal(t, []string{"plan", "list", "--help"}, got)
		assert.Contains(t, stderr, "-> Correcting to: plan list --help")
	})

	t.Run("a suggest-only rewrite leaves argv for cobra to reject", func(t *testing.T) {
		in := []string{"daemon", "--kill"}
		var got []string
		stderr := captureStderr(t, func() { got = normalizeInvocation(in) })
		assert.Equal(t, in, got)
		assert.Empty(t, stderr, "the recovery path, not this step, shows the suggestion")
	})

	t.Run("a valid invocation passes through silently", func(t *testing.T) {
		in := []string{"plan", "list", "--json"}
		var got []string
		stderr := captureStderr(t, func() { got = normalizeInvocation(in) })
		assert.Equal(t, in, got)
		assert.Empty(t, stderr)
	})

	t.Run("an empty argv passes through", func(t *testing.T) {
		assert.Empty(t, normalizeInvocation(nil))
	})
}
