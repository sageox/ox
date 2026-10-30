package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented across this file (ox-y3ok): the pre-push secret gate
// repairs content before publication. Auto-redact what it can, quarantine what it
// cannot safely rewrite. Publication proceeds only after clean verification.

// seedSessionMeta writes a minimal valid meta.json at sessions/<name>/.
// Required because the canonical RedactionPass writer
// (writeRedactionPassesPerSession → lfs.MutateSessionMeta) refuses to
// synthesize a meta.json from scratch — see the comment at meta.go.
func seedSessionMeta(t *testing.T, sessionDir, sessionName string) {
	t.Helper()
	meta := lfs.SessionMeta{
		Version:     "1.0",
		SessionName: sessionName,
		AgentID:     "test-agent",
		AgentType:   "claude-code",
		CreatedAt:   time.Now().UTC(),
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "raw.jsonl")); err == nil {
		meta.Files = map[string]lfs.FileRef{"raw.jsonl": {Storage: lfs.StorageGit}}
	}
	buf, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), buf, 0o644))
}

// TestRunPrePushSecretGate_AutoRedactsJSONLAndAllowsPush is the happy
// path of the recovery pipeline: a JSONL session file with a planted
// canary gets rewritten through the canonical chokepoint, a
// RedactionPass lands in meta.json, the holding commit is amended,
// and the gate returns nil so the push proceeds.
//
// Failure prevented: a single session with a chokepoint-recoverable
// finding blocks an otherwise repairable push. The repair must remain
// idempotent while preserving the final verification requirement.
func TestRunPrePushSecretGate_AutoRedactsJSONLAndAllowsPush(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "") // ensure no override
	const sessionName = "2026-05-12-autoredact"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	rec := `{"type":"text","value":"` + canary + `"}` + "\n"

	work := makeLedgerWithCommit(t, map[string]string{
		filepath.ToSlash(filepath.Join("sessions", sessionName, "raw.jsonl")): rec,
	})
	seedSessionMeta(t, filepath.Join(work, "sessions", sessionName), sessionName)
	mustGit(t, work, "add", "--sparse", filepath.Join("sessions", sessionName, "meta.json"))
	mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")
	identity := mustGitCapture(t, work, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD")
	planPath := filepath.Join(work, "data", "plans", "unfinished", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planPath), 0o755))
	require.NoError(t, os.WriteFile(planPath, []byte("staged plan\n"), 0o644))
	mustGit(t, work, "add", "--sparse", planPath)
	require.NoError(t, os.WriteFile(planPath, []byte("later edit\n"), 0o644))
	const pending = "sessions/pending/meta.json"
	seedSessionMeta(t, filepath.Join(work, "sessions", "pending"), "pending")
	mustGit(t, work, "add", "--sparse", pending)

	// Match pushLedger: the gate repairs commits while holding the repo lock.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := gitutil.WithRepoLock(ctx, work, func() error {
		return runPrePushSecretGate(ctx, work)
	})
	require.NoError(t, err, "verified repaired content must pass")
	assert.Empty(t, mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", "data/plans"))
	assert.Equal(t, "staged plan", mustGitCapture(t, work, "show", ":data/plans/unfinished/plan.md"))
	assert.Equal(t, identity, mustGitCapture(t, work, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD"))
	assert.Empty(t, mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", pending))
	assert.Contains(t, mustGitCapture(t, work, "diff", "--cached", "--name-only"), pending)

	// raw.jsonl on disk must no longer contain the canary bytes.
	rawAbs := filepath.Join(work, "sessions", sessionName, "raw.jsonl")
	bytes, readErr := os.ReadFile(rawAbs)
	require.NoError(t, readErr)
	assert.NotContains(t, string(bytes), canary,
		"canary still on disk; auto-redact did not run or did not scrub")
	assert.NotContains(t, mustGitCapture(t, work, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), canary,
		"publication must carry the repaired blob, not only a clean worktree")

	// Re-scan must come back clean — the chokepoint and the scanner
	// share a detector catalog; if redact says it cleared, scan must
	// agree, otherwise we'd loop forever on a future iteration.
	rescan, scanErr := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, scanErr)
	assert.Empty(t, rescan.Findings, "scanner still finds the canary after auto-redact: %v", rescan.Findings)

	// meta.json must carry a RedactionPass entry recording the catalog
	// identity and the per-line finding metadata (file/line/detector).
	metaBuf, err := os.ReadFile(filepath.Join(work, "sessions", sessionName, "meta.json"))
	require.NoError(t, err)
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal(metaBuf, &meta))
	require.NotEmpty(t, meta.Redactions, "meta.json missing RedactionPass entry")
	pass := meta.Redactions[len(meta.Redactions)-1]
	assert.NotEmpty(t, pass.PassID)
	assert.NotEmpty(t, pass.CatalogVersion)
	assert.NotEmpty(t, pass.CatalogHash)
	assert.GreaterOrEqual(t, len(pass.Entries), 1, "expected at least one per-line entry")
	for _, e := range pass.Entries {
		assert.Equal(t, "raw.jsonl", e.File)
		assert.NotEmpty(t, e.Detector)
		// MUST NEVER carry matched bytes — ox-zyg7.
		assert.NotContains(t, e.Detector, canary)
	}

	// The next normal snapshot must still pick up the other session, while
	// leaving the plan alone. Verify both sessions actually reach the remote.
	require.NoError(t, gitutil.WithRepoLock(ctx, work, func() error {
		_, err := gitutil.CommitLedgerSnapshot(ctx, work, "recover pending session", ".", ":(exclude)data/plans")
		return err
	}))
	require.NoError(t, gitutil.PushWithRetry(ctx, work, gitutil.PushOpts{}))
	remote := mustGitCapture(t, work, "remote", "get-url", "origin")
	assert.NotEmpty(t, mustGitCapture(t, remote, "show", "main:"+pending))
	assert.NotContains(t, mustGitCapture(t, remote, "show", "main:sessions/"+sessionName+"/raw.jsonl"), canary)
	assert.Empty(t, mustGitCapture(t, remote, "ls-tree", "--name-only", "main", "data/plans"))
	plan, err := os.ReadFile(planPath)
	require.NoError(t, err)
	assert.Equal(t, "later edit\n", string(plan))
}

// Repairs must preserve an unfinished rebase or autostash conflict and resume
// after it is resolved, without staging over the unresolved index entries.
func TestRedactionRepairs_RefuseConflictsAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git redaction repair and retry")
	}
	for _, tc := range []struct {
		name, file  string
		autostash   bool
		interactive bool
	}{
		{"rebase JSONL", "raw.jsonl", false, false},
		{"rebase notes", "notes.md", false, false},
		{"autostash conflict", "raw.jsonl", true, false},
		{"interactive rebase", "raw.jsonl", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const name = "2026-10-22-repair"
			const canary = "AKIAIOSFODNN7EXAMPLE"
			rel := "sessions/" + name + "/" + tc.file
			work := makeLedgerWithCommit(t, map[string]string{rel: `{"text":"` + canary + `"}` + "\n"})
			seedSessionMeta(t, filepath.Join(work, "sessions", name), name)
			mustGit(t, work, "add", "--sparse", "sessions/"+name+"/meta.json")
			mustGit(t, work, "commit", "--amend", "--no-edit")
			rebase := filepath.Join(work, ".git", "rebase-merge")
			if tc.autostash {
				require.NoError(t, os.WriteFile(filepath.Join(work, rel), []byte(`{"text":"local `+canary+`"}`+"\n"), 0o644))
				mustGit(t, work, "stash", "push")
				require.NoError(t, os.WriteFile(filepath.Join(work, rel), []byte(`{"text":"upstream `+canary+`"}`+"\n"), 0o644))
				mustGit(t, work, "commit", "-am", "upstream change")
				output, err := exec.Command("git", "-C", work, "stash", "pop").CombinedOutput()
				require.Error(t, err)
				require.Contains(t, string(output), "CONFLICT")
				require.Contains(t, mustGitCapture(t, work, "ls-files", "--unmerged"), rel)
				require.NoFileExists(t, filepath.Join(work, ".git", "MERGE_HEAD"))
			} else {
				require.NoError(t, os.MkdirAll(rebase, 0o755))
			}
			tip := mustGitCapture(t, work, "rev-parse", "HEAD")
			index := mustGitCapture(t, work, "ls-files", "--stage")
			before, err := os.ReadFile(filepath.Join(work, rel))
			require.NoError(t, err)
			repair := func() error {
				if tc.interactive {
					return runRedactHistoryWorkflow(redactHistoryOptions{
						LedgerPath: work,
						BackupDir:  t.TempDir(),
						Scope:      &sessionScope{Names: []string{name}},
						Stdin:      strings.NewReader("y\n"),
						Stdout:     &strings.Builder{},
					})
				}
				return gitutil.WithRepoLock(context.Background(), work, func() error {
					return runPrePushSecretGate(context.Background(), work)
				})
			}
			err = repair()
			require.Error(t, err)
			if tc.autostash {
				require.ErrorContains(t, err, "unresolved conflicts")
			}
			assert.Equal(t, tip, mustGitCapture(t, work, "rev-parse", "HEAD"))
			assert.Equal(t, index, mustGitCapture(t, work, "ls-files", "--stage"))
			content, err := os.ReadFile(filepath.Join(work, rel))
			require.NoError(t, err)
			assert.Equal(t, before, content, "refusal must precede worktree mutation")
			if tc.autostash {
				mustGit(t, work, "restore", "--source=HEAD", "--staged", "--worktree", "--", rel)
			} else {
				require.NoError(t, os.RemoveAll(rebase))
			}
			require.NoError(t, repair())
			if tc.file == "raw.jsonl" {
				assert.NotContains(t, mustGitCapture(t, work, "show", "HEAD:"+rel), canary)
			} else {
				assert.Empty(t, mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", rel))
			}
		})
	}
}

func TestRunPrePushSecretGate_QuarantinesNonJSONLAndAllowsPush(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	const sessionName = "2026-05-12-quarantine"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	relMD := filepath.ToSlash(filepath.Join("sessions", sessionName, "notes[1].md"))
	neighbor := filepath.ToSlash(filepath.Join("sessions", sessionName, "notes1.md"))

	work := makeLedgerWithCommit(t, map[string]string{
		// Also include an UNRELATED file in the same commit so we can
		// assert that the rest of the push survives.
		"unrelated.txt": "hello world\n",
		relMD:           "# Notes\n\nfound a token: " + canary + "\n",
		neighbor:        "committed notes\n",
	})
	seedSessionMeta(t, filepath.Join(work, "sessions", sessionName), sessionName)
	mustGit(t, work, "add", "--sparse", filepath.Join("sessions", sessionName, "meta.json"))
	mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")
	identity := mustGitCapture(t, work, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD")
	planPath := filepath.Join(work, "data", "plans", "unfinished", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planPath), 0o755))
	require.NoError(t, os.WriteFile(planPath, []byte("staged plan\n"), 0o644))
	mustGit(t, work, "add", "--sparse", planPath)
	require.NoError(t, os.WriteFile(filepath.Join(work, neighbor), []byte("pending notes\n"), 0o644))
	mustGit(t, work, "add", "--", neighbor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := gitutil.WithRepoLock(ctx, work, func() error {
		return runPrePushSecretGate(ctx, work)
	})
	require.NoError(t, err, "verified quarantine must permit safe publication")
	assert.Empty(t, mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", "data/plans"))
	assert.Equal(t, "staged plan", mustGitCapture(t, work, "show", ":data/plans/unfinished/plan.md"))
	assert.Equal(t, identity, mustGitCapture(t, work, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD"))
	assert.Equal(t, "pending notes", mustGitCapture(t, work, "show", ":"+neighbor), "quarantine must treat brackets literally, preserving neighboring staging")
	assert.Equal(t, "committed notes", mustGitCapture(t, work, "show", "HEAD:"+neighbor))

	// Original in-place path must be gone (moved aside).
	_, statErr := os.Stat(filepath.Join(work, relMD))
	assert.True(t, os.IsNotExist(statErr),
		"original notes[1].md should have been moved to quarantine; stat err=%v", statErr)

	// Quarantine path must hold the bytes.
	quarRel := filepath.Join(".sageox", "cache", "quarantine", sessionName, "notes[1].md")
	quarAbs := filepath.Join(work, quarRel)
	bytes, readErr := os.ReadFile(quarAbs)
	require.NoError(t, readErr, "quarantine path missing")
	assert.Contains(t, string(bytes), canary,
		"bytes must be preserved verbatim in quarantine; the point of quarantine is no data loss")

	// Debt marker must exist and parse.
	markerRel := filepath.Join(".sageox", "cache", "redaction-debt", sessionName+".json")
	markerBuf, err := os.ReadFile(filepath.Join(work, markerRel))
	require.NoError(t, err, "debt marker missing")
	var rec redactionDebtRecord
	require.NoError(t, json.Unmarshal(markerBuf, &rec))
	assert.Equal(t, sessionName, rec.SessionName)
	require.NotEmpty(t, rec.Findings, "marker missing findings")
	require.NotEmpty(t, rec.QuarantinePaths, "marker missing quarantine path mapping")
	assert.Equal(t, relMD, rec.QuarantinePaths[0].From)
	assert.Equal(t, filepath.ToSlash(quarRel), rec.QuarantinePaths[0].To)

	// The holding commit must NOT carry the quarantined path anymore.
	mustGit(t, work, "rev-parse", "HEAD")
	if mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", filepath.ToSlash(relMD)) != "" {
		t.Fatalf("notes.md still tracked at HEAD; carve-out failed")
	}
	// And the rest of the commit must survive: unrelated.txt and the
	// session's meta.json should still be in HEAD's tree.
	if mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", "unrelated.txt") == "" {
		t.Fatalf("unrelated.txt missing from HEAD; quarantine over-reached")
	}
}

// A later failure must not hide files already moved to quarantine from either
// doctor's debt summary or the session redaction recovery command.
func TestQuarantineBeforeUpload_PartialFailureRemainsDiscoverable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and quarantine recovery")
	}
	for _, tc := range []struct {
		name      string
		failOp    string
		untracked bool
		staged    bool
	}{
		{name: "index locked", failOp: "rm"},
		{name: "rename blocked"},
		{name: "rename blocked with staged changes", staged: true},
		{name: "rename blocked for untracked file", untracked: true},
		{name: "index read failed", failOp: "ls-files"},
		{name: "index restore failed", failOp: "update-index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.failOp != "" && runtime.GOOS == "windows" {
				t.Skip("git failure injection uses a POSIX wrapper")
			}
			const sessionName = "2026-05-12-partial-quarantine"
			firstRel := "sessions/" + sessionName + "/notes.md"
			failedRel := "sessions/" + sessionName + "/summary.md"
			const content = "preserve this quarantined session content\n"
			const workingContent = "failed file remains here\n"
			files := map[string]string{firstRel: content, "unrelated.txt": "keep\n"}
			if !tc.untracked {
				files[failedRel] = workingContent
			}
			work := makeLedgerWithCommit(t, files)
			failedAbs := filepath.Join(work, failedRel)
			if tc.staged {
				require.NoError(t, os.WriteFile(failedAbs, []byte("different staged content\n"), 0o600))
				mustGit(t, work, "add", "--", failedRel)
				mustGit(t, work, "update-index", "--chmod=+x", "--", failedRel)
			}
			require.NoError(t, os.WriteFile(failedAbs, []byte(workingContent), 0o600))
			headBefore := mustGitCapture(t, work, "rev-parse", "HEAD")
			indexBefore := mustGitCapture(t, work, "ls-files", "--stage", "-z", "--", failedRel)
			quarantineDir := filepath.Join(work, ".sageox", "cache", "quarantine", sessionName)
			if tc.failOp == "" || tc.failOp == "update-index" {
				require.NoError(t, os.MkdirAll(filepath.Join(quarantineDir, "summary.md"), 0o700))
			}
			previousPath := os.Getenv("PATH")
			if tc.failOp != "" {
				realGit, err := exec.LookPath("git")
				require.NoError(t, err)
				binDir := t.TempDir()
				// Real git handles every operation. The later read uses a broken
				// alternate index; removal/restore failures use a competing lock.
				wrapper := "#!/bin/sh\nfor arg do\n  last=\"$arg\"\n  case \"$arg\" in ls-files|rm|update-index) op=\"$arg\";; esac\ndone\n" +
					"if [ \"$op\" = \"$OX_TEST_QUARANTINE_FAIL_OP\" ] && { [ \"$op\" = update-index ] || [ \"$last\" = \"$OX_TEST_QUARANTINE_FAIL_PATH\" ]; }; then\n" +
					"  if [ \"$op\" = ls-files ]; then\n" +
					"    GIT_INDEX_FILE=\"$OX_TEST_QUARANTINE_BAD_INDEX\" exec \"$OX_TEST_QUARANTINE_REAL_GIT\" \"$@\"\n  fi\n" +
					"  : > \"$OX_TEST_QUARANTINE_LOCK\"\nfi\n" +
					"exec \"$OX_TEST_QUARANTINE_REAL_GIT\" \"$@\"\n"
				require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755))
				badIndex := filepath.Join(binDir, "bad-index")
				require.NoError(t, os.WriteFile(badIndex, []byte("invalid index"), 0o600))
				t.Setenv("OX_TEST_QUARANTINE_BAD_INDEX", badIndex)
				t.Setenv("OX_TEST_QUARANTINE_FAIL_OP", tc.failOp)
				t.Setenv("OX_TEST_QUARANTINE_FAIL_PATH", ":(literal)"+failedRel)
				t.Setenv("OX_TEST_QUARANTINE_LOCK", filepath.Join(work, ".git", "index.lock"))
				t.Setenv("OX_TEST_QUARANTINE_REAL_GIT", realGit)
				t.Setenv("PATH", binDir+string(os.PathListSeparator)+previousPath)
			}

			out, err := quarantineUnredactableFindings(context.Background(), work, []PrePushFinding{
				{Path: firstRel, Line: 1, Detector: "aws_access_key"},
				{Path: failedRel, Line: 1, Detector: "aws_access_key"},
			}, false)
			require.Error(t, err)
			switch tc.failOp {
			case "rm":
				assert.ErrorContains(t, err, "remove quarantined path from index")
			case "ls-files":
				assert.ErrorContains(t, err, "read quarantined path from index")
			default:
				assert.ErrorContains(t, err, "quarantine "+failedRel)
				var renameErr *os.LinkError
				assert.ErrorAs(t, err, &renameErr, "the original rename error must remain available")
				if tc.failOp == "update-index" {
					assert.ErrorContains(t, err, "restore quarantined path in index")
					var restoreErr *exec.ExitError
					assert.ErrorAs(t, err, &restoreErr, "restore failure must not be hidden by the rename error")
				}
			}
			if assert.NotNil(t, out, "partial quarantine results must survive the error") {
				assert.Equal(t, []string{firstRel}, out.QuarantinedRels)
				assert.Equal(t, []string{".sageox/cache/redaction-debt/" + sessionName + ".json"}, out.DebtMarkers)
			}
			preserved, readErr := os.ReadFile(filepath.Join(quarantineDir, "notes.md"))
			require.NoError(t, readErr)
			assert.Equal(t, content, string(preserved))
			require.NoFileExists(t, filepath.Join(work, firstRel))
			remaining, readErr := os.ReadFile(failedAbs)
			require.NoError(t, readErr)
			assert.Equal(t, workingContent, string(remaining), "the failed move must leave its source intact")
			t.Setenv("PATH", previousPath)
			assert.Equal(t, headBefore, mustGitCapture(t, work, "rev-parse", "HEAD"), "pre-upload quarantine must not amend a commit")
			indexAfter := mustGitCapture(t, work, "ls-files", "--stage", "-z", "--", failedRel)
			if tc.failOp == "update-index" {
				assert.Empty(t, indexAfter, "the reported restore failure must have happened")
			} else {
				assert.Equal(t, indexBefore, indexAfter, "failed quarantine must restore the exact prior index entries, not stage working bytes")
			}

			summaries, malformed := readDebtSummaries(filepath.Join(work, ".sageox", "cache", "redaction-debt"))
			assert.Empty(t, malformed)
			require.Len(t, summaries, 1, "doctor must discover the earlier successful quarantine")
			assert.Equal(t, sessionName, summaries[0].session)
			assert.Equal(t, 1, summaries[0].files)
			assert.Equal(t, 1, summaries[0].findings)
			recovery, recoveryErr := enumerateQuarantineFindings(work, nil)
			require.NoError(t, recoveryErr, "failed candidates must not become unmapped findings in the debt marker")
			require.Len(t, recovery, 1, "session redaction must discover the preserved file")
			assert.Equal(t, firstRel, recovery[0].Path)
			assert.Equal(t, filepath.ToSlash(filepath.Join(".sageox", "cache", "quarantine", sessionName, "notes.md")), recovery[0].QuarantinePath)
		})
	}
}

// A canceled Git operation must release the Ledger lock and leave the session
// recoverable, whether quarantine runs before upload or repairs a holding commit.
func TestQuarantineCancellationReleasesLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git subprocess cancellation and recovery")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Git stall injection uses a POSIX wrapper")
	}
	for _, op := range []string{"ls-files", "rm", "reset"} {
		t.Run(op, func(t *testing.T) {
			amend := op == "reset"
			const name = "2026-10-29-canceled-quarantine"
			const rel = "sessions/" + name + "/notes.md"
			const content = "keep these session bytes\n"
			work := makeLedgerWithCommit(t, map[string]string{rel: content})
			tip := mustGitCapture(t, work, "rev-parse", "HEAD")
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			binDir := t.TempDir()
			started := filepath.Join(binDir, "started")
			wrapper := "#!/bin/sh\nfor arg do\n  last=\"$arg\"\n" +
				"  if [ \"$arg\" = \"$OX_TEST_QUARANTINE_STALL\" ]; then matched=1; fi\ndone\n" +
				"if [ \"$matched\" = 1 ] && [ \"$last\" = \"$OX_TEST_QUARANTINE_PATH\" ]; then\n" +
				"  echo $$ > \"$OX_TEST_QUARANTINE_STARTED\"\n  exec sleep 30\nfi\n" +
				"exec \"$OX_TEST_QUARANTINE_REAL_GIT\" \"$@\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755))
			t.Setenv("OX_TEST_QUARANTINE_REAL_GIT", realGit)
			t.Setenv("OX_TEST_QUARANTINE_STARTED", started)
			t.Setenv("OX_TEST_QUARANTINE_STALL", op)
			t.Setenv("OX_TEST_QUARANTINE_PATH", ":(literal)"+rel)
			previousPath := os.Getenv("PATH")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+previousPath)
			completed := false
			done := make(chan error, 1)
			t.Cleanup(func() {
				if completed {
					return
				}
				data, _ := os.ReadFile(started)
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					process, _ := os.FindProcess(pid)
					_ = process.Kill()
					<-done
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			findings := []PrePushFinding{{Path: rel, Line: 1, Detector: "test"}}
			go func() {
				repair := func() error {
					_, err := quarantineUnredactableFindings(ctx, work, findings, amend)
					return err
				}
				if amend {
					done <- gitutil.WithRepoLock(ctx, work, repair)
				} else {
					done <- repair()
				}
			}()
			require.Eventually(t, func() bool {
				_, err := os.Stat(started)
				return err == nil
			}, 5*time.Second, 10*time.Millisecond, "quarantine must reach the Git operation")
			cancel()
			select {
			case err := <-done:
				completed = true
				require.Error(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("canceled quarantine kept the Ledger locked")
			}
			t.Setenv("PATH", previousPath)
			lockCtx, unlock := context.WithTimeout(context.Background(), time.Second)
			defer unlock()
			require.NoError(t, gitutil.WithRepoLock(lockCtx, work, func() error { return nil }))
			assert.Equal(t, tip, mustGitCapture(t, work, "rev-parse", "HEAD"))
			preserved := filepath.Join(work, rel)
			if amend {
				preserved = filepath.Join(work, ".sageox", "cache", "quarantine", name, "notes.md")
				recovery, err := enumerateQuarantineFindings(work, nil)
				require.NoError(t, err)
				require.Len(t, recovery, 1)
				// Put the preserved file back to retry the interrupted repair.
				require.NoError(t, os.Rename(preserved, filepath.Join(work, rel)))
				preserved = filepath.Join(work, rel)
			}
			data, err := os.ReadFile(preserved)
			require.NoError(t, err)
			assert.Equal(t, content, string(data))
			retry := func() error {
				_, err := quarantineUnredactableFindings(context.Background(), work, findings, amend)
				return err
			}
			if amend {
				err = gitutil.WithRepoLock(context.Background(), work, retry)
			} else {
				err = retry()
			}
			require.NoError(t, err, "a subsequent repair must still work")
		})
	}
}

// A legacy override environment cannot suppress automatic redaction.
func TestRunPrePushSecretGate_OverrideCannotSkipRecovery(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	const sessionName = "2026-05-12-override"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	relJSONL := filepath.ToSlash(filepath.Join("sessions", sessionName, "raw.jsonl"))
	rec := `{"type":"text","value":"` + canary + `"}` + "\n"
	work := makeLedgerWithCommit(t, map[string]string{relJSONL: rec})
	seedSessionMeta(t, filepath.Join(work, "sessions", sessionName), sessionName)
	mustGit(t, work, "add", "--sparse", filepath.Join("sessions", sessionName, "meta.json"))
	mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")
	require.NoError(t, runPrePushSecretGate(context.Background(), work))
	data, err := os.ReadFile(filepath.Join(work, relJSONL))
	require.NoError(t, err)
	assert.NotContains(t, string(data), canary, "legacy override must not publish sensitive bytes")
}

// mustGitCapture runs git and returns trimmed stdout. Helper for tests
// that need to assert on tree contents.
func mustGitCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := captureGit(dir, args...)
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(out)
}

func captureGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out), err
}
