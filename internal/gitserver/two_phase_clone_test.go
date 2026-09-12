package gitserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPhaseOneCloneArgs_IncludesCredentialHelper locks in the fix for the
// team-context clone that prompted for a username non-interactively.
// Failure prevented: the phase-1 clone shells out with a bare URL and no
// credential helper, so git prompts for a username and EOFs in the daemon.
func TestPhaseOneCloneArgs_IncludesCredentialHelper(t *testing.T) {
	orig := DefaultHelperCommand()
	t.Cleanup(func() { SetHelperCommand(orig) })
	SetHelperCommand("!ox git-credential-helper")
	args := phaseOneCloneArgs("https://git.sageox.ai/team/ctx.git", "/tmp/ctx")

	// credential helper must be present: empty reset followed by the ox helper
	require.Contains(t, args, "credential.helper=")
	require.Contains(t, args, "credential.helper=!ox git-credential-helper")

	// the credential helper must precede the `clone` verb (config flags only
	// apply when they come before the subcommand)
	cloneIdx := indexOf(args, "clone")
	helperIdx := indexOf(args, "credential.helper=!ox git-credential-helper")
	require.GreaterOrEqual(t, cloneIdx, 0, "clone verb present")
	require.GreaterOrEqual(t, helperIdx, 0, "helper present")
	assert.Less(t, helperIdx, cloneIdx, "credential helper must come before clone verb")

	// partial-clone shape preserved
	assert.Contains(t, args, "--filter=blob:none")
	assert.Contains(t, args, "--depth=1")
	assert.Contains(t, args, "--no-checkout")

	// `--` terminates options; URL and path are the trailing positionals
	assert.Equal(t, "https://git.sageox.ai/team/ctx.git", args[len(args)-2])
	assert.Equal(t, "/tmp/ctx", args[len(args)-1])
	assert.Equal(t, "--", args[len(args)-3])

	// protocol hardening present by default
	assert.Contains(t, args, "protocol.ext.allow=never")
	assert.Contains(t, args, "protocol.file.allow=never")
}

// TestPhaseOneCloneArgs_FileTransportOverride verifies the test-only escape
// hatch drops the file:// hardening so local bare-repo clones work in tests.
func TestPhaseOneCloneArgs_FileTransportOverride(t *testing.T) {
	TestAllowFileTransport = true
	t.Cleanup(func() { TestAllowFileTransport = false })

	args := phaseOneCloneArgs("file:///tmp/bare.git", "/tmp/ctx")
	assert.NotContains(t, args, "protocol.file.allow=never",
		"file transport hardening must be dropped under the test override")
	// ext hardening always applies
	assert.Contains(t, args, "protocol.ext.allow=never")
}

// TestCloneHost verifies credential-helper scoping only targets https hosts.
// Failure prevented: installing a helper for file:// test clones (no host) or
// scoping it wrong so it fires for unrelated remotes.
func TestCloneHost(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		want     string
	}{
		{"https with path", "https://git.sageox.ai/team/ctx.git", "git.sageox.ai"},
		{"https with port", "https://git.sageox.ai:443/team/ctx.git", "git.sageox.ai"},
		{"file scheme has no host", "file:///tmp/bare.git", ""},
		{"ssh scheme ignored", "git@git.sageox.ai:team/ctx.git", ""},
		{"garbage", "::not-a-url::", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cloneHost(tt.cloneURL))
		})
	}
}

func indexOf(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}

func TestValidateTeamContextClone_CoreFilesPresent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# Soul"), 0644))

	// should not warn when at least one core file exists
	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_NoCoreFiles(t *testing.T) {
	dir := t.TempDir()
	// empty dir — warns but does not error
	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_WithMemoryDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "TEAM.md"), []byte("# Team"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "memory"), 0755))

	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_DeniedPathExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# Memory"), 0644))

	// create a path that should have been denied
	deniedDir := filepath.Join(dir, "secrets")
	require.NoError(t, os.MkdirAll(deniedDir, 0755))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"secrets/"},
	}

	// should warn about denied path but not error
	ValidateTeamContextClone(dir, cfg)

	// verify the path still exists (validation is read-only)
	_, err := os.Stat(deniedDir)
	assert.NoError(t, err, "validation should not remove denied paths")
}

func TestValidateTeamContextClone_DeniedPathNotPresent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# Soul"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"secrets/", "private/"},
	}

	// should not warn when denied paths don't exist
	ValidateTeamContextClone(dir, cfg)
}

// seedKBLikeRemote builds a bare repo (filter-capable) whose main carries a
// .sageox/sync.manifest and a Curator-style artifact under .sageox/curator/,
// the shape a server-provisioned Knowledge Bubble has.
func seedKBLikeRemote(t *testing.T) (bareDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	bareDir = filepath.Join(tmp, "kb.bare")
	workDir := filepath.Join(tmp, "kb.work")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	git(bareDir, "config", "uploadpack.allowfilter", "true")
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	git(workDir, "config", "user.name", "test")
	git(workDir, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".sageox", "curator", "marks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "sync.manifest"), []byte("version 1\ninclude .sageox/\ninclude README.md\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "curator", "marks", "a.json"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("v1\n"), 0o644))
	git(workDir, "add", ".")
	git(workDir, "commit", "-m", "seed")
	git(workDir, "push", "origin", "HEAD:main")
	return bareDir
}

// gitCheckIgnored asks git itself whether rel would be ignored in dir.
func gitCheckIgnored(t *testing.T, dir, rel string) bool {
	t.Helper()
	err := exec.Command("git", "-C", dir, "check-ignore", "-q", rel).Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", rel, err)
	return false
}

// TestTwoPhaseClone_KBKind_NoCommittedGitignore is the customer promise for
// Knowledge Bubbles: cloning a bubble never leaves a daemon-authored
// .sageox/.gitignore in the tree, never adds a local commit, and never makes
// git ignore a Curator artifact. Failure prevented: the `*` rule reaching a
// bubble's main so the server Curator's `git add -A` skips its own
// .sageox/curator/marks save-mark and re-drives synthesis every hour.
func TestTwoPhaseClone_KBKind_NoCommittedGitignore(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	TestAllowFileTransport = true
	t.Cleanup(func() { TestAllowFileTransport = false })

	bareDir := seedKBLikeRemote(t)
	target := filepath.Join(t.TempDir(), "kb_clone")
	_, err := TwoPhaseClone(context.Background(), "file://"+bareDir, target, manifest.RepoKindKB)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(target, ".sageox", ".gitignore"))
	assert.True(t, os.IsNotExist(statErr), "KB clone must not get a .sageox/.gitignore")

	localHead, err := exec.Command("git", "-C", target, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	remoteHead, err := exec.Command("git", "-C", bareDir, "rev-parse", "main").Output()
	require.NoError(t, err)
	assert.Equal(t, string(remoteHead), string(localHead), "KB clone must carry no daemon-authored commit")

	assert.False(t, gitCheckIgnored(t, target, ".sageox/curator/marks/new.json"),
		"a new Curator mark must not be ignored in a KB clone")
	assert.False(t, gitCheckIgnored(t, target, ".sageox/curator/synopses/s.md"),
		"a new Curator synopsis must not be ignored in a KB clone")
}

// TestTwoPhaseClone_TeamContextKind_StillCommitsGitignore pins the
// unchanged ledger/team-context behavior next to the KB case above, so a
// future edit cannot silently flip either kind. Same fixture, other kind:
// the committed file appears and the blanket rule applies.
func TestTwoPhaseClone_TeamContextKind_StillCommitsGitignore(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	TestAllowFileTransport = true
	t.Cleanup(func() { TestAllowFileTransport = false })

	bareDir := seedKBLikeRemote(t)
	target := filepath.Join(t.TempDir(), "tc_clone")
	_, err := TwoPhaseClone(context.Background(), "file://"+bareDir, target, manifest.RepoKindTeamContext)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(target, ".sageox", ".gitignore"))
	tracked, err := exec.Command("git", "-C", target, "ls-files", ".sageox/.gitignore").Output()
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(tracked)), "team-context clone commits .sageox/.gitignore")
	assert.True(t, gitCheckIgnored(t, target, ".sageox/cache/sync-state.json"),
		"team-context blanket rule still ignores daemon cache files")
}
