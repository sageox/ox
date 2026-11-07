package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: `ox agent prime` kept printing "Daemon has issues ...
// Clone failed" for a team context that `ox doctor --fix` had already cloned,
// because only the daemon's own clone path cleared the clone_failed issue and a
// successful pull never did.
func TestPullTeamContext_ClearsStaleCloneFailedIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and pull")
	}
	isolateCredentials(t)
	s := newTestScheduler(t.TempDir())
	s.issues = NewIssueTracker()
	teamDir := filepath.Join(t.TempDir(), "team_repaired")

	bare, writer := initBareRepo(t, "team")
	require.NoError(t, os.WriteFile(filepath.Join(writer, "TEAM.md"), []byte("team\n"), 0o644))
	gitInDir(t, writer, "add", "TEAM.md")
	gitInDir(t, writer, "commit", "-m", "seed")
	gitInDir(t, writer, "push", "origin", "main")
	gitInDir(t, filepath.Dir(teamDir), "clone", bare, teamDir)

	s.issues.SetIssue(DaemonIssue{Type: IssueTypeCloneFailed, Severity: SeverityError, Repo: "team_repaired", Summary: "Clone failed for team-context"})
	// negative control: another team's clone failure must survive this pull
	s.issues.SetIssue(DaemonIssue{Type: IssueTypeCloneFailed, Severity: SeverityError, Repo: "team_other", Summary: "Clone failed for team-context"})

	_, err := s.pullTeamContext(context.Background(), teamDir)
	require.NoError(t, err)

	_, stale := s.issues.GetIssue(IssueTypeCloneFailed, "team_repaired")
	assert.False(t, stale, "a team context that pulls must not keep a clone_failed issue")
	_, other := s.issues.GetIssue(IssueTypeCloneFailed, "team_other")
	assert.True(t, other, "an unrelated team's clone failure must remain")
}
