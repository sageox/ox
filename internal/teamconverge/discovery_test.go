package teamconverge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTeamFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func gitTeam(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func TestFilesystemDiscovery_ProducesTypedSnapshotInventory(t *testing.T) {
	team := t.TempDir()
	gitTeam(t, team, "init", "-q")
	gitTeam(t, team, "config", "user.email", "test@sageox.ai")
	gitTeam(t, team, "config", "user.name", "test")
	gitTeam(t, team, "config", "commit.gpgsign", "false")
	writeTeamFile(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: deploy safely\nrepos: [api]\n---\n\nDo it.\n")
	writeTeamFile(t, team, "agents/skills/mobile/SKILL.md", "---\nname: mobile\ndescription: mobile only\nrepos: [ios]\n---\n")
	writeTeamFile(t, team, "agents/rules/security.md", "---\nname: security\ndescription: secure defaults\nvisibility: always\n---\n\nNever log secrets.\n")
	writeTeamFile(t, team, "docs/architecture.md", "---\ntitle: Architecture\ndescription: System map\n---\n\n# Architecture\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "team context")
	commit := gitTeam(t, team, "rev-parse", "HEAD")

	discovery := FilesystemDiscovery{}
	snapshot, artifacts, err := discovery.Discover(context.Background(), Request{TeamPath: team, RepoSlug: "api"})
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(team), filepath.Clean(snapshot.Path))
	require.Equal(t, commit[:len(commit)-1], snapshot.Commit)
	require.Len(t, artifacts, 4)

	byKey := map[string]Artifact{}
	for _, artifact := range artifacts {
		byKey[string(artifact.Kind)+"/"+artifact.Name] = artifact
		require.NotEmpty(t, artifact.SourcePath)
		require.False(t, filepath.IsAbs(artifact.SourcePath))
	}
	require.True(t, byKey["skill/deploy"].Applicable)
	require.False(t, byKey["skill/mobile"].Applicable)
	require.Equal(t, OriginLoose, byKey["rule/security"].Origin.Kind)
	require.Equal(t, "always", byKey["rule/security"].Visibility)
	require.Equal(t, "docs/architecture.md", byKey["context/architecture.md"].SourcePath)
}

func TestFilesystemDiscovery_ReportsEachBoundaryFailure(t *testing.T) {
	discovery := FilesystemDiscovery{}
	_, _, err := discovery.Discover(context.Background(), Request{})
	require.ErrorContains(t, err, "path is required")

	_, _, err = discovery.Discover(context.Background(), Request{TeamPath: t.TempDir()})
	require.ErrorContains(t, err, "resolve Team Context commit")

	t.Run("skills", func(t *testing.T) {
		team := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(team, "agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(team, "agents", "skills"), []byte("not a directory"), 0o644))
		_, _, err := discovery.Discover(context.Background(), Request{TeamPath: team, TeamCommit: "abc"})
		require.ErrorContains(t, err, "discover Team Context skills")
	})

	t.Run("rules", func(t *testing.T) {
		team := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(team, "agents"), 0o755))
		if err := os.Symlink("rules", filepath.Join(team, "agents", "rules")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, _, err := discovery.Discover(context.Background(), Request{TeamPath: team, TeamCommit: "abc"})
		require.ErrorContains(t, err, "discover Team Context rules")
	})

	t.Run("docs", func(t *testing.T) {
		team := t.TempDir()
		if err := os.Symlink("docs", filepath.Join(team, "docs")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, _, err := discovery.Discover(context.Background(), Request{TeamPath: team, TeamCommit: "abc"})
		require.ErrorContains(t, err, "discover Team Context docs")
	})
}
