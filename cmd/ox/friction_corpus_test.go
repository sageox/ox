package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// frictionCorpusCase is one anonymized invocation an AI coworker or person
// actually typed, with the outcome ox is meant to produce for it.
type frictionCorpusCase struct {
	Argv         []string `json:"argv"`
	Events       int      `json:"events"`
	Expect       string   `json:"expect"`
	Target       string   `json:"target"`
	RuntimeError string   `json:"runtime_error"`
}

// corpusBannedSuggestions are commands a failed invocation must never be
// steered toward by a guess: each discards or rebuilds local state.
var corpusBannedSuggestions = []string{"gc", "uninstall", "logout"}

// TestFrictionCorpus replays every invocation shape from a month of failed ox
// commands and asserts what ox does with it today: run it as typed, correct
// and run it, suggest a fix without running it, or keep it an error. A
// regression here means an AI coworker is back to guessing.
func TestFrictionCorpus(t *testing.T) {
	unregisterGatedCommandsForTest(t)

	// TestMain moves the working directory into a sandbox; packageDir is the source dir
	raw, err := os.ReadFile(filepath.Join(packageDir, "testdata", "friction-corpus.json"))
	require.NoError(t, err)
	var doc struct {
		Cases []frictionCorpusCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Cases)

	var firstTry, total int
	for _, c := range doc.Cases {
		total += c.Events
		name := strings.Join(c.Argv, " ")
		t.Run(name, func(t *testing.T) {
			outcome, detail := whatOxDoes(c.Argv, c.RuntimeError)
			switch c.Expect {
			case "resolves":
				require.Equal(t, "resolves", outcome, "ox should run %q as typed; got %s (%s)", name, outcome, detail)
			case "auto":
				require.Equal(t, "auto", outcome, "ox should correct and run %q; got %s (%s)", name, outcome, detail)
				require.Equal(t, c.Target, detail, "ox corrected %q to the wrong command", name)
			case "suggest":
				require.Equal(t, "suggest", outcome, "ox should suggest a fix for %q without running it; got %s (%s)", name, outcome, detail)
				require.Contains(t, detail, c.Target, "ox suggested the wrong command for %q", name)
			case "error":
				require.NotEqual(t, "resolves", outcome, "%q should stay an error", name)
				require.NotEqual(t, "auto", outcome, "%q must not be corrected and run (ran %s)", name, detail)
				for _, banned := range corpusBannedSuggestions {
					require.NotEqual(t, banned, strings.Fields(detail+" ")[0], "%q must not suggest %q", name, banned)
				}
			default:
				t.Fatalf("unknown expectation %q", c.Expect)
			}
		})
		if c.Expect == "resolves" || c.Expect == "auto" {
			firstTry += c.Events
		}
	}
	t.Logf("friction corpus: %d of %d failed invocations now reach a real command on the first try", firstTry, total)
}

// whatOxDoes predicts, without executing anything, how ox handles argv: the
// pre-cobra rewrites, the catalog flag rewrites main applies, the cobra error
// the argv would raise (or the dispatcher's runtime error), and the correction
// planned for that error.
func whatOxDoes(argv []string, runtimeError string) (outcome, detail string) {
	if plan := planCorrection(argv, nil); plan.Auto {
		return "auto", strings.Join(plan.Corrected, " ")
	}
	rewritten := applyCatalogTokenRewrites(argv, loadFlagAliases(defaultCatalogJSON))
	err := predictCobraError(rewritten)
	if err == nil && runtimeError != "" {
		err = errors.New(runtimeError)
	}
	if err == nil {
		return "resolves", strings.Join(rewritten, " ")
	}
	plan := planCorrection(rewritten, err)
	if plan.Auto {
		return "auto", strings.Join(plan.Corrected, " ")
	}
	if plan.Suggestion != "" {
		return "suggest", plan.Suggestion
	}
	return "error", fmt.Sprint(err)
}

// unregisterGatedCommandsForTest removes feature-gated commands for the test,
// so the corpus sees them the way a coworker without the feature does.
func unregisterGatedCommandsForTest(t *testing.T) {
	t.Helper()
	for _, gated := range []struct{ parent, command *cobra.Command }{
		{rootCmd, scoutCmd},
		{rootCmd, bulletinCmd},
		{rootCmd, cartsCmd},
		{sessionCmd, sessionTraceCmd},
	} {
		wasRegistered := commandRegistered(gated.parent, gated.command)
		setCommandRegistered(gated.parent, gated.command, false)
		t.Cleanup(func() { setCommandRegistered(gated.parent, gated.command, wasRegistered) })
	}
}
