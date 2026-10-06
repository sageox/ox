package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/errkind"
)

// TestRunInit_StoppingEarlyFailsAndSaysWhy drives ox init, from a home that has
// never logged in, into each way it stops before initializing anything.
//
// Failure prevented: an ox init that never logged in exiting 0, so
// `ox init && git add .sageox` carried on and the usage dashboard counted a
// repository initialized; and these stops reaching usage telemetry as
// error_kind=other with no detail.
func TestRunInit_StoppingEarlyFailsAndSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string // SAGEOX_ENDPOINT; empty is read as unset
		repo     func(t *testing.T) string
		kind     errkind.Kind
		detail   string
		says     []string // what the person reads
	}{
		{
			// No one there to choose, as for an AI coworker or a script: the
			// endpoint picker takes its only offer, which has no login.
			name:   "never logged in, endpoint picked for them",
			repo:   createTestGitRepo,
			kind:   errkind.NotLoggedIn,
			detail: "ox init requires login to the selected endpoint",
			says: []string{"Authentication required",
				"You need to login to " + endpoint.NormalizeSlug(endpoint.Default) + " first.", "ox login"},
		},
		{
			name:     "endpoint set, never logged in",
			endpoint: "http://127.0.0.1:9", // loopback discard port: nothing answers
			repo:     createTestGitRepo,
			kind:     errkind.NotLoggedIn,
			detail:   "ox init requires authentication",
			says: []string{"Authentication required",
				"ox init requires authentication to associate this repository with your team.", "ox login"},
		},
		{
			name:     "a teammate already initialized it",
			endpoint: "http://127.0.0.1:9",
			repo:     repoATeammateAlreadyInitialized,
			kind:     errkind.Usage,
			detail:   "already initialized on the remote",
			says:     []string{"This repo is already initialized on the remote", "git pull"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateInitFromLogin(t, tc.endpoint)
			repo := tc.repo(t)
			t.Chdir(repo)

			var err error
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdoutForPlanCLI(t, func() {
					withStdin(t, "", func() { err = runInit() })
				})
			})

			// A failure, so ox exits non-zero; silent, because ox has already
			// explained it; and usage telemetry learns why.
			assertFailureKind(t, err, tc.kind, tc.detail)
			assert.ErrorIs(t, err, cli.ErrSilent)
			for _, s := range tc.says {
				assert.Contains(t, stdout+stderr, s)
			}
			assert.NoDirExists(t, filepath.Join(repo, ".sageox"), "nothing was initialized")
		})
	}
}

// isolateInitFromLogin gives runInit a home with no login in it, SAGEOX_ENDPOINT
// set to ep, and the flags it reads before its login check at their defaults.
func isolateInitFromLogin(t *testing.T, ep string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv(auth.EnvVarToken, "")
	require.NoError(t, os.Unsetenv(auth.EnvVarToken)) // unset, not empty
	t.Setenv(endpoint.EnvVar, ep)

	prevForce, prevEndpoint := initForce, initEndpointFlag
	initForce, initEndpointFlag = false, ""
	t.Cleanup(func() { initForce, initEndpointFlag = prevForce, prevEndpoint })
}

// repoATeammateAlreadyInitialized returns a repository whose origin/main holds
// a teammate's .sageox/, fetched but not yet pulled.
func repoATeammateAlreadyInitialized(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "init", "-q", "--bare", remote)

	teammate := t.TempDir()
	initGitRepo(t, teammate)
	require.NoError(t, os.MkdirAll(filepath.Join(teammate, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teammate, ".sageox", "config.json"), []byte("{}\n"), 0o644))
	runGit(t, teammate, "add", ".sageox")
	runGit(t, teammate, "commit", "-q", "-m", "initialize SageOx")
	runGit(t, teammate, "push", "-q", remote, "HEAD:main")

	mine := createTestGitRepo(t)
	runGit(t, mine, "remote", "add", "origin", remote)
	runGit(t, mine, "fetch", "-q", "origin")
	return mine
}
