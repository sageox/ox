//go:build !short

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
)

// coworkerMachine is an initialized repo on a machine whose git refuses to
// guess an identity (a fresh container), with SAGEOX_TOKEN a team token acting
// as the AI coworker Rip. keepRepoIdentity leaves the person's own local
// identity in the repo.
func coworkerMachine(t *testing.T, keepRepoIdentity bool) *oxE2E {
	t.Helper()
	env := initializedE2E(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte("[user]\n\tuseConfigOnly = true\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
	if !keepRepoIdentity {
		mustRunGit(t, env.Root, "config", "--unset", "user.name")
		mustRunGit(t, env.Root, "config", "--unset", "user.email")
	}
	env.Handle(auth.IntrospectEndpoint, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":{"id":"agt_rip","display_name":"Rip"}}`)
	})
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	return env
}

// unsetTeamToken ends the coworker's token for the rest of the test.
func unsetTeamToken(t *testing.T) {
	t.Helper()
	t.Setenv("SAGEOX_TOKEN", "")
	require.NoError(t, os.Unsetenv("SAGEOX_TOKEN"))
}

// commitInProject makes a real commit in repo the way an AI coworker would,
// with the machine's environment, and returns its author.
func commitInProject(t *testing.T, repo string) (string, error) {
	t.Helper()
	// hooksPath off: ox init's hooks would run whatever ox is on PATH
	if out, err := exec.Command("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "work").CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%an <%ae>").Output()
	return strings.TrimSpace(string(out)), err
}

// Failure prevented: an AI coworker on a machine whose git has no identity (a
// fresh container) cannot commit its work in the project, or commits it as the
// machine; its identity outlives its team token; or a person's own identity in
// their repository is replaced.
func TestRunAgentPrime_AICoworkerCommitsItsWorkAsItself(t *testing.T) {
	for _, tc := range []struct {
		name             string
		keepRepoIdentity bool
		want             string
	}{
		{"a container's repo", false, "Rip <agt_rip@ai-coworker.invalid>"},
		{"a person's repo", true, "E2E <e2e@example.com>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := coworkerMachine(t, tc.keepRepoIdentity)

			runPrimeJSON(t)
			author, err := commitInProject(t, env.Root)
			require.NoError(t, err, "the AI coworker must be able to commit its work")
			assert.Equal(t, tc.want, author)

			unsetTeamToken(t)
			runPrimeJSON(t)
			author, err = commitInProject(t, env.Root)
			if tc.keepRepoIdentity {
				require.NoError(t, err)
				assert.Equal(t, tc.want, author, "a person's own identity survives")
				return
			}
			assert.Error(t, err, "without the team token, the coworker's identity is gone and git has none: %s", author)
		})
	}
}

// Failure prevented: the hooks re-prime with --idempotent, which returns
// before priming once the session is primed, so a token changed or removed
// mid-session leaves the earlier coworker authoring the project's commits.
func TestRunAgentPrime_IdempotentRePrimeFollowsTheToken(t *testing.T) {
	env := coworkerMachine(t, false)
	session := uniqueSessionID(t, "coworker-idempotent")

	primeWithHook(t, session, "startup")
	marker, err := ReadSessionMarker(session)
	require.NoError(t, err)
	require.True(t, marker != nil && marker.IsPrimed(), "fixture: the hook prime must mark the session primed")
	author, err := commitInProject(t, env.Root)
	require.NoError(t, err)
	require.Equal(t, "Rip <agt_rip@ai-coworker.invalid>", author)

	unsetTeamToken(t)
	require.NoError(t, agentPrimeCmd.Flags().Set("idempotent", "true"))
	t.Cleanup(func() { _ = agentPrimeCmd.Flags().Set("idempotent", "false") })
	out := primeWithHook(t, session, "resume")
	require.Empty(t, strings.TrimSpace(out), "fixture: an idempotent re-prime of a primed session prints nothing")

	author, err = commitInProject(t, env.Root)
	assert.Error(t, err, "the removed token's coworker still authors the project's commits: %s", author)
}

// Failure prevented: every idempotent re-prime with a team token asks the
// server about it, stalling up to five seconds each when the server is
// unreachable and nothing fresh is cached.
func TestRunAgentPrime_IdempotentRePrimeNeverWaitsOnTheServer(t *testing.T) {
	env := coworkerMachine(t, false)
	session := uniqueSessionID(t, "coworker-offline")
	primeWithHook(t, session, "startup")
	author, err := commitInProject(t, env.Root)
	require.NoError(t, err)
	require.Equal(t, "Rip <agt_rip@ai-coworker.invalid>", author)

	var introspections atomic.Int32
	env.OnRequest(func(r *http.Request) {
		if r.URL.Path == auth.IntrospectEndpoint {
			introspections.Add(1)
		}
	})
	t.Setenv("SAGEOX_TOKEN", "oxt_rotated_1lKvCA") // a team token this machine never fetched
	require.NoError(t, agentPrimeCmd.Flags().Set("idempotent", "true"))
	t.Cleanup(func() { _ = agentPrimeCmd.Flags().Set("idempotent", "false") })
	primeWithHook(t, session, "resume")

	assert.Zero(t, introspections.Load(), "the idempotent re-prime asked the server about the token")
	author, err = commitInProject(t, env.Root)
	require.NoError(t, err)
	assert.Equal(t, "Rip <agt_rip@ai-coworker.invalid>", author, "an unknown token changes nothing until a full prime learns it")
}
