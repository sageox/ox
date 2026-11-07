package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented (sageox/ox#1505): redaction cleans the working files, the
// holding-commit amendment fails, and every later retry scans clean files, so
// HEAD keeps the credential and publication is held forever. The recorded
// repair must complete on retry, using only the bytes it produced, and must
// never amend a newer HEAD or replace another writer's staged work.
func TestPrePushGate_ResumesFailedRedactionAmend(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git amendment failure and retry")
	}
	const name = "2026-11-07-resume"
	// Synthetic key: AWS's documented EXAMPLE key is allowlisted by the
	// publication validator, which this test needs to see refuse.
	const canary = "AKIA" + "QWERTYUIOPASDFGH"
	rel := "sessions/" + name + "/raw.jsonl"

	setup := func(t *testing.T, prep func(work string)) (work, remote, holding string) {
		work = makeLedgerWithCommit(t, map[string]string{rel: `{"text":"` + canary + `"}` + "\n"})
		seedSessionMeta(t, filepath.Join(work, "sessions", name), name)
		mustGit(t, work, "add", "--sparse", "sessions/"+name+"/meta.json")
		mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")
		holding = mustGitCapture(t, work, "rev-parse", "HEAD")
		remote = mustGitCapture(t, work, "remote", "get-url", "origin")
		if prep != nil {
			prep(work)
		}

		// Blank committer identity so the scoped amendment cannot be written.
		t.Setenv("GIT_COMMITTER_NAME", "")
		err := runGate(t, work)
		require.Error(t, err, "amendment without a committer identity must fail")
		require.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		require.Contains(t, mustGitCapture(t, work, "show", "HEAD:"+rel), canary)
		raw, err := os.ReadFile(filepath.Join(work, rel))
		require.NoError(t, err)
		require.NotContains(t, string(raw), canary, "working file is redacted before the amendment")
		require.FileExists(t, pendingRecordPath(t, work))
		t.Setenv("GIT_COMMITTER_NAME", "ox-test")
		return work, remote, holding
	}

	t.Run("retry completes the repair", func(t *testing.T) {
		work, remote, holding := setup(t, nil)
		redacted := mustGitCapture(t, work, "hash-object", rel)
		// Without the record the outgoing-object validator holds publication.
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)

		// A later clean append belongs to the next snapshot, not this repair.
		f, err := os.OpenFile(filepath.Join(work, rel), os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.WriteString(`{"text":"later"}` + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		require.NoError(t, runGate(t, work))
		assert.NotEqual(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Equal(t, mustGitCapture(t, work, "rev-parse", holding+"^"), mustGitCapture(t, work, "rev-parse", "HEAD^"),
			"the holding commit is replaced, not extended")
		assert.Equal(t, redacted, mustGitCapture(t, work, "rev-parse", "HEAD:"+rel), "only the recorded bytes are amended")
		assert.Contains(t, mustGitCapture(t, work, "show", "HEAD:sessions/"+name+"/meta.json"), "redactions")
		assert.Equal(t, rel, mustGitCapture(t, work, "diff", "--name-only"), "the later append stays uncommitted")
		assert.Empty(t, mustGitCapture(t, work, "diff", "--cached", "--name-only"))
		assert.NoFileExists(t, pendingRecordPath(t, work))

		require.NoError(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assert.Equal(t, mustGitCapture(t, work, "rev-parse", "HEAD"), mustGitCapture(t, remote, "rev-parse", "main"))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})

	t.Run("newer HEAD is never amended", func(t *testing.T) {
		work, remote, _ := setup(t, nil)
		other := filepath.Join(work, "sessions", "other", "summary.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o755))
		require.NoError(t, os.WriteFile(other, []byte("unrelated\n"), 0o644))
		mustGit(t, work, "add", "--sparse", "sessions/other/summary.md")
		mustGit(t, work, "commit", "-m", "newer snapshot", "--no-verify")
		newer := mustGitCapture(t, work, "rev-parse", "HEAD")

		err := runGate(t, work)
		require.ErrorContains(t, err, "newer Ledger commits")
		assert.Equal(t, newer, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.FileExists(t, pendingRecordPath(t, work))
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)

		// Once history no longer contains the holding commit the record is obsolete.
		mustGit(t, work, "reset", "--hard", "origin/main")
		require.NoError(t, runGate(t, work))
		assert.NoFileExists(t, pendingRecordPath(t, work))
	})

	t.Run("another writer's staged bytes are kept", func(t *testing.T) {
		work, remote, holding := setup(t, nil)
		require.NoError(t, os.WriteFile(filepath.Join(work, rel), []byte(`{"text":"other writer"}`+"\n"), 0o644))
		mustGit(t, work, "add", "--sparse", rel)
		staged := mustGitCapture(t, work, "rev-parse", ":"+rel)

		err := runGate(t, work)
		require.ErrorContains(t, err, "refusing to replace staged changes")
		assert.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Equal(t, staged, mustGitCapture(t, work, "rev-parse", ":"+rel))
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})

	t.Run("unstaging a mode change before retry still completes the repair", func(t *testing.T) {
		work, remote, _ := setup(t, func(work string) {
			mustGit(t, work, "update-index", "--chmod=+x", "--", rel)
		})
		mustGit(t, work, "reset", "-q", "--", rel)

		require.NoError(t, runGate(t, work))
		assert.Contains(t, mustGitCapture(t, work, "ls-tree", "HEAD", "--", rel), "100644 blob ", "the withdrawn mode change is not committed")
		assert.NoFileExists(t, pendingRecordPath(t, work))
		require.NoError(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})

	t.Run("another writer's staged mode is kept", func(t *testing.T) {
		work, remote, holding := setup(t, nil)
		mustGit(t, work, "update-index", "--chmod=+x", "--", rel)

		err := runGate(t, work)
		require.ErrorContains(t, err, "refusing to replace staged changes")
		assert.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Contains(t, mustGitCapture(t, work, "ls-files", "--stage", "--", rel), "100755")
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})
}

func runGate(t *testing.T, work string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return gitutil.WithRepoLock(ctx, work, func() error {
		return runPrePushSecretGate(ctx, work)
	})
}

func pendingRecordPath(t *testing.T, work string) string {
	t.Helper()
	p, err := pendingRedactionAmendPath(context.Background(), work)
	require.NoError(t, err)
	require.NotContains(t, p, filepath.Join(work, ".sageox"), "the record must not live in Ledger content")
	return p
}

// Failure prevented: a recovery record that cannot be replayed safely must
// never amend HEAD. An unusable or obsolete record is dropped so it cannot hold
// publication forever; a record whose repair bytes are gone keeps publication
// held, since the outgoing-object validator alone cannot repair HEAD.
func TestResumePendingRedactionAmend_RecordsThatCannotReplay(t *testing.T) {
	const rel = "sessions/s/raw.jsonl"
	valid := func(tip, blob string) []byte {
		buf, err := json.Marshal(pendingRedactionAmend{HoldingTip: tip, Paths: []pendingRedactionPath{{Path: rel, Mode: "100644", Blob: blob}}})
		require.NoError(t, err)
		return buf
	}
	missingOID := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name    string
		record  func(work, head string) []byte
		prep    func(t *testing.T, work string)
		wantErr string
		kept    bool
	}{
		{name: "unreadable JSON is dropped", record: func(_, _ string) []byte { return []byte("{") }},
		{name: "record without paths is dropped", record: func(_, head string) []byte { return []byte(`{"holding_tip":"` + head + `"}`) }},
		{name: "path that cannot be staged literally is dropped", record: func(_, head string) []byte {
			buf, err := json.Marshal(pendingRedactionAmend{HoldingTip: head, Paths: []pendingRedactionPath{{Path: "a\tb", Mode: "100644", Blob: missingOID}}})
			require.NoError(t, err)
			return buf
		}},
		{name: "holding commit absent from the repository is dropped", record: func(work, _ string) []byte {
			return valid(missingOID, mustGitCapture(t, work, "rev-parse", "HEAD:"+rel))
		}},
		{name: "holding commit outside HEAD's history is dropped", record: func(work, _ string) []byte {
			side := mustGitCapture(t, work, "commit-tree", "-p", "HEAD~1", "-m", "side", "HEAD^{tree}")
			return valid(side, mustGitCapture(t, work, "rev-parse", "HEAD:"+rel))
		}},
		{name: "missing repair bytes keep publication held", record: func(_, head string) []byte {
			return valid(head, missingOID)
		}, wantErr: "missing from the object store", kept: true},
		{name: "unresolved conflicts keep publication held", record: func(work, head string) []byte {
			return valid(head, mustGitCapture(t, work, "rev-parse", "HEAD:"+rel))
		}, prep: func(t *testing.T, work string) { conflictIndexEntry(t, work, rel) }, wantErr: "unresolved conflicts", kept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := makeLedgerWithCommit(t, map[string]string{rel: "{}\n"})
			head := mustGitCapture(t, work, "rev-parse", "HEAD")
			path := pendingRecordPath(t, work)
			require.NoError(t, os.WriteFile(path, tt.record(work, head), 0o600))
			if tt.prep != nil {
				tt.prep(t, work)
			}

			resumed, err := resumePendingRedactionAmend(context.Background(), work)
			assert.False(t, resumed)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, head, mustGitCapture(t, work, "rev-parse", "HEAD"), "HEAD is never amended")
			if tt.kept {
				assert.FileExists(t, path)
			} else {
				assert.NoFileExists(t, path)
			}
		})
	}

	t.Run("outside a Git repository fails closed", func(t *testing.T) {
		_, err := resumePendingRedactionAmend(context.Background(), t.TempDir())
		require.ErrorContains(t, err, "resolve pending redaction path")
	})

	t.Run("unborn HEAD keeps publication held", func(t *testing.T) {
		work := t.TempDir()
		mustGit(t, work, "init", "--initial-branch=main")
		path := pendingRecordPath(t, work)
		require.NoError(t, os.WriteFile(path, valid(missingOID, missingOID), 0o600))
		_, err := resumePendingRedactionAmend(context.Background(), work)
		require.ErrorContains(t, err, "resolve Ledger HEAD")
		assert.FileExists(t, path)
	})
}

// Failure prevented: a repair that cannot be recorded exactly must fail before
// anything is written, and a repair file new to the holding commit (such as a
// freshly written meta.json) must still be amended in.
func TestStageAndAmendRedactedFiles_RecordsExactRepair(t *testing.T) {
	const rel = "sessions/s/raw.jsonl"
	t.Run("file new to the holding commit is amended in", func(t *testing.T) {
		work := makeLedgerWithCommit(t, map[string]string{rel: "{}\n"})
		holding := mustGitCapture(t, work, "rev-parse", "HEAD")
		added := "sessions/s/meta.json"
		require.NoError(t, os.WriteFile(filepath.Join(work, added), []byte("{}\n"), 0o644))

		require.NoError(t, stageAndAmendRedactedFiles(context.Background(), work, []string{added, added}))
		assert.Equal(t, mustGitCapture(t, work, "rev-parse", holding+"~1"), mustGitCapture(t, work, "rev-parse", "HEAD~1"), "HEAD is the amended holding commit")
		assert.Equal(t, mustGitCapture(t, work, "hash-object", filepath.Join(work, added)), mustGitCapture(t, work, "rev-parse", "HEAD:"+added))
		assert.NoFileExists(t, pendingRecordPath(t, work))
	})

	tests := []struct {
		name    string
		prep    func(t *testing.T, work string)
		paths   []string
		wantErr string
	}{
		{name: "vanished repair file", paths: []string{"sessions/s/gone.jsonl"}, wantErr: "record redacted"},
		{name: "conflicted repair path", paths: []string{rel}, prep: func(t *testing.T, work string) { conflictIndexEntry(t, work, rel) }, wantErr: "unexpected index entry"},
		{name: "unreplayable earlier record", paths: []string{rel}, prep: func(t *testing.T, work string) {
			head := mustGitCapture(t, work, "rev-parse", "HEAD")
			buf, err := json.Marshal(pendingRedactionAmend{HoldingTip: head, Paths: []pendingRedactionPath{{Path: rel, Mode: "100644", Blob: "0123456789abcdef0123456789abcdef01234567"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(pendingRecordPath(t, work), buf, 0o600))
		}, wantErr: "missing from the object store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := makeLedgerWithCommit(t, map[string]string{rel: "{}\n"})
			head := mustGitCapture(t, work, "rev-parse", "HEAD")
			if tt.prep != nil {
				tt.prep(t, work)
			}
			_, before := os.Stat(pendingRecordPath(t, work))

			require.ErrorContains(t, stageAndAmendRedactedFiles(context.Background(), work, tt.paths), tt.wantErr)
			assert.Equal(t, head, mustGitCapture(t, work, "rev-parse", "HEAD"), "HEAD is never amended")
			_, after := os.Stat(pendingRecordPath(t, work))
			assert.Equal(t, before == nil, after == nil, "no new record is written")
		})
	}

	t.Run("outside a Git repository fails closed", func(t *testing.T) {
		require.ErrorContains(t, stageAndAmendRedactedFiles(context.Background(), t.TempDir(), []string{rel}), "resolve pending redaction path")
	})

	t.Run("unborn HEAD fails closed", func(t *testing.T) {
		work := t.TempDir()
		mustGit(t, work, "init", "--initial-branch=main")
		require.ErrorContains(t, stageAndAmendRedactedFiles(context.Background(), work, []string{rel}), "resolve holding commit")
	})
}

// conflictIndexEntry replaces rel's index entry with unmerged stages.
func conflictIndexEntry(t *testing.T, work, rel string) {
	t.Helper()
	blob := mustGitCapture(t, work, "rev-parse", "HEAD:"+rel)
	mustGit(t, work, "rm", "--cached", "-q", "--", rel)
	cmd := exec.Command("git", "-C", work, "update-index", "--index-info")
	cmd.Stdin = strings.NewReader("100644 " + blob + " 1\t" + rel + "\n100644 " + blob + " 2\t" + rel + "\n100644 " + blob + " 3\t" + rel + "\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestPendingRedactionOwnsIndexEntry(t *testing.T) {
	tracked := pendingRedactionPath{Mode: "100644", Blob: "repair", HoldingMode: "100644", HoldingBlob: "holding"}
	added := pendingRedactionPath{Mode: "100644", Blob: "repair"}
	tests := []struct {
		name       string
		p          pendingRedactionPath
		mode, blob string
		want       bool
	}{
		{"holding entry", tracked, "100644", "holding", true},
		{"recorded repair already staged", tracked, "100644", "repair", true},
		{"another writer's bytes", tracked, "100644", "other", false},
		{"another writer's mode", tracked, "100755", "holding", false},
		{"another writer unstaged the path", tracked, "", "", false},
		{"path new in the holding commit, absent from the index", added, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pendingRedactionOwnsIndexEntry(tt.p, tt.mode, tt.blob))
		})
	}
}
