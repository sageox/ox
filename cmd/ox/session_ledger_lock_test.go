package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Session-stop Ledger writers vs. the daemon's sync rebase (sageox/ox#1541) ---
//
// The session upload and push-summary commits used to run without the repo
// lock, so they could commit to main while a daemon's `pull --rebase` replayed
// — the race that wedged the Ledger — and could stage into a rebase that was
// already wedged.

// wedgeLedgerRebase leaves ledgerPath halted mid-rebase with HEAD detached,
// the state a sync rebase that lost its race leaves behind.
func wedgeLedgerRebase(t *testing.T, ledgerPath string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, "local.txt"), []byte("local"), 0o644))
	runGit(t, ledgerPath, "add", "local.txt")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "local")
	_, err := runGitCombined(ledgerPath, "rebase", "-x", "false", "HEAD~1")
	require.Error(t, err, "the failing exec must halt the rebase")
	require.True(t, gitutil.IsRebaseInProgress(ledgerPath))
}

// TestSessionLedgerWriters_RefuseToStageIntoWedgedRebase: under the repo lock no
// sync rebase is live, so one on disk is a wedge. Failure prevented: a session
// stop staging its files into the wedged rebase (marking its conflicts
// resolved) or committing onto the rebase's detached HEAD, where the next
// recovery discards it.
func TestSessionLedgerWriters_RefuseToStageIntoWedgedRebase(t *testing.T) {
	writers := []struct {
		name  string
		write func(t *testing.T, ledgerPath, sessionName, sessionDir string) error
	}{
		{"session upload", func(t *testing.T, ledgerPath, sessionName, _ string) error {
			return commitAndPushLedger(ledgerPath, sessionName)
		}},
		{"batch re-redaction", func(t *testing.T, ledgerPath, sessionName, _ string) error {
			return commitAndPushLedgerBatch(ledgerPath, []string{sessionName})
		}},
		{"session remove", func(t *testing.T, ledgerPath, sessionName, _ string) error {
			_, err := batchDeleteSessionsFromLedger(ledgerPath, []string{sessionName})
			return err
		}},
		{"session delete", func(t *testing.T, ledgerPath, sessionName, sessionDir string) error {
			return deleteSessionFromLedger(ledgerPath, sessionName, sessionDir)
		}},
		{"push-summary", func(t *testing.T, _, _, sessionDir string) error {
			summary := filepath.Join(t.TempDir(), "raw-summary.json")
			require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))
			out := pushSummaryToLedger(summary, sessionDir)
			if out.Success {
				return nil
			}
			return &pushSummaryError{out.Error}
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			_, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			sessionName := "2026-01-01T00-00-testuser-OxWdg1"
			sessionDir := writeSessionFiles(t, clonePath, sessionName)
			wedgeLedgerRebase(t, clonePath)
			headBefore := runGit(t, clonePath, "rev-parse", "HEAD")

			err := w.write(t, clonePath, sessionName, sessionDir)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "broken rebase state")
			assert.Empty(t, runGit(t, clonePath, "diff", "--cached", "--name-only"), "nothing may be staged into the wedged rebase")
			assert.Equal(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"), "nothing may be committed onto the rebase's HEAD")
		})
	}
}

// Refusal must preserve staged merge/rebase resolutions, including in a
// configured linked checkout, so finishing the operation can unblock writes.
func TestSessionDeletion_PreservesUnfinishedGitOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git merge, rebase and push")
	}
	for _, tt := range []struct{ writer, operation string }{
		{"session remove", "merge"},
		{"session delete", "merge"},
		{"session remove", "linked rebase"},
		{"session delete", "linked rebase"},
	} {
		t.Run(tt.writer+"/"+tt.operation, func(t *testing.T) {
			remote, repo := createBareAndClone(t)
			isolatePushEnv(t, repo)
			const name = "2026-01-01T00-00-testuser-OxMerge"
			dir := writeSessionFiles(t, repo, name)
			runGit(t, repo, "add", "sessions")
			runGit(t, repo, "commit", "-m", "seed session")
			runGit(t, repo, "push")
			remoteBefore := runGit(t, remote, "rev-parse", "HEAD")
			branch := runGit(t, repo, "branch", "--show-current")
			marker, blocked := "MERGE_HEAD", "merge in progress"
			if tt.operation == "merge" {
				runGit(t, repo, "checkout", "-b", "manual-merge")
				require.NoError(t, os.WriteFile(filepath.Join(repo, "merge-note.txt"), []byte("manual merge work\n"), 0o644))
				runGit(t, repo, "add", "merge-note.txt")
				runGit(t, repo, "commit", "-m", "manual work")
				runGit(t, repo, "checkout", branch)
				runGit(t, repo, "merge", "--no-ff", "--no-commit", "manual-merge")
			} else {
				// Conflicting edits to the selected session leave a real rebase
				// paused in a linked checkout. Stage its resolution without continuing.
				runGit(t, repo, "checkout", "-b", "rebase-upstream")
				linked := filepath.Join(t.TempDir(), "linked")
				runGit(t, repo, "worktree", "add", linked, branch)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"upstream"}`), 0o644))
				runGit(t, repo, "commit", "-am", "upstream edit")
				repo, dir = linked, filepath.Join(linked, "sessions", name)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"local"}`), 0o644))
				runGit(t, repo, "commit", "-am", "local edit")
				_, err := runGitCombined(repo, "rebase", "rebase-upstream")
				require.Error(t, err)
				require.NotEmpty(t, runGit(t, repo, "ls-files", "--unmerged"))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"resolved"}`), 0o644))
				runGit(t, repo, "add", "sessions")
				marker, blocked = "rebase-merge", "broken rebase state"
			}
			markerPath := runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", marker)
			_, err := os.Stat(markerPath)
			require.NoError(t, err)
			require.Empty(t, runGit(t, repo, "ls-files", "--unmerged"), "the index is fully resolved")
			headBefore := runGit(t, repo, "rev-parse", "HEAD")
			indexBefore := runGit(t, repo, "write-tree")
			workBefore, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			require.NoError(t, err)
			remove := func() error {
				if tt.writer == "session remove" {
					_, err := batchDeleteSessionsFromLedger(repo, []string{name})
					return err
				}
				return deleteSessionFromLedger(repo, name, dir)
			}

			assert.ErrorContains(t, remove(), blocked)
			assert.Equal(t, headBefore, runGit(t, repo, "rev-parse", "HEAD"))
			assert.Equal(t, remoteBefore, runGit(t, remote, "rev-parse", "HEAD"))
			assert.Equal(t, indexBefore, runGit(t, repo, "write-tree"), "refusal must preserve staged resolutions")
			workAfter, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			require.NoError(t, err)
			assert.Equal(t, workBefore, workAfter)
			_, err = os.Stat(markerPath)
			require.NoError(t, err)

			// Finishing the operation unblocks deletion and synchronization.
			if tt.operation == "merge" {
				runGit(t, repo, "commit", "-m", "complete manual merge")
				require.Len(t, strings.Fields(runGit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")), 3)
			} else {
				runGit(t, repo, "-c", "core.editor=true", "rebase", "--continue")
			}
			require.NoError(t, remove())
			assert.NoDirExists(t, dir)
			_, err = runGitCombined(remote, "cat-file", "-e", "HEAD:sessions/"+name)
			assert.Error(t, err, "the deletion must reach the remote after recovery")
			runGit(t, repo, "pull", "--rebase", "--autostash")
		})
	}
}

// runGitCombined runs git for a step that may fail on purpose.
func runGitCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type pushSummaryError struct{ msg string }

func (e *pushSummaryError) Error() string { return e.msg }

// TestLedgerWriters_QueueBehindRepoLock: while a sync holds the repo lock,
// every ox writer that moves a Ledger branch must wait rather than commit. A
// writer that skips the lock commits within milliseconds, so the watch window
// only bounds the check. Failure prevented: the commit moving main under a
// live sync rebase, which makes git refuse to finish it, and landing inside a
// recovery's re-anchor-to-abort window, where the abort rewinds past it.
func TestLedgerWriters_QueueBehindRepoLock(t *testing.T) {
	committedSession := func(t *testing.T, clonePath, name string) {
		writeSessionFiles(t, clonePath, name)
		runGit(t, clonePath, "add", "-A")
		runGit(t, clonePath, "commit", "--no-verify", "-m", "seed session")
		runGit(t, clonePath, "push")
	}
	writers := []struct {
		name    string
		prepare func(t *testing.T, clonePath, name string) func() error
	}{
		{"session upload", func(t *testing.T, clonePath, name string) func() error {
			writeSessionFiles(t, clonePath, name)
			return func() error { return commitAndPushLedger(clonePath, name) }
		}},
		{"batch re-redaction", func(t *testing.T, clonePath, name string) func() error {
			writeSessionFiles(t, clonePath, name)
			return func() error { return commitAndPushLedgerBatch(clonePath, []string{name}) }
		}},
		{"push-summary", func(t *testing.T, clonePath, name string) func() error {
			sessionDir := writeSessionFiles(t, clonePath, name)
			summary := pushSummaryFixture(t)
			return func() error {
				out := pushSummaryToLedger(summary, sessionDir)
				if !out.Success {
					return &pushSummaryError{out.Error}
				}
				return nil
			}
		}},
		{"session delete", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			return func() error {
				return deleteSessionFromLedger(clonePath, name, filepath.Join(clonePath, "sessions", name))
			}
		}},
		{"session remove", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			return func() error {
				_, err := batchDeleteSessionsFromLedger(clonePath, []string{name})
				return err
			}
		}},
		{"murmur delete", func(t *testing.T, clonePath, _ string) func() error {
			const murmur = "data/murmurs/m1.json"
			require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(murmur)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(clonePath, murmur), []byte(`{"content":"wip"}`), 0o644))
			runGit(t, clonePath, "add", murmur)
			runGit(t, clonePath, "commit", "--no-verify", "-m", "seed murmur")
			return func() error { return commitMurmurDeletion(context.Background(), clonePath, murmur, "m1") }
		}},
		{"session-ID backfill", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			meta := filepath.Join(clonePath, "sessions", name, "meta.json")
			require.NoError(t, os.WriteFile(meta, []byte(`{"session_name":"`+name+`","session_id":"ses_test"}`), 0o644))
			return func() error { return commitAndPushSessionIDBackfill(clonePath, []string{name}) }
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			_, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			write := w.prepare(t, clonePath, "2026-01-01T00-00-testuser-OxLck1")
			headBefore := runGit(t, clonePath, "rev-parse", "HEAD")

			held, release := make(chan struct{}), make(chan struct{})
			syncDone := make(chan error, 1)
			go func() {
				syncDone <- gitutil.WithRepoLock(context.Background(), clonePath, func() error {
					close(held)
					<-release
					return nil
				})
			}()
			<-held
			writeDone := make(chan error, 1)
			go func() { writeDone <- write() }()

			assert.Never(t, func() bool {
				head, err := runGitCombined(clonePath, "rev-parse", "HEAD")
				return err == nil && head != headBefore
			}, 500*time.Millisecond, 25*time.Millisecond, "the writer must not commit while a sync holds the repo lock")
			close(release)
			require.NoError(t, <-syncDone)
			require.NoError(t, <-writeDone)
			assert.NotEqual(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"), "the queued write must still land once the lock is free")
		})
	}
}

// Maintenance must leave unsaved plans alone without stranding another staged session.
func TestLedgerMaintenance_LeavesPlansStagedAndIncludesOtherSessions(t *testing.T) {
	for _, tt := range []struct {
		name          string
		writer        string
		advanceRemote bool
	}{
		{"batch re-redaction", "batch re-redaction", false},
		{"murmur deletion", "murmur deletion", false},
		{"session remove", "session remove", false},
		{"session delete", "session delete", false},
		{"batch re-redaction after remote advance", "batch re-redaction", true},
		{"murmur deletion after remote advance", "murmur deletion", true},
		{"session remove after remote advance", "session remove", true},
		{"session delete after remote advance", "session delete", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			barePath, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			const plan = "data/plans/draft/plan.md"
			const removedPlan = "data/plans/removed/plan.md"
			const newPlan = "data/plans/new/plan.md"
			const murmur = "data/murmurs/m1.json"
			const deletedName = "2026-01-01T00-00-testuser-OxDelete"
			if tt.writer == "session remove" || tt.writer == "session delete" {
				dir := writeSessionFiles(t, clonePath, deletedName)
				// A confirmed session deletion may exceed the automatic guard's limit.
				for i := range 6 {
					require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("artifact-%d.txt", i)), []byte("recorded work\n"), 0o644))
				}
				runGit(t, clonePath, "add", "sessions")
			}
			for _, path := range []string{plan, removedPlan, murmur} {
				require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(path)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(clonePath, path), []byte("original\n"), 0o644))
			}
			runGit(t, clonePath, "add", "data")
			runGit(t, clonePath, "commit", "--no-verify", "-m", "seed plans and murmur")
			runGit(t, clonePath, "push")
			if tt.advanceRemote {
				other := cloneBare(t, barePath)
				require.NoError(t, os.WriteFile(filepath.Join(other, "remote.txt"), []byte("teammate change\n"), 0o644))
				runGit(t, other, "add", "remote.txt")
				runGit(t, other, "commit", "-m", "advance remote")
				runGit(t, other, "push")
			}
			remoteBefore := runGit(t, barePath, "rev-parse", "HEAD")

			require.NoError(t, os.WriteFile(filepath.Join(clonePath, plan), []byte("staged draft\n"), 0o644))
			require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(newPlan)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(clonePath, newPlan), []byte("new draft\n"), 0o644))
			runGit(t, clonePath, "add", plan, newPlan)
			runGit(t, clonePath, "rm", removedPlan)
			require.NoError(t, os.WriteFile(filepath.Join(clonePath, plan), []byte("later working draft\n"), 0o644))
			stagedPlans := runGit(t, clonePath, "diff", "--cached", "--binary", "--", "data/plans")
			workingPlans := runGit(t, clonePath, "diff", "--binary", "--", "data/plans")

			const otherName = "2026-01-01T00-00-testuser-OxOther"
			const batchName = "2026-01-01T00-00-testuser-OxBatch"
			names := []string{otherName}
			if tt.writer == "batch re-redaction" {
				names = append(names, batchName)
			}
			store := newFakeLFSStore(t)
			for _, name := range names {
				dir := writeSessionFiles(t, clonePath, name)
				raw := &session.StoredSession{
					Meta:    &session.StoreMeta{Version: "1", AgentType: "claude-code"},
					Entries: []map[string]any{{"type": "user", "content": "Keep the recovered session."}},
				}
				require.NoError(t, rewriteRawJSONL(filepath.Join(dir, "raw.jsonl"), raw))
				require.NoError(t, regenerateArtifacts(dir, raw))
				refs, err := lfs.UploadSessionFiles(store.client(), dir, nil)
				require.NoError(t, err)
				meta, err := lfs.ReadSessionMeta(dir)
				require.NoError(t, err)
				meta.Files = refs
				require.NoError(t, lfs.WriteSessionMeta(dir, meta))
			}
			runGit(t, clonePath, "add", "sessions/"+otherName)
			switch tt.writer {
			case "batch re-redaction":
				require.NoError(t, commitAndPushLedgerBatch(clonePath, []string{batchName}))
			case "session remove":
				removed, err := batchDeleteSessionsFromLedger(clonePath, []string{deletedName})
				require.NoError(t, err)
				assert.Equal(t, 1, removed)
			case "session delete":
				require.NoError(t, deleteSessionFromLedger(clonePath, deletedName, filepath.Join(clonePath, "sessions", deletedName)))
			case "murmur deletion":
				require.NoError(t, commitMurmurDeletion(context.Background(), clonePath, murmur, "m1"))
				_, err := runGitCombined(clonePath, "cat-file", "-e", "HEAD:"+murmur)
				assert.Error(t, err, "the murmur must be deleted from the commit")
				assert.Equal(t, remoteBefore, runGit(t, barePath, "rev-parse", "HEAD"), "murmur deletion must remain a local commit")
				require.NoError(t, pushLedger(context.Background(), clonePath))
			}
			if tt.writer == "session remove" || tt.writer == "session delete" {
				_, err := runGitCombined(barePath, "cat-file", "-e", "HEAD:sessions/"+deletedName)
				assert.Error(t, err, "the selected session must be deleted remotely")
			}

			for _, name := range names {
				assert.NotEmpty(t, runGit(t, barePath, "show", "HEAD:sessions/"+name+"/meta.json"))
				for _, artifact := range []string{"raw.jsonl", "session.md"} {
					pointer := runGit(t, barePath, "show", "HEAD:sessions/"+name+"/"+artifact)
					oid, _, err := lfs.ParsePointer(pointer)
					require.NoError(t, err)
					assert.Contains(t, string(store.object(oid)), "Keep the recovered session.")
				}
			}
			assert.Equal(t, "original", runGit(t, barePath, "show", "HEAD:"+plan))
			assert.Equal(t, "original", runGit(t, barePath, "show", "HEAD:"+removedPlan))
			_, err := runGitCombined(barePath, "cat-file", "-e", "HEAD:"+newPlan)
			assert.Error(t, err, "a staged new plan must not be published")
			if tt.advanceRemote {
				assert.Equal(t, "teammate change", runGit(t, clonePath, "show", "HEAD:remote.txt"))
				assert.FileExists(t, filepath.Join(clonePath, newPlan))
				assert.NoFileExists(t, filepath.Join(clonePath, removedPlan))
				content, err := os.ReadFile(filepath.Join(clonePath, plan))
				require.NoError(t, err)
				assert.Equal(t, "later working draft\n", string(content), "autostash must preserve the working plan")
			} else {
				assert.Equal(t, stagedPlans, runGit(t, clonePath, "diff", "--cached", "--binary", "--", "data/plans"))
				assert.Equal(t, workingPlans, runGit(t, clonePath, "diff", "--binary", "--", "data/plans"))
			}
		})
	}
}

// Maintenance must preserve a conflicted or unreadable index until it is repaired.
func TestLedgerMaintenance_PreservesIndexAndWorksAfterRecovery(t *testing.T) {
	for _, writer := range []string{"batch re-redaction", "murmur deletion", "session remove", "session delete"} {
		t.Run(writer, func(t *testing.T) {
			_, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			const name = "2026-01-01T00-00-testuser-OxBatch"
			path := "data/murmurs/m1.json"
			if writer != "murmur deletion" {
				writeSessionFiles(t, clonePath, name)
				path = "sessions/" + name + "/meta.json"
			} else {
				require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(path)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(clonePath, path), []byte(`{"content":"wip"}`), 0o644))
			}
			runGit(t, clonePath, "add", "-A")
			runGit(t, clonePath, "commit", "--no-verify", "-m", "seed maintenance file")
			blob := runGit(t, clonePath, "rev-parse", "HEAD:"+path)
			update := exec.Command("git", "-C", clonePath, "update-index", "--index-info")
			update.Stdin = strings.NewReader(fmt.Sprintf("0 %[1]s 0\t%[2]s\n100644 %[3]s 1\t%[2]s\n100644 %[3]s 2\t%[2]s\n100644 %[3]s 3\t%[2]s\n", strings.Repeat("0", len(blob)), path, blob))
			out, err := update.CombinedOutput()
			require.NoError(t, err, "%s", out)
			indexBefore := runGit(t, clonePath, "ls-files", "--stage")
			headBefore := runGit(t, clonePath, "rev-parse", "HEAD")
			write := func() error {
				switch writer {
				case "batch re-redaction":
					return commitAndPushLedgerBatch(clonePath, []string{name})
				case "session remove":
					_, err := batchDeleteSessionsFromLedger(clonePath, []string{name})
					return err
				case "session delete":
					return deleteSessionFromLedger(clonePath, name, filepath.Join(clonePath, "sessions", name))
				default:
					return commitMurmurDeletion(context.Background(), clonePath, path, "m1")
				}
			}

			require.ErrorContains(t, write(), "unresolved conflicts")
			assert.Equal(t, indexBefore, runGit(t, clonePath, "ls-files", "--stage"))
			assert.Equal(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"))
			assert.FileExists(t, filepath.Join(clonePath, path))

			runGit(t, clonePath, "reset", "HEAD", "--", path)
			indexPath := filepath.Join(clonePath, ".git", "index")
			cleanIndex, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(indexPath, []byte("corrupt index"), 0o644))
			require.ErrorContains(t, write(), "git ls-files --unmerged")
			indexAfter, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			assert.Equal(t, "corrupt index", string(indexAfter))
			assert.Equal(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"))
			assert.FileExists(t, filepath.Join(clonePath, path))
			require.NoError(t, os.WriteFile(indexPath, cleanIndex, 0o644))

			const otherName = "2026-01-01T00-00-testuser-OxInvalid"
			otherDir := writeSessionFiles(t, clonePath, otherName)
			otherMeta := filepath.Join(otherDir, "meta.json")
			require.NoError(t, os.WriteFile(otherMeta, []byte("invalid JSON"), 0o644))
			runGit(t, clonePath, "add", otherMeta)
			require.ErrorContains(t, write(), "invalid JSON object")
			assert.Equal(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"))
			assert.Equal(t, "invalid JSON", runGit(t, clonePath, "show", ":sessions/"+otherName+"/meta.json"))
			if writer == "session remove" || writer == "session delete" {
				assert.FileExists(t, filepath.Join(clonePath, path), "a refused deletion must restore the selected session")
				assert.Empty(t, runGit(t, clonePath, "diff", "HEAD", "--", "sessions/"+name))
			}
			lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, gitutil.WithRepoLock(lockCtx, clonePath, func() error { return nil }), "validation failure must release the lock")
			writeSessionFiles(t, clonePath, otherName)
			runGit(t, clonePath, "add", otherMeta)
			// git rm already staged the deletion before validation refused the commit.
			// Restore the fixture so the same delete command can be retried.
			if writer == "murmur deletion" {
				runGit(t, clonePath, "restore", "--source=HEAD", "--staged", "--worktree", "--", path)
			}
			require.NoError(t, os.WriteFile(filepath.Join(clonePath, path), []byte(`{"content":"recovered"}`), 0o644))
			require.NoError(t, write(), "a refused write must release the lock for the next attempt")
			assert.NotEqual(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"))
		})
	}
}

// TestPushSummary_LeavesUnrelatedStagedEntriesStaged: the summary commit goes
// through the validated Ledger snapshot, which never publishes data/plans.
// Failure prevented: a bare `git commit` sweeping a coworker's staged plan
// edit, unvalidated, into the session's summary commit (#1442).
func TestPushSummary_LeavesUnrelatedStagedEntriesStaged(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionName := "2026-01-01T00-00-testuser-OxSnp1"
	sessionDir := writeSessionFiles(t, clonePath, sessionName)
	const plan = "data/plans/pln_test/plan.md"
	require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(plan)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clonePath, plan), []byte("# draft plan\n"), 0o644))
	runGit(t, clonePath, "add", plan)
	otherSession := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxOther")
	runGit(t, clonePath, "add", filepath.Join(otherSession, "meta.json"))
	summary := filepath.Join(t.TempDir(), "raw-summary.json")
	require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))

	out := pushSummaryToLedger(summary, sessionDir)

	require.True(t, out.Success, out.Error)
	_, err := runGitCombined(clonePath, "cat-file", "-e", "HEAD:sessions/"+sessionName+"/summary.json")
	assert.NoError(t, err, "the summary must be committed")
	_, err = runGitCombined(clonePath, "cat-file", "-e", "HEAD:"+plan)
	assert.Error(t, err, "the staged plan must not ride along in the summary commit")
	assert.Contains(t, runGit(t, clonePath, "diff", "--cached", "--name-only"), plan, "the plan edit must stay staged")
	assert.NotEmpty(t, runGit(t, barePath, "show", "HEAD:sessions/"+filepath.Base(otherSession)+"/meta.json"), "another staged session must still be published")
}

// pushSummaryFixture returns a summary file push-summary accepts.
func pushSummaryFixture(t *testing.T) string {
	t.Helper()
	summary := filepath.Join(t.TempDir(), "raw-summary.json")
	require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))
	return summary
}

// TestPushSummary_RetryAfterCommitSucceeds: a hook that re-runs push-summary
// for a summary already in the Ledger must get success, not a commit error.
// Failure prevented: the snapshot's "nothing to commit" (committed=false)
// reading as a failure, so a retried session stop reports a broken upload.
func TestPushSummary_RetryAfterCommitSucceeds(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxRty1")
	summary := pushSummaryFixture(t)
	first := pushSummaryToLedger(summary, sessionDir)
	require.True(t, first.Success, first.Error)
	head := runGit(t, clonePath, "rev-parse", "HEAD")

	retry := pushSummaryToLedger(summary, sessionDir)

	require.True(t, retry.Success, retry.Error)
	assert.Equal(t, "summary pushed to ledger", retry.Message)
	assert.Equal(t, head, runGit(t, clonePath, "rev-parse", "HEAD"), "a retry must not add a commit")
}

// A failed push leaves the summary committed locally. Identical retries must
// report failure while the remote is unavailable, then publish that commit.
func TestPushSummary_RetryAfterFailedPush(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxPushRetry")
	summary := pushSummaryFixture(t)
	cacheDir := filepath.Join(session.GetContextPath(getRepoIDOrDefault(clonePath)), "sessions", filepath.Base(sessionDir))
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	rawPath := filepath.Join(cacheDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("{\"type\":\"user\",\"content\":\"Keep this session for retry.\"}\n"), 0o600))
	require.NoError(t, session.WriteNeedsSummaryMarker(cacheDir, rawPath, sessionDir))
	remoteHead := runGit(t, barePath, "rev-parse", "HEAD")
	runGit(t, clonePath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))

	first := pushSummaryToLedger(summary, sessionDir)
	require.False(t, first.Success)
	assert.Contains(t, first.Error, "git push")
	assert.FileExists(t, rawPath)
	assert.True(t, session.HasNeedsSummaryMarker(cacheDir))
	head := runGit(t, clonePath, "rev-parse", "HEAD")
	require.NotEqual(t, remoteHead, head, "the commit must succeed before the push fails")

	retry := pushSummaryToLedger(summary, sessionDir)
	require.False(t, retry.Success, "an unchanged summary must not hide another push failure")
	assert.Contains(t, retry.Error, "git push")
	assert.Equal(t, head, runGit(t, clonePath, "rev-parse", "HEAD"))
	assert.Equal(t, remoteHead, runGit(t, barePath, "rev-parse", "HEAD"))
	assert.FileExists(t, rawPath, "failed retries must retain recovery content")
	assert.True(t, session.HasNeedsSummaryMarker(cacheDir), "failed retries must remain pending")

	runGit(t, clonePath, "remote", "set-url", "--push", "origin", barePath)
	retry = pushSummaryToLedger(summary, sessionDir)
	require.True(t, retry.Success, retry.Error)
	assert.Equal(t, head, runGit(t, clonePath, "rev-parse", "HEAD"), "a retry must not add a commit")
	assert.Equal(t, head, runGit(t, barePath, "rev-parse", "HEAD"), "success must mean the pending commit reached the remote")
	assert.NoDirExists(t, cacheDir, "recovery cleanup must wait for a successful push")
}

func TestPushSummary_RemoteAdvanceWithStagedPlan(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	other := cloneBare(t, barePath)
	require.NoError(t, os.WriteFile(filepath.Join(other, "remote.txt"), []byte("teammate change\n"), 0o644))
	runGit(t, other, "add", "remote.txt")
	runGit(t, other, "commit", "-m", "advance remote")
	runGit(t, other, "push")

	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxAutoStash")
	const plan = "data/plans/pln_test/plan.md"
	require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(plan)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clonePath, plan), []byte("# pending plan\n"), 0o644))
	runGit(t, clonePath, "add", plan)

	out := pushSummaryToLedger(pushSummaryFixture(t), sessionDir)
	require.True(t, out.Success, out.Error)
	assert.Equal(t, runGit(t, clonePath, "rev-parse", "HEAD"), runGit(t, barePath, "rev-parse", "HEAD"))
	assert.NotEmpty(t, runGit(t, barePath, "show", "HEAD:sessions/"+filepath.Base(sessionDir)+"/summary.json"))
	assert.Equal(t, "teammate change", runGit(t, clonePath, "show", "HEAD:remote.txt"))
	planContent, err := os.ReadFile(filepath.Join(clonePath, plan))
	require.NoError(t, err)
	assert.Equal(t, "# pending plan\n", string(planContent), "autostash must retain the plan content locally")
	assert.Empty(t, runGit(t, barePath, "ls-tree", "--name-only", "HEAD", "--", plan), "the pending plan must not be published")
}

// TestPushSummary_UnmergedIndexPointsAtDoctor: an index left unmerged outside a
// rebase (an autostash or merge conflict) blocks the snapshot commit. Failure
// prevented: the coworker getting a raw snapshot error with no way forward
// instead of the `ox doctor --fix` guidance (bd ox-v30g).
func TestPushSummary_UnmergedIndexPointsAtDoctor(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxUnm1")
	conflicted := filepath.Join(clonePath, "notes.txt")
	require.NoError(t, os.WriteFile(conflicted, []byte("base\n"), 0o644))
	runGit(t, clonePath, "add", "notes.txt")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "base")
	branch := runGit(t, clonePath, "branch", "--show-current")
	runGit(t, clonePath, "checkout", "-q", "-b", "other")
	require.NoError(t, os.WriteFile(conflicted, []byte("other\n"), 0o644))
	runGit(t, clonePath, "commit", "--no-verify", "-qam", "other")
	runGit(t, clonePath, "checkout", "-q", branch)
	require.NoError(t, os.WriteFile(conflicted, []byte("ours\n"), 0o644))
	runGit(t, clonePath, "commit", "--no-verify", "-qam", "ours")
	_, err := runGitCombined(clonePath, "merge", "other")
	require.Error(t, err, "the merge must conflict")
	require.False(t, gitutil.IsRebaseInProgress(clonePath))

	out := pushSummaryToLedger(pushSummaryFixture(t), sessionDir)

	require.False(t, out.Success)
	assert.Contains(t, out.Error, "ox doctor --fix")
}
