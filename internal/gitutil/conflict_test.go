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
				for file, original := range before {
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
