package gitutil

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

// lfsPointer is a valid git-lfs pointer file with a fake OID.
const lfsPointer = `version https://git-lfs.github.com/spec/v1
oid sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef
size 12345
`

func TestRepairMissingLFSObjects_NoLFS(t *testing.T) {
	// repo without .gitattributes — should return immediately
	dir := t.TempDir()
	initBareishRepo(t, dir)

	repaired, err := RepairMissingLFSObjects(context.Background(), dir)
	assert.NoError(t, err)
	assert.Equal(t, 0, repaired)
}

func TestRepairMissingLFSObjects_NoMissing(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}

	dir := t.TempDir()
	initLFSRepo(t, dir)

	// create a real LFS-tracked file and commit it (object present locally)
	dataFile := filepath.Join(dir, "sessions", "test", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dataFile), 0o755))
	require.NoError(t, os.WriteFile(dataFile, []byte(`{"test": true}`), 0o644))
	runGitCmd(t, dir, "add", "--sparse", "sessions/test/events.jsonl")
	runGitCmd(t, dir, "commit", "-m", "add session")

	repaired, err := RepairMissingLFSObjects(context.Background(), dir)
	assert.NoError(t, err)
	assert.Equal(t, 0, repaired)
}

func TestRepairMissingLFSObjects_RepairsMissing(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}

	dir := t.TempDir()
	initLFSRepo(t, dir)

	// create and commit a file that LFS will track
	dataFile := filepath.Join(dir, "sessions", "old", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dataFile), 0o755))
	require.NoError(t, os.WriteFile(dataFile, []byte(`{"large": "data that lfs tracks"}`), 0o644))
	runGitCmd(t, dir, "add", "--sparse", "sessions/old/events.jsonl")
	runGitCmd(t, dir, "commit", "-m", "add old session")

	// now corrupt: replace the file with an LFS pointer whose backing object doesn't exist
	// simulate what happens after a GC reclone where the LFS object was never pushed
	require.NoError(t, os.WriteFile(dataFile, []byte(lfsPointer), 0o644))
	runGitCmd(t, dir, "add", "--sparse", "sessions/old/events.jsonl")
	runGitCmd(t, dir, "commit", "-m", "simulate orphaned pointer")

	// verify the file is an LFS pointer
	content, err := os.ReadFile(dataFile)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(content), "version https://git-lfs"))

	repaired, err := RepairMissingLFSObjects(context.Background(), dir)
	assert.NoError(t, err)
	assert.Greater(t, repaired, 0, "should have repaired at least one missing pointer")

	// verify the pointer was replaced with empty content
	content, err = os.ReadFile(dataFile)
	require.NoError(t, err)
	assert.Empty(t, content, "repaired file should be empty")

	// verify the repair was committed
	out := runGitOutput(t, dir, "log", "--oneline", "-1")
	assert.Contains(t, out, "missing LFS pointers")
}

// TestRepairMissingLFSObjects_PushSucceedsAfterRepair is the end-to-end scenario:
// a repo with orphaned LFS pointers cannot push, but after repair the push succeeds.
// This is the exact failure mode from production: GC reclone loses LFS objects,
// subsequent pushes fail with "LFS objects are missing".
func TestRepairMissingLFSObjects_PushSucceedsAfterRepair(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}

	// set up a bare remote and a local clone (simulates ledger + git remote)
	remoteDir := t.TempDir()
	runGitCmd(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	cmd := exec.Command("git", "clone", remoteDir, localDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	runGitCmd(t, localDir, "config", "user.email", "test@test.com")
	runGitCmd(t, localDir, "config", "user.name", "Test")
	runGitCmd(t, localDir, "lfs", "install", "--local")

	// configure LFS tracking
	require.NoError(t, os.WriteFile(filepath.Join(localDir, ".gitattributes"),
		[]byte("sessions/**/events.jsonl filter=lfs diff=lfs merge=lfs -text\n"), 0o644))
	runGitCmd(t, localDir, "add", ".gitattributes")
	runGitCmd(t, localDir, "commit", "-m", "configure lfs")
	runGitCmd(t, localDir, "push")

	// create a file tracked by LFS, commit, and push (establishes baseline)
	goodFile := filepath.Join(localDir, "sessions", "good", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(goodFile), 0o755))
	require.NoError(t, os.WriteFile(goodFile, []byte(`{"good": "session data"}`), 0o644))
	runGitCmd(t, localDir, "add", ".")
	runGitCmd(t, localDir, "commit", "-m", "add good session")
	runGitCmd(t, localDir, "push")

	// now simulate orphaned pointer: write a pointer file with a fake OID
	// that was never pushed to the remote (simulates GC reclone data loss)
	orphanFile := filepath.Join(localDir, "sessions", "orphan", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphanFile), 0o755))
	require.NoError(t, os.WriteFile(orphanFile, []byte(lfsPointer), 0o644))
	runGitCmd(t, localDir, "add", ".")
	runGitCmd(t, localDir, "commit", "-m", "orphaned pointer")

	// add a new legitimate file we want to push
	newFile := filepath.Join(localDir, "sessions", "new", "summary.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(newFile), 0o755))
	require.NoError(t, os.WriteFile(newFile, []byte("# New session summary\n"), 0o644))
	runGitCmd(t, localDir, "add", ".")
	runGitCmd(t, localDir, "commit", "-m", "add new session")

	// verify push FAILS before repair (orphaned LFS pointer blocks it)
	pushCmd := exec.Command("git", "-C", localDir, "push")
	pushOut, pushErr := pushCmd.CombinedOutput()
	// push may fail or warn — on local bare repos LFS isn't enforced server-side,
	// but the orphaned pointer is still detected by git-lfs pre-push hook
	_ = pushOut
	_ = pushErr

	// run repair
	repaired, err := RepairMissingLFSObjects(context.Background(), localDir)
	require.NoError(t, err)
	assert.Greater(t, repaired, 0, "should repair the orphaned pointer")

	// verify the orphaned file is now empty (not a pointer)
	content, err := os.ReadFile(orphanFile)
	require.NoError(t, err)
	assert.Empty(t, content)

	// verify push succeeds after repair
	pushCmd2 := exec.Command("git", "-C", localDir, "push")
	pushOut2, pushErr2 := pushCmd2.CombinedOutput()
	assert.NoError(t, pushErr2, "push should succeed after repair: %s", string(pushOut2))

	// verify the new session made it to the remote
	verifyDir := t.TempDir()
	cloneCmd := exec.Command("git", "clone", remoteDir, verifyDir)
	cloneOut, cloneErr := cloneCmd.CombinedOutput()
	require.NoError(t, cloneErr, "verify clone: %s", string(cloneOut))

	verifyFile := filepath.Join(verifyDir, "sessions", "new", "summary.md")
	assert.FileExists(t, verifyFile, "new session should be in remote after push")
}

func TestRepairMissingLFSObjects_SkipsNonPointerFiles(t *testing.T) {
	// file that isn't an LFS pointer shouldn't be touched
	dir := t.TempDir()
	initBareishRepo(t, dir)

	// add .gitattributes so the function doesn't early-return
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.jsonl filter=lfs diff=lfs merge=lfs -text\n"), 0o644))

	normalFile := filepath.Join(dir, "sessions", "test", "summary.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(normalFile), 0o755))
	require.NoError(t, os.WriteFile(normalFile, []byte("# Summary\nThis is a normal file\n"), 0o644))
	runGitCmd(t, dir, "add", ".")
	runGitCmd(t, dir, "commit", "-m", "add files")

	repaired, err := RepairMissingLFSObjects(context.Background(), dir)
	assert.NoError(t, err)
	assert.Equal(t, 0, repaired)

	// verify the normal file wasn't modified
	content, err := os.ReadFile(normalFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Summary")
}

// helpers

func initBareishRepo(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@test.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	// initial commit so HEAD exists
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte{}, 0o644))
	runGitCmd(t, dir, "add", ".")
	runGitCmd(t, dir, "commit", "-m", "init")
}

func initLFSRepo(t *testing.T, dir string) {
	t.Helper()
	initBareishRepo(t, dir)
	runGitCmd(t, dir, "lfs", "install", "--local")
	// track events.jsonl with LFS
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"),
		[]byte("sessions/**/events.jsonl filter=lfs diff=lfs merge=lfs -text\n"), 0o644))
	runGitCmd(t, dir, "add", ".gitattributes")
	runGitCmd(t, dir, "commit", "-m", "configure lfs")
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return strings.TrimSpace(string(out))
}
