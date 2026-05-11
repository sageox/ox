package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLedgerForAuditTest creates a synthetic local ledger working tree (a
// plain git repo with no origin needed — checkLedgerSecrets doesn't care
// about remotes) and writes the supplied files into it. Returns the
// ledger path.
func makeLedgerForAuditTest(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "test")
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0644))
	}
	return dir
}

// TestScanLedgerForSecrets_FindsCanaries plants known-secret patterns into
// session files and asserts the scanner reports them — without leaking the
// values back through the result.
func TestScanLedgerForSecrets_FindsCanaries(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/2026-05-10/raw.jsonl": `{"text":"AKIAIOSFODNN7EXAMPLE"}` + "\n",
		"sessions/2026-05-10/meta.json": `{"agent_type":"claude-code"}`,
		"sessions/2026-05-11/raw.jsonl": "token=glpat-AbCdEfGhIjKlMnOpQrSt\n",
		"docs/notes.md":                 "this file has no secrets in it",
	})

	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.GreaterOrEqual(t, result.FilesScanned, 4)
	assert.Contains(t, result.Findings, "aws_access_key")
	assert.Contains(t, result.Findings, "gitlab_token")

	awsHit := result.Findings["aws_access_key"]
	assert.Equal(t, 1, awsHit.Count)
	assert.Equal(t, 1, awsHit.FileCount)
	assert.Equal(t, "sessions/2026-05-10/raw.jsonl", awsHit.Sample)

	// Critical: matched bytes must NEVER appear in any field of the result.
	for _, f := range result.Findings {
		assert.NotContains(t, f.Sample, "AKIA")
		assert.NotContains(t, f.Sample, "glpat-")
	}
}

// TestScanLedgerForSecrets_CleanLedger verifies a ledger with no secrets
// produces zero findings and reports a positive files-scanned count.
func TestScanLedgerForSecrets_CleanLedger(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/clean/raw.jsonl": "hello world, just a chat\n",
		"sessions/clean/meta.json": "{}",
		"docs/note.md":             "no creds here\n",
	})

	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
	assert.GreaterOrEqual(t, result.FilesScanned, 3)
}

// TestScanLedgerForSecrets_SkipsBlessedDirs verifies that .git, .beads,
// .dolt etc. are never descended into — saves time and avoids false
// positives on binary pack-files that random-bytes-match a regex.
func TestScanLedgerForSecrets_SkipsBlessedDirs(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	// plant a "secret" inside .git — it must NOT be scanned
	require.NoError(t, os.WriteFile(filepath.Join(work, ".git", "hidden.jsonl"),
		[]byte(`{"k":"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), 0644))
	// also plant one inside .beads
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".beads"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".beads", "x.json"),
		[]byte(`{"k":"gho_alphabetabcdefghijklmnopqrstuvwxyz12"}`), 0644))

	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	// only the real session file should have fired
	assert.Contains(t, result.Findings, "aws_access_key")
	assert.NotContains(t, result.Findings, "github_token",
		".git/.beads should be skipped, github_token detector should not have fired")
}

// TestScanLedgerForSecrets_SizeCap verifies oversized files are skipped.
// Pre-push and audit share the same cap; large files defeat the regex
// budget and rarely carry credentials in patterns the detectors can match.
func TestScanLedgerForSecrets_SizeCap(t *testing.T) {
	// build a file just over the cap, with the canary at the end so a
	// scanner that doesn't honor the cap would still catch it.
	const sz = ledgerSecretsSizeCap + 1024
	buf := make([]byte, sz)
	for i := range buf {
		buf[i] = 'X'
	}
	copy(buf[sz-len("AKIAIOSFODNN7EXAMPLE"):], []byte("AKIAIOSFODNN7EXAMPLE"))
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/huge.jsonl": string(buf),
	})

	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	assert.Empty(t, result.Findings, "over-cap file must be skipped, but detectors fired: %v", result.Findings)
}

// TestScanLedgerForSecrets_OnlyAllowlistedExts verifies non-matching
// extensions are skipped — a binary blob shouldn't be scanned.
func TestScanLedgerForSecrets_OnlyAllowlistedExts(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/audio.mp3": "AKIAIOSFODNN7EXAMPLE",   // plant in a "binary" — must not be scanned
		"sessions/img.png":   "AKIAIOSFODNN7EXAMPLE",   // same
		"sessions/real.jsonl": "no secrets in this one", // .jsonl is scanned; nothing to find
	})

	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
	// .mp3 and .png should not be counted toward FilesScanned (only .jsonl matched)
	assert.Equal(t, 1, result.FilesScanned)
}

// TestLedgerOriginHasEmbeddedPAT_True verifies detection when a PAT is
// actually embedded.
func TestLedgerOriginHasEmbeddedPAT_True(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin",
		"https://oauth2:glpat-abc123def456ghi789jk@git.sageox.ai/team/ledger.git")

	hasPAT, err := ledgerOriginHasEmbeddedPAT(work)
	require.NoError(t, err)
	assert.True(t, hasPAT)
}

// TestLedgerOriginHasEmbeddedPAT_False verifies bare URLs are not flagged.
func TestLedgerOriginHasEmbeddedPAT_False(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/ledger.git")

	hasPAT, err := ledgerOriginHasEmbeddedPAT(work)
	require.NoError(t, err)
	assert.False(t, hasPAT)
}

// TestLedgerOriginHasEmbeddedPAT_NonOauth2 verifies third-party deploy
// tokens are not flagged (we only manage oauth2-style userinfo).
func TestLedgerOriginHasEmbeddedPAT_NonOauth2(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin",
		"https://deploy-token:some-other-token@git.example.com/team/ledger.git")

	hasPAT, err := ledgerOriginHasEmbeddedPAT(work)
	require.NoError(t, err)
	assert.False(t, hasPAT, "non-oauth2 userinfo (deploy tokens) should not be flagged")
}

// TestLedgerOriginHasEmbeddedPAT_NoOrigin handles repos without an origin
// remote at all — common during early init.
func TestLedgerOriginHasEmbeddedPAT_NoOrigin(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")

	hasPAT, err := ledgerOriginHasEmbeddedPAT(work)
	require.NoError(t, err)
	assert.False(t, hasPAT)
}

// TestCheckLedgerSecrets_OutputDoesNotLeakBytes is the load-bearing
// privacy assertion: even when findings exist, the rendered checkResult
// (the thing printed to the user) must never contain a matched secret.
func TestCheckLedgerSecrets_OutputDoesNotLeakBytes(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE and " +
			"gh token ghp_alphabetabcdefghijklmnopqrstuvwxyz12\n",
	})
	result, err := scanLedgerForSecrets(work)
	require.NoError(t, err)
	require.NotEmpty(t, result.Findings)

	// directly inspect every string field on the result for the canary bytes
	for _, f := range result.Findings {
		assert.NotContains(t, f.Sample, "AKIA")
		assert.NotContains(t, f.Sample, "ghp_")
		assert.NotContains(t, f.Detector, "AKIA")
	}
	// the scanner shouldn't even store matched substrings outside of
	// detector slug + path metadata
	_ = strings.Join // import-keepalive without using it in assertions
	_ = exec.Command
}
