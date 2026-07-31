package adapterprotocol

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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
// contract permits absolute paths. ox drops them at the staging boundary
// rather than the adapter having to know it can't stage them.
func TestRepoRelativePaths_OutsideRepoStaysAbsolute(t *testing.T) {
	repo := filepath.FromSlash("/tmp/repo")
	outside := filepath.FromSlash("/home/someone/.codex/hooks.json")

	got := RepoRelativePaths(repo, filepath.Join(repo, ".codex"), []string{outside})

	// filepath.Rel can express this as ../.., which is still a valid
	// answer; what matters is that it round-trips back to the same file.
	assert.Len(t, got, 1)
	resolved := got[0]
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(repo, resolved)
	}
	assert.Equal(t, outside, filepath.Clean(resolved),
		"the entry must still identify the same file, wherever it lives")
}
