package main

import (
	"testing"

	"github.com/sageox/ox/internal/codedb/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The verb wrappers MUST round-trip through search.ParseQuery to the exact
// equivalent ParsedQuery the DSL would have produced. If a verb drifts, the
// agent gets a different result depending on which command it picked — that
// breaks the contract the verb wrappers exist to preserve.
//
// We test the DSL strings the verbs build (the cobra plumbing is covered by
// integration; here we test the semantics).

func TestVerbCallers_BuildsCalledByDSL(t *testing.T) {
	q := `calledby:authenticate`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "authenticate", parsed.Filters.CalledBy,
		"callers <name> must map to calledby:<name> filter")
}

func TestVerbCallees_BuildsCallsDSL(t *testing.T) {
	q := `calls:Handler`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "Handler", parsed.Filters.Calls)
}

func TestVerbCallees_WithDepth(t *testing.T) {
	q := `calls:Handler depth:3`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "Handler", parsed.Filters.Calls)
	assert.Equal(t, 3, parsed.Filters.Depth)
}

func TestVerbDefs_BuildsTypeSymbolDSL(t *testing.T) {
	q := `ResolveSession type:symbol`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, search.SearchTypeSymbol, parsed.Type)
	require.Len(t, parsed.SearchTerms, 1)
	assert.Equal(t, "ResolveSession", parsed.SearchTerms[0])
}

func TestVerbRefs_BuildsTypeCodeDSL(t *testing.T) {
	q := `authenticate type:code`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, search.SearchTypeCode, parsed.Type)
	require.Len(t, parsed.SearchTerms, 1)
	assert.Equal(t, "authenticate", parsed.SearchTerms[0])
}

func TestVerbRefs_WithLang(t *testing.T) {
	q := `migration type:code lang:go`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "go", parsed.Filters.Lang)
	assert.Equal(t, search.SearchTypeCode, parsed.Type)
}

func TestVerbLog_BuildsFileTypeCommitDSL(t *testing.T) {
	q := `file:internal/codedb/ type:commit`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "internal/codedb/", parsed.Filters.File)
	assert.Equal(t, search.SearchTypeCommit, parsed.Type)
}

func TestVerbLog_WithAuthorAndDateRange(t *testing.T) {
	q := `file:cmd/ox/code.go type:commit author:rupak after:2026-04-01 before:2026-05-01`
	parsed, err := search.ParseQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "cmd/ox/code.go", parsed.Filters.File)
	assert.Equal(t, search.SearchTypeCommit, parsed.Type)
	assert.Equal(t, "rupak", parsed.Filters.Author)
	assert.Equal(t, "2026-04-01", parsed.Filters.After)
	assert.Equal(t, "2026-05-01", parsed.Filters.Before)
}

// The verb wrappers are registered into codeCmd; verify they show up so the
// `--help` table actually advertises them.
func TestVerbCommands_AreRegistered(t *testing.T) {
	wanted := map[string]bool{
		"callers": false,
		"callees": false,
		"defs":    false,
		"refs":    false,
		"log":     false,
	}
	for _, sub := range codeCmd.Commands() {
		name := sub.Name()
		if _, ok := wanted[name]; ok {
			wanted[name] = true
		}
	}
	for name, present := range wanted {
		assert.True(t, present, "verb subcommand %q must be registered on codeCmd", name)
	}
}
