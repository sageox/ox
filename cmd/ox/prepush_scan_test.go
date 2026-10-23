package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionSecretGateCannotBypassFailedVerification(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	ledger := newLedgerTestRepo(t)
	for _, amend := range []bool{false, true} {
		for _, failOn := range []int{1, 2} {
			calls := 0
			failure := errors.New("synthetic scanner failure")
			err := runSessionSecretGate(context.Background(), ledger, func() (*PrePushScanResult, error) {
				calls++
				if calls == failOn {
					return nil, failure
				}
				return &PrePushScanResult{Findings: []PrePushFinding{{Detector: "synthetic", Path: "sessions/unrecoverable.txt", Line: 1}}}, nil
			}, amend)
			if !errors.Is(err, failure) {
				t.Error("failed scan did not block publication despite override environment")
			}
		}
	}
}

// Failure prevented: a clean working-tree rescan hides a failed amend, leaving
// the secret-bearing holding commit unchanged.
func TestSessionSecretGate_RejectsIncompleteAutoRedaction(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger history and canonical redaction")
	}
	t.Setenv("OX_ALLOW_SECRETS", "1")
	const rel = "sessions/incomplete/raw.jsonl"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	work := makeLedgerWithCommit(t, map[string]string{
		rel:                             `{"type":"text","value":"` + canary + `"}` + "\n",
		"sessions/incomplete/meta.json": `{"version":"1.0","session_name":"incomplete","agent_id":"OxHold","created_at":"2026-05-11T00:00:00Z","files":{}}`,
	})
	head := mustGitCapture(t, work, "rev-parse", "HEAD")

	err := runPrePushSecretGate(context.Background(), work)

	require.Error(t, err, "working-tree cleanup cannot substitute for a successful commit repair")
	assert.Contains(t, err.Error(), "auto-redact")
	assert.Contains(t, err.Error(), "LFS pointer", "unregistered raw content must use the canonical storage boundary")
	assert.Equal(t, head, mustGitCapture(t, work, "rev-parse", "HEAD"))
	assert.True(t, strings.Contains(mustGitCapture(t, work, "show", "HEAD:"+rel), canary), "the original commit remains pending")
	working, readErr := os.ReadFile(filepath.Join(work, rel))
	require.NoError(t, readErr)
	assert.False(t, strings.Contains(string(working), canary), "clean local bytes must not hide the earlier failure")
}

func TestSessionSecretGateRequiresVerifiedCleanAfterQuarantine(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	calls := 0
	ledger := newLedgerTestRepo(t)
	err := runSessionSecretGate(context.Background(), ledger, func() (*PrePushScanResult, error) {
		calls++
		return &PrePushScanResult{Findings: []PrePushFinding{{Detector: "synthetic", Path: "sessions/unrecoverable.txt", Line: 1}}}, nil
	}, false)
	if err == nil {
		t.Error("publication accepted without a verified clean final scan")
	}
	if calls != 3 {
		t.Errorf("scanner calls = %d, want final post-quarantine verification", calls)
	}
}

func TestScanPathsRefusesUninspectableSessionContent(t *testing.T) {
	t.Run("stat failure", func(t *testing.T) {
		ledger := t.TempDir()
		path := filepath.Join(ledger, "sessions", "unreadable.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.Symlink("unreadable.jsonl", path))
		_, err := scanPaths(ledger, []string{"sessions/unreadable.jsonl"})
		if err == nil {
			t.Fatal("uninspectable session path was silently accepted")
		}
	})
	t.Run("legitimate deletion", func(t *testing.T) {
		_, err := scanPaths(t.TempDir(), []string{"sessions/deleted.jsonl"})
		require.NoError(t, err)
	})
}

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
	require.NoError(t, os.MkdirAll(work, 0755))

	initBareRemote(t, bare, "--initial-branch=main")
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

// A read failure must keep both session uploads and ledger pushes pending.
func TestSessionSecretGate_ScanErrorsKeepUploadsPending(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	for _, mode := range []struct {
		name        string
		amendCommit bool
	}{
		{name: "upload"},
		{name: "existing_push", amendCommit: true},
	} {
		for _, phase := range []struct {
			name   string
			failOn int
		}{
			{name: "initial_scan", failOn: 1},
			{name: "rescan", failOn: 2},
		} {
			t.Run(mode.name+"/"+phase.name, func(t *testing.T) {
				ledger := newLedgerTestRepo(t)
				const rel = "sessions/scan-error/summary.md"
				path := filepath.Join(ledger, filepath.FromSlash(rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte("AKIAIOSFODNN7EXAMPLE\n"), 0o600))
				calls := 0
				var scanErr error
				err := runSessionSecretGate(context.Background(), ledger, func() (*PrePushScanResult, error) {
					calls++
					if calls == phase.failOn {
						// Simulate a filesystem change between preparation and reading.
						// A directory fails to scan on every supported platform.
						require.NoError(t, os.Remove(path))
						require.NoError(t, os.Mkdir(path, 0o700))
					}
					result, err := scanPaths(ledger, []string{rel})
					if err != nil {
						scanErr = err
					}
					return result, err
				}, mode.amendCommit)
				require.Error(t, scanErr, "the filesystem failure must reach the scanner")
				assert.Equal(t, phase.failOn, calls)
				require.ErrorIs(t, err, scanErr, "uninspected content must not reach publication")
			})
		}
	}
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

// Malformed paths that recovery cannot recognize still block publication.
func TestRunPrePushSecretGate_BlocksUnrecoverableFlatPath(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "token=ghp_alphabetabcdefghijklmnopqrstuvwxyz12\n",
	})
	require.Error(t, runPrePushSecretGate(context.Background(), work))
}

func TestRunPrePushSecretGate_OverrideCannotBypassVerification(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	work := makeLedgerWithCommit(t, map[string]string{
		"sessions/leak.jsonl": "AKIAIOSFODNN7EXAMPLE\n",
	})
	require.Error(t, runPrePushSecretGate(context.Background(), work), "legacy environment override must not publish credentials")
}

func TestPrePushScanner_InspectsBinaryExtensions(t *testing.T) {
	bin := []byte("\x89PNG\r\n\x1a\nAKIAIOSFODNN7EXAMPLE")
	work := makeLedgerWithCommit(t, map[string]string{"sessions/img.png": string(bin)})
	result, err := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, err)
	require.NotEmpty(t, result.Findings, "renaming session content must not bypass inspection")
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
	assert.NotContains(t, msg, "OX_ALLOW_SECRETS")
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
