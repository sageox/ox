package adapterprotocol

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoRelativePaths(t *testing.T) {
	repo := filepath.FromSlash("/tmp/repo")

	tests := []struct {
		name    string
		baseDir string
		names   []string
		want    []string
		why     string
	}{
		{
			name:    "rules-dir-relative names become repo-relative",
			baseDir: filepath.Join(repo, ".claude", "rules"),
			names:   []string{"ox.md", filepath.FromSlash("sageox/use-team-context.md")},
			want: []string{
				filepath.FromSlash(".claude/rules/ox.md"),
				filepath.FromSlash(".claude/rules/sageox/use-team-context.md"),
			},
			why: "this is the exact conversion agentx's rules manager needs (GH #731)",
		},
		{
			name:    "skills-dir-relative names become repo-relative",
			baseDir: filepath.Join(repo, ".claude", "skills"),
			names:   []string{filepath.FromSlash("ox-plan/SKILL.md")},
			want:    []string{filepath.FromSlash(".claude/skills/ox-plan/SKILL.md")},
			why:     "reporting ox-plan/SKILL.md made ox look for it at the repo root",
		},
		{
			name:    "already-absolute names are re-expressed, not re-joined",
			baseDir: filepath.Join(repo, ".claude", "skills"),
			names:   []string{filepath.Join(repo, ".claude", "settings.json")},
			want:    []string{filepath.FromSlash(".claude/settings.json")},
			why:     "an adapter mixing conventions in one list must still produce sane output",
		},
		{
			name:    "relative baseDir is resolved against repoRoot",
			baseDir: filepath.FromSlash(".claude/rules"),
			names:   []string{"ox.md"},
			want:    []string{filepath.FromSlash(".claude/rules/ox.md")},
			why:     "callers should not have to pre-absolutize",
		},
		{
			name:    "empty input yields empty output",
			baseDir: filepath.Join(repo, ".claude"),
			names:   nil,
			want:    []string{},
			why:     "an install that wrote nothing is normal, not an error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RepoRelativePaths(repo, tt.baseDir, tt.names), tt.why)
		})
	}
}

// TestRepoRelativePaths_OutsideRepoStaysAbsolute documents the escape
// hatch: user-scope installs genuinely live outside the repo, and the
// contract permits absolute paths.
//
// It must stay ABSOLUTE rather than degrade to a `../..` traversal — the
// absolute form is unambiguous from any working directory, while a
// traversal only means the right thing relative to repoRoot.
func TestRepoRelativePaths_OutsideRepoStaysAbsolute(t *testing.T) {
	repo := filepath.FromSlash("/tmp/repo")
	outside := filepath.FromSlash("/home/someone/.codex/hooks.json")

	got := RepoRelativePaths(repo, filepath.Join(repo, ".codex"), []string{outside})

	require.Len(t, got, 1)
	assert.True(t, filepath.IsAbs(got[0]), "must not become a ../.. traversal; got %q", got[0])
	assert.Equal(t, outside, got[0])
}

// TestRepoRelativePaths_SiblingOfRepoStaysAbsolute covers the near-miss:
// a path that shares a parent with the repo resolves to a short `../x`
// traversal, which is exactly the case a naive filepath.Rel accepts.
func TestRepoRelativePaths_SiblingOfRepoStaysAbsolute(t *testing.T) {
	repo := filepath.FromSlash("/tmp/repo")
	sibling := filepath.FromSlash("/tmp/other/hooks.json")

	got := RepoRelativePaths(repo, repo, []string{sibling})

	require.Len(t, got, 1)
	assert.True(t, filepath.IsAbs(got[0]), "got %q", got[0])
	assert.Equal(t, sibling, got[0])
}
