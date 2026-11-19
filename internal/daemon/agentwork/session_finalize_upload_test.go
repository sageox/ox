package agentwork

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// rawContent is a minimal valid session with substantive entries.
const testRawContent = `{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"testuser","agent_id":"OxTEST"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"world","seq":2}
{"entry_count":2}
`

// --- A. Detect: fully-finalized-but-not-pushed sessions ---
// TestDetect_FullyFinalizedInCache_NotPushed verifies that Detect() queues sessions
// that have all artifacts in the ledger cache but are absent from ledger/sessions/.
// Failure prevented: such sessions were silently skipped because missingArtifacts()
// returned [] — they accumulated indefinitely without being committed/pushed.
func TestDetect_FullyFinalizedInCache_NotPushed(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	// create a session in ledger cache with ALL artifacts present
	sessionName := "2026-01-10T12-00-testuser-OxCACHE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 upload-only item, got %d", len(items))
	}
	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", items[0].Payload)
	}
	if !payload.UploadOnly {
		t.Error("expected UploadOnly=true for fully-finalized cache session")
	}
}

// TestDetect_FullyFinalizedInCache_StaleLedgerMetaStillDetected verifies that a
// session sitting in the cache is detected even when ledger/sessions/<name>/meta.json
// already exists.
//
// This test previously asserted the opposite, on the theory that a ledger
// meta.json proves the session was pushed and that re-detecting it would create
// duplicates. Neither holds. meta.json is written before the commit, so it is
// present on sessions whose push failed — and that combination (cache dir plus
// ledger meta.json) is exactly the wedged state ~3,000 sessions were stranded in,
// invisible to anti-entropy precisely because of this rule.
//
// Re-detection is safe: processUploadOnly stages by copy, a no-op commit is
// recognized as such, and the cache prune ends the loop after one pass. The
// prune is the only signal that means "it reached the remote".
func TestDetect_FullyFinalizedInCache_StaleLedgerMetaStillDetected(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	sessionName := "2026-01-10T13-00-testuser-OxDONE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("done"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// also present in ledger/sessions/ (already uploaded)
	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(ledgerSessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, "meta.json"), []byte(`{"session":"done"}`), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected the cache-resident session to be detected, got %d items", len(items))
	}
	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", items[0].Payload)
	}
	if !payload.UploadOnly {
		t.Error("a fully-finalized cache session should be queued as upload-only, not for re-summarization")
	}
}

// --- B. BuildPrompt: UploadOnly skips LLM ---
// TestBuildPrompt_UploadOnly_SkipsLLM verifies that UploadOnly payloads return
// SkipLLM=true, preventing an unnecessary and costly LLM invocation.
// Failure prevented: UploadOnly sessions triggering a full LLM summarization run.
func TestBuildPrompt_UploadOnly_SkipsLLM(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	item := &WorkItem{
		ID:   "test-upload-only",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			UploadOnly: true,
			SessionDir: "/tmp/fake",
			RawPath:    "/tmp/fake/raw.jsonl",
			LedgerPath: "/tmp/fake-ledger",
		},
	}

	req, err := handler.BuildPrompt(item)
	if err != nil {
		t.Fatalf("BuildPrompt failed: %v", err)
	}
	if !req.SkipLLM {
		t.Error("expected SkipLLM=true for UploadOnly payload")
	}
	if req.Prompt != "" {
		t.Errorf("expected empty prompt for UploadOnly, got %q", req.Prompt)
	}
}

// --- C. stageSessionInLedger: moves from cache to sessions/ ---
// TestStageSessionInLedger_CopiesFiles verifies that stageSessionInLedger copies
// all files from .sageox/cache/sessions/<name>/ to ledger/sessions/<name>/ and
// updates payload.SessionDir.
// Failure prevented: gitCommitAndPush silently staging nothing because .sageox/cache/
// is gitignored in the ledger.
func TestStageSessionInLedger_CopiesFiles(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := initTestGitRepo(t)

	sessionName := "2026-01-10T14-00-testuser-OxSTAGE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFiles := []string{"raw.jsonl", "summary.md", "meta.json"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(cacheDir, f), []byte("content of "+f), 0644); err != nil {
			t.Fatal(err)
		}
	}

	payload := &SessionFinalizePayload{
		SessionDir: cacheDir,
		LedgerPath: ledgerPath,
	}

	if _, err := handler.stageSessionInLedger(payload, nil); err != nil {
		t.Fatalf("stageSessionInLedger failed: %v", err)
	}

	expectedDest := filepath.Join(ledgerPath, "sessions", sessionName)
	if payload.SessionDir != expectedDest {
		t.Errorf("payload.SessionDir not updated: got %q, want %q", payload.SessionDir, expectedDest)
	}

	for _, f := range testFiles {
		dst := filepath.Join(expectedDest, f)
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Errorf("file %s not copied: %v", f, err)
			continue
		}
		if string(data) != "content of "+f {
			t.Errorf("file %s has wrong content: %q", f, data)
		}
	}
}

// A session finalized in place needs a cache copy before pointer preparation.
func TestStageSessionInLedger_BacksUpTrackedSession(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "new backup"},
		{name: "existing private backup", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewSessionFinalizeHandler(slog.Default())
			ledgerPath := t.TempDir()
			sessionName := "2026-01-10T14-30-testuser-OxNOOP"
			sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
			if err := os.MkdirAll(sessionDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(testRawContent), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.existing {
				cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
				if err := os.MkdirAll(cacheDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte("previous recording"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			payload := &SessionFinalizePayload{SessionDir: sessionDir, LedgerPath: ledgerPath}
			cacheDir, err := handler.stageSessionInLedger(payload, nil)
			if err != nil {
				t.Fatalf("stageSessionInLedger failed: %v", err)
			}
			if payload.SessionDir != sessionDir {
				t.Errorf("payload.SessionDir should not change: got %q", payload.SessionDir)
			}
			backupPath := filepath.Join(cacheDir, "raw.jsonl")
			backup, err := os.ReadFile(backupPath)
			if err != nil || string(backup) != testRawContent {
				t.Fatalf("tracked session must retain its raw content in cache: %v", err)
			}
			// Windows file modes do not express POSIX owner/group permissions.
			if runtime.GOOS != "windows" {
				info, err := os.Stat(backupPath)
				if err != nil {
					t.Fatal(err)
				}
				if mode := info.Mode().Perm(); mode != 0600 {
					t.Errorf("recording backup must remain owner-only: got %04o, want 0600", mode)
				}
			}
		})
	}
}

// --- D. isInLedgerCacheDir ---
func TestIsInLedgerCacheDir(t *testing.T) {
	ledger := "/home/user/.local/share/sageox/sageox.ai/ledgers/repo_abc"
	cases := []struct {
		sessionDir string
		want       bool
	}{
		{ledger + "/.sageox/cache/sessions/2026-01-01T00-00-user-OxABC", true},
		{ledger + "/sessions/2026-01-01T00-00-user-OxABC", false},
		{"/other/path/sessions/something", false},
		// a sibling dir that shares a prefix with the cache path but isn't under it
		{ledger + "/.sageox/cache/sessions-extra/OxABC", false},
	}
	for _, tc := range cases {
		got := isInLedgerCacheDir(tc.sessionDir, ledger)
		if got != tc.want {
			t.Errorf("isInLedgerCacheDir(%q) = %v, want %v", tc.sessionDir, got, tc.want)
		}
	}
}

// --- E. End-to-end: UploadOnly session commits to git ---
// TestProcessResult_UploadOnly_CommitsToGit verifies the full pipeline for an
// UploadOnly session: cache → stage → git commit → push, without LLM involvement.
// Failure prevented: UploadOnly sessions silently never landing in the ledger.
func TestProcessResult_UploadOnly_CommitsToGit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T10-00-testuser-OxUPLOAD"

	// put session in cache (fully finalized, all artifacts present)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session_name":"`+sessionName+`"}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)

	item := &WorkItem{
		ID:   "test-upload-only-git",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{}); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// session must be in ledger/sessions/ after commit
	ledgerSessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "raw.jsonl")); err != nil {
		t.Errorf("raw.jsonl not found in ledger/sessions/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "meta.json")); err != nil {
		t.Errorf("meta.json not found in ledger/sessions/: %v", err)
	}

	// cache dir must be pruned after successful commit
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be removed after successful upload")
	}
}

// TestProcessResult_RegularFinalize_StagesToLedger verifies that the regular
// finalization path (with LLM output) also stages from cache to ledger/sessions/.
// Failure prevented: regular ProcessResult silently committing to the gitignored
// cache path, leaving sessions stuck in "local only" state forever.
func TestProcessResult_RegularFinalize_StagesToLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T11-00-testuser-OxREGULAR"

	// put raw-only session in cache (typical: session stop failed before summarization)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)

	llmOutput := `{"title":"Test Session","summary":"A test session.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["did something"]}`

	item := &WorkItem{
		ID:   "test-regular-stage",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: clonePath,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{Output: llmOutput}); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// session must be in ledger/sessions/, not just in cache
	ledgerSessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "raw.jsonl")); err != nil {
		t.Errorf("raw.jsonl not in ledger/sessions/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "summary.json")); err != nil {
		t.Errorf("summary.json not in ledger/sessions/: %v", err)
	}

	// cache dir must be pruned
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be pruned after finalize+commit")
	}
}

func TestProcessResult_RegularFinalizePreservesDifferentLedgerRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	barePath, clonePath := setupBareAndCloneLedger(t)
	const name = "2026-01-15T11-00-testuser-OxCOLLIDE"
	const ledgerID = "ses_01950000-0000-7000-8000-000000000101"
	const cachedID = "ses_01950000-0000-7000-8000-000000000102"
	ledgerRaw := strings.Replace(testRawContent, `{"metadata":{`, `{"type":"header","metadata":{"session_id":"`+ledgerID+`",`, 1)
	cachedRaw := strings.Replace(ledgerRaw, ledgerID, cachedID, 1)
	f := cacheFixture{
		ledgerPath: clonePath,
		name:       name,
		cacheDir:   filepath.Join(clonePath, ".sageox", "cache", "sessions", name),
	}
	f.write(t, "raw.jsonl", cachedRaw)
	f.write(t, ".needs-summary", "{}")
	ref := lfs.NewFileRef([]byte(ledgerRaw))
	metadata, err := json.Marshal(lfs.SessionMeta{
		SessionID: ledgerID,
		Title:     "Original Ledger recording",
		Files:     map[string]lfs.FileRef{"raw.jsonl": ref},
	})
	require.NoError(t, err)
	f.ledgerMeta(t, string(metadata))
	dir := filepath.Join(clonePath, "sessions", name)
	pointer := lfs.FormatPointer(ref.OID, ref.Size)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(pointer), 0644))
	runGitCmd(t, clonePath, "add", "--", "sessions")
	runGitCmd(t, clonePath, "commit", "-m", "publish original recording")
	runGitCmd(t, clonePath, "push")
	branch := gitOutput(t, clonePath, "branch", "--show-current")
	before := gitOutput(t, barePath, "rev-parse", branch)

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)
	items := detectCacheOnly(t, handler, clonePath)
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, cachedID, session.ReadHeaderSessionID(filepath.Join(f.cacheDir, "raw.jsonl")))
	require.ErrorIs(t, handler.ProcessResult(item, &RunResult{Output: "{}"}), session.ErrLedgerSessionOverwrite)

	require.Equal(t, before, gitOutput(t, barePath, "rev-parse", branch))
	got, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	require.Equal(t, metadata, got)
	gotRaw, err := os.ReadFile(filepath.Join(dir, "raw.jsonl"))
	require.NoError(t, err)
	require.Equal(t, pointer, string(gotRaw))
	require.DirExists(t, f.cacheDir)
}

func TestProcessResult_LegacySummaryRepairUsesTrackedRawPointer(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	for _, state := range []string{"missing summary", "retry after stub", "interrupted copy", "interrupted copy after missing artifact", "interrupted copy with local edits", "newer summary", "changed recording"} {
		t.Run(state, func(t *testing.T) {
			checkLegacySummaryRepair(t, state)
		})
	}
}

func checkLegacySummaryRepair(t *testing.T, state string) {
	t.Helper()
	bare, ledger := setupBareAndCloneLedger(t)
	const name = "2026-01-15T11-00-testuser-OxLEGACY"
	const userID = "usr_legacy_owner"
	f := cacheFixture{
		ledgerPath: ledger,
		name:       name,
		cacheDir:   filepath.Join(ledger, ".sageox", "cache", "sessions", name),
	}
	f.write(t, "raw.jsonl", testRawContent)
	f.write(t, ".needs-summary", "{}")
	if state == "retry after stub" {
		f.write(t, "meta.json", `{"version":"1.0","session_name":"`+name+`","user_id":"`+userID+`","summary_status":"failed_validation","summary_attempts":1}`)
	}
	ledgerMeta := fmt.Sprintf(`"version":"1.0","repo_id":"repo_legacy","session_name":%q,"user_id":%q`, name, userID)
	var existingTitle string
	switch state {
	case "newer summary":
		existingTitle = "New summary"
		ledgerMeta += `,"title":"New summary","summary":"A newer summary from the Ledger.","summary_status":"ok"`
	case "interrupted copy":
		existingTitle = "Previous stub"
		ledgerMeta += `,"title":"Previous stub","summary_status":"failed_validation"`
	case "interrupted copy after missing artifact", "interrupted copy with local edits":
		existingTitle = "Previous summary"
		ledgerMeta += `,"title":"Previous summary","summary_status":"ok"`
	}
	dir := filepath.Join(ledger, "sessions", name)
	ref := lfs.NewFileRef([]byte(testRawContent))
	if state == "changed recording" {
		manifest, err := json.Marshal(map[string]lfs.FileRef{"raw.jsonl": ref})
		require.NoError(t, err)
		ledgerMeta += `,"files":` + string(manifest)
		ref = lfs.NewFileRef([]byte(strings.Replace(testRawContent, "hello", "new message", 1)))
	}
	f.ledgerMeta(t, "{"+ledgerMeta+"}")
	if existingTitle != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), fmt.Appendf(nil, `{"title":%q}`, existingTitle), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), []byte("# "+existingTitle), 0644))
		if state != "interrupted copy after missing artifact" && state != "interrupted copy with local edits" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "session.md"), []byte("# Previous session"), 0644))
		}
	}
	pointer := lfs.FormatPointer(ref.OID, ref.Size)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(pointer), 0644))
	runGitCmd(t, ledger, "add", "--", "sessions")
	runGitCmd(t, ledger, "commit", "-m", "publish legacy recording")
	runGitCmd(t, ledger, "push")
	branch := gitOutput(t, ledger, "branch", "--show-current")
	before := gitOutput(t, bare, "rev-parse", branch)

	h := NewSessionFinalizeHandler(slog.Default())
	h.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, h, ledger)
	items := detectCacheOnly(t, h, ledger)
	require.Len(t, items, 1)
	output := `{"title":"Legacy Session Repair","summary":"Restored the missing summary for a legacy recording.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["restored the summary"]}`
	var err error
	var resumedSummaryPointer string
	if strings.HasPrefix(state, "interrupted copy") {
		// Force the atomic replacement of summary.md to fail after staging
		// started, then restore the old file before anti-entropy retries.
		artifact := filepath.Join(dir, "summary.md")
		original, readErr := os.ReadFile(artifact)
		require.NoError(t, readErr)
		require.NoError(t, os.Remove(artifact))
		require.NoError(t, os.Mkdir(artifact, 0755))
		require.NoError(t, h.ProcessResult(items[0], &RunResult{Output: output}))
		require.Equal(t, before, gitOutput(t, bare, "rev-parse", branch))
		require.DirExists(t, f.cacheDir)
		if state == "interrupted copy after missing artifact" {
			require.FileExists(t, filepath.Join(dir, "session.md"))
		}
		cached, readErr := os.ReadFile(filepath.Join(f.cacheDir, "summary.md"))
		require.NoError(t, readErr)
		ref := lfs.NewFileRef(cached)
		resumedSummaryPointer = strings.TrimSpace(lfs.FormatPointer(ref.OID, ref.Size))
		require.NoError(t, os.Remove(artifact))
		require.NoError(t, os.WriteFile(artifact, original, 0644))
		if state == "interrupted copy with local edits" {
			// An unrelated edit after the interrupted copy cannot be
			// attributed to this repair, even though HEAD is incomplete.
			require.NoError(t, os.WriteFile(artifact, []byte("# Local edits"), 0644))
		}

		items = detectCacheOnly(t, h, ledger)
		require.Len(t, items, 1)
		req, buildErr := h.BuildPrompt(items[0])
		require.NoError(t, buildErr)
		require.True(t, req.SkipLLM)
		err = h.ProcessResult(items[0], &RunResult{})
	} else {
		err = h.ProcessResult(items[0], &RunResult{Output: output})
	}
	if state == "changed recording" {
		require.ErrorIs(t, err, session.ErrLedgerSessionOverwrite)
	} else if state == "interrupted copy with local edits" {
		require.ErrorContains(t, err, "uncommitted changes in Ledger session")
		require.Equal(t, before, gitOutput(t, bare, "rev-parse", branch))
		summary, readErr := os.ReadFile(filepath.Join(dir, "summary.md"))
		require.NoError(t, readErr)
		require.Equal(t, "# Local edits", string(summary))
		require.DirExists(t, f.cacheDir)
		return
	} else {
		require.NoError(t, err)
	}

	if state == "newer summary" || state == "changed recording" {
		require.Equal(t, before, gitOutput(t, bare, "rev-parse", branch))
		require.Equal(t, strings.TrimSpace(pointer), gitOutput(t, bare, "show", branch+":sessions/"+name+"/raw.jsonl"))
		if state == "changed recording" {
			require.DirExists(t, f.cacheDir)
		}
		return
	}
	require.NotEqual(t, before, gitOutput(t, bare, "rev-parse", branch))
	meta, err := lfs.ParseSessionMeta([]byte(gitOutput(t, bare, "show", branch+":sessions/"+name+"/meta.json")))
	require.NoError(t, err)
	require.Empty(t, meta.SessionID)
	require.Equal(t, userID, meta.UserID)
	require.Equal(t, "Legacy Session Repair", meta.Title)
	require.Equal(t, strings.TrimSpace(pointer), gitOutput(t, bare, "show", branch+":sessions/"+name+"/raw.jsonl"))
	if resumedSummaryPointer != "" {
		require.Equal(t, resumedSummaryPointer, gitOutput(t, bare, "show", branch+":sessions/"+name+"/summary.md"))
	}
	for _, artifact := range []string{"summary.md", "summary.json", "session.md"} {
		content, err := gitutil.ReadBlob(t.Context(), bare, branch, "sessions/"+name+"/"+artifact)
		require.NoError(t, err)
		require.NotEmpty(t, content, "published %s", artifact)
	}
	require.NoDirExists(t, f.cacheDir)
}

// --- F. Cache survival on push failure ---

// TestProcessResult_RegularFinalize_CachePreservedOnPushFail verifies that when
// git push fails, the original cache dir is NOT deleted.
// Failure prevented: push failure + cache pruning = irrecoverable data loss. The
// session would disappear from both cache (gone) and remote (not pushed), with
// only a local git commit containing the data.
func TestProcessResult_RegularFinalize_CachePreservedOnPushFail(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)

	// break the remote so push fails
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	sessionName := "2026-01-15T12-00-testuser-OxPFAIL"
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)

	llmOutput := `{"title":"Test","summary":"A test.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["did something"]}`
	item := &WorkItem{
		ID:   "test-push-fail-cache",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: clonePath,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{Output: llmOutput}); err != nil {
		t.Fatalf("ProcessResult returned unexpected error: %v", err)
	}

	// CRITICAL: cache must survive push failure — it's the only surviving copy
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		t.Error("DATALOSS: cache dir removed after push failure — session content irrecoverable")
	}
}

// TestProcessResult_UploadOnly_CachePreservedOnPushFail verifies that the upload-only
// path does not delete the cache when push fails.
// Failure prevented: same data-loss scenario as above, in the upload-only code path.
func TestProcessResult_UploadOnly_CachePreservedOnPushFail(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)

	// break the remote so push fails
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	sessionName := "2026-01-15T13-00-testuser-OxPFAIL2"
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)

	item := &WorkItem{
		ID:   "test-upload-push-fail",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	// The failure must be reported, not swallowed. Returning nil here is what let
	// 897 failed commits in a single day log "agent work complete status=success".
	if err := handler.ProcessResult(item, &RunResult{}); err == nil {
		t.Error("ProcessResult returned nil after a failed push — the failure must reach the manager")
	}

	// CRITICAL: cache must survive push failure
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		t.Error("DATALOSS: upload-only cache removed after push failure — session content irrecoverable")
	}
}

// TestProcessResult_UploadOnly_AlreadyCommitted_PrunesCache reproduces the wedge
// that pinned four ox daemons at ~100% CPU on every 5-minute doctor tick.
//
// When a session's files are already committed at HEAD, stageSessionInLedger
// copies the cache over identical content and `git commit` exits 1 with
// "nothing to commit, working tree clean". Treating that as failure left the
// cache dir in place, so Detect() re-queued the same session every cycle,
// forever: 897 failed commits and 43,996 queue-full rejections in one day,
// against a backlog that grew to ~3,000 sessions.
func TestProcessResult_UploadOnly_AlreadyCommitted_PrunesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)

	sessionName := "2026-01-15T14-00-testuser-OxDUP"
	const sessionID = "ses_01950000-0000-7000-8000-000000001106"
	raw := strings.Replace(testRawContent, `{"metadata":{`, `{"type":"header","metadata":{"session_id":"`+sessionID+`",`, 1)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	ledgerDir := filepath.Join(clonePath, "sessions", sessionName)
	for _, dir := range []string{cacheDir, ledgerDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
		for _, name := range requiredArtifacts {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("artifact content"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Put meta.json only in the Ledger, with the same ID as the raw header.
	// This lets the retry prove ownership without changing the committed tree.
	if err := lfs.WriteSessionMetaOnly(ledgerDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: sessionName, SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}

	// the session is already committed and pushed — this is the steady state that
	// made every subsequent finalize a no-op commit
	runGitCmd(t, clonePath, "add", "sessions/"+sessionName)
	runGitCmd(t, clonePath, "commit", "--no-verify", "-m", "finalize session "+sessionName)
	runGitCmd(t, clonePath, "push")

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	item := &WorkItem{
		ID:   "test-upload-already-committed",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	headBefore := gitOutput(t, clonePath, "rev-parse", "HEAD")

	if err := handler.ProcessResult(item, &RunResult{}); err != nil {
		t.Fatalf("ProcessResult failed on an already-committed session: %v", err)
	}

	// Guard the guard: this test is only meaningful if the run actually took the
	// zero-delta path. If some future change makes the staged diff non-empty
	// (a synthesized file, a new artifact), the ordinary commit path runs, the
	// assertions below still pass, and the no-op regression this test exists to
	// catch would sail through unnoticed. An unchanged HEAD proves no commit was
	// created, which is only true on the branch under test.
	if headAfter := gitOutput(t, clonePath, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("a commit was created (%s to %s) — this run did not exercise the "+
			"zero-delta path, so it does not test what it claims",
			headBefore[:8], headAfter[:8])
	}

	// the cache prune is what breaks the loop: Detect() keys on isInLedgerCacheDir,
	// so a surviving cache dir means the same session is re-queued in 5 minutes
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir survived an already-committed session — Detect() will re-queue it forever")
	}
}

// A session whose content lives outside the ledger cannot be committed: an XDG
// cache dir that stageSessionInLedger left in place, a symlink under sessions/
// that points at one, or a sessions/ root that is itself a symlink. Either way
// the only copy must keep real content, not pointers.
func TestGitCommitAndPush_LeavesOutOfLedgerSessionIntact(t *testing.T) {
	for _, mode := range []string{"xdg dir", "symlink under sessions", "symlinked sessions root"} {
		t.Run(mode, func(t *testing.T) {
			ledgerPath := t.TempDir()
			runGitCmd(t, ledgerPath, "init", "--quiet")
			externalDir := filepath.Join(t.TempDir(), "sessions", "2026-01-10T14-30-testuser-OxXDG")
			if err := os.MkdirAll(externalDir, 0o755); err != nil {
				t.Fatal(err)
			}
			raw := []byte(testRawContent)
			rawPath := filepath.Join(externalDir, "raw.jsonl")
			if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			sessionDir := externalDir
			link := func(target, name string) {
				if err := os.Symlink(target, name); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			}
			switch mode {
			case "symlink under sessions":
				if err := os.MkdirAll(filepath.Join(ledgerPath, "sessions"), 0o755); err != nil {
					t.Fatal(err)
				}
				sessionDir = filepath.Join(ledgerPath, "sessions", filepath.Base(externalDir))
				link(externalDir, sessionDir)
			case "symlinked sessions root":
				link(filepath.Dir(externalDir), filepath.Join(ledgerPath, "sessions"))
				sessionDir = filepath.Join(ledgerPath, "sessions", filepath.Base(externalDir))
			}

			handler := NewSessionFinalizeHandler(slog.Default())
			payload := &SessionFinalizePayload{SessionDir: sessionDir, RawPath: filepath.Join(sessionDir, "raw.jsonl"), LedgerPath: ledgerPath}
			if pushed, _ := handler.gitCommitAndPush(payload, map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef(raw)}, nil); pushed {
				t.Fatal("a session outside the ledger must not report a successful push")
			}

			got, err := os.ReadFile(rawPath)
			if err != nil {
				t.Fatal(err)
			}
			if lfs.IsPointerFile(rawPath) || !bytes.Equal(got, raw) {
				t.Fatalf("external raw.jsonl must keep its content, got %d bytes", len(got))
			}
		})
	}
}

// A stash-pop conflict can exist as an ordinary stage-0 file after git add.
// Finalization must refuse it before commit, even though Git itself considers
// that index entry resolved and would happily publish the marker bytes.
func TestGitCommitAndPush_RefusesConflictMarkers(t *testing.T) {
	ledgerPath := t.TempDir()
	runGitCmd(t, ledgerPath, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(ledgerPath, "README.md"), []byte("ledger\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, ledgerPath, "add", "README.md")
	runGitCmd(t, ledgerPath, "commit", "-m", "initial")

	const sessionName = "2026-09-10T12-00-test-Oxguard"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markers := []byte("{\n<<<<<<< Updated upstream\n  \"title\": \"remote\"\n=======\n  \"title\": \"local\"\n>>>>>>> Stashed changes\n}\n")
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), markers, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(testRawContent), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.ledgerMu = &sync.Mutex{}
	headBefore := gitOutput(t, ledgerPath, "rev-parse", "HEAD")
	pushed, _ := handler.gitCommitAndPush(&SessionFinalizePayload{
		SessionDir: sessionDir,
		RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
		LedgerPath: ledgerPath,
	}, nil, nil)
	if pushed {
		t.Fatal("marker-laden metadata must not report a successful publication")
	}
	if headAfter := gitOutput(t, ledgerPath, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("validation failure advanced HEAD: before=%s after=%s", headBefore, headAfter)
	}
	if got, err := os.ReadFile(filepath.Join(sessionDir, "meta.json")); err != nil || !bytes.Equal(got, markers) {
		t.Fatalf("failed commit must preserve the conflicted file for recovery: %q, %v", got, err)
	}
}

// TestGitCommitAndPush_CommitsStagedBytesNotWorktree verifies the commit
// publishes the bytes `git add` staged, not whatever the worktree holds by the
// time the commit actually runs. A manual git process (or any concurrent
// writer) that rewrites meta.json between staging and commit must not have
// those bytes ride into the published commit — CommitLedgerSnapshot builds
// the committed tree from the INDEX, never the worktree.
func TestGitCommitAndPush_CommitsStagedBytesNotWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	_, ledgerPath := setupBareAndCloneLedger(t)

	const sessionName = "2026-09-10T12-00-test-OxTOCTOU"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanMeta := []byte(`{"title":"clean"}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), cleanMeta, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(testRawContent), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := newGitBackedHandler()
	// Simulate a manual Git process rewriting meta.json in the worktree AFTER
	// this finalize has already staged the clean bytes.
	handler.afterStageTestHook = func() {
		conflicted := []byte("{\n<<<<<<< Updated upstream\n  \"title\": \"remote\"\n=======\n  \"title\": \"local\"\n>>>>>>> Stashed changes\n}\n")
		if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), conflicted, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pushed, _ := handler.gitCommitAndPush(&SessionFinalizePayload{
		SessionDir: sessionDir,
		RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
		LedgerPath: ledgerPath,
	}, map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef([]byte(testRawContent))}, nil)
	if !pushed {
		t.Fatal("a clean staged session must commit and push")
	}

	got := gitOutput(t, ledgerPath, "show", "HEAD:sessions/"+sessionName+"/meta.json")
	if got != string(cleanMeta) {
		t.Fatalf("commit must publish the STAGED bytes, not the worktree's post-stage rewrite: got %q, want %q", got, cleanMeta)
	}
	subject := gitOutput(t, ledgerPath, "log", "-1", "--format=%s")
	if subject != "finalize session "+sessionName {
		t.Fatalf("unexpected commit subject: %q", subject)
	}
}

// A failed/skipped LFS upload must leave the recording recoverable locally;
// the daemon must never publish real raw bytes as a fallback git artifact.
func TestGitCommitAndPush_RefusesUnuploadedArtifacts(t *testing.T) {
	_, ledgerPath := setupBareAndCloneLedger(t)
	sessionDir := filepath.Join(ledgerPath, "sessions", "unuploaded")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(`{"title":"Pending upload"}`), 0o644))
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(testRawContent), 0o644))
	before := gitOutput(t, ledgerPath, "rev-parse", "HEAD")
	handler := newGitBackedHandler()
	pushed, _ := handler.gitCommitAndPush(&SessionFinalizePayload{SessionDir: sessionDir, RawPath: rawPath, LedgerPath: ledgerPath}, nil, nil)
	require.False(t, pushed)
	require.Equal(t, before, gitOutput(t, ledgerPath, "rev-parse", "HEAD"))
	content, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, testRawContent, string(content))
}

// TestUploadOnly_SecretGuardRefusalWaitsForAChange: the secret guard refuses
// the same bytes every time.
// Failure prevented: the daemon re-detected and retried a refused session every
// few minutes, forever, re-scanning the whole transcript each time. Any change
// to the refused files, including a deletion or an edit stamped older than the
// refusal, must make the session eligible again, and a redacted transcript must
// then publish.
func TestUploadOnly_SecretGuardRefusalWaitsForAChange(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	// a synthetic credential the bundled rules refuse, so no rule download is needed
	const withSecret = `{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"testuser","agent_id":"OxSECR"}}
{"type":"user","content":"MAILLAB_API_KEY=syntheticOpaqueLFSBoundarySentinel39xyAZ","seq":1}
{"type":"assistant","content":"world","seq":2}
{"entry_count":2}
`
	rewrite := func(t *testing.T, cacheDir string) {
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0o644))
	}
	for _, tc := range []struct {
		name      string
		change    func(t *testing.T, cacheDir string)
		publishes bool // the change removed the credential
		draft     bool // the Ledger still holds the session's draft placeholder
	}{
		{"transcript rewritten", rewrite, true, false},
		// copying the draft's ses_ id into the cache rewrites meta.json, which the
		// refusal fingerprint covers
		{"transcript rewritten under a draft placeholder", rewrite, true, true},
		{"transcript edited with an older timestamp", func(t *testing.T, cacheDir string) {
			rawPath := filepath.Join(cacheDir, "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, []byte(testRawContent), 0o644))
			earlier := time.Now().Add(-time.Hour)
			require.NoError(t, os.Chtimes(rawPath, earlier, earlier))
		}, true, false},
		{"artifact deleted", func(t *testing.T, cacheDir string) {
			require.NoError(t, os.Remove(filepath.Join(cacheDir, requiredArtifacts[0])))
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bare, ledger := setupBareAndCloneLedger(t)
			handler := NewSessionFinalizeHandler(slog.Default())
			handler.skipGit = false
			handler.ledgerMu = &sync.Mutex{}
			enableLocalFinalizeLFS(t, handler, ledger)

			const name = "2026-10-16T22-52-testuser-OxSECR"
			cacheDir := filepath.Join(ledger, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(withSecret), 0o644))
			for _, artifact := range requiredArtifacts {
				require.NoError(t, os.WriteFile(filepath.Join(cacheDir, artifact), []byte("artifact"), 0o644))
			}
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session_name":"`+name+`"}`), 0o644))
			if tc.draft {
				writeDraftMeta(t, filepath.Join(ledger, "sessions", name))
				runGitCmd(t, ledger, "add", "sessions")
				runGitCmd(t, ledger, "commit", "-m", "session-draft: "+name)
				runGitCmd(t, ledger, "push", "origin", "HEAD")
			}

			detected := func() *WorkItem {
				t.Helper()
				items, err := handler.Detect(ledger)
				require.NoError(t, err)
				for _, item := range items {
					if item.DedupKey == sessionFinalizeType+":"+name {
						return item
					}
				}
				return nil
			}

			item := detected()
			require.NotNil(t, item, "precondition: the finalized cache session is queued for upload")
			err := handler.ProcessResult(item, &RunResult{})
			require.ErrorContains(t, err, "content contains credentials")
			require.Nil(t, detected(), "a refused session must not be retried until it changes")
			require.NoFileExists(t, filepath.Join(ledger, "sessions", name, publicationRefusedMarker),
				"the refusal marker must stay in the cache, never in the Ledger's tracked sessions/")

			tc.change(t, cacheDir)
			item = detected()
			require.NotNil(t, item, "a session changed since the refusal must be retried")
			if !tc.publishes {
				return
			}
			require.NoError(t, handler.ProcessResult(item, &RunResult{}), "the redacted session must publish")
			require.Equal(t, gitOutput(t, ledger, "rev-parse", "HEAD"), gitOutput(t, bare, "rev-parse", "HEAD"),
				"the publication must reach the remote")
			oid, size, err := lfs.ParsePointer(gitOutput(t, bare, "show", "HEAD:sessions/"+name+"/raw.jsonl"))
			require.NoError(t, err, "the published transcript must be an LFS pointer")
			require.Equal(t, "sha256:"+lfs.ComputeOID([]byte(testRawContent)), oid, "the pointer must name the redacted transcript")
			require.Equal(t, int64(len(testRawContent)), size)
			var meta lfs.SessionMeta
			require.NoError(t, json.Unmarshal([]byte(gitOutput(t, bare, "show", "HEAD:sessions/"+name+"/meta.json")), &meta))
			require.Equal(t, oid, meta.Files["raw.jsonl"].OID, "the published manifest must name the redacted transcript")
		})
	}
}
