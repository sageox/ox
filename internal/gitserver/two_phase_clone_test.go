package gitserver

import (
	"os"
	"path/filepath"
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
