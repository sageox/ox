package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInitTemp creates a real git repo in a temp dir and chdirs into it, so
// repotools.FindRepoRoot (which shells out to `git rev-parse`) resolves. No
// CodeDB index is built, so the metadata.db is absent — the "never indexed"
// state the not_indexed contract exists to cover.
func gitInitTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "git init")
	t.Chdir(dir)
}

// Review fix (greptile + CodeRabbit, code.go): every verb routes through
// runCodeSearch, which only handled the actively-indexing case. On a repo that
// was never indexed, codedb.Open returned a raw Go error to agents — the exact
// "hard error that trains agents to abandon the tool" the PR targets. The guard
// must emit the structured not_indexed contract for agents.
//
// Red-first: with the guard removed, an agent call falls through to
// codedb.Open and returns a plain `open codedb: ...` error (require.NoError
// fails, and the JSON unmarshal never sees status=not_indexed).
func TestRunCodeSearch_NeverIndexed_AgentGetsNotIndexedJSON(t *testing.T) {
	gitInitTemp(t)
	t.Setenv("SAGEOX_AGENT_ID", "OxTest") // valid id → agent context

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	require.NoError(t, runCodeSearch(cmd, "anything"))

	var got indexNotReadyResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, indexStatusNotIndexed, got.Status)
	assert.Contains(t, got.FallbackHint, "ox code index")
}

// Human callers get a clear, actionable error (not the raw codedb.Open string).
func TestRunCodeSearch_NeverIndexed_HumanGetsClearError(t *testing.T) {
	gitInitTemp(t)
	t.Setenv("SAGEOX_AGENT_ID", "") // no agent context

	err := runCodeSearch(&cobra.Command{}, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no code index found")
}

// Review fix (CodeRabbit, code_verbs.go): `--after`/`--before` were validated
// with validateSymbolArg, which forbids ':' and so rejected valid ISO 8601
// timestamps. validateDateArg permits ':' while still blocking the real
// injection vectors (whitespace, quotes).
func TestValidateDateArg_AcceptsISO8601(t *testing.T) {
	for _, v := range []string{
		"2026-04-01",
		"2026-04-01T10:00:00Z",
		"2026-04-01T10:00:00+02:00",
	} {
		assert.NoError(t, validateDateArg("--after", v), v)
	}
}

func TestValidateDateArg_RejectsInjectionVectors(t *testing.T) {
	for _, v := range []string{
		"",                       // empty
		"2026-04-01 type:symbol", // whitespace → second filter
		"2026\t04",               // tab
		`"2026-04-01"`,           // quote
		"2026-04-01'",            // quote
	} {
		assert.Error(t, validateDateArg("--after", v), v)
	}
}

// Red-first contrast: the symbol validator (the old code path) rejects a valid
// timestamp — that IS the bug — while the date validator (the fix) accepts it.
func TestDateFilters_SymbolValidatorRejects_DateValidatorAccepts(t *testing.T) {
	const ts = "2026-04-01T10:00:00Z"
	assert.Error(t, validateSymbolArg("--after", ts),
		"symbol validator rejects ':' timestamps — the bug the branch fixes")
	assert.NoError(t, validateDateArg("--after", ts),
		"date validator accepts ISO 8601 timestamps — the fix")
}

// Wiring proof: `ox code log <path> --after <ISO-timestamp>` must pass the
// validation loop (which now routes --after/--before through validateDateArg)
// and fail only downstream on the missing index. Before the fix the loop used
// validateSymbolArg and the command errored on the ':' as a "DSL delimiter".
func TestCodeLog_AfterTimestamp_PassesValidationLoop(t *testing.T) {
	gitInitTemp(t)
	t.Setenv("SAGEOX_AGENT_ID", "") // human path → clear downstream error

	require.NoError(t, codeLogCmd.Flags().Set("after", "2026-04-01T10:00:00Z"))
	t.Cleanup(func() { _ = codeLogCmd.Flags().Set("after", "") })

	err := codeLogCmd.RunE(codeLogCmd, []string{"cmd/ox/code.go"})
	require.Error(t, err) // never indexed → downstream no-index error
	assert.NotContains(t, err.Error(), "DSL delimiter",
		"--after timestamp must clear validation, not be rejected as a DSL delimiter")
	assert.Contains(t, err.Error(), "no code index found",
		"error must be the downstream no-index one, proving validation passed")
}
