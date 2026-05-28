package checks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAll_ReturnsAllChecks(t *testing.T) {
	t.Parallel()

	git := &mockGitRunner{results: map[string]mockGitResult{}}
	fs := newMockFS()
	lookPath := func(string) (string, error) { return "/usr/local/bin/ox", nil }

	allChecks := All(Deps{
		Git:      git,
		FS:       fs,
		LookPath: lookPath,
	})

	require.Len(t, allChecks, 4, "expected 4 registered checks")

	// verify each check has a unique, non-empty name
	names := make(map[string]bool)
	for _, c := range allChecks {
		name := c.Name()
		assert.NotEmpty(t, name, "check name should not be empty")
		assert.False(t, names[name], "duplicate check name: %s", name)
		names[name] = true
	}

	// verify expected checks are present
	assert.True(t, names[".sageox/ directory"], "missing SageoxDirectoryCheck")
	assert.True(t, names[".gitignore"], "missing GitignoreCheck")
	assert.True(t, names["ox in PATH"], "missing OxInPathCheck")
	assert.True(t, names["terminal-patterns framework"], "missing TerminalPatternsCheck")
}

func TestAll_NilDeps(t *testing.T) {
	t.Parallel()
	// prevents: nil deps causes panic — All should handle gracefully
	// Git=nil will be passed through to checks; OxInPathCheck handles nil lookPath
	allChecks := All(Deps{})
	assert.Len(t, allChecks, 4)
}

func TestAll_EachCheckHasCategory(t *testing.T) {
	t.Parallel()

	git := &mockGitRunner{results: map[string]mockGitResult{}}
	allChecks := All(Deps{Git: git})

	for _, c := range allChecks {
		assert.NotEmpty(t, c.Category(), "check %s should have a category", c.Name())
	}
}

func TestByName_ReturnsMapWithAllChecks(t *testing.T) {
	t.Parallel()

	git := &mockGitRunner{results: map[string]mockGitResult{}}
	m := ByName(Deps{Git: git})

	require.Len(t, m, 4, "ByName should return all checks")
	assert.NotNil(t, m[".sageox/ directory"], "missing SageoxDirectoryCheck")
	assert.NotNil(t, m[".gitignore"], "missing GitignoreCheck")
	assert.NotNil(t, m["ox in PATH"], "missing OxInPathCheck")
	assert.NotNil(t, m["terminal-patterns framework"], "missing TerminalPatternsCheck")
}

func TestByName_LookupWorks(t *testing.T) {
	t.Parallel()

	git := &mockGitRunner{results: map[string]mockGitResult{}}
	m := ByName(Deps{Git: git})

	// look up a specific check and verify it has the right category
	check, ok := m[".gitignore"]
	require.True(t, ok, ".gitignore check should exist in map")
	assert.Equal(t, "Git Status", check.Category())
}
