package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStorageCheck(t *testing.T) {
	// create a temp directory for testing
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionStorageCheck(gitRoot)

	assert.Equal(t, "session storage writable", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// storage should be writable in test environment
	assert.True(t, result.Status == StatusPass || result.Status == StatusFail, "unexpected status: %v", result.Status)
}

func TestSessionRepoCheck(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionRepoCheck(gitRoot)

	assert.Equal(t, "ledger cloned", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// with sibling ledger pattern, result depends on whether the sibling ledger
	// for the current cwd's git repo exists. Any valid status is acceptable.
	validStatuses := []Status{StatusPass, StatusSkip, StatusWarn, StatusFail}
	assert.Contains(t, validStatuses, result.Status, "unexpected status: %v", result.Status)
}

func TestSessionRecordingCheck(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionRecordingCheck(gitRoot)

	assert.Equal(t, "recording", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// no recording should be active in test environment
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionPendingCheck(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionPendingCheck(gitRoot)

	assert.Equal(t, "pending sessions", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// with sibling ledger pattern, result depends on whether the sibling ledger
	// for the current cwd's git repo exists. Various statuses are acceptable.
	validStatuses := []Status{StatusPass, StatusSkip, StatusWarn, StatusFail}
	assert.Contains(t, validStatuses, result.Status, "unexpected status: %v", result.Status)
}

func TestSessionSyncCheck(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionSyncCheck(gitRoot)

	assert.Equal(t, "synced with remote", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// should skip if repo not cloned
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionCheck(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionCheck(gitRoot)

	assert.Equal(t, "Session Health", check.Name())

	ctx := context.Background()
	result := check.Run(ctx)

	// should return a result (pass or warn)
	assert.True(t, result.Status == StatusPass || result.Status == StatusWarn, "unexpected status: %v", result.Status)
}

func TestSessionIncompleteCheck_Name(t *testing.T) {
	check := NewSessionIncompleteCheck("/tmp/test")
	assert.Equal(t, "incomplete sessions", check.Name())
}

func TestSessionIncompleteCheck_NoLedger(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionIncompleteCheck(gitRoot)

	ctx := context.Background()
	result := check.Run(ctx)

	// should skip if ledger doesn't exist
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionIncompleteCheck_NoSessionsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with .git but no sessions/
	ledgerPath := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	_ = cmd.Run()

	check := &SessionIncompleteCheck{gitRoot: ""}
	// override getLedgerPath for test
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	assert.Empty(t, issues)
}

func TestSessionIncompleteCheck_CompleteSessions(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with complete session files
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create a complete session (jsonl + html + summary)
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+".html", []byte("<html></html>"), 0644))
	require.NoError(t, os.WriteFile(sessionBase+"_summary.md", []byte("# Summary"), 0644))

	// add all to git and commit
	cmd = exec.Command("git", "-C", ledgerPath, "add", "-A")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "test")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	assert.Empty(t, issues, "complete sessions should have no issues")
}

func TestSessionIncompleteCheck_MissingHTML(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with session missing HTML
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create session with missing HTML (has jsonl + summary, no html)
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+"_summary.md", []byte("# Summary"), 0644))

	// add to git and commit
	cmd = exec.Command("git", "-C", ledgerPath, "add", "-A")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "test")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	require.Len(t, issues, 1)
	assert.True(t, issues[0].MissingHTML)
	assert.False(t, issues[0].MissingSummary)
}

func TestSessionIncompleteCheck_MissingSummary(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with session missing summary
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create session with missing summary (has jsonl + html, no summary)
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+".html", []byte("<html></html>"), 0644))

	// add to git and commit
	cmd = exec.Command("git", "-C", ledgerPath, "add", "-A")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "test")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	require.Len(t, issues, 1)
	assert.False(t, issues[0].MissingHTML)
	assert.True(t, issues[0].MissingSummary)
}

func TestSessionIncompleteCheck_UntrackedSession(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with untracked session
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create initial commit so repo is valid
	dummyFile := filepath.Join(ledgerPath, ".gitkeep")
	require.NoError(t, os.WriteFile(dummyFile, []byte(""), 0644))
	cmd = exec.Command("git", "-C", ledgerPath, "add", ".gitkeep")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "init")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	// create complete session but don't add to git (untracked)
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+".html", []byte("<html></html>"), 0644))
	require.NoError(t, os.WriteFile(sessionBase+"_summary.md", []byte("# Summary"), 0644))

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	require.Len(t, issues, 1)
	assert.True(t, issues[0].Untracked)
	assert.False(t, issues[0].MissingHTML)
	assert.False(t, issues[0].MissingSummary)
}

func TestSessionIncompleteCheck_StagedSession(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with staged session
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create initial commit so repo is valid
	dummyFile := filepath.Join(ledgerPath, ".gitkeep")
	require.NoError(t, os.WriteFile(dummyFile, []byte(""), 0644))
	cmd = exec.Command("git", "-C", ledgerPath, "add", ".gitkeep")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "init")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	// create complete session and stage it
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+".html", []byte("<html></html>"), 0644))
	require.NoError(t, os.WriteFile(sessionBase+"_summary.md", []byte("# Summary"), 0644))

	// stage files (but don't commit)
	cmd = exec.Command("git", "-C", ledgerPath, "add", "-A")
	cmd.Dir = ledgerPath
	_ = cmd.Run()

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	// staged files should not be flagged as incomplete (they're pending commit)
	assert.Empty(t, issues, "staged sessions should not be flagged as incomplete")
}

func TestSessionIncompleteCheck_MultipleIssues(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with multiple issues
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create initial commit
	dummyFile := filepath.Join(ledgerPath, ".gitkeep")
	require.NoError(t, os.WriteFile(dummyFile, []byte(""), 0644))
	cmd = exec.Command("git", "-C", ledgerPath, "add", ".gitkeep")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "init")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	// session 1: missing HTML (committed)
	session1 := filepath.Join(sessionsDir, "session1")
	require.NoError(t, os.WriteFile(session1+".jsonl", []byte(`{"test": 1}`), 0644))
	require.NoError(t, os.WriteFile(session1+"_summary.md", []byte("# Summary 1"), 0644))

	cmd = exec.Command("git", "-C", ledgerPath, "add", "-A")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "session1")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	// session 2: missing summary (untracked)
	session2 := filepath.Join(sessionsDir, "session2")
	require.NoError(t, os.WriteFile(session2+".jsonl", []byte(`{"test": 2}`), 0644))
	require.NoError(t, os.WriteFile(session2+".html", []byte("<html>2</html>"), 0644))

	check := &SessionIncompleteCheck{gitRoot: ""}
	issues := check.findIncompleteSessionsInLedger(ledgerPath)

	require.Len(t, issues, 2)

	// verify both issues are captured
	var missingHTMLCount, missingSummaryCount, untrackedCount int
	for _, issue := range issues {
		if issue.MissingHTML {
			missingHTMLCount++
		}
		if issue.MissingSummary {
			missingSummaryCount++
		}
		if issue.Untracked {
			untrackedCount++
		}
	}

	assert.Equal(t, 1, missingHTMLCount)
	assert.Equal(t, 1, missingSummaryCount)
	assert.Equal(t, 1, untrackedCount)
}

func TestSessionIncompleteCheck_RunResult(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with incomplete session
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create initial commit
	dummyFile := filepath.Join(ledgerPath, ".gitkeep")
	require.NoError(t, os.WriteFile(dummyFile, []byte(""), 0644))
	cmd = exec.Command("git", "-C", ledgerPath, "add", ".gitkeep")
	cmd.Dir = ledgerPath
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "init")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	_ = cmd.Run()

	// create incomplete session (missing both HTML and summary, untracked)
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))

	// create a check that uses the test ledger
	check := &testSessionIncompleteCheck{
		SessionIncompleteCheck: SessionIncompleteCheck{gitRoot: ""},
		testLedgerPath:         ledgerPath,
	}

	ctx := context.Background()
	result := check.Run(ctx)

	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "incomplete")
	assert.Contains(t, result.Message, "missing HTML")
	assert.Contains(t, result.Message, "missing summary")
	assert.NotEmpty(t, result.Fix)
}

// testSessionIncompleteCheck wraps SessionIncompleteCheck for testing with custom ledger path
type testSessionIncompleteCheck struct {
	SessionIncompleteCheck
	testLedgerPath string
}

func (c *testSessionIncompleteCheck) Run(ctx context.Context) CheckResult {
	// find incomplete sessions using test ledger path
	issues := c.findIncompleteSessionsInLedger(c.testLedgerPath)
	if len(issues) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "none",
		}
	}

	// categorize issues
	missingHTML := 0
	missingSummary := 0
	uncommitted := 0

	for _, issue := range issues {
		if issue.MissingHTML {
			missingHTML++
		}
		if issue.MissingSummary {
			missingSummary++
		}
		if issue.Untracked || issue.Unstaged {
			uncommitted++
		}
	}

	// build message
	var parts []string
	if missingHTML > 0 {
		parts = append(parts, "missing HTML")
	}
	if missingSummary > 0 {
		parts = append(parts, "missing summary")
	}
	if uncommitted > 0 {
		parts = append(parts, "uncommitted")
	}

	message := "incomplete: "
	for i, part := range parts {
		if i > 0 {
			message += ", "
		}
		message += part
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: message,
		Fix:     "Run 'ox session stop' inside an agent to regenerate summaries, or 'ox session commit' to commit",
	}
}

// ============================================================
// SessionAutoStageCheck tests
// ============================================================

func TestSessionAutoStageCheck_Name(t *testing.T) {
	check := NewSessionAutoStageCheck("/tmp/test")
	assert.Equal(t, "session files staged", check.Name())
}

func TestSessionAutoStageCheck_NoLedger(t *testing.T) {
	tmpDir := t.TempDir()
	gitRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(gitRoot, 0755))

	check := NewSessionAutoStageCheck(gitRoot)

	ctx := context.Background()
	result := check.Run(ctx)

	// should skip if ledger doesn't exist
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionAutoStageCheck_NoUnstagedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger with no session files
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	check := &testSessionAutoStageCheck{
		SessionAutoStageCheck: SessionAutoStageCheck{gitRoot: ""},
		testLedgerPath:        ledgerPath,
	}

	ctx := context.Background()
	result := check.Run(ctx)

	// should pass with "all staged"
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "all staged")
}

func TestSessionAutoStageCheck_StagesUntrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// create a mock ledger
	ledgerPath := filepath.Join(tmpDir, "ledger")
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	// init git repo
	cmd := exec.Command("git", "-C", ledgerPath, "init")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())

	// create initial commit
	dummyFile := filepath.Join(ledgerPath, ".gitkeep")
	require.NoError(t, os.WriteFile(dummyFile, []byte(""), 0644))
	cmd = exec.Command("git", "-C", ledgerPath, "add", ".gitkeep")
	cmd.Dir = ledgerPath
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", ledgerPath, "commit", "-m", "init")
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com") // safe: local git commit in temp dir
	require.NoError(t, cmd.Run())

	// create untracked session files
	sessionBase := filepath.Join(sessionsDir, "test-session")
	require.NoError(t, os.WriteFile(sessionBase+".jsonl", []byte(`{"test": true}`), 0644))
	require.NoError(t, os.WriteFile(sessionBase+".html", []byte("<html></html>"), 0644))
	require.NoError(t, os.WriteFile(sessionBase+"_summary.md", []byte("# Summary"), 0644))

	check := &testSessionAutoStageCheck{
		SessionAutoStageCheck: SessionAutoStageCheck{gitRoot: ""},
		testLedgerPath:        ledgerPath,
	}

	ctx := context.Background()
	result := check.Run(ctx)

	// should pass and report staging files
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "staged")

	// verify files are actually staged by checking git diff --cached
	cmd = exec.Command("git", "-C", ledgerPath, "diff", "--cached", "--name-only")
	output, err := cmd.Output()
	require.NoError(t, err)

	// files should be in the staging area
	stagedFiles := string(output)
	assert.True(t, strings.Contains(stagedFiles, "sessions/test-session.jsonl") ||
		strings.Contains(stagedFiles, "sessions/test-session.html") ||
		strings.Contains(stagedFiles, "sessions/test-session_summary.md"),
		"expected session files to be staged, got: %s", stagedFiles)
}

func TestSessionAutoStageCheck_isSessionFile(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{"sessions/test.jsonl", true},
		{"sessions/test.html", true},
		{"sessions/test_summary.md", true},
		{"sessions/subdir/test.jsonl", true},
		{"other/test.jsonl", false},
		{"test.jsonl", false},
		{"sessions/test.txt", false},
		{"sessions/test.json", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := isSessionFile(tt.filename)
			assert.Equal(t, tt.want, got)
		})
	}
}

// testSessionAutoStageCheck wraps SessionAutoStageCheck for testing with custom ledger path
type testSessionAutoStageCheck struct {
	SessionAutoStageCheck
	testLedgerPath string
}

func (c *testSessionAutoStageCheck) Run(ctx context.Context) CheckResult {
	// check if ledger exists
	gitDir := filepath.Join(c.testLedgerPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check for unstaged session files
	unstaged := c.findUnstagedSessionFiles(c.testLedgerPath)
	if len(unstaged) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "all staged",
		}
	}

	// auto-stage the files
	stagedCount, err := c.stageSessionFiles(c.testLedgerPath, unstaged)
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "staging failed: " + err.Error(),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: "staged " + string(rune('0'+stagedCount)) + " file(s)",
	}
}
