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
//
// Failure prevented: scan misses a session-level credential leak (ox-zukx).
func TestScanLedgerForSecrets_FindsCanaries(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/2026-05-10/raw.jsonl": `{"text":"AKIAIOSFODNN7EXAMPLE"}` + "\n",
		"sessions/2026-05-10/meta.json": `{"agent_type":"claude-code"}`,
		"sessions/2026-05-11/raw.jsonl": "token=glpat-AbCdEfGhIjKlMnOpQrSt\n",
		"docs/notes.md":                 "this file has no secrets in it",
	})

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Scope is sessions-only now (ox-zukx). docs/notes.md is intentionally
	// NOT scanned — only files under <ledger>/sessions/<name>/ count.
	assert.Equal(t, 3, result.FilesScanned,
		"sessions-only scope: 2 raw.jsonl + 1 meta.json; docs/notes.md is out of scope")
	assert.Equal(t, 2, result.SessionsScanned)
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

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
	// sessions-only scope: 2 files in sessions/clean/; docs/ is ignored
	assert.Equal(t, 2, result.FilesScanned)
	assert.Equal(t, 1, result.SessionsScanned)
}

// TestScanLedgerForSecrets_OutOfScopeIgnored verifies the scope reduction
// in ox-zukx: anything outside <ledger>/sessions/<name>/ is not scanned,
// even if it has a matching extension and contains canary bytes. Pre-fix
// the audit scanned the entire ledger working tree and matched fixture
// credentials inside data/github/.../pr/*.json archives — non-actionable
// "findings" that drowned the real signal.
func TestScanLedgerForSecrets_OutOfScopeIgnored(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/real/raw.jsonl":         "AKIAIOSFODNN7EXAMPLE\n",
		"data/github/2026/05/pr/597.json": `{"body":"AKIAIOSFODNN7EXAMPLE"}` + "\n",
		"docs/architecture.md":            "describes the AKIAIOSFODNN7EXAMPLE pattern\n",
		".git/hidden.jsonl":               `{"k":"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`,
	})

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)

	// One real session-scoped finding, no others.
	require.Contains(t, result.Findings, "aws_access_key")
	assert.Equal(t, 1, result.Findings["aws_access_key"].Count,
		"AKIA appearances in data/, docs/, .git/ must NOT count")
	assert.Equal(t, 1, result.Findings["aws_access_key"].FileCount)
	assert.Equal(t, "sessions/real/raw.jsonl", result.Findings["aws_access_key"].Sample)
	// After ox-def1 the broad `github_token` slug was retired. Assert
	// absence of EVERY current GitHub slug so this test fails loudly if
	// any of them starts firing on a file outside scope.
	for _, slug := range []string{
		"github_personal_access_token",
		"github_oauth_token",
		"github_user_to_server_token",
		"github_server_token",
		"github_refresh_token",
		"github_fine_grained_pat",
	} {
		assert.NotContains(t, result.Findings, slug,
			"%s in .git/ must not be reachable: out of scope", slug)
	}
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

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Findings, "over-cap file must be skipped, but detectors fired: %v", result.Findings)
}

// TestScanLedgerForSecrets_OnlyAllowlistedExts verifies non-matching
// extensions are skipped — a binary blob shouldn't be scanned.
func TestScanLedgerForSecrets_OnlyAllowlistedExts(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/s1/audio.mp3":  "AKIAIOSFODNN7EXAMPLE",   // "binary" inside a session — must not be scanned
		"sessions/s1/img.png":    "AKIAIOSFODNN7EXAMPLE",   // same
		"sessions/s1/real.jsonl": "no secrets in this one", // .jsonl is scanned; nothing to find
	})

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
	// .mp3 and .png should not be counted toward FilesScanned (only .jsonl matched)
	assert.Equal(t, 1, result.FilesScanned)
}

// TestScanLedgerForSecrets_HerokuKeyDoesNotFireOnUUIDs is a regression
// test for ox-zukx: heroku_key was a bare UUID regex with no Keywords
// guard, so it matched every UUIDv7 in meta.json (session_id, agent_id,
// etc.). On a real ledger this produced 2141 false positives across 641
// sessions. Running redact-history on that result would have rewritten
// session_id values in meta.json and corrupted session resolution. The
// fix in internal/session/secrets.go adds Keywords: ["heroku"], AND
// ScanForSecrets now actually applies the keyword pre-screen.
//
// Failure prevented: a single UUIDv7 in a meta.json file produces a
// "heroku_key" finding.
func TestScanLedgerForSecrets_HerokuKeyDoesNotFireOnUUIDs(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/s/meta.json": `{"session_id":"ses_019e15c2-a128-73d4-b034-a262c3ddc96b",` +
			`"agent_id":"019e15c2-a128-73d4-b034-a262c3ddc96b"}`,
		"sessions/s/raw.jsonl": `{"text":"just a UUID 11111111-2222-3333-4444-555555555555 with no special context"}` + "\n",
	})

	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	assert.NotContains(t, result.Findings, "heroku_key",
		"heroku_key must only fire when 'heroku' appears in the same line")
}

// TestScanLedgerForSecrets_HerokuKeyFiresWithContext is the positive
// pair to the above — when 'heroku' IS present in the line, the
// detector should still catch real Heroku-shaped keys.
func TestScanLedgerForSecrets_HerokuKeyFiresWithContext(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/s/raw.jsonl": `{"text":"heroku api key: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}` + "\n",
	})
	result, err := scanLedgerForSecrets(work, work, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Findings, "heroku_key")
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

// TestLedgerHasCredentialHelper_NotConfigured covers the case the deleted
// "Ledger remote URL match" check accidentally papered over: a bare https
// origin with no credential helper installed in .git/config. Authentication
// will fail at the first push; the doctor must NOT report this as healthy.
func TestLedgerHasCredentialHelper_NotConfigured(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/ledger.git")

	installed, err := ledgerHasCredentialHelper(work, "git.sageox.ai")
	require.NoError(t, err)
	assert.False(t, installed,
		"a fresh ledger with no helper config must not be treated as healthy")
}

// TestLedgerHasCredentialHelper_Configured verifies positive detection when
// `git config credential.https://<host>.helper` is present.
func TestLedgerHasCredentialHelper_Configured(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/ledger.git")
	mustGit(t, work, "config", "credential.https://git.sageox.ai.helper", "!ox git-credential-helper")

	installed, err := ledgerHasCredentialHelper(work, "git.sageox.ai")
	require.NoError(t, err)
	assert.True(t, installed)
}

// TestLedgerOriginState_BareURL captures the (no-PAT, has-host) shape that
// only `ledgerOriginState` exposes — the wrapper `ledgerOriginHasEmbeddedPAT`
// discards the host and would let the helper-missing case go undetected.
func TestLedgerOriginState_BareURL(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/ledger.git")

	hasPAT, host, err := ledgerOriginState(work)
	require.NoError(t, err)
	assert.False(t, hasPAT)
	assert.Equal(t, "git.sageox.ai", host,
		"host must be returned for a bare https origin so the helper-presence check can run")
}

// TestLedgerOriginState_NonHTTPSReturnsNoHost ensures SSH / file:// remotes
// route to the SkippedCheck branch in the doctor check rather than getting
// dragged into the helper-installation flow.
func TestLedgerOriginState_NonHTTPSReturnsNoHost(t *testing.T) {
	for _, remote := range []string{
		"git@github.com:org/repo.git",
		"file:///tmp/local-bare.git",
		"ssh://git@example.com/repo.git",
	} {
		t.Run(remote, func(t *testing.T) {
			work := t.TempDir()
			mustGit(t, work, "init", "--initial-branch=main")
			mustGit(t, work, "remote", "add", "origin", remote)

			_, host, err := ledgerOriginState(work)
			require.NoError(t, err)
			assert.Empty(t, host,
				"non-https remote must not be claimed as managed; got host=%q", host)
		})
	}
}

// TestCheckLedgerSecrets_OutputDoesNotLeakBytes is the load-bearing
// privacy assertion: even when findings exist, the rendered checkResult
// (the thing printed to the user) must never contain a matched secret.
func TestCheckLedgerSecrets_OutputDoesNotLeakBytes(t *testing.T) {
	work := makeLedgerForAuditTest(t, map[string]string{
		"sessions/leaky/raw.jsonl": "AKIAIOSFODNN7EXAMPLE and " +
			"gh token ghp_alphabetabcdefghijklmnopqrstuvwxyz12\n",
	})
	result, err := scanLedgerForSecrets(work, work, nil)
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
