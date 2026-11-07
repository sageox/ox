package main

import (
	"encoding/json"
	"strings"
	"testing"

	friction "github.com/sageox/frictionax"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The friction catalog is a map from what coworkers type to what ox has. These
// tests keep that map honest as commands are added, renamed and removed:
//
//   - a target that names no command sends the coworker from one error to
//     another (the old "ledgers" -> "ledger" entry did exactly that);
//   - a pattern that is already a command can never fire, so it only misleads
//     the next person who reads the catalog.

// embeddedCatalogForTest parses the catalog that ships in the binary.
func embeddedCatalogForTest(t *testing.T) friction.CatalogData {
	t.Helper()
	var data friction.CatalogData
	require.NoError(t, json.Unmarshal(defaultCatalogJSON, &data), "embedded default_catalog.json must be valid CatalogData")
	return data
}

// registerGatedCommandsForTest makes the default-off commands resolvable, so a
// catalog entry that points at one is judged against the full command tree. The
// catalog itself carries no entries for them (they are invisible when off), but
// a pattern that collides with one must still be seen as dead when it is on.
func registerGatedCommandsForTest(t *testing.T) {
	t.Helper()
	for _, gated := range []struct{ parent, command *cobra.Command }{
		{rootCmd, scoutCmd},
		{rootCmd, bulletinCmd},
		{rootCmd, cartsCmd},
		{sessionCmd, sessionTraceCmd},
	} {
		wasRegistered := commandRegistered(gated.parent, gated.command)
		setCommandRegistered(gated.parent, gated.command, true)
		t.Cleanup(func() { setCommandRegistered(gated.parent, gated.command, wasRegistered) })
	}
}

// deliberateDeadPatterns are catalog patterns that resolve to a real command on
// purpose. Each needs a reason a reviewer can check.
var deliberateDeadPatterns = map[string]string{
	// TestFrictionCatalog_TeamRedirects pins this redirect, and
	// TestInviteAlias_ResolvesSoFrictionNeverFires documents that the hidden
	// alias is real. The entry is kept so the redirect survives the alias being
	// retired, instead of being deleted and re-added.
	"invite": "hidden alias of `team invite`; the redirect is a pinned contract",
}

// TestFrictionCatalog_CommandTargetsResolve proves every literal target is a
// command ox has, with no words left over that name nothing. (A target such as
// `plan view` still needs an argument from the coworker; the parse safety net,
// not this test, decides whether a bare one may run.)
// Failure prevented: an auto-executed correction that lands on "unknown
// command" again, so the coworker gets two errors for one mistake.
func TestFrictionCatalog_CommandTargetsResolve(t *testing.T) {
	registerGatedCommandsForTest(t)
	data := embeddedCatalogForTest(t)

	for _, mapping := range data.Commands {
		if mapping.HasRegex {
			continue
		}
		t.Run(mapping.Pattern, func(t *testing.T) {
			words := strings.Fields(mapping.Target)
			require.NotEmpty(t, words, "catalog entry %q has an empty target", mapping.Pattern)

			cmd, leftover, err := rootCmd.Find(words)
			require.NoError(t, err, "target %q of %q must resolve", mapping.Target, mapping.Pattern)
			require.NotSame(t, rootCmd, cmd, "target %q of %q resolves to the root command, so it names nothing", mapping.Target, mapping.Pattern)
			if cmd != agentCmd {
				// the agent dispatcher takes free-form words (ids, subcommands);
				// every other target must be exactly a command path
				assert.Empty(t, leftover, "target %q of %q has words after the command %q that name nothing", mapping.Target, mapping.Pattern, cmd.CommandPath())
			}
		})
	}
}

// TestFrictionCatalog_NoPatternIsAlreadyACommand proves no literal pattern is a
// command ox has registered.
// Failure prevented: dead catalog entries. The friction path only runs after an
// invocation fails, so an entry for a registered command can never fire; it
// just misleads the next reader (`agent list` -> `daemon list` kept claiming a
// command that had shipped, and `teams` is an alias of `team`).
//
// A pattern that is a command followed by a word that command rejects at run
// time (`view team`, or `agent status` on the dispatcher) is live: cobra
// resolves the command, then the command's own error reaches the catalog. Only
// a pattern that is exactly a registered command path is dead.
func TestFrictionCatalog_NoPatternIsAlreadyACommand(t *testing.T) {
	registerGatedCommandsForTest(t)
	data := embeddedCatalogForTest(t)

	for _, mapping := range data.Commands {
		if mapping.HasRegex {
			continue
		}
		t.Run(mapping.Pattern, func(t *testing.T) {
			cmd, leftover, err := rootCmd.Find(strings.Fields(mapping.Pattern))
			isCommand := err == nil && cmd != rootCmd && len(leftover) == 0
			if reason, deliberate := deliberateDeadPatterns[mapping.Pattern]; deliberate {
				assert.True(t, isCommand, "exception for %q is stale (%s): it no longer resolves to a command, delete the exception", mapping.Pattern, reason)
				return
			}
			path := ""
			if isCommand {
				path = cmd.CommandPath()
			}
			assert.False(t, isCommand, "pattern %q is the registered command %q, so this entry can never fire: delete it", mapping.Pattern, path)
		})
	}
}

// TestFrictionCatalog_PatternsAreUnique proves no two entries share a
// normalized pattern: the later one silently replaces the earlier.
func TestFrictionCatalog_PatternsAreUnique(t *testing.T) {
	data := embeddedCatalogForTest(t)
	seen := make(map[string]bool, len(data.Commands))
	for _, mapping := range data.Commands {
		key := strings.Join(strings.Fields(mapping.Pattern), " ")
		assert.False(t, seen[key], "pattern %q appears twice in the catalog", key)
		seen[key] = true
	}
}

// regexExamples gives every regex catalog entry at least one input it must
// handle, and the argv it must expand to. A new regex entry without a row here
// fails the test below, so a pattern is never shipped untried.
var regexExamples = map[string][]struct{ input, want string }{
	`^agent ([a-zA-Z0-9]+) close$`:         {{"agent Oxa7b3 close", "agent Oxa7b3 session stop"}},
	`^agent close ([a-zA-Z0-9]+)$`:         {{"agent close Oxa7b3", "agent Oxa7b3 session stop"}},
	`^agent ([a-zA-Z0-9]+) stop$`:          {{"agent Oxa7b3 stop", "agent Oxa7b3 session stop"}},
	`^agent ([a-zA-Z0-9]+) end$`:           {{"agent Oxa7b3 end", "agent Oxa7b3 session stop"}},
	`^agent ([a-zA-Z0-9]+) finish$`:        {{"agent Oxa7b3 finish", "agent Oxa7b3 session stop"}},
	`^agent ([a-zA-Z0-9]+) status$`:        {{"agent Oxa7b3 status", "session status"}},
	`^agent ([a-zA-Z0-9]+) session score$`: {{"agent Oxa7b3 session score", "session score"}},
	`^([a-z][a-z-]*) help$`:                {{"sync help", "sync --help"}, {"plan help", "plan --help"}, {"agent help", "agent --help"}},
}

// TestFrictionCatalog_RegexEntriesAreExercised proves each regex entry matches
// a realistic input and expands to an argv that resolves.
// Failure prevented: a regex that never matches (a typo in the pattern, or
// has_regex left false so the anchors are matched literally), which looks
// shipped and does nothing.
func TestFrictionCatalog_RegexEntriesAreExercised(t *testing.T) {
	registerGatedCommandsForTest(t)
	data := embeddedCatalogForTest(t)

	covered := make(map[string]bool, len(regexExamples))
	for i := range data.Commands {
		mapping := &data.Commands[i]
		isRegexLike := strings.HasPrefix(mapping.Pattern, "^") || strings.HasSuffix(mapping.Pattern, "$")
		if isRegexLike {
			require.True(t, mapping.HasRegex, "pattern %q is written as a regex but has_regex is false, so its anchors are matched literally and it never fires", mapping.Pattern)
		}
		if !mapping.HasRegex {
			continue
		}
		examples, ok := regexExamples[mapping.Pattern]
		require.True(t, ok, "regex entry %q has no example in regexExamples: add the input it exists to catch", mapping.Pattern)
		covered[mapping.Pattern] = true

		for _, example := range examples {
			t.Run(example.input, func(t *testing.T) {
				got, matched := mapping.ApplyMapping(example.input)
				require.True(t, matched, "%q must match %q", mapping.Pattern, example.input)
				assert.Equal(t, example.want, got)

				words := strings.Fields(got)
				cmd, _, err := rootCmd.Find(words)
				require.NoError(t, err, "expansion %q must resolve", got)
				require.NotSame(t, rootCmd, cmd, "expansion %q resolves to the root command", got)
				assert.True(t, argvParsesCleanly(words), "expansion %q must parse as an invocation", got)
			})
		}
	}
	for pattern := range regexExamples {
		assert.True(t, covered[pattern], "regexExamples lists %q but the catalog has no such regex entry", pattern)
	}
}

// TestFrictionCatalog_AutoExecutePolicy proves the catalog holds to the
// auto-execute rule: an entry runs unprompted only when its confidence clears
// frictionax's threshold and its target keeps the verb without mutating or
// destroying anything.
// Failure prevented: an agent typing "signout" and being logged out, or "update"
// replacing the binary, because the catalog marked a synonym as safe to run.
func TestFrictionCatalog_AutoExecutePolicy(t *testing.T) {
	data := embeddedCatalogForTest(t)
	const autoExecuteThreshold = 0.85 // frictionax: below this an auto entry is silently suggest-only
	mutating := map[string]bool{"upgrade": true, "gc": true, "uninstall": true, "logout": true}

	for _, mapping := range data.Commands {
		t.Run(mapping.Pattern, func(t *testing.T) {
			if !mapping.AutoExecute {
				return
			}
			assert.GreaterOrEqual(t, mapping.Confidence, autoExecuteThreshold,
				"%q is marked auto_execute but its confidence is below frictionax's threshold, so it would never auto-run", mapping.Pattern)
			words := strings.Fields(mapping.Target)
			require.NotEmpty(t, words)
			assert.False(t, mutating[words[0]], "%q auto-executes %q, which mutates or is irreversible: make it suggest-only", mapping.Pattern, mapping.Target)
		})
	}
}

// TestFrictionCatalog_RewriteTokensAreRealFlags proves every bare-boolean
// rewrite token turns the typed flag into a spelling ox accepts.
// Failure prevented: a rewrite that swaps one unknown flag for another.
func TestFrictionCatalog_RewriteTokensAreRealFlags(t *testing.T) {
	var data struct {
		Tokens []flagAlias `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(defaultCatalogJSON, &data))

	rewrites := 0
	for _, token := range data.Tokens {
		if !token.Rewrite {
			continue
		}
		rewrites++
		t.Run(token.Pattern, func(t *testing.T) {
			assert.Equal(t, "unknown-flag", token.Kind)
			assert.True(t, strings.HasPrefix(token.Pattern, "--") && !strings.Contains(token.Pattern, "="),
				"a rewrite token is a bare flag; fully-qualified patterns already rewrite")
			assert.True(t, strings.HasPrefix(token.Target, "--"), "target %q must be a flag", token.Target)
			assert.NoError(t, predictCobraError([]string{"version", token.Target}),
				"rewrite target %q must be accepted by a command that takes the global flags", token.Target)
		})
	}
	assert.GreaterOrEqual(t, rewrites, 2, "the catalog is expected to carry the --text and --full-json rewrites")
}
