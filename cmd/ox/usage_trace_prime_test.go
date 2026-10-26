package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/teamdocs"
)

// primeUsageOutput is a prime large enough that the Claude Code hook cap
// trims it.
func primeUsageOutput(fullPath string) agentPrimeOutput {
	return agentPrimeOutput{
		AgentID:            usageAgentID,
		Status:             "fresh",
		HookOutputBudget:   primeHookBudget,
		HookFullBundlePath: fullPath,
		TeamContext: &teamContextInfo{
			TeamID: "team-1", TeamName: "Acme",
			TeamRules:     []teamdocs.TeamRule{{Name: "retry-policy", Visibility: "always", AbsPath: "/t/r.md", Body: strings.Repeat("retry with backoff. ", 60)}},
			MemoryContent: strings.Repeat("- memory\n", 40),
		},
	}
}

func runPrimeXML(t *testing.T, out agentPrimeOutput) (string, *prime.ContextBudget) {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	budget, err := outputAgentPrimeXML(cmd, out)
	require.NoError(t, err)
	return buf.String(), budget
}

var contextBudgetAttrRe = regexp.MustCompile(`<context-budget hint="[^"]*"((?: [a-z]+="\d+")*)>`)

// contextBudgetAttrs parses the <context-budget> block prime showed the model.
func contextBudgetAttrs(t *testing.T, doc string) map[string]int {
	t.Helper()
	m := contextBudgetAttrRe.FindStringSubmatch(doc)
	require.NotNil(t, m, "prime carries a <context-budget> block")
	attrs := map[string]int{}
	for _, kv := range regexp.MustCompile(`([a-z]+)="(\d+)"`).FindAllStringSubmatch(m[1], -1) {
		n, err := strconv.Atoi(kv[2])
		require.NoError(t, err)
		attrs[kv[1]] = n
	}
	return attrs
}

// TestPrime_RecordsContextBudgetIntoSessionTrace is the customer claim for
// prime: the team context a prime injected shows up in the session's usage
// trace with its per-source token split, and a hook-capped prime says how much
// was trimmed and what was deferred. Failure prevented: `ox usage` reports no
// context from prime (the largest single injection), or reports the
// untrimmed size as if it all reached the model.
func TestPrime_RecordsContextBudgetIntoSessionTrace(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	fullPath := filepath.Join(t.TempDir(), "prime", usageAgentID+"-full.xml")

	emitted, budget := runPrimeXML(t, primeUsageOutput(fullPath))

	events := invokedEvents(t, sessionTracePath(state))
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, "agent prime", ev.Command)
	assert.Equal(t, "loaded team context", ev.Label)
	assert.Equal(t, len(emitted), ev.OutputBytes)

	sum := 0
	for _, n := range ev.TokensBySource {
		sum += n
	}
	assert.Equal(t, budget.Total(), sum, "tokens_by_source is prime's own budget")
	assert.Equal(t, sum, ev.InlineTokens)

	// The <context-budget> block reports every source as of just before the
	// block; only the sageox bucket grows after it (the block itself and the
	// closing tag are ox framing).
	full, err := os.ReadFile(fullPath)
	require.NoError(t, err)
	shown := contextBudgetAttrs(t, string(full))
	for src, n := range shown {
		if src == "total" || src == prime.BudgetSourceSageox {
			continue
		}
		assert.Equal(t, n, ev.TokensBySource[src], "source %s matches <context-budget>", src)
	}
	assert.GreaterOrEqual(t, ev.TokensBySource[prime.BudgetSourceSageox], shown[prime.BudgetSourceSageox])
	assert.Less(t, sum-shown["total"], 200, "only the trailing budget block and closing tag are added")

	assert.Equal(t, len(full), ev.FullBytes, "full_bytes is the untrimmed prime")
	assert.Greater(t, ev.FullBytes, ev.OutputBytes)
	_, wantDeferred := fitPrimeToHookCap(string(full), primeHookBudget, fullPath)
	require.NotEmpty(t, wantDeferred)
	assert.Equal(t, wantDeferred, ev.Deferred)
}

// TestPrime_UntrimmedPrimeGoesToAgentCacheWhenNotRecording covers the
// not-recording destination and an untrimmed prime. Failure prevented: an untrimmed prime reports a stale bundle's size as
// trimmed, or a prime with no live recording is lost.
func TestPrime_UntrimmedPrimeGoesToAgentCacheWhenNotRecording(t *testing.T) {
	root := usageTraceEnv(t, nil)
	out := primeUsageOutput(filepath.Join(t.TempDir(), "stale.xml"))
	require.NoError(t, os.WriteFile(out.HookFullBundlePath, []byte(strings.Repeat("s", 50_000)), 0o600))
	out.HookOutputBudget = 0 // direct invocation: no cap

	emitted, _ := runPrimeXML(t, out)

	events := invokedEvents(t, filepath.Join(agentTraceCacheDir(root), usageAgentID+".jsonl"))
	require.Len(t, events, 1)
	assert.Equal(t, len(emitted), events[0].OutputBytes)
	assert.Zero(t, events[0].FullBytes, "nothing was trimmed; the stale bundle is ignored")
	assert.Empty(t, events[0].Deferred)
	assert.NotEmpty(t, events[0].TokensBySource)
}

// TestPrime_ViaBashYieldsExactlyOneEvent runs prime and then its PostToolUse
// hook. Failure prevented: every prime is counted twice — once by prime and
// once by the hook — doubling the context the report says ox injected.
func TestPrime_ViaBashYieldsExactlyOneEvent(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)

	emitted, _ := runPrimeXML(t, primeUsageOutput(filepath.Join(t.TempDir(), "full.xml")))
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox agent prime"), ToolResponse: bashStdout(t, emitted),
	})

	events := invokedEvents(t, sessionTracePath(state))
	require.Len(t, events, 1)
	assert.Equal(t, "agent prime", events[0].Command)
	assert.NotEmpty(t, events[0].TokensBySource, "the one event is prime's own, not the hook's")
}

// TestPrime_OptOutWritesNoInvokedEvent turns recap.trace_consultations off.
// Failure prevented: a coworker who opted out still has prime's call recorded.
func TestPrime_OptOutWritesNoInvokedEvent(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))

	runPrimeXML(t, primeUsageOutput(filepath.Join(t.TempDir(), "full.xml")))
	assert.Empty(t, invokedEvents(t, sessionTracePath(state)))
}

// TestPrimeDeferredSections parses the pointer prime emits.
func TestPrimeDeferredSections(t *testing.T) {
	assert.Nil(t, primeDeferredSections("<ox-prime>\n</ox-prime>\n"))
	doc := "<ox-prime>\n" + "\n<deferred path=\"/x\" hint=\"h\">\n- team-knowledge/docs: read it\n- commands\n</deferred>\n</ox-prime>\n"
	assert.Equal(t, []string{"team-knowledge/docs", "commands"}, primeDeferredSections(doc))
}
