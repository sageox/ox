//go:build !short

package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const readestPublicKey = "948faf8132e02fda1774738168cc08164107b1193a39214e013bddc0b9cbaebb"

// runUninitializedPrime runs the real `ox agent prime` in the harness repo,
// which nobody ran ox init in, after pointing its origin at remote.
func runUninitializedPrime(t *testing.T, env *oxE2E, remote string) string {
	t.Helper()
	gitCmd := exec.Command("git", "remote", "add", "origin", remote)
	gitCmd.Dir = env.Root
	out, err := gitCmd.CombinedOutput()
	require.NoError(t, err, string(out))

	var buf bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	require.NoError(t, runAgentPrime(cmd, nil))
	return buf.String()
}

func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain", "--ignored")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// TestRunAgentPrime_PublicProjectPrimesAnyCoworker — Failure prevented: a
// contributor's AI coworker in a clone of an onboarded open-source project
// starts with nothing because they are not on the team, while the project
// page tells them to prime it.
func TestRunAgentPrime_PublicProjectPrimesAnyCoworker(t *testing.T) {
	require.Equal(t, readestPublicKey, api.PublicRepoKey("readest", "readest"))
	env := newOxE2E(t)
	var gotQuery, gotAuth string
	env.Handle("/api/v1/public/prime", func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotAuth = r.URL.RawQuery, r.Header.Get("Authorization")
		if r.URL.Query().Get("repo_sha256") != readestPublicKey {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := os.ReadFile(publicPrimeGoldenPath())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	})
	before := gitStatusPorcelain(t, env.Root)

	out := runUninitializedPrime(t, env, "git@github.com:Readest/readest.git")

	asOf := time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC).In(time.Local).Format("3:04 PM")
	assert.True(t, strings.HasPrefix(out, "<ox-prime mode=\"public\">\n"), out)
	assert.Contains(t, out, "<public-project>\nSageOx primed from Readest, public, as of "+asOf+"\n")
	assert.Contains(t, out, "set aside Dolt as the only remote\n")
	assert.NotContains(t, out, "Project not initialized")

	assert.Equal(t, "repo_sha256="+readestPublicKey, gotQuery, "the repo is named by its key, never its name")
	assert.Equal(t, "Bearer e2e-access-token", gotAuth, "a signed-in coworker primes as themselves")

	// read-only: nothing registered, nothing written into the clone
	assert.NotContains(t, env.Requested(), "/api/v1/repo/init")
	_, err := os.Stat(filepath.Join(env.Root, ".sageox"))
	assert.True(t, os.IsNotExist(err), "public priming must never initialize the repo")
	assert.Equal(t, before, gitStatusPorcelain(t, env.Root), "the clone's working tree is untouched")
}

// TestRunAgentPrime_RepoNotOnSageOxKeepsTheUsualAnswer — Failure prevented:
// every ordinary repo's session start gains noise (or loses the "run ox init"
// guidance) because the public lookup found nothing.
func TestRunAgentPrime_RepoNotOnSageOxKeepsTheUsualAnswer(t *testing.T) {
	for name, remote := range map[string]string{
		"github repo SageOx does not know": "https://github.com/someone/private-thing.git",
		"not a github repo":                "git@gitlab.com:someone/thing.git",
	} {
		t.Run(name, func(t *testing.T) {
			env := newOxE2E(t)
			out := runUninitializedPrime(t, env, remote)
			assert.Contains(t, out, "unavailable")
			assert.NotContains(t, out, "SageOx primed from")
			assert.NotContains(t, out, "public prime")
			if !strings.Contains(remote, "github.com") {
				assert.NotContains(t, env.Requested(), "/api/v1/public/prime", "only GitHub origins are looked up")
			}
		})
	}
}

// TestRunAgentPrime_PublicLookupOutageKeepsTheUsualAnswer — Failure
// prevented: SageOx being down turns into an error at session start.
func TestRunAgentPrime_PublicLookupOutageKeepsTheUsualAnswer(t *testing.T) {
	env := newOxE2E(t)
	env.Handle("/api/v1/public/prime", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	out := runUninitializedPrime(t, env, "https://github.com/readest/readest.git")
	assert.Contains(t, out, "unavailable")
	assert.NotContains(t, out, "SageOx primed from")
	assert.Contains(t, env.Requested(), "/api/v1/public/prime")
}
