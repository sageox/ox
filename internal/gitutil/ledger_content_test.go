package gitutil

import (
	"context"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testLFSPointer = "version https://git-lfs.github.com/spec/v1\noid sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "\nsize 42\n"

func TestCommitLedgerSnapshot_ContentStorage(t *testing.T) {
	for _, tc := range []struct {
		name, filename, content, stagedMeta, worktreeMeta string
		refused                                           bool
	}{
		{name: "real raw refused", filename: "raw.jsonl", content: `{"type":"user","content":"private"}`, refused: true},
		{name: "valid pointer", filename: "raw.jsonl", content: testLFSPointer},
		{name: "zero-byte pointer", filename: "raw.jsonl", content: strings.ReplaceAll(testLFSPointer, "size 42", "size 0")},
		{name: "malformed pointer refused", filename: "raw.jsonl", content: "version https://git-lfs.github.com/spec/v1\noid sha256:abc\n", refused: true},
		{name: "summary JSON stays in git", filename: "summary.json", content: `{"title":"Summary"}`},
		{name: "explicit git registration", filename: "raw.jsonl", content: "legacy raw", stagedMeta: `{"files":{"raw.jsonl":{"storage":"git"}}}`},
		{name: "legacy unspecified storage is LFS", filename: "raw.jsonl", content: "raw", stagedMeta: `{"files":{"raw.jsonl":{"oid":"sha256:abc"}}}`, refused: true},
		{name: "worktree registration cannot bypass", filename: "raw.jsonl", content: "raw", stagedMeta: `{"files":{}}`, worktreeMeta: `{"files":{"raw.jsonl":{"storage":"git"}}}`, refused: true},
		{name: "worktree removal cannot invalidate snapshot", filename: "raw.jsonl", content: "raw", stagedMeta: `{"files":{"raw.jsonl":{"storage":"git"}}}`, worktreeMeta: `{"files":{}}`},
		{name: "invalid manifest fails closed", filename: "raw.jsonl", content: testLFSPointer, stagedMeta: `{"files":`, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			path := "sessions/test/" + tc.filename
			writeGitutilFixture(t, repo, path, tc.content)
			if tc.stagedMeta != "" {
				writeGitutilFixture(t, repo, "sessions/test/meta.json", tc.stagedMeta)
			}
			gitInRepo(t, repo, "add", "--sparse", "sessions/test")
			if tc.worktreeMeta != "" {
				writeGitutilFixture(t, repo, "sessions/test/meta.json", tc.worktreeMeta)
			}
			before := gitInRepo(t, repo, "rev-parse", "HEAD")
			committed, err := CommitLedgerSnapshot(context.Background(), repo, "session content", "sessions/test/")
			if tc.refused {
				require.Error(t, err)
				assert.False(t, committed)
				assert.Equal(t, before, gitInRepo(t, repo, "rev-parse", "HEAD"))
				assert.False(t, headHasPath(t, repo, path))
				return
			}
			require.NoError(t, err)
			require.True(t, committed)
			assert.Equal(t, tc.content, headBlob(t, repo, path))
		})
	}
}

func TestValidateLedgerBlob_AllContentNamesRequirePointers(t *testing.T) {
	for _, name := range pipeline.LedgerContentFiles {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, ValidateLedgerBlob("sessions/example/"+name, []byte("actual content")), "LFS pointer")
			require.NoError(t, ValidateLedgerBlob("sessions/example/"+name, []byte(testLFSPointer)))
			require.NoError(t, ValidateLedgerBlob("imports/example/"+name, []byte("actual content")))
		})
	}
}

// A path-scoped commit can exclude a staged manifest. Only the manifest that
// actually reaches the commit may opt an artifact out of pointer storage.
func TestCommitLedgerSnapshot_StorageRegistrationUsesScopedTree(t *testing.T) {
	for _, tc := range []struct {
		name, committedMeta, stagedMeta string
		refused                         bool
	}{
		{name: "out-of-scope registration cannot bypass", committedMeta: `{"files":{}}`, stagedMeta: `{"files":{"raw.jsonl":{"storage":"git"}}}`, refused: true},
		{name: "unchanged committed registration applies", committedMeta: `{"files":{"raw.jsonl":{"storage":"git"}}}`, stagedMeta: `{"files":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			writeGitutilFixture(t, repo, "sessions/test/meta.json", tc.committedMeta)
			gitInRepo(t, repo, "add", "--sparse", "sessions/test/meta.json")
			gitInRepo(t, repo, "commit", "-m", "manifest")
			writeGitutilFixture(t, repo, "sessions/test/meta.json", tc.stagedMeta)
			writeGitutilFixture(t, repo, "sessions/test/raw.jsonl", "raw content")
			gitInRepo(t, repo, "add", "--sparse", "sessions/test/")
			committed, err := CommitLedgerSnapshot(context.Background(), repo, "session raw", "sessions/test/raw.jsonl")
			if tc.refused {
				require.ErrorContains(t, err, "LFS pointer")
				require.False(t, committed)
				return
			}
			require.NoError(t, err)
			require.True(t, committed)
			assert.Equal(t, tc.committedMeta, headBlob(t, repo, "sessions/test/meta.json"))
		})
	}
}
