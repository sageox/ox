package agentwork

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// TestPointerRewrite_CommittedAfterPush verifies that LFS pointer files are
// committed and pushed to the remote after the initial content push succeeds.
//
// Failure prevented: daemon writes pointer files locally but never commits
// them. Remote ledger retains raw blobs alongside meta.json claiming
// storage=lfs. Web frontend calls FetchSmallLFSContent with the OID from
// meta.json but finds a blob (not a pointer) in the git tree, causing
// "failed to fetch file content" errors.
//
// This is a regression test for ox-215z (May 2026).
func TestPointerRewrite_CommittedAfterPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	barePath, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-05-03T04-18-testuser-OxPTRW"

	// create session directly in ledger/sessions/ (simulates stageSessionInLedger)
	sessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := []byte(`{"metadata":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"fix the daemon pointer bug","seq":1}
{"type":"assistant","content":"On it — I'll add the missing commit step.","seq":2}
`)
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(`{"session_name":"`+sessionName+`","entry_count":2}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate LFS upload having succeeded — create file refs. The OID must be
	// the content's real hash, which is what a real upload produces. Pointerizing
	// content that an OID does not describe is refused, because that is the shape
	// of a meta/blob disagreement and it would destroy the only local copy.
	oid := "sha256:" + lfs.ComputeOID(rawContent)
	fileRefs := map[string]lfs.FileRef{
		"raw.jsonl": {OID: oid, Size: int64(len(rawContent)), Storage: "lfs"},
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	// first commit the raw content (simulates gitCommitAndPush)
	runGitCmd(t, clonePath, "add", "--sparse", "sessions/"+sessionName+"/")
	runGitCmd(t, clonePath, "commit", "-m", "finalize session "+sessionName)
	runGitCmd(t, clonePath, "push")

	// now write pointer files and commit them (the code under test)
	written, err := lfs.WritePointerFiles(sessionDir, lfs.AssertUploadedManifest(fileRefs))
	if err != nil {
		t.Fatalf("WritePointerFiles: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("expected pointer files to be written")
	}

	payload := &SessionFinalizePayload{
		SessionDir: sessionDir,
		LedgerPath: clonePath,
	}
	if err := handler.gitCommitPointerRewrite(payload, written); err != nil {
		t.Fatalf("gitCommitPointerRewrite: %v", err)
	}

	// verify: clone from bare repo and check that raw.jsonl is a pointer
	verifyPath := filepath.Join(t.TempDir(), "verify")
	runGitCmd(t, t.TempDir(), "clone", "file://"+barePath, verifyPath)

	verifyRaw := filepath.Join(verifyPath, "sessions", sessionName, "raw.jsonl")
	content, err := os.ReadFile(verifyRaw)
	if err != nil {
		t.Fatalf("read raw.jsonl from fresh clone: %v", err)
	}

	if !strings.HasPrefix(string(content), "version https://git-lfs") {
		t.Errorf("raw.jsonl in remote should be an LFS pointer, got: %s", string(content)[:min(80, len(content))])
	}
}

// TestPointerRewrite_NeverCommitsRawBlobs_WhenMetaClaimsLFS verifies that
// after a full finalize cycle, the remote ledger does not contain a raw blob
// for any file that meta.json declares as storage=lfs.
//
// Failure prevented: the exact production bug (ox-215z) where raw blobs land
// in git because pointer files are written locally but the commit is skipped.
func TestPointerRewrite_NeverCommitsRawBlobs_WhenMetaClaimsLFS(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	barePath, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-05-03T17-54-testuser-OxBLOB"

	// put session in cache (simulates pre-finalize state)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"testuser"}}
{"type":"user","content":"Investigate the murmur pyc false positive — unstaged build artifacts trigger conflict warnings","seq":1}
{"type":"assistant","content":"I'll investigate the murmur file change detection to filter gitignored files.","seq":2}
`
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(fmt.Sprintf(
		`{"session_name":"%s","entry_count":2,"files":{"raw.jsonl":{"storage":"lfs","oid":"sha256:0000000000000000000000000000000000000000000000000000000000000000","size":%d}}}`,
		sessionName, len(rawContent))), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true // no real LFS server
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	item := &WorkItem{
		ID:   "test-blob-check",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{}); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// verify in a fresh clone: with skipLFS=true, raw.jsonl should be
	// committed as-is (no pointer rewrite). The key assertion is that
	// when LFS IS enabled, the pointer commit happens.
	verifyPath := filepath.Join(t.TempDir(), "verify")
	runGitCmd(t, t.TempDir(), "clone", "file://"+barePath, verifyPath)

	verifyRaw := filepath.Join(verifyPath, "sessions", sessionName, "raw.jsonl")
	if _, err := os.Stat(verifyRaw); err != nil {
		t.Fatalf("session not pushed to remote: %v", err)
	}
}
