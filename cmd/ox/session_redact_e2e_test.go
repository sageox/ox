package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr redirects the process stderr to a pipe for the duration of fn
// and returns everything written to it. The pre-push gate writes its
// customer-facing guidance (quarantine recovery, publication holds) straight to
// os.Stderr, so asserting on it is the only way to protect those messages from
// silent removal.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	done := make(chan string, 1)
	go func() {
		defer func() { _ = r.Close() }() // close the read end too — cleanup only closes w
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	// cleanup restores os.Stderr and closes the writer so the reader goroutine
	// unblocks. It runs on EVERY exit path: the deferred call covers a require.*
	// Goexit inside fn, and the explicit call on the normal path closes w before
	// we read `done`. Double-close returns an ignored error, not a panic.
	cleanup := func() {
		os.Stderr = orig
		_ = w.Close()
	}
	defer cleanup()

	os.Stderr = w
	fn()
	cleanup()
	return <-done
}

// This file is the end-to-end, customer-facing proof of the redaction promise:
// a credential a coworker records into a session NEVER reaches the shared Ledger
// remote. It is the executable mirror of
// tests/acceptance/features/redaction/secrets-never-leak.feature.
//
// Why this lens is not covered elsewhere: every existing secret test asserts on
// LOCAL bytes (a re-scan came back clean, a working-tree file was rewritten) and
// stops before `git push`. The gate must repair or quarantine leaked bytes and
// verify the immutable outgoing content before permitting publication. These
// tests exercise that final push boundary and drive the
// real pushLedger pipeline to a real bare remote and assert on the remote's
// object database — the only surface a teammate can actually read.
//
// The canary AKIAIOSFODNN7EXAMPLE is AWS's own published non-secret example key
// (fires the aws_access_key detector, safe to commit — used throughout this repo).

// assertRemoteObjectsCleanOf fails if the secret appears in ANY object reachable
// in the bare remote's database — not just the HEAD tree. A secret amended out
// of the tip but still pushed in an earlier pack is still readable by every
// teammate; scanning every object is the only honest oracle. Mirrors the
// object-scan oracle in TestDraftPublish_NoTurnContentInAnyGitObject.
func assertRemoteObjectsCleanOf(t *testing.T, barePath, secret string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "git cat-file --batch-all-objects --batch")
	cmd.Dir = barePath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(out), secret,
		"a credential reached a git object in the shared Ledger remote — every teammate who pulls can read it")
}

// commitSession stages and commits a session dir as a local holding commit,
// exactly as the finalize path does, WITHOUT pushing — so the pre-push gate sees
// the secret in the push range on the next push.
func commitSession(t *testing.T, ledgerPath string, sessionNames ...string) {
	t.Helper()
	args := []string{"add", "--sparse", "--", "sessions/.gitignore"}
	for _, n := range sessionNames {
		args = append(args, "sessions/"+n)
	}
	runGit(t, ledgerPath, args...)
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "session: holding commit")
}

// TestRedactE2E_SecretInJSONLNeverReachesRemote is the core contract.
//
// A coworker's raw.jsonl carries a live AWS key past the write-time layer. When
// the Ledger is pushed, the pre-push gate auto-redacts the JSONL in place and
// the push proceeds — but the credential must never appear in any object on the
// remote, and the redacted slug is what teammates receive instead.
//
// Failure prevented: a session credential reaching every teammate through the
// shared Ledger. Red-first: neuter autoRedactSessionFindings (return the input
// unchanged) and this test fails — the canary reaches the bare remote.
func TestRedactE2E_SecretInJSONLNeverReachesRemote(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxSjsn"
	const canary = "AKIAIOSFODNN7EXAMPLE"

	// Given: a session whose raw.jsonl contains a live credential, committed to a
	// local holding commit (not yet pushed).
	dir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	rawLine := `{"type":"assistant","content":"export AWS_ACCESS_KEY_ID=` + canary + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(rawLine), 0644))
	commitSession(t, f.ledgerPath, sessionName)
	require.Contains(t, runGit(t, f.ledgerPath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), canary,
		"precondition: the credential is in the local commit the push would publish")

	// When: the coworker pushes the Ledger through the real gate.
	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))

	// Then: no object on the remote carries the credential.
	assertRemoteObjectsCleanOf(t, f.barePath, canary)
	// And: teammates receive the redacted slug in its place.
	assert.Contains(t, runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), "[REDACTED_AWS_KEY]",
		"the redacted slug is what shipped to the remote")
	// And: the redaction is recorded as a real RedactionPass — decode meta.json
	// rather than grep the member name, which an empty default member satisfies.
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal(
		[]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/meta.json")), &meta))
	assert.NotEmpty(t, meta.Redactions,
		"a RedactionPass must be recorded in meta.json so the scrub is auditable")
	gitFsckClean(t, f.barePath)
}

// TestRedactE2E_SecretInSummaryQuarantinedNotPushed covers the non-JSONL path.
//
// A credential lands in a non-JSONL artifact (summary.md) that the auto-redactor
// cannot rewrite in place. The gate quarantines the file — drops it from the
// push, preserves the bytes locally, records a debt marker — and the rest of the
// push proceeds, including an UNRELATED clean session. One bad artifact must not
// leak, and must not wedge everyone else's sync.
//
// Failure prevented: a leaked summary shipping to teammates, or a single bad
// session stalling the whole Ledger push.
func TestRedactE2E_SecretInSummaryQuarantinedNotPushed(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const leaky = "2026-01-01T00-00-testuser-OxLeak"
	const clean = "2026-01-01T00-00-testuser-OxClen"
	const canary = "AKIAIOSFODNN7EXAMPLE"

	// Given: a session with a credential in its (non-JSONL) summary.md, plus an
	// unrelated clean session, both in one holding commit.
	leakyDir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, leaky)
	require.NoError(t, os.WriteFile(filepath.Join(leakyDir, "summary.md"),
		[]byte("Shipped the fix. Set AWS_ACCESS_KEY_ID="+canary+" to reproduce.\n"), 0644))
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, clean)
	commitSession(t, f.ledgerPath, leaky, clean)

	// When: the coworker pushes.
	stderr := captureStderr(t, func() {
		require.NoError(t, pushLedger(context.Background(), f.ledgerPath))
	})

	// And: ox tells the coworker what was quarantined and how to recover — a
	// customer-facing promise that must not silently degrade.
	assert.Contains(t, stderr, "Quarantined",
		"the coworker must be told a file was held back from the push")
	assert.Contains(t, stderr, "ox doctor",
		"the coworker must be pointed at recovery")

	// Then: no object on the remote carries the credential.
	assertRemoteObjectsCleanOf(t, f.barePath, canary)
	// And: the leaking summary was dropped from the push (quarantined out).
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+leaky+"/summary.md",
		"the credential-bearing summary must not reach the remote")
	// And: the bytes are preserved locally and a debt marker records the state.
	assert.FileExists(t, filepath.Join(f.ledgerPath, ".sageox", "cache", "quarantine", leaky, "summary.md"),
		"quarantined bytes must be preserved on disk for recovery")
	assert.FileExists(t, filepath.Join(f.ledgerPath, ".sageox", "cache", "redaction-debt", leaky+".json"),
		"a redaction-debt marker must record the quarantine so `ox doctor` can surface it")
	// And: the unrelated clean session shipped normally — one bad session does not wedge the sync.
	assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+clean+"/meta.json",
		"an unrelated clean session must still reach the remote")
	gitFsckClean(t, f.barePath)
}

// A legacy environment override cannot publish credential-bearing session bytes.
func TestRedactE2E_AllowSecretsOverrideCannotShipRawSecret(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Setenv("OX_ALLOW_SECRETS", "1")
	const sessionName = "2026-01-01T00-00-testuser-OxOvrd"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	dir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	rawLine := `{"type":"assistant","content":"export AWS_ACCESS_KEY_ID=` + canary + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(rawLine), 0644))
	commitSession(t, f.ledgerPath, sessionName)
	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))
	assert.NotContains(t, runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), canary,
		"legacy environment override must not ship a raw secret")
}
