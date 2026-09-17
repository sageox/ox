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

// TestAutostashRecoveryMergesBookkeepingCounters pins the field-level policy
// from #956: summary_attempts is ox's own monotonic retry counter, written
// daemon-side on BOTH sides of a pull --autostash, so it is the one conflict
// class ox generates against itself — and before the policy it was the one
// class auto-resolve refused, leaving an unmerged index that wedged ledger sync
// until a human ran git by hand in the internal clone.
//
// The refuse cases matter as much as the merge cases. The blanket refusal is
// what keeps a genuine content field from being silently half-discarded, so
// every case below that is NOT summary_attempts-shaped must still leave the
// worktree and index byte-identical.
func TestAutostashRecoveryMergesBookkeepingCounters(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git stash conflicts")
	}
	for _, tc := range []struct {
		name string
		base string
		// upstream lands as index stage 2 ("Updated upstream"); local is
		// stashed and returns as stage 3 ("Stashed changes").
		upstream   string
		local      string
		path       string
		deny       []string
		wantMerged string
		// wantLiteral guards the re-encode: a counter that round-trips through
		// float64 would come back as 2e+00 and stop being a counter.
		wantLiteral string
		wantErr     string
	}{
		{
			name:        "counter 2 upstream vs 1 stashed resolves to the max",
			base:        `{"title":"Ready","summary_attempts":0}`,
			upstream:    `{"title":"Ready","summary_attempts":2}`,
			local:       `{"title":"Ready","summary_attempts":1}`,
			wantMerged:  `{"title":"Ready","summary_attempts":2}`,
			wantLiteral: `"summary_attempts": 2`,
		},
		{
			name:        "counter 3 upstream vs 2 stashed resolves to the max",
			base:        `{"title":"Ready","summary_attempts":0}`,
			upstream:    `{"title":"Ready","summary_attempts":3}`,
			local:       `{"title":"Ready","summary_attempts":2}`,
			wantMerged:  `{"title":"Ready","summary_attempts":3}`,
			wantLiteral: `"summary_attempts": 3`,
		},
		{
			// max, not "prefer ours": the stashed side can legitimately hold the
			// higher count when the local daemon retried after the fetch.
			name:        "stashed side wins when it holds the higher count",
			base:        `{"title":"Ready","summary_attempts":0}`,
			upstream:    `{"title":"Ready","summary_attempts":1}`,
			local:       `{"title":"Ready","summary_attempts":4}`,
			wantMerged:  `{"title":"Ready","summary_attempts":4}`,
			wantLiteral: `"summary_attempts": 4`,
		},
		{
			// The accepted undercount, pinned as understood behavior rather
			// than left to be rediscovered. Divergent clones make DISTINCT
			// attempts, so base 1 + one bump upstream + two bumps locally is
			// four attempts and max records three. sum (5) or the three-way
			// delta ours+theirs-base (4) would each fail here — which is the
			// point: swapping the rule is a policy change with a cost
			// (overcounting flips a live session to "unrecoverable" early),
			// not a bug fix. See sessionMetaBookkeepingMerges.
			name:        "divergent clones undercount: max is a lower bound, not the total",
			base:        `{"title":"Ready","summary_attempts":1}`,
			upstream:    `{"title":"Ready","summary_attempts":2}`,
			local:       `{"title":"Ready","summary_attempts":3}`,
			wantMerged:  `{"title":"Ready","summary_attempts":3}`,
			wantLiteral: `"summary_attempts": 3`,
		},
		{
			name:        "identical counters still merge the surrounding union",
			base:        `{"title":"","summary_attempts":0}`,
			upstream:    `{"title":"Ready","summary_attempts":2,"cloud_only":true}`,
			local:       `{"title":"Ready","summary_attempts":2,"local_only":true}`,
			wantMerged:  `{"title":"Ready","summary_attempts":2,"cloud_only":true,"local_only":true}`,
			wantLiteral: `"summary_attempts": 2`,
		},
		{
			// The policy resolves the counter but must not rescue the set: one
			// unmergeable field still refuses the whole path.
			name:     "counter alongside a differing content field still refuses",
			base:     `{"summary":"","summary_attempts":0}`,
			upstream: `{"summary":"upstream body","summary_attempts":2}`,
			local:    `{"summary":"local body","summary_attempts":1}`,
			wantErr:  "field summary differs",
		},
		{
			// The RESET shape, and the reason max is safe here at all.
			// summary_attempts is NOT globally monotonic: a successful
			// summarization sets it back to 0 (session_finalize.go), as do
			// RecoverEmptyTitleMeta and ResetInlineSummaryEligible. Across a
			// reset max is actively WRONG — it would resurrect the stale 3 over
			// the newer 0 and re-trip MaxSummaryAttempts a try early.
			//
			// What prevents that is not the counter rule but the fact that
			// every resetting writer also writes summary_status, which has no
			// merge rule and refuses the whole path first. Writers that BUMP
			// the counter alone already exist and are fine; a writer that
			// RESETS it alone would leave this case passing while the rule
			// silently becomes unsound — so it is the canary for that change,
			// not a guarantee against it.
			name:     "a counter RESET paired with its status write still refuses",
			base:     `{"summary_status":"pending","summary_attempts":0}`,
			upstream: `{"summary_status":"unrecoverable","summary_attempts":3}`,
			local:    `{"summary_status":"ok","summary_attempts":0}`,
			wantErr:  "field summary_status differs",
		},
		{
			name:     "differing validation_error still refuses",
			base:     `{"validation_error":"","summary_attempts":0}`,
			upstream: `{"validation_error":"missing transcript","summary_attempts":2}`,
			local:    `{"validation_error":"model timeout","summary_attempts":1}`,
			wantErr:  "field validation_error differs",
		},
		{
			// Deliberately unhandled: summary_status is a state label with no
			// total order, so no rule picks between these without inventing one.
			name:     "differing summary_status still refuses",
			base:     `{"summary_status":"pending","summary_attempts":0}`,
			upstream: `{"summary_status":"failed","summary_attempts":2}`,
			local:    `{"summary_status":"complete","summary_attempts":1}`,
			wantErr:  "field summary_status differs",
		},
		{
			name:     "non-integer counter is not a counter this rule understands",
			base:     `{"title":"Ready","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2.5}`,
			local:    `{"title":"Ready","summary_attempts":1}`,
			wantErr:  "field summary_attempts differs",
		},
		{
			// The mirror of the case above. mergeMonotonicCounter validates BOTH
			// sides, but every other bad-value case here spoils whichever side
			// is read first, so the second guard had never executed — the rule
			// was only ever proven to reject a bad ours, not a bad theirs.
			// Asymmetric validation is exactly the kind of gap that survives a
			// green suite, so this pins the other half.
			name:     "a non-integer counter on the OTHER side refuses too",
			base:     `{"title":"Ready","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2}`,
			local:    `{"title":"Ready","summary_attempts":1.5}`,
			wantErr:  "field summary_attempts differs",
		},
		{
			name:     "string-typed counter refuses rather than guessing",
			base:     `{"title":"Ready","summary_attempts":"0"}`,
			upstream: `{"title":"Ready","summary_attempts":"2"}`,
			local:    `{"title":"Ready","summary_attempts":"1"}`,
			wantErr:  "field summary_attempts differs",
		},
		{
			name:     "deleted counter refuses before any merge rule applies",
			base:     `{"title":"","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2}`,
			local:    `{"title":"Ready"}`,
			wantErr:  "was deleted",
		},
		{
			// Scope check: the policy rides inside the sessions/<name>/meta.json
			// guard, so a denied prefix keeps refusing even for a mergeable field.
			name:     "denied prefix refuses a mergeable counter",
			base:     `{"title":"Ready","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2}`,
			local:    `{"title":"Ready","summary_attempts":1}`,
			deny:     []string{"sessions/test/"},
			wantErr:  "requires manual resolution",
		},
		{
			name:     "other session artifact refuses a mergeable counter",
			base:     `{"title":"Ready","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2}`,
			local:    `{"title":"Ready","summary_attempts":1}`,
			path:     "sessions/test/summary.json",
			wantErr:  "requires manual resolution",
		},
		{
			name:     "path outside sessions refuses a mergeable counter",
			base:     `{"title":"Ready","summary_attempts":0}`,
			upstream: `{"title":"Ready","summary_attempts":2}`,
			local:    `{"title":"Ready","summary_attempts":1}`,
			path:     "data/test/meta.json",
			wantErr:  "requires manual resolution",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := tc.path
			if rel == "" {
				rel = "sessions/test/meta.json"
			}
			repo, path := seedAutostashConflict(t, rel, tc.base, tc.upstream, tc.local)
			indexPath := filepath.Join(repo, ".git/index")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			index, err := os.ReadFile(indexPath)
			require.NoError(t, err)

			var resolved bool
			err = WithRepoLock(context.Background(), repo, func() error {
				var err error
				resolved, err = ResolveAutostashConflicts(context.Background(), repo, []string{"sessions/"}, tc.deny)
				return err
			})

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.False(t, resolved)
				after, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				assert.Equal(t, before, after, "refused repair must not rewrite the worktree")
				afterIndex, readErr := os.ReadFile(indexPath)
				require.NoError(t, readErr)
				assert.Equal(t, index, afterIndex, "refused repair must not touch the index")
				return
			}

			require.NoError(t, err)
			assert.True(t, resolved)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.JSONEq(t, tc.wantMerged, string(after))
			assert.Contains(t, string(after), tc.wantLiteral, "counter must re-encode as an integer literal")
			assert.Empty(t, gitInRepo(t, repo, "ls-files", "--unmerged"))
			assert.Equal(t, string(after), gitInRepo(t, repo, "show", ":"+rel)+"\n")
		})
	}
}

// seedAutostashConflict reproduces the index state pull --autostash leaves
// behind: base is committed, local is stashed, upstream is committed on top,
// and applying the stash conflicts. Stage 2 therefore holds upstream ("Updated
// upstream") and stage 3 holds local ("Stashed changes").
func seedAutostashConflict(t *testing.T, rel, base, upstream, local string) (repo, path string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	path = filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(base+"\n"), 0o644))
	gitInRepo(t, repo, "add", "--sparse", ".")
	gitInRepo(t, repo, "commit", "-m", "base")
	require.NoError(t, os.WriteFile(path, []byte(local+"\n"), 0o644))
	gitInRepo(t, repo, "stash", "push", "-m", "autostash")
	require.NoError(t, os.WriteFile(path, []byte(upstream+"\n"), 0o644))
	gitInRepo(t, repo, "commit", "-am", "upstream")
	cmd := exec.Command("git", "stash", "apply")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "fixture must conflict: %s", out)
	return repo, path
}
