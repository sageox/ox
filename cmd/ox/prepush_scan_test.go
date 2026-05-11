package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLedgerWithCommit creates a git repo, makes an initial commit on main,
// configures origin to point at a bare clone (so origin/main resolves), then
// adds the given files in a second commit that the test can inspect.
//
// Returns the ledger working tree path. The bare clone is in tempdir.
func makeLedgerWithCommit(t *testing.T, files map[string]string) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	work := filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(bare, 0755))
	require.NoError(t, os.MkdirAll(work, 0755))

	mustGit(t, bare, "init", "--bare", "--initial-branch=main")
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	mustGit(t, work, "remote", "add", "origin", bare)

	// initial empty commit on main
	require.NoError(t, os.WriteFile(filepath.Join(work, "README"), []byte("initial"), 0644))
	mustGit(t, work, "add", "README")
	mustGit(t, work, "commit", "-m", "initial")
	mustGit(t, work, "push", "-u", "origin", "main")

	// second commit with the test files
	for rel, content := range files {
		abs := filepath.Join(work, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0644))
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "test changes")
	// note: NOT pushed — the scanner inspects what would be pushed

	return work
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), string(out))
}

// TestScanPrePushForSecrets_FindsAwsKey is the load-bearing test: a planted
// AWS canary must be flagged before any bytes reach the cloud.
func TestScanPrePushForSecrets_FindsAwsKey(t *testing.T) {
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/2026-05-10/raw.jsonl": `{"text":"AKIAIOSFODNN7EXAMPLE"}` + "\n",
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotNil(t, result)

	// must flag the canary
	assert.NotEmpty(t, result.Findings)
	hit := false
	for _, f := range result.Findings {
		if f.Detector == "aws_access_key" {
			hit = true
			assert.Equal(t, "sessions/2026-05-10/raw.jsonl", f.Path)
			assert.Equal(t, 1, f.Line)
		}
	}
	assert.True(t, hit, "aws_access_key detector did not fire: %v", result.Findings)
}

// TestScanPrePushForSecrets_CleanLedgerPasses verifies the gate does not
// false-positive on a Ledger whose new commit contains only legitimate text.
// Failure prevented: gate becomes a permanent block on routine session
// pushes, training users to set OX_ALLOW_SECRETS=1 and defeating the point.
func TestScanPrePushForSecrets_CleanLedgerPasses(t *testing.T) {
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/2026-05-10/raw.jsonl": `{"text":"hello world, just a chat message"}` + "\n",
		"sessions/2026-05-10/meta.json": `{"agent_type":"claude-code","files":[]}` + "\n",
		"docs/README.md":                "# Sessions\n\nProse with no credentials.\n",
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Findings, "false-positive: %v", result.Findings)
	assert.GreaterOrEqual(t, result.FilesScanned, 3, "expected to scan all 3 files")
}

func TestScanPrePushForSecrets_ScansOnlyDiffFiles(t *testing.T) {
	// File that exists on origin/main with a secret should NOT be flagged —
	// it's not part of the diff being pushed. Only NEW changes matter for
	// the pre-push gate.
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/clean.jsonl": "no secrets here\n",
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
	// initial README is on origin/main, not in the diff — should not count
	for _, f := range result.Findings {
		assert.NotEqual(t, "README", f.Path)
	}
}

// TestRunPrePushSecretGate_RefusesWhenSecretsPresent verifies the gate
// returns a formatted error and the error contains the detector name and
// path (but NOT the secret bytes themselves).
func TestRunPrePushSecretGate_RefusesWhenSecretsPresent(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "") // ensure override off
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "token=ghp_alphabetabcdefghijklmnopqrstuvwxyz12\n",
	})

	err := runPrePushSecretGate(context.Background(), work)
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "Push refused")
	assert.Contains(t, msg, "github_token") // detector name
	assert.Contains(t, msg, "sessions/leak.jsonl")
	// MUST NOT include the secret bytes
	assert.NotContains(t, msg, "alphabetabcdefghijklmnopqrstuvwxyz")
	// remediation guidance must be present
	assert.Contains(t, msg, "OX_ALLOW_SECRETS=1")
}

// TestRunPrePushSecretGate_AllowSecretsOverride verifies the env-var escape
// hatch lets the push through with a loud warning. Required for emergency
// overrides — without this, the gate would be a permanent block in any
// scenario the detectors mis-classify.
func TestRunPrePushSecretGate_AllowSecretsOverride(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})

	err := runPrePushSecretGate(context.Background(), work)
	assert.NoError(t, err, "OX_ALLOW_SECRETS=1 must allow push")
}

// TestRunPrePushSecretGate_AllowSecretsRecognizesFalse verifies that values
// that look like "off" (0, false, no, off, "") do NOT bypass the gate.
func TestRunPrePushSecretGate_AllowSecretsRecognizesFalse(t *testing.T) {
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	for _, off := range []string{"", "0", "false", "no", "OFF"} {
		t.Run("OX_ALLOW_SECRETS="+off, func(t *testing.T) {
			t.Setenv("OX_ALLOW_SECRETS", off)
			err := runPrePushSecretGate(context.Background(), work)
			assert.Error(t, err, "off-like value %q should NOT bypass gate", off)
		})
	}
}

// TestPrePushScanner_SkipsBinaryExtensions verifies binary blobs are skipped.
// Without this, the scanner spends time running regexes against PNG bytes
// — high cost, zero signal. Worse, by random luck a PNG could trip a
// detector and false-positive a Ledger.
func TestPrePushScanner_SkipsBinaryExtensions(t *testing.T) {
	// Use bytes that contain a substring matching an AWS key on plain regex —
	// proves the skip works (otherwise this PNG would be flagged).
	bin := []byte("\x89PNG\r\n\x1a\nAKIAIOSFODNN7EXAMPLE")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/img.png": string(bin),
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	for _, f := range result.Findings {
		assert.NotEqual(t, "sessions/img.png", f.Path,
			"PNG should be skipped, but detector %s fired", f.Detector)
	}
}

// TestPrePushScanner_NoUpstreamFallsBackToHead verifies the case where the
// ledger has no origin/main yet — scanner falls back to scanning all
// tracked files. Without this, brand-new ledgers (first push) would skip
// the gate entirely.
func TestPrePushScanner_NoUpstreamFallsBackToHead(t *testing.T) {
	// Bare repo + working clone, but DON'T configure origin or push.
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(work, 0755))
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "leak.txt"), []byte("AKIAIOSFODNN7EXAMPLE\n"), 0644))
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "first")

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotEmpty(t, result.Findings, "fallback scan should still catch the canary")
}

func TestFormatPrePushFindings_DoesNotLeakBytes(t *testing.T) {
	r := &PrePushScanResult{
		Findings: []PrePushFinding{
			{Detector: "aws_access_key", Path: "leak.jsonl", Line: 42},
		},
	}
	msg := FormatPrePushFindings(r)
	assert.Contains(t, msg, "aws_access_key")
	assert.Contains(t, msg, "leak.jsonl:42")
	assert.Contains(t, msg, "ox doctor --check=ledger-secrets")
	assert.Contains(t, msg, "OX_ALLOW_SECRETS=1")
}

func TestPrePushSecretsAllowed_Truthy(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"no":    false,
		"OFF":   false,
		"1":     true,
		"true":  true,
		"yes":   true,
		"y":     true,
	}
	for v, want := range cases {
		t.Run("v="+v, func(t *testing.T) {
			t.Setenv("OX_ALLOW_SECRETS", v)
			assert.Equal(t, want, prePushSecretsAllowed())
		})
	}
}

// TestPrePushScanner_LargeFileExceedsCap verifies that files over the size
// cap are skipped (not scanned and not flagged). Performance budget.
func TestPrePushScanner_LargeFileExceedsCap(t *testing.T) {
	// Build a file just past the cap, with the canary at the end so a naive
	// scanner would catch it.
	const sz = prePushScannerSizeCap + 1024
	buf := make([]byte, sz)
	for i := range buf {
		buf[i] = 'A'
	}
	copy(buf[sz-len("AKIAIOSFODNN7EXAMPLE"):], []byte("AKIAIOSFODNN7EXAMPLE"))
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/huge.txt": string(buf),
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	for _, f := range result.Findings {
		assert.NotEqual(t, "sessions/huge.txt", f.Path,
			"over-cap file should be skipped, but %s fired", f.Detector)
	}
}
