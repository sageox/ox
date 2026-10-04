package agentwork

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

func TestTracePointerWriteFailureStillPublishesOrdinarySession(t *testing.T) {
	_, ledger := setupBareAndCloneLedger(t)
	dir := filepath.Join(ledger, "sessions", "trace-write-failure")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw := []byte(testRawContent)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), raw, 0o644))
	refs := map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef(raw), pipeline.LedgerFileTraceSpans: lfs.NewFileRef([]byte("spans")), pipeline.LedgerFileTraceEvents: lfs.NewFileRef([]byte("events"))}
	meta := &lfs.SessionMeta{Files: refs, Trace: &model.Metadata{}}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644))
	// A malformed trace destination must neither be removed recursively nor
	// force an ordinary recording to remain unpublished.
	blocked := filepath.Join(dir, pipeline.LedgerFileTraceEvents)
	require.NoError(t, os.MkdirAll(blocked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocked, "keep"), []byte("local recovery"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, pipeline.LedgerFileTraceSpans), []byte(lfs.FormatPointer(refs[pipeline.LedgerFileTraceSpans].OID, refs[pipeline.LedgerFileTraceSpans].Size)), 0o644))
	// -f: a coworker's global gitignore (e.g. "*.gz") must not decide what this fixture stages
	runGitCmd(t, ledger, "add", "--sparse", "-f", filepath.Join("sessions", "trace-write-failure", pipeline.LedgerFileTraceSpans))
	handler := newGitBackedHandler()
	pushed, pushErr := handler.gitCommitAndPush(&SessionFinalizePayload{SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), LedgerPath: ledger}, refs)
	require.NoError(t, pushErr)
	require.True(t, pushed)
	committed := gitOutput(t, ledger, "show", "HEAD:sessions/trace-write-failure/raw.jsonl")
	_, _, err = lfs.ParsePointer(committed)
	require.NoError(t, err)
	paths := gitOutput(t, ledger, "ls-tree", "-r", "--name-only", "HEAD")
	require.NotContains(t, paths, "trace-spans")
	require.NotContains(t, paths, "trace-events")
	saved, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	require.Nil(t, saved.Trace)
	require.NotContains(t, saved.Files, pipeline.LedgerFileTraceSpans)
	require.NotContains(t, saved.Files, pipeline.LedgerFileTraceEvents)
	preserved, err := os.ReadFile(filepath.Join(blocked, "keep"))
	require.NoError(t, err)
	require.Equal(t, "local recovery", string(preserved))
}

func TestTraceMissingBlobDoesNotBlockOrdinaryUpload(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		ordinaryMissing, sameOID bool
	}{
		{name: "only trace missing"},
		{name: "ordinary missing still blocks", ordinaryMissing: true},
		{name: "same missing object cannot hide ordinary artifact", ordinaryMissing: true, sameOID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ordinaryMissing := tc.ordinaryMissing
			_, ledger := setupBareAndCloneLedger(t)
			dir := filepath.Join(ledger, "sessions", "missing-trace")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			traceOID := strings.Repeat("a", 64)
			rawOID := strings.Repeat("b", 64)
			if tc.sameOID {
				rawOID = traceOID
			}
			raw := testRawContent
			if ordinaryMissing {
				raw = lfs.FormatPointer("sha256:"+rawOID, 100)
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, pipeline.LedgerFileTraceSpans), []byte(lfs.FormatPointer("sha256:"+traceOID, 100)), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"Keep ordinary recording","files":{"trace-spans.jsonl.gz":{"oid":"sha256:`+traceOID+`","size":100}},"trace":{}}`), 0o644))
			handler := newGitBackedHandler()
			enableLocalFinalizeLFS(t, handler, ledger, traceOID, rawOID)
			before := gitOutput(t, ledger, "rev-parse", "HEAD")
			item := &WorkItem{Payload: &SessionFinalizePayload{SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), LedgerPath: ledger, UploadOnly: true}}
			err := handler.ProcessResult(item, &RunResult{})
			after := gitOutput(t, ledger, "rev-parse", "HEAD")
			if ordinaryMissing {
				// a failure, not success: nil would reset the manager's failure
				// count and keep this session out of the retry cap forever
				require.ErrorContains(t, err, "missing from the remote")
				require.Equal(t, before, after)
				return
			}
			require.NoError(t, err)
			require.NotEqual(t, before, after)
			paths := gitOutput(t, ledger, "ls-tree", "-r", "--name-only", "HEAD")
			require.Contains(t, paths, "sessions/missing-trace/raw.jsonl")
			require.NotContains(t, paths, "trace-spans")
			meta, err := lfs.ReadSessionMeta(dir)
			require.NoError(t, err)
			require.NotContains(t, meta.Files, pipeline.LedgerFileTraceSpans)
			require.Nil(t, meta.Trace)
		})
	}
}

// Optional omission is not permission to resolve an active Git conflict.
func TestTraceOmissionPreservesUnmergedIndex(t *testing.T) {
	_, ledger := setupBareAndCloneLedger(t)
	dir := filepath.Join(ledger, "sessions", "conflicted-trace")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(testRawContent), 0o644))
	rel := filepath.ToSlash(filepath.Join("sessions", "conflicted-trace", pipeline.LedgerFileTraceSpans))
	tracePath := filepath.Join(ledger, rel)
	require.NoError(t, os.WriteFile(tracePath, []byte("ours"), 0o600))
	ours := gitOutput(t, ledger, "hash-object", "-w", tracePath)
	require.NoError(t, os.WriteFile(tracePath, []byte("theirs"), 0o600))
	theirs := gitOutput(t, ledger, "hash-object", "-w", tracePath)
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = ledger
	cmd.Stdin = strings.NewReader(fmt.Sprintf("100644 %s 2\t%s\n100644 %s 3\t%s\n", ours, rel, theirs, rel))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	before := gitOutput(t, ledger, "ls-files", "--stage")
	payload := &SessionFinalizePayload{SessionDir: dir, RawPath: rawPath, LedgerPath: ledger, omitTraces: true}
	pushed, _ := newGitBackedHandler().gitCommitAndPush(payload, map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef([]byte(testRawContent))})
	require.False(t, pushed)
	require.Equal(t, before, gitOutput(t, ledger, "ls-files", "--stage"))
	content, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, testRawContent, string(content))
}

// A coworker's personal gitignore must not decide what reaches the Ledger.
// git reads $XDG_CONFIG_HOME/git/ignore even with no global config file, and a
// "*.gz" there made the trace-omitting `git add` refuse the session, so every
// finalize on that machine failed.
func TestFinalizeIgnoresCoworkerGlobalGitignore(t *testing.T) {
	xdg := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "git", "ignore"), []byte("*.gz\n"), 0o644))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, ledger := setupBareAndCloneLedger(t)
	dir := filepath.Join(ledger, "sessions", "global-ignore")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	traceOID := strings.Repeat("c", 64)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(testRawContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, pipeline.LedgerFileTraceSpans), []byte(lfs.FormatPointer("sha256:"+traceOID, 100)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"title":"Global ignore","files":{"trace-spans.jsonl.gz":{"oid":"sha256:`+traceOID+`","size":100}},"trace":{}}`), 0o644))
	handler := newGitBackedHandler()
	enableLocalFinalizeLFS(t, handler, ledger, traceOID)

	item := &WorkItem{Payload: &SessionFinalizePayload{SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), LedgerPath: ledger, UploadOnly: true}}
	require.NoError(t, handler.ProcessResult(item, &RunResult{}))
	paths := gitOutput(t, ledger, "ls-tree", "-r", "--name-only", "HEAD")
	require.Contains(t, paths, "sessions/global-ignore/raw.jsonl")
}
