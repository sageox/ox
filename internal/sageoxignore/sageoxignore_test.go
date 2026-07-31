package sageoxignore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasEntry(t *testing.T) {
	tests := []struct {
		name    string
		content string
		entry   string
		want    bool
		why     string
	}{
		{"exact match", "kb/\n", "kb/", true, "the basic case"},
		{"among others", "cache/\nkb/\nlogs/\n", "kb/", true, "position must not matter"},
		{"leading whitespace", "  kb/\n", "kb/", true, "git trims, so must we"},
		{"absent", "cache/\nlogs/\n", "kb/", false, "no false positive"},
		{"empty content", "", "kb/", false, "a missing file must not read as present"},
		{
			"commented out", "# kb/\n", "kb/", false,
			"a documentary comment is not a live rule — treating it as one would " +
				"silently leave the directory tracked",
		},
		{
			"substring only", "kb/foo\n", "kb/", false,
			"prefix matching would make us skip writing a rule we actually need",
		},
		{"negation is a different rule", "!kb/\n", "kb/", false, "!kb/ re-includes; it is not kb/"},
		{"no trailing newline", "cache/\nkb/", "kb/", true, "last line still counts"},
		{"CRLF", "cache/\r\nkb/\r\n", "kb/", true, "Windows checkouts must not double-write the rule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasEntry(tt.content, tt.entry), tt.why)
		})
	}
}

func TestEnsureEntry_CreatesFileWithExactlyTheEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")

	added, created, err := EnsureEntry(path, KBEntry)
	require.NoError(t, err)
	assert.True(t, added)
	assert.True(t, created, "caller needs this to choose created-vs-modified for rollback")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "kb/\n", string(got), "must not emit a stray blank line or empty file")
}

func TestEnsureEntry_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("cache/\nkb/\nlogs/\n"), 0o644))

	added, created, err := EnsureEntry(path, KBEntry)
	require.NoError(t, err)
	assert.False(t, added, "already present")
	assert.False(t, created)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "cache/\nkb/\nlogs/\n", string(got),
		"a no-op pass must leave the file byte-identical, or every daemon tick dirties the worktree")
}

func TestEnsureEntry_AppendsWithoutCorruptingAMissingTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("cache/\nlogs/"), 0o644))

	added, _, err := EnsureEntry(path, KBEntry)
	require.NoError(t, err)
	assert.True(t, added)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "cache/\nlogs/\nkb/\n", string(got),
		"appending to a file with no trailing newline must not produce `logs/kb/`")
}

// TestRemoveLine_OnlyExactMatches is the safety argument for the GH #732
// cleanup, stated as assertions. Every "must survive" case below is a
// rule that means something different from `.sageox/kb/`, so removing it
// could start tracking a path the user expects to be ignored.
func TestRemoveLine_OnlyExactMatches(t *testing.T) {
	survivors := []struct {
		line string
		why  string
	}{
		{".sageox/", "ignores the entire .sageox directory — vastly broader"},
		{".sageox", "same, without the slash"},
		{"/.sageox/kb/", "anchored to the repo root — a different pattern"},
		{".sageox/kb", "no trailing slash matches a *file* named kb too"},
		{".sageox/kb/*", "contents-only; leaves the directory itself matchable"},
		{"!.sageox/kb/", "a re-include; deleting it would flip the meaning"},
		{"# .sageox/kb/", "a comment the user wrote for themselves"},
		{".sageox/kbextra/", "different directory that merely shares a prefix"},
	}

	for _, s := range survivors {
		t.Run(s.line, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".gitignore")
			content := "node_modules/\n" + s.line + "\ndist/\n"
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

			removed, err := RemoveLine(path, LegacyRootKBLine)
			require.NoError(t, err)
			assert.False(t, removed, "must not report a removal")

			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, content, string(got), "must survive: "+s.why)
		})
	}
}

func TestRemoveLine_RemovesTheLegacyLineAndNothingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	content := "# build output\nnode_modules/\ndist/\n\n.sageox/kb/\n\n# editor\n.idea/\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	removed, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err)
	assert.True(t, removed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# build output\nnode_modules/\ndist/\n\n\n# editor\n.idea/\n", string(got),
		"comments, blank lines, ordering and the trailing newline must all be preserved")
}

func TestRemoveLine_RemovesEveryOccurrence(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	// a repo that ran several older ox versions could accumulate duplicates.
	require.NoError(t, os.WriteFile(path, []byte(".sageox/kb/\nnode_modules/\n.sageox/kb/\n"), 0o644))

	removed, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err)
	assert.True(t, removed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "node_modules/\n", string(got), "a single pass must clear all duplicates")
}

func TestRemoveLine_PreservesFilesWithNoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules/\n.sageox/kb/"), 0o644))

	removed, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err)
	assert.True(t, removed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "node_modules/\n", string(got),
		"removing the final line must not leave a dangling separator")
}

func TestRemoveLine_MissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")

	removed, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err, "a repo with no .gitignore is the common case, not a failure")
	assert.False(t, removed)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "must not create the file it was asked to clean")
}

func TestRemoveLine_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules/\n.sageox/kb/\n"), 0o644))

	removed, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err)
	require.True(t, removed)
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	removed2, err := RemoveLine(path, LegacyRootKBLine)
	require.NoError(t, err)
	assert.False(t, removed2, "second pass has nothing to do")

	second, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second), "second pass must not rewrite the file")
}

// TestKBEntryAndLegacyLineDescribeTheSamePaths documents the equivalence
// the whole #732 migration depends on: `kb/` inside .sageox/.gitignore
// covers exactly what `.sageox/kb/` covered from the repo root, because
// nested gitignore patterns resolve relative to their own directory.
func TestKBEntryAndLegacyLineDescribeTheSamePaths(t *testing.T) {
	assert.Equal(t, ".sageox/"+KBEntry, LegacyRootKBLine,
		"if these ever diverge the cleanup stops being behavior-preserving and must be re-argued")
}
