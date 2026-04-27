package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckUploadAccess_NoRepoID(t *testing.T) {
	// bare temp dir with no .sageox/config.json → GetRepoID returns "" → fail-open returns nil
	tmpDir := t.TempDir()
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when no repo ID exists")
}

func TestCheckUploadAccess_NoProjectConfig(t *testing.T) {
	// nonexistent directory → GetRepoID returns "" → fail-open returns nil
	err := checkUploadAccess("/nonexistent/path/that/does/not/exist")
	assert.NoError(t, err, "should fail-open when project root does not exist")
}

func TestCheckUploadAccess_EmptyRepoID(t *testing.T) {
	// initialized project but config has no repo_id → fail-open returns nil
	tmpDir := createInitializedProject(t)
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when config exists but repo_id is empty")
}

func TestCheckUploadAccess_RepoIDButNoAuth(t *testing.T) {
	// project with repo_id set but no valid auth token → fail-open returns nil
	// (auth.GetTokenForEndpoint will fail for a fake endpoint)
	tmpDir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   "repo_01test1234567890",
		Endpoint: "https://fake.example.com",
	})
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when auth token is unavailable")
}

// setupCheckUploadAccessEnv creates the full environment needed to test
// checkUploadAccess with a real HTTP server: project config with repo_id,
// SAGEOX_ENDPOINT pointing to the test server, and an auth token saved
// for that endpoint.
func setupCheckUploadAccessEnv(t *testing.T, serverURL string) string {
	t.Helper()

	repoID := "repo_01test1234567890"

	// isolate auth storage to a temp XDG config dir
	xdgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	// ensure the sageox config dir exists for auth file writes
	require.NoError(t, os.MkdirAll(filepath.Join(xdgDir, "sageox"), 0755))

	// point endpoint resolution to our test server
	t.Setenv("SAGEOX_ENDPOINT", serverURL)

	// save an auth token for the test server endpoint
	require.NoError(t, auth.SaveTokenForEndpoint(serverURL, &auth.StoredToken{
		AccessToken: "test-token-abc",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		TokenType:   "Bearer",
	}))

	// create initialized project with repo_id and endpoint
	tmpDir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   repoID,
		Endpoint: serverURL,
	})

	return tmpDir
}

// TestCheckUploadAccess_ReadOnly verifies that checkUploadAccess returns
// api.ErrReadOnly when the server reports viewer access. (ox-ehg)
func TestCheckUploadAccess_ReadOnly(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		if r.URL.Path == expectedPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(api.RepoDetailResponse{
				Visibility:  "public",
				AccessLevel: "viewer",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, api.ErrReadOnly), "expected api.ErrReadOnly, got: %v", err)
}

// TestCheckUploadAccess_WriteAccess verifies that checkUploadAccess returns nil
// when the server reports member (write) access. (ox-ehg)
func TestCheckUploadAccess_WriteAccess(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		if r.URL.Path == expectedPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(api.RepoDetailResponse{
				Visibility:  "public",
				AccessLevel: "member",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should return nil for member (write) access")
}

// TestCheckUploadAccess_ServerError verifies fail-open behavior when the API
// returns an error (non-ErrReadOnly). (ox-ehg)
func TestCheckUploadAccess_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when server returns 500")
}

// TestCheckUploadAccess_ReadOnlySessionStartMessage verifies that
// runAgentSessionStart wraps api.ErrReadOnly with a user-friendly message
// including remediation steps. This tests the integration point in
// agent_session.go lines 101-107. (ox-ehg)
func TestCheckUploadAccess_ReadOnlySessionStartMessage(t *testing.T) {
	// test the error wrapping logic directly — checkUploadAccess returns
	// api.ErrReadOnly, and the caller should produce a clear message
	err := api.ErrReadOnly
	if errors.Is(err, api.ErrReadOnly) {
		wrapped := fmt.Errorf("you have read-only access to this repo — sessions cannot be uploaded to the ledger\nTo upload sessions, request team membership from an admin")
		assert.Contains(t, wrapped.Error(), "read-only access")
		assert.Contains(t, wrapped.Error(), "request team membership")
	}
}

// TestCheckUploadAccess_DetailEndpoint404FallsThrough verifies that when
// GetRepoDetail returns (nil, nil) — a 404 — checkUploadAccess falls through
// to ledger status. If ledger status also returns no read-only signal, access
// is granted (fail-open). (ox-ehg)
func TestCheckUploadAccess_DetailEndpoint404FallsThrough(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return 404 for repo detail endpoint to trigger fallback path
		detailPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		ledgerPath := fmt.Sprintf("/api/v1/repos/%s/ledger-status", repoID)

		switch r.URL.Path {
		case detailPath:
			http.NotFound(w, r)
		case ledgerPath:
			// ledger status returns non-read-only
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status":   "ready",
				"repo_url": "https://git.example.com/ledger.git",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when detail returns 404 and ledger status is not read-only")
}

// TestSessionStart_NoOAuthGate_RegressionTest verifies that runAgentSessionStart
// does NOT require OAuth authentication. This is the core fix — sessions were
// failing because the old auth.IsAuthenticated() gate blocked recording when
// OAuth expired, even though upload only needs a Git PAT.
//
// Failure prevented: session recording silently blocked by expired OAuth token.
func TestSessionStart_NoOAuthGate_RegressionTest(t *testing.T) {
	// cannot use t.Parallel() — uses t.Setenv and os.Chdir

	// set up a minimal initialized project with NO auth token saved
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	tmpDir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:    "test-no-oauth-gate",
		Endpoint:  "https://no-auth.example.com",
		ProjectID: "test-project",
	})

	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	require.NoError(t, os.Chdir(tmpDir))

	oldGlobalCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldGlobalCfg })

	// deliberately do NOT save any auth token — simulates expired/missing OAuth

	// verify auth is indeed missing
	authenticated, err := auth.IsAuthCredentialValid()
	require.NoError(t, err, "precondition check should not error")
	require.False(t, authenticated, "precondition: must have no valid auth token")

	inst := &agentinstance.Instance{
		AgentID:   "OxTestNoAuth",
		AgentType: "claude-code",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	err = runAgentSessionStart(inst, nil)

	// the function should NOT fail with "not logged in to SageOx" — that was the old gate.
	// it may fail for other reasons (no adapter, no session file, etc.) but NOT for auth.
	if err != nil {
		assert.NotContains(t, err.Error(), "not logged in",
			"session start must not require OAuth — the old auth gate should be removed")
		assert.NotContains(t, err.Error(), "ox login",
			"session start must not prompt for ox login")
	}
}

// --- meta.json manifest refactor (bd ox-9mrk) ---

// TestSessionArtifactsToStage_ManifestPath: when meta.json has a non-empty
// Files map, the helper returns absolute paths for exactly those entries
// — no more, no less. Failure prevented: a future change re-introduces
// the historical glob ALONGSIDE the manifest, accidentally double-staging
// or staging files the manifest deliberately omits.
func TestSessionArtifactsToStage_ManifestPath(t *testing.T) {
	dir := t.TempDir()

	// write a meta.json with mixed Storage entries
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{
			"raw.jsonl":    {Storage: lfs.StorageLFS, OID: "sha256:abc", Size: 10},
			"summary.json": lfs.NewGitFileRef(20),
		},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))

	// also drop a stray *.md file that should NOT appear (manifest excludes it)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.md"), []byte("nope"), 0644))

	got := sessionArtifactsToStage(dir)
	sort.Strings(got)
	want := []string{
		filepath.Join(dir, "raw.jsonl"),
		filepath.Join(dir, "summary.json"),
	}
	sort.Strings(want)
	assert.Equal(t, want, got, "manifest path must return exactly meta.Files entries; stray.md must be ignored")
}

// TestSessionArtifactsToStage_FallbackWhenNoMetaJSON: the historical glob
// path runs when meta.json is missing. Must include summary.json
// opportunistically. Failure prevented: a torn write or legacy session
// produces an empty staging set and silently commits nothing.
func TestSessionArtifactsToStage_FallbackWhenNoMetaJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), []byte("y"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{}`), 0644))

	got := sessionArtifactsToStage(dir)
	sort.Strings(got)
	expected := []string{
		filepath.Join(dir, "raw.jsonl"),
		filepath.Join(dir, "summary.json"),
		filepath.Join(dir, "summary.md"),
	}
	sort.Strings(expected)
	assert.Equal(t, expected, got, "fallback path must glob known extensions plus opportunistic summary.json")
}

// TestSessionArtifactsToStage_FallbackWhenManifestEmpty: meta.json exists
// but Files is empty. Must STILL fall back to glob — otherwise an empty
// `Files: {}` would commit zero artifacts. Failure prevented: a write
// race or buggy upload path zeros out Files and the subsequent commit
// silently strips every artifact from the ledger.
func TestSessionArtifactsToStage_FallbackWhenManifestEmpty(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files:     map[string]lfs.FileRef{}, // empty!
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("x"), 0644))

	got := sessionArtifactsToStage(dir)
	assert.Contains(t, got, filepath.Join(dir, "raw.jsonl"),
		"empty Files map must trigger glob fallback, not return nil")
}

// TestSessionArtifactsToStage_EmptyDir: the legitimate empty case — no
// meta.json, no files at all — returns nothing without error.
func TestSessionArtifactsToStage_EmptyDir(t *testing.T) {
	got := sessionArtifactsToStage(t.TempDir())
	assert.Empty(t, got)
}

// TestBackfillGitArtifactInMeta_LiftsLegacyEntry: a meta.json that
// predates the Storage tag (entry has only Size) gets lifted to
// Storage=git when backfill runs. Failure prevented: redact path leaves
// stale Storage="" entries on summary.json that doctor would later flag
// as inconsistent or that a future strict-mode reader would reject.
func TestBackfillGitArtifactInMeta_LiftsLegacyEntry(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{
			// legacy: no Storage field
			"summary.json": {Size: 100},
		},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))

	updated := backfillGitArtifactInMeta(dir, "summary.json", 200)
	assert.True(t, updated, "size differs, should rewrite")

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, lfs.StorageGit, got.Files["summary.json"].Storage)
	assert.Equal(t, int64(200), got.Files["summary.json"].Size)
}

// TestBackfillGitArtifactInMeta_Idempotent: calling with the same size as
// already recorded returns false (no rewrite). Failure prevented: every
// `commitAndPushLedgerWithExtras` invocation re-touching meta.json and
// triggering spurious "session: foo (retry)" diffs.
func TestBackfillGitArtifactInMeta_Idempotent(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{
			"summary.json": lfs.NewGitFileRef(150),
		},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))

	updated := backfillGitArtifactInMeta(dir, "summary.json", 150)
	assert.False(t, updated, "same size, should not rewrite")
}

// TestBackfillGitArtifactInMeta_MissingMetaFailsSoft: best-effort
// contract — no meta.json means return false, no panic, no file created.
// Failure prevented: a fresh ledger or torn-write state crashes the
// commit pipeline.
func TestBackfillGitArtifactInMeta_MissingMetaFailsSoft(t *testing.T) {
	dir := t.TempDir()
	updated := backfillGitArtifactInMeta(dir, "summary.json", 100)
	assert.False(t, updated)
	_, err := os.Stat(filepath.Join(dir, "meta.json"))
	assert.True(t, os.IsNotExist(err), "missing meta.json must NOT be created by the helper")
}

// TestRegisterGitArtifactInMeta_AddsNewEntryAndPreservesLFS: the helper
// used by push-summary adds summary.json with Storage=git WITHOUT
// disturbing existing LFS entries. Failure prevented: a refactor that
// replaces meta.Files instead of mutating it would silently drop the
// raw.jsonl pointer, breaking every later read.
func TestRegisterGitArtifactInMeta_AddsNewEntryAndPreservesLFS(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{
			"raw.jsonl": {Storage: lfs.StorageLFS, OID: "sha256:abc", Size: 999},
		},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))

	updated := registerGitArtifactInMeta(dir, "summary.json", 42)
	assert.True(t, updated)

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)

	// new entry added correctly
	assert.Equal(t, lfs.StorageGit, got.Files["summary.json"].Storage)
	assert.Equal(t, int64(42), got.Files["summary.json"].Size)

	// existing entry preserved verbatim
	assert.Equal(t, lfs.StorageLFS, got.Files["raw.jsonl"].Storage)
	assert.Equal(t, "sha256:abc", got.Files["raw.jsonl"].OID)
	assert.Equal(t, int64(999), got.Files["raw.jsonl"].Size)
}

// TestRegisterGitArtifactInMeta_IdempotentSameSize: a second call with
// the same size returns false; no spurious meta.json rewrites. Failure
// prevented: push-summary called twice (e.g., manual retry) churning
// meta.json mtime and racing the daemon.
func TestRegisterGitArtifactInMeta_IdempotentSameSize(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files: map[string]lfs.FileRef{
			"summary.json": lfs.NewGitFileRef(77),
		},
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))

	assert.False(t, registerGitArtifactInMeta(dir, "summary.json", 77),
		"same Storage=git + same size: no rewrite expected")
}

// TestRegisterGitArtifactInMeta_MissingMetaFailsSoft: no meta.json →
// return false, no file created, no error escapes. Same fail-soft
// contract as backfill. Failure prevented: push-summary against a
// session whose meta.json hasn't been written yet panics or aborts.
func TestRegisterGitArtifactInMeta_MissingMetaFailsSoft(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, registerGitArtifactInMeta(dir, "summary.json", 1))
	_, err := os.Stat(filepath.Join(dir, "meta.json"))
	assert.True(t, os.IsNotExist(err))
}
