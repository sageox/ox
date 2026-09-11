package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHasConflictMarkers covers the shapes HasConflictMarkers must
// distinguish. Failure prevented: a caller stages a file with this check as
// its only guard against baking conflict markers into a commit — a false
// negative here means real corruption ships silently.
func TestHasConflictMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "clean file",
			content: `{"summary_status": "ok"}`,
			want:    false,
		},
		{
			name: "canonical three-way conflict",
			content: `{
<<<<<<< Updated upstream
  "summary_attempts": 3,
=======
  "summary_attempts": 2,
>>>>>>> Stashed changes
}`,
			want: true,
		},
		{
			name:    "marker must start the line, not just appear mid-line",
			content: `{"note": "the diff had <<<<<<< in it but this line doesn't start with it"}`,
			want:    false,
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "meta.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0644))

			got, err := HasConflictMarkers(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHasConflictMarkers_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := HasConflictMarkers(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err)
}

func TestValidateLedgerBlob(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		path    string
		content string
		wantErr string
	}{
		{name: "valid session metadata", path: "sessions/example/meta.json", content: `{"title":"Ready"}`},
		{name: "invalid session metadata", path: "sessions/example/meta.json", content: `{"title":`, wantErr: "invalid JSON"},
		{name: "array session metadata", path: "sessions/example/meta.json", content: `[]`, wantErr: "invalid JSON object"},
		{name: "scalar session metadata", path: "sessions/example/meta.json", content: `"metadata"`, wantErr: "invalid JSON object"},
		{name: "null session metadata", path: "sessions/example/meta.json", content: `null`, wantErr: "invalid JSON object"},
		{name: "invalid JSON outside session metadata", path: "data/github/event.json", content: `{"partial":`},
		{name: "conflict marker in any artifact", path: "sessions/example/summary.md", content: conflictMarkerFixture, wantErr: "unresolved conflict"},
		{name: "nested path named meta is not session metadata", path: "sessions/example/nested/meta.json", content: `{"partial":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLedgerBlob(tc.path, []byte(tc.content))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestValidateLedgerEntryMode — Git stores a symlink's target as the blob, so
// content checks alone let a link whose target reads as valid JSON through.
func TestValidateLedgerEntryMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		path    string
		mode    string
		wantErr string
	}{
		{name: "regular session metadata", path: "sessions/example/meta.json", mode: "100644"},
		{name: "executable session artifact", path: "sessions/example/tool.sh", mode: "100755"},
		{name: "symlink session metadata", path: "sessions/example/meta.json", mode: "120000", wantErr: "symbolic link"},
		{name: "symlink session transcript", path: "sessions/example/raw.jsonl", mode: "120000", wantErr: "symbolic link"},
		{name: "gitlink session metadata", path: "sessions/example/meta.json", mode: "160000", wantErr: "submodule"},
		{name: "unknown mode under sessions", path: "sessions/example/meta.json", mode: "040000", wantErr: "unsupported index mode"},
		{name: "symlink outside sessions is not this validator's call", path: "docs/link.md", mode: "120000"},
		{name: "sessions root file is not a session entry", path: "sessions/README.md", mode: "120000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLedgerEntryMode(tc.path, tc.mode)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

const conflictMarkerFixture = "<<<<<<< Updated upstream\nours\n=======\ntheirs\n>>>>>>> Stashed changes\n"

// Automatic Ledger commits validate index blobs, not worktree bytes. These
// real-Git cases prevent a writer from publishing a staged conflict or malformed
// session metadata while preserving path-scoped commit isolation.
func TestValidateStagedLedgerCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	for _, tc := range []struct {
		name       string
		stagedPath string
		content    string
		pathspec   []string
		wantErr    string
		setup      func(t *testing.T, repo string)
	}{
		{name: "clean metadata", stagedPath: "sessions/example/meta.json", content: `{"title":"Ready"}` + "\n"},
		{name: "invalid metadata", stagedPath: "sessions/example/meta.json", content: `{"title":`, wantErr: "invalid JSON"},
		{name: "marker in non-JSON artifact", stagedPath: "sessions/example/summary.md", content: conflictMarkerFixture, wantErr: "unresolved conflict"},
		{
			name:       "path scope excludes unrelated staged corruption",
			stagedPath: "data/github/event.json",
			content:    `{"ok":true}` + "\n",
			pathspec:   []string{"data/github/"},
			setup: func(t *testing.T, repo string) {
				writeGitutilFixture(t, repo, "sessions/other/meta.json", conflictMarkerFixture)
				gitInRepo(t, repo, "add", "--sparse", "sessions/other/meta.json")
			},
		},
		{
			name:       "unmerged index outside path scope",
			stagedPath: "data/github/event.json",
			content:    `{"ok":true}` + "\n",
			pathspec:   []string{"data/github/"},
			wantErr:    "unresolved conflict in index",
			setup: func(t *testing.T, repo string) {
				writeGitutilFixture(t, repo, "shared.txt", "base\n")
				gitInRepo(t, repo, "add", "shared.txt")
				gitInRepo(t, repo, "commit", "-m", "shared base")
				gitInRepo(t, repo, "checkout", "-b", "other")
				writeGitutilFixture(t, repo, "shared.txt", "other\n")
				gitInRepo(t, repo, "commit", "-am", "other change")
				gitInRepo(t, repo, "checkout", "main")
				writeGitutilFixture(t, repo, "shared.txt", "main\n")
				gitInRepo(t, repo, "commit", "-am", "main change")
				cmd := exec.Command("git", "merge", "other")
				cmd.Dir = repo
				require.Error(t, cmd.Run(), "fixture must leave an unmerged index")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitInRepo(t, repo, "init", "-b", "main")
			writeGitutilFixture(t, repo, "base.txt", "base\n")
			gitInRepo(t, repo, "add", "base.txt")
			gitInRepo(t, repo, "commit", "-m", "base")
			writeGitutilFixture(t, repo, tc.stagedPath, tc.content)
			gitInRepo(t, repo, "add", "--sparse", tc.stagedPath)
			if tc.setup != nil {
				tc.setup(t, repo)
			}

			err := ValidateStagedLedgerCommit(context.Background(), repo, tc.pathspec...)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidateStagedLedgerCommit_AllowsDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index state")
	}
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	writeGitutilFixture(t, repo, "sessions/example/meta.json", `{"title":"Ready"}`+"\n")
	gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")
	gitInRepo(t, repo, "commit", "-m", "base")
	gitInRepo(t, repo, "rm", "sessions/example/meta.json")

	require.NoError(t, ValidateStagedLedgerCommit(context.Background(), repo, "sessions/example/"))
}

func TestValidateStagedLedgerCommit_RejectsTypeChangedBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index state")
	}
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	path := filepath.Join(repo, "sessions", "example", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	if err := os.Symlink("target.json", path); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")
	gitInRepo(t, repo, "commit", "-m", "seed symlink")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte(conflictMarkerFixture), 0o644))
	gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")

	err := ValidateStagedLedgerCommit(context.Background(), repo, "sessions/example/")
	require.ErrorContains(t, err, "unresolved conflict")
}

// TestValidateStagedLedgerCommit_RejectsSymlinkSessionMetadata is the PR #910
// review regression: a regular meta.json replaced by a symlink whose TARGET
// string is a valid JSON object. `git show :path` returns that target text, so
// a content-only validator publishes sessions/<name>/meta.json as a symlink.
func TestValidateStagedLedgerCommit_RejectsSymlinkSessionMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index state")
	}
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	writeGitutilFixture(t, repo, "sessions/example/meta.json", `{"title":"Ready"}`+"\n")
	gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")
	gitInRepo(t, repo, "commit", "-m", "regular file")

	path := filepath.Join(repo, "sessions", "example", "meta.json")
	require.NoError(t, os.Remove(path))
	if err := os.Symlink(`{"title":"Ready"}`, path); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")

	err := ValidateStagedLedgerCommit(context.Background(), repo, "sessions/example/")
	require.ErrorContains(t, err, "symbolic link")
}

// TestValidateStagedLedgerCommit_RejectsGitlinkSessionMetadata covers the
// other non-blob entry type, without needing symlink support on the host.
func TestValidateStagedLedgerCommit_RejectsGitlinkSessionMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index state")
	}
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	writeGitutilFixture(t, repo, "base.txt", "base\n")
	gitInRepo(t, repo, "add", "base.txt")
	gitInRepo(t, repo, "commit", "-m", "base")
	head := gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",sessions/example/meta.json")

	err := ValidateStagedLedgerCommit(context.Background(), repo, "sessions/example/")
	require.ErrorContains(t, err, "submodule")
}

func TestValidateStagedLedgerCommit_UnbornBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index state")
	}
	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "clean object", content: `{"title":"Ready"}` + "\n"},
		{name: "invalid object", content: `[]`, wantErr: "invalid JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitInRepo(t, repo, "init", "-b", "main")
			writeGitutilFixture(t, repo, "sessions/example/meta.json", tc.content)
			gitInRepo(t, repo, "add", "--sparse", "sessions/example/meta.json")

			err := ValidateStagedLedgerCommit(context.Background(), repo, "sessions/example/")
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func writeGitutilFixture(t *testing.T, repo, rel, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// Recovery must not lose metadata, round numeric IDs, or overwrite edits made
// after git produced the conflict. Refused repairs leave both index and file intact.
func TestAutostashRecoveryPreservesData(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git stash conflicts")
	}
	const base = `{"title":"","keep":"yes"}`
	const upstream = `{"title":"Ready","keep":"yes","id":9007199254740993,"nested":{"a":1,"b":2},"cloud_only":true}`
	const local = `{"nested":{"b":2,"a":1},"id":9007199254740993,"keep":"yes","title":"Ready","local_only":true}`
	const merged = `{"title":"Ready","keep":"yes","id":9007199254740993,"nested":{"a":1,"b":2},"cloud_only":true,"local_only":true}`
	for _, tc := range []struct {
		name           string
		local          string
		path           string
		deny           []string
		edit           bool
		style          string
		extraConflict  bool
		marker         string
		stagingFailure bool
		missingFile    bool
		missingBlob    bool
		wantError      bool
	}{
		{name: "retain all fields and large IDs", local: local},
		{name: "diff3 conflict style", local: local, style: "diff3"},
		{name: "different large IDs", local: `{"title":"Ready","keep":"yes","id":9007199254740992}`, wantError: true},
		{name: "deleted field", local: `{"title":"Ready"}`, wantError: true},
		{name: "invalid JSON", local: `{"title":`, wantError: true},
		{name: "manual edits", local: local, edit: true, wantError: true},
		{name: "denied path", local: local, deny: []string{"sessions/test/"}, wantError: true},
		{name: "other session artifact", local: local, path: "sessions/test/summary.json", wantError: true},
		{name: "mixed eligible and unsupported conflicts", local: local, extraConflict: true, wantError: true},
		{name: "merge in progress", local: local, marker: "MERGE_HEAD", wantError: true},
		{name: "rebase merge in progress", local: local, marker: "rebase-merge", wantError: true},
		{name: "rebase apply in progress", local: local, marker: "rebase-apply", wantError: true},
		{name: "cherry-pick in progress", local: local, marker: "CHERRY_PICK_HEAD", wantError: true},
		{name: "revert in progress", local: local, marker: "REVERT_HEAD", wantError: true},
		{name: "resume after staging failure", local: local, stagingFailure: true},
		{name: "missing conflicted file", local: local, missingFile: true, wantError: true},
		{name: "missing conflict blob", local: local, missingBlob: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitInRepo(t, repo, "init", "-b", "main")
			if tc.style != "" {
				gitInRepo(t, repo, "config", "merge.conflictStyle", tc.style)
			}
			rel := tc.path
			if rel == "" {
				rel = "sessions/test/meta.json"
			}
			path := filepath.Join(repo, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			paths := []string{path}
			if tc.extraConflict {
				paths = append(paths, filepath.Join(repo, "unsupported.json"))
			}
			for _, file := range paths {
				require.NoError(t, os.WriteFile(file, []byte(base+"\n"), 0o644))
			}
			gitInRepo(t, repo, "add", "--sparse", ".")
			gitInRepo(t, repo, "commit", "-m", "base")
			for _, file := range paths {
				require.NoError(t, os.WriteFile(file, []byte(tc.local+"\n"), 0o644))
			}
			gitInRepo(t, repo, "stash", "push", "-m", "autostash")
			for _, file := range paths {
				require.NoError(t, os.WriteFile(file, []byte(upstream+"\n"), 0o644))
			}
			gitInRepo(t, repo, "commit", "-am", "upstream")
			cmd := exec.Command("git", "stash", "apply")
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "fixture must conflict: %s", out)
			if tc.edit {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(data, []byte("manual addition\n")...), 0o644))
			}
			before := make(map[string][]byte)
			for _, file := range paths {
				before[file], err = os.ReadFile(file)
				require.NoError(t, err)
			}
			index, err := os.ReadFile(filepath.Join(repo, ".git/index"))
			require.NoError(t, err)
			stash := gitInRepo(t, repo, "rev-parse", "refs/stash")
			if tc.marker != "" {
				markerPath := filepath.Join(repo, ".git", tc.marker)
				if tc.marker == "rebase-merge" || tc.marker == "rebase-apply" {
					require.NoError(t, os.Mkdir(markerPath, 0o755))
				} else {
					require.NoError(t, os.WriteFile(markerPath, []byte(gitInRepo(t, repo, "rev-parse", "HEAD")+"\n"), 0o644))
				}
			}
			if tc.stagingFailure {
				require.NoError(t, os.WriteFile(filepath.Join(repo, ".git/index.lock"), nil, 0o600))
			}
			if tc.missingFile {
				require.NoError(t, os.Remove(path))
			}
			if tc.missingBlob {
				oid := gitInRepo(t, repo, "rev-parse", ":1:"+rel)
				require.NoError(t, os.Remove(filepath.Join(repo, ".git/objects", oid[:2], oid[2:])))
			}
			var resolved bool
			err = WithRepoLock(context.Background(), repo, func() error {
				var err error
				resolved, err = ResolveAutostashConflicts(context.Background(), repo, []string{"sessions/"}, tc.deny)
				return err
			})
			if tc.stagingFailure {
				require.Error(t, err)
				assert.False(t, resolved)
				after, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				assert.JSONEq(t, merged, string(after))
				afterIndex, readErr := os.ReadFile(filepath.Join(repo, ".git/index"))
				require.NoError(t, readErr)
				assert.Equal(t, index, afterIndex)
				assert.NotEmpty(t, gitInRepo(t, repo, "ls-files", "--unmerged"))
				require.NoError(t, os.Remove(filepath.Join(repo, ".git/index.lock")))
				err = WithRepoLock(context.Background(), repo, func() error {
					var err error
					resolved, err = ResolveAutostashConflicts(context.Background(), repo, []string{"sessions/"}, tc.deny)
					return err
				})
			}
			if tc.wantError {
				require.Error(t, err)
				assert.False(t, resolved)
				if tc.missingFile {
					assert.ErrorContains(t, err, "missing or not a regular file")
				}
				if tc.missingBlob {
					assert.ErrorContains(t, err, "read conflict stage")
				}
				for file, original := range before {
					if tc.missingFile && file == path {
						assert.NoFileExists(t, file, "recovery must not recreate a removed file")
						continue
					}
					after, err := os.ReadFile(file)
					require.NoError(t, err)
					assert.Equal(t, original, after, file)
				}
				afterIndex, err := os.ReadFile(filepath.Join(repo, ".git/index"))
				require.NoError(t, err)
				assert.Equal(t, index, afterIndex)
			} else {
				require.NoError(t, err)
				assert.True(t, resolved)
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Contains(t, string(after), "9007199254740993")
				assert.JSONEq(t, merged, string(after))
				assert.Empty(t, gitInRepo(t, repo, "ls-files", "--unmerged"))
				assert.Equal(t, string(after), gitInRepo(t, repo, "show", ":"+rel)+"\n")
			}
			assert.Equal(t, stash, gitInRepo(t, repo, "rev-parse", "refs/stash"))
		})
	}
}

// An unreadable index must report failure; an already-resolved index is a no-op.
// Neither state may rewrite metadata or the index while trying to recover it.
func TestAutostashRecoveryWithoutReadableConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	for _, corrupt := range []bool{false, true} {
		name := "clean index"
		if corrupt {
			name = "corrupt index"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			gitInRepo(t, repo, "init", "-b", "main")
			path := filepath.Join(repo, "sessions/test/meta.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			original := []byte(`{"title":"Keep me"}` + "\n")
			require.NoError(t, os.WriteFile(path, original, 0o644))
			gitInRepo(t, repo, "add", "--sparse", ".")
			indexPath := filepath.Join(repo, ".git/index")
			if corrupt {
				require.NoError(t, os.WriteFile(indexPath, []byte("invalid index"), 0o644))
			}
			index, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			err = WithRepoLock(context.Background(), repo, func() error {
				resolved, err := ResolveAutostashConflicts(context.Background(), repo, []string{"sessions/"}, nil)
				assert.False(t, resolved)
				return err
			})
			if corrupt {
				require.ErrorContains(t, err, "inspect unmerged index")
			} else {
				require.NoError(t, err)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, original, after)
			afterIndex, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			assert.Equal(t, index, afterIndex)
		})
	}
}
