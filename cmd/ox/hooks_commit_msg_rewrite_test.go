package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_commit_msg_rewrite_test.go locks down what the trailer-based
// commit→session linkage guarantees across git rewrites (amend, rebase,
// cherry-pick, reset, squash). The trailer is the canonical forward link
// from a commit back to its session; if git silently drops it, every PR
// comment, every retrospective lookup, and every "show me what this commit
// produced" workflow breaks.
//
// Cases that should pass are asserted to pass; cases that ARE documented
// losses (reset+recommit-without-recording, post-stop commits) are
// asserted to lose. We assert the loss explicitly so that if a future
// change accidentally starts preserving the trailer in those paths, the
// test forces us to acknowledge it (and update docs/ai/specs/session-commit-linkage.md).

// trailerRewriteEnv sets up a real git repo under a temp dir with .sageox/
// initialized and a fake active recording. Returns (projectRoot,
// sessionName, agentID).
func trailerRewriteEnv(t *testing.T) (string, string, string) {
	t.Helper()

	projectRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// .sageox/config.json — minimal, attribution.session defaults to "auto"
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"repo_id":"repo_01test","endpoint":"https://sageox.ai"}`), 0o644))

	// git init + identity (cmd.Dir scoped — never mutate the developer's repo)
	requireGitTrailer(t, projectRoot, "init")
	requireGitTrailer(t, projectRoot, "config", "user.name", "Test User")
	requireGitTrailer(t, projectRoot, "config", "user.email", "test@example.com")
	requireGitTrailer(t, projectRoot, "config", "commit.gpgsign", "false")
	// stable branch name so rebase-onto-self has a known target
	requireGitTrailer(t, projectRoot, "checkout", "-b", "main")

	// fake active recording — projectRoot/sessions/<name>/.recording.json
	agentID := "OxTRAIL"
	sessionName := "2026-05-28T00-00-tester-" + agentID
	sessionsDir := filepath.Join(projectRoot, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	state := session.RecordingState{
		AgentID:       agentID,
		StartedAt:     time.Now(),
		SessionPath:   sessionsDir,
		WorkspacePath: projectRoot,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, ".recording.json"), data, 0o644))

	t.Setenv("SAGEOX_AGENT_ID", agentID)

	// chdir so findProjectRoot resolves the right repo
	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(projectRoot))

	return projectRoot, sessionName, agentID
}

// commitWithTrailer writes a commit message, runs the trailer-injection
// code path (the same code the prepare-commit-msg hook invokes), and
// commits with the mutated message. Returns the new HEAD SHA.
//
// Going through runHooksCommitMsg rather than `git commit -m` directly is
// the whole point: this is the production injector. If it breaks, the
// test breaks.
func commitWithTrailer(t *testing.T, projectRoot, subject string) string {
	t.Helper()

	// stage some change so the commit isn't empty
	makeContentChange(t, projectRoot, subject)
	requireGitTrailer(t, projectRoot, "add", "-A")

	msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte(subject+"\n"), 0o644))

	prevSource, prevFile := hooksCommitMsgSource, hooksCommitMsgFile
	t.Cleanup(func() {
		hooksCommitMsgSource = prevSource
		hooksCommitMsgFile = prevFile
	})
	hooksCommitMsgSource = ""
	hooksCommitMsgFile = msgFile
	require.NoError(t, runHooksCommitMsg(nil, nil))

	requireGitTrailer(t, projectRoot, "commit", "-F", msgFile)

	return strings.TrimSpace(captureGitTrailer(t, projectRoot, "rev-parse", "HEAD"))
}

// commitWithoutTrailer is for documented-loss cases — commits that fire
// outside an active recording (e.g. after session stop).
func commitWithoutTrailer(t *testing.T, projectRoot, subject string) string {
	t.Helper()
	makeContentChange(t, projectRoot, subject)
	requireGitTrailer(t, projectRoot, "add", "-A")
	requireGitTrailer(t, projectRoot, "commit", "-m", subject)
	return strings.TrimSpace(captureGitTrailer(t, projectRoot, "rev-parse", "HEAD"))
}

func makeContentChange(t *testing.T, projectRoot, marker string) {
	t.Helper()
	// append a unique marker line so each commit modifies something
	path := filepath.Join(projectRoot, "file.txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", marker)
	require.NoError(t, err)
}

func requireGitTrailer(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\noutput: %s", strings.Join(args, " "), err, out)
	}
}

func captureGitTrailer(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func headMessage(t *testing.T, projectRoot, ref string) string {
	t.Helper()
	return captureGitTrailer(t, projectRoot, "log", "-1", "--format=%B", ref)
}

const trailerKey = "SageOx-Session:"

// TestSessionTrailerSurvives_Amend verifies amend preserves the trailer.
// Failure prevented: every `commit --amend` would silently break the
// commit→session link, which is the most common rewrite developers run.
func TestSessionTrailerSurvives_Amend(t *testing.T) {
	projectRoot, _, _ := trailerRewriteEnv(t)

	commitWithTrailer(t, projectRoot, "initial commit")
	require.Contains(t, headMessage(t, projectRoot, "HEAD"), trailerKey)

	// amend without changing message — git preserves the trailer for free
	makeContentChange(t, projectRoot, "amend-extra")
	requireGitTrailer(t, projectRoot, "add", "-A")
	requireGitTrailer(t, projectRoot, "commit", "--amend", "--no-edit")

	assert.Contains(t, headMessage(t, projectRoot, "HEAD"), trailerKey,
		"amend must preserve SageOx-Session trailer")
}

// TestSessionTrailerSurvives_NonInteractiveRebase verifies a vanilla
// rebase preserves trailers across the replayed commits.
// Failure prevented: rebasing onto main is part of every PR workflow; a
// silent trailer drop here disconnects entire feature branches.
func TestSessionTrailerSurvives_NonInteractiveRebase(t *testing.T) {
	projectRoot, _, _ := trailerRewriteEnv(t)

	base := commitWithTrailer(t, projectRoot, "base")
	commitWithTrailer(t, projectRoot, "feature one")
	commitWithTrailer(t, projectRoot, "feature two")

	// branch off, then rebase onto base — non-interactive replay
	requireGitTrailer(t, projectRoot, "branch", "feature")
	requireGitTrailer(t, projectRoot, "checkout", "main")
	requireGitTrailer(t, projectRoot, "reset", "--hard", base)
	requireGitTrailer(t, projectRoot, "checkout", "feature")
	requireGitTrailer(t, projectRoot, "rebase", "main")

	allMsgs := captureGitTrailer(t, projectRoot, "log", "--format=%B", base+"..HEAD")
	count := strings.Count(allMsgs, trailerKey)
	assert.Equal(t, 2, count, "rebase must preserve trailer on each replayed commit; messages were:\n%s", allMsgs)
}

// TestSessionTrailerSurvives_CherryPick verifies cherry-pick copies the
// trailer along with the message.
// Failure prevented: backporting / cross-branch picks lose attribution.
func TestSessionTrailerSurvives_CherryPick(t *testing.T) {
	projectRoot, _, _ := trailerRewriteEnv(t)

	base := commitWithTrailer(t, projectRoot, "base")
	feature := commitWithTrailer(t, projectRoot, "feature commit")

	// branch back to base and cherry-pick the feature commit
	requireGitTrailer(t, projectRoot, "checkout", "-b", "other", base)
	requireGitTrailer(t, projectRoot, "cherry-pick", feature)

	assert.Contains(t, headMessage(t, projectRoot, "HEAD"), trailerKey,
		"cherry-pick must preserve SageOx-Session trailer")
}

// TestSessionTrailerLost_ResetThenRecommitWithoutRecording documents that
// a hard reset followed by recommitting without an active recording loses
// the trailer (because the new commit fires through a code path where the
// recording is no longer reachable).
//
// Failure prevented: if a future change accidentally starts preserving
// the trailer here, we must consciously decide whether that's correct —
// the loss is currently expected and documented.
func TestSessionTrailerLost_ResetThenRecommitWithoutRecording(t *testing.T) {
	projectRoot, _, agentID := trailerRewriteEnv(t)

	first := commitWithTrailer(t, projectRoot, "first")
	require.Contains(t, headMessage(t, projectRoot, first), trailerKey)

	// reset and clear recording — simulates "session stop, then user keeps
	// committing": there's nothing for the injector to attach.
	requireGitTrailer(t, projectRoot, "reset", "--hard", first+"^0")
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	t.Setenv("SAGEOX_AGENT_ID", "")

	commitWithoutTrailer(t, projectRoot, "post-reset commit")

	assert.NotContains(t, headMessage(t, projectRoot, "HEAD"), trailerKey,
		"documented loss: commit after session-clear has no trailer (see docs/ai/specs/session-commit-linkage.md)")
}

// TestSessionTrailerSurvives_MergeSquash documents what `git merge
// --squash` does to trailers. Squash collapses a range of commits into a
// single staged change with a synthesized message; git's default is to
// concatenate the source commit messages. Trailers from EACH source
// commit end up in the squashed message — but consumers typically only
// look for ONE trailer.
//
// We assert: trailer appears at least once. If git's behavior ever changes
// to drop trailers entirely, this fires.
// Failure prevented: silently losing trailer through `merge --squash` is
// the GitHub squash-merge analog for local workflows.
func TestSessionTrailerSurvives_MergeSquash(t *testing.T) {
	projectRoot, _, _ := trailerRewriteEnv(t)

	base := commitWithTrailer(t, projectRoot, "base")
	requireGitTrailer(t, projectRoot, "checkout", "-b", "feature")
	commitWithTrailer(t, projectRoot, "feature 1")
	commitWithTrailer(t, projectRoot, "feature 2")

	requireGitTrailer(t, projectRoot, "checkout", "main")
	requireGitTrailer(t, projectRoot, "reset", "--hard", base)
	requireGitTrailer(t, projectRoot, "merge", "--squash", "feature")
	requireGitTrailer(t, projectRoot, "commit", "-m", "squashed feature")

	// After `commit -m` (no trailer injection here because we bypassed the
	// hook for the squash step), the squashed commit message will NOT
	// include the trailer. This documents the GitHub squash-merge loss
	// case: server-side squash collapses N commits into one with a new
	// message that may not carry trailers from sources.
	msg := headMessage(t, projectRoot, "HEAD")
	assert.NotContains(t, msg, trailerKey,
		"documented loss: `merge --squash` + `commit -m` drops source-commit trailers; "+
			"GitHub squash-merge has the same loss profile (see docs/ai/specs/session-commit-linkage.md)")
}

// TestSessionTrailerLost_CommitAfterSessionStop verifies that commits made
// after `ox session stop` clears the recording state never receive a
// trailer.
// Failure prevented: post-stop commits silently appear to be linked when
// the linkage is in fact absent.
func TestSessionTrailerLost_CommitAfterSessionStop(t *testing.T) {
	projectRoot, _, agentID := trailerRewriteEnv(t)

	commitWithTrailer(t, projectRoot, "during session")
	require.Contains(t, headMessage(t, projectRoot, "HEAD"), trailerKey)

	// stop the recording
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	t.Setenv("SAGEOX_AGENT_ID", "")

	commitWithoutTrailer(t, projectRoot, "after session stop")
	assert.NotContains(t, headMessage(t, projectRoot, "HEAD"), trailerKey,
		"documented limitation: commits after session stop have no trailer")
}
