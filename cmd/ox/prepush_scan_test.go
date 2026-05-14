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
		// docs/ is out of scope (see prePushScannerScopePrefixes) and must
		// not be counted in FilesScanned.
		"docs/README.md": "# Sessions\n\nProse with no credentials.\n",
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Findings, "false-positive: %v", result.Findings)
	assert.Equal(t, 2, result.FilesScanned,
		"expected only the two sessions/ files; docs/ is out of scope")
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

// TestRunPrePushSecretGate_NeverBlocks_FlatPathNoOpsRecovery covers the
// degenerate-path case (file directly under sessions/ with no session
// subdir). After ox-y3ok, neither auto-redact nor quarantine recognize
// that shape (splitSessionPath returns ok=false), but the gate still
// MUST return nil — that's the load-bearing "never block" invariant.
//
// Failure prevented: a malformed session path causes the gate to error
// and block routine pushes because the recovery path can't run.
func TestRunPrePushSecretGate_NeverBlocks_FlatPathNoOpsRecovery(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "token=ghp_alphabetabcdefghijklmnopqrstuvwxyz12\n",
	})

	err := runPrePushSecretGate(context.Background(), work)
	assert.NoError(t, err, "gate must NEVER return an error; recovery may no-op but push proceeds")
}

// TestRunPrePushSecretGate_AllowSecretsOverride verifies the env-var
// override still short-circuits the recovery pipeline. Under the
// never-block policy this is just a faster path: no scan, no
// auto-redact, no quarantine — publish as-is per user request.
func TestRunPrePushSecretGate_AllowSecretsOverride(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})

	err := runPrePushSecretGate(context.Background(), work)
	assert.NoError(t, err, "OX_ALLOW_SECRETS=1 must allow push")
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
//
// The canary lives under sessions/ — the fresh-ledger fallback respects
// the same scope filter as the diff path (see ox-cqdo).
func TestPrePushScanner_NoUpstreamFallsBackToHead(t *testing.T) {
	// Bare repo + working clone, but DON'T configure origin or push.
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(work, 0755))
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "test")
	leakPath := filepath.Join(work, "sessions", "fresh", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(leakPath), 0755))
	require.NoError(t, os.WriteFile(leakPath, []byte("AKIAIOSFODNN7EXAMPLE\n"), 0644))
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
	assert.Contains(t, msg, "ox session audit")
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

// TestScanPrePushForSecrets_OnlyScansSessionsScope is the scope contract.
//
// Failure prevented (ox-cqdo, 2026-05-12): in PR #597 the gate was unscoped
// and would scan every changed file in the push range. A daemon-written
// data/github/<date>/pr/<n>.json containing PR copy that happens to match
// `aws_sts_session_key` or `generic_secret` blocked every subsequent push
// of a healthy ledger — and the gate's recovery message pointed the user
// at `ox session audit` / `ox session redact`, which only operate on
// sessions/. The fix scoped the scanner to prePushScannerScopePrefixes;
// this test pins that contract so a future widening of scope must come
// with a deliberate broadening of the recovery surface.
func TestScanPrePushForSecrets_OnlyScansSessionsScope(t *testing.T) {
	// Same canary in both a sessions/ path (must be flagged) and a
	// data/github/ path (must NOT be flagged — it's a verbatim cache of
	// bytes already published on GitHub, intentionally not gated; see
	// prePushScannerScopePrefixes for the policy).
	canary := `{"text":"AKIAIOSFODNN7EXAMPLE"}` + "\n"
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/2026-05-12/raw.jsonl":       canary,
		"data/github/2026/05/09/pr/42.json":   canary,
		"data/github/2026/05/11/issue/7.json": canary,
		"kb/notes.md":                         canary,
		"team-context/conventions.md":         canary,
	})

	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotNil(t, result)

	// exactly one path is in scope; it must be the only one scanned and
	// the only one with findings.
	assert.Equal(t, 1, result.FilesScanned,
		"only sessions/ should be scanned; got FilesScanned=%d", result.FilesScanned)
	require.NotEmpty(t, result.Findings)
	for _, f := range result.Findings {
		assert.True(t, strings.HasPrefix(f.Path, "sessions/"),
			"finding outside sessions/ scope: %s (%s)", f.Path, f.Detector)
	}
}

// TestInPrePushScannerScope pins the scope predicate itself. Cheap to run
// and keeps the contract surface visible — the production scanner is built
// on top of this function, so widening it without a deliberate test edit
// would let new paths into the gate silently.
func TestInPrePushScannerScope(t *testing.T) {
	cases := map[string]bool{
		"sessions/abc/raw.jsonl":           true,
		"sessions/2026-05-12/meta.json":    true,
		"data/github/2026/05/11/pr/1.json": false,
		"data/github/issue/x.json":         false,
		"kb/notes.md":                      false,
		"team-context/conventions.md":      false,
		"docs/README.md":                   false,
		".sageox/config.json":              false,
		"":                                 false,
		"session/typo/raw.jsonl":           false, // singular: not the prefix
		"sessionsfoo/raw.jsonl":            false, // no trailing slash
	}
	for path, want := range cases {
		t.Run(path, func(t *testing.T) {
			assert.Equal(t, want, inPrePushScannerScope(path))
		})
	}
}

// TestFormatPrePushFindings_RecoveryMatchesScannerScope encodes the
// writer-vs-message contract that the original bug exposed: the recovery
// message tells the user to run `ox session audit` and `ox session redact`.
// Those commands operate on the same path prefix that the scanner inspects.
// If a future change widens prePushScannerScopePrefixes beyond what the
// recovery commands can fix, the user is again handed instructions that
// don't apply to the flagged path — exactly the symptom that prompted this
// fix.
//
// We pin both directions: every scope prefix must appear in some recovery
// command surface, and the recovery message must continue to reference
// session-scoped tooling.
func TestFormatPrePushFindings_RecoveryMatchesScannerScope(t *testing.T) {
	r := &PrePushScanResult{
		Findings: []PrePushFinding{
			{Detector: "aws_access_key", Path: "sessions/x/raw.jsonl", Line: 1},
		},
	}
	msg := FormatPrePushFindings(r)
	assert.Contains(t, msg, "ox session audit",
		"recovery message must reference the audit command that covers sessions/")
	assert.Contains(t, msg, "ox session redact",
		"recovery message must reference the redact command that covers sessions/")
	// If the scanner is ever widened, the recovery surface must widen too.
	// Today only "sessions/" is in scope.
	assert.Equal(t, []string{"sessions/"}, prePushScannerScopePrefixes,
		"scope widened without updating recovery message; see ox-cqdo")
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
