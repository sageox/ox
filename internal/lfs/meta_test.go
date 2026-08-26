package lfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndReadSessionMeta(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "2026-01-06T14-32-ryan-Ox7f3a")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "2026-01-06T14-32-ryan-Ox7f3a",
		Username:    "ryan@sageox.ai",
		AgentID:     "Ox7f3a",
		AgentType:   "claude-code",
		Model:       "claude-sonnet-4",
		CreatedAt:   time.Date(2026, 1, 6, 14, 32, 0, 0, time.UTC),
		EntryCount:  42,
		Summary:     "Implemented LFS pipeline",
		Files: map[string]FileRef{
			"raw.jsonl":    {OID: "sha256:abc123", Size: 1024},
			"summary.md":   {OID: "sha256:ghi789", Size: 256},
			"session.md":   {OID: "sha256:jkl012", Size: 2048},
			"session.html": {OID: "sha256:mno345", Size: 4096},
		},
	}

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)

	// read back
	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, meta.Version, got.Version)
	assert.Equal(t, meta.SessionName, got.SessionName)
	assert.Equal(t, meta.Username, got.Username)
	assert.Equal(t, meta.AgentID, got.AgentID)
	assert.Equal(t, meta.AgentType, got.AgentType)
	assert.Equal(t, meta.Model, got.Model)
	assert.Equal(t, meta.EntryCount, got.EntryCount)
	assert.Equal(t, meta.Summary, got.Summary)
	assert.Len(t, got.Files, 4)
	assert.Equal(t, "sha256:abc123", got.Files["raw.jsonl"].OID)
	assert.Equal(t, int64(1024), got.Files["raw.jsonl"].Size)
}

func TestWriteSessionMeta_Nil(t *testing.T) {
	err := WriteSessionMeta(t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil session meta")
}

func TestReadSessionMeta_NotFound(t *testing.T) {
	_, err := ReadSessionMeta(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta.json not found")
}

func TestCheckHydrationStatus_Hydrated(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// create content files
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("data"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 4},
			"summary.md": {OID: "sha256:def", Size: 4},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusHydrated, status)
}

func TestCheckHydrationStatus_Dehydrated(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestCheckHydrationStatus_Partial(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// only create one of two files
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 4},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusPartial, status)
}

func TestCheckHydrationStatus_NilMeta(t *testing.T) {
	status := CheckHydrationStatus(t.TempDir(), nil)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestCheckHydrationStatus_EmptyFiles(t *testing.T) {
	meta := &SessionMeta{
		Files: map[string]FileRef{},
	}
	status := CheckHydrationStatus(t.TempDir(), meta)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestNewFileRef(t *testing.T) {
	content := []byte("test content")
	ref := NewFileRef(content)
	assert.Equal(t, int64(len(content)), ref.Size)
	assert.True(t, len(ref.OID) > 7)
	assert.Equal(t, "sha256:", ref.OID[:7])
}

func TestNewSessionMeta_Builder(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	meta := NewSessionMeta("session-1", "user@test.com", "Ox1234", "claude-code", ts).
		Model("claude-sonnet-4").
		Title("Test session").
		Summary("Did some work").
		EntryCount(10).
		UserID("usr_abc123").
		RepoID("repo_xyz789").
		Build()

	assert.Equal(t, "1.0", meta.Version)
	assert.Equal(t, "session-1", meta.SessionName)
	assert.Equal(t, "user@test.com", meta.Username)
	assert.Equal(t, "Ox1234", meta.AgentID)
	assert.Equal(t, "claude-code", meta.AgentType)
	assert.Equal(t, ts, meta.CreatedAt)
	assert.Equal(t, "claude-sonnet-4", meta.Model)
	assert.Equal(t, "Test session", meta.Title)
	assert.Equal(t, "Did some work", meta.Summary)
	assert.Equal(t, 10, meta.EntryCount)
	assert.Equal(t, "usr_abc123", meta.UserID)
	assert.Equal(t, "repo_xyz789", meta.RepoID)
	assert.NotNil(t, meta.Files)
	assert.Empty(t, meta.Files)
}

func TestNewSessionMeta_DefaultFiles(t *testing.T) {
	meta := NewSessionMeta("s", "u", "a", "t", time.Now()).Build()
	assert.NotNil(t, meta.Files)
	assert.Empty(t, meta.Files)
}

func TestNewSessionMeta_WithFiles(t *testing.T) {
	files := map[string]FileRef{
		"raw.jsonl": {OID: "sha256:abc", Size: 100},
	}
	meta := NewSessionMeta("s", "u", "a", "t", time.Now()).
		WithFiles(files).
		Build()
	assert.Equal(t, files, meta.Files)
}

func TestNewSessionMeta_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := NewSessionMeta("test-session", "user@test.com", "Ox1234", "claude-code",
		time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)).
		Model("claude-sonnet-4").
		UserID("usr_abc").
		RepoID("repo_xyz").
		WithFiles(map[string]FileRef{
			"raw.jsonl": {OID: "sha256:abc123", Size: 1024},
		}).
		Build()

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, meta.UserID, got.UserID)
	assert.Equal(t, meta.RepoID, got.RepoID)
	assert.Equal(t, meta.Model, got.Model)
	assert.Equal(t, "sha256:abc123", got.Files["raw.jsonl"].OID)
}

// TestSessionMeta_LinkageFieldsRoundTrip verifies the ProducedCommits,
// LinkedPRs, LinkedIssues, and LinkageStatus fields survive a write/read
// cycle. Failure prevented: a JSON tag typo silently drops linkage data,
// making `ox session view` show empty PR/Issue/Commit sections.
func TestSessionMeta_LinkageFieldsRoundTrip(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "linked-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	meta := NewSessionMeta("linked-session", "user", "Ox1234", "claude-code",
		time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)).
		RepoID("repo_xyz").
		ContinuedFromSessionID("ses_019f6f6a-d182-7f4e-a54d-8dcb44567c8d").
		ProducedCommits([]string{"abc1234", "def5678"}).
		LinkedPRs([]string{"https://github.com/owner/repo/pull/42"}).
		LinkedIssues([]string{"#101", "#103"}).
		LinkageStatus(LinkageStatusUploaded).
		Build()

	require.NoError(t, WriteSessionMeta(sessionDir, meta))

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"abc1234", "def5678"}, got.ProducedCommits)
	assert.Equal(t, []string{"https://github.com/owner/repo/pull/42"}, got.LinkedPRs)
	assert.Equal(t, []string{"#101", "#103"}, got.LinkedIssues)
	assert.Equal(t, LinkageStatusUploaded, got.LinkageStatus)
	assert.Equal(t, meta.ContinuedFromSessionID, got.ContinuedFromSessionID)
}

// TestSessionMeta_LegacyWithoutLinkageFields verifies a meta.json written
// before these fields existed (no linkage keys) reads back with empty
// slices and empty status — no error, no spurious data.
// Failure prevented: a required (non-omitempty) tag would break legacy reads.
func TestSessionMeta_LegacyWithoutLinkageFields(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "legacy-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// hand-write a minimal legacy meta.json with no linkage keys
	legacy := `{"version":"1.0","session_name":"legacy-session","username":"user","agent_id":"Ox1","agent_type":"claude-code","created_at":"2026-01-01T00:00:00Z","files":{}}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(legacy), 0o644))

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Empty(t, got.ProducedCommits)
	assert.Empty(t, got.LinkedPRs)
	assert.Empty(t, got.LinkedIssues)
	assert.Empty(t, got.LinkageStatus)
}

func TestUpdateMetaSummary(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string)
		summary     string
		wantErr     bool
		wantErrMsg  string
		wantSummary string
		wantSession string
		wantUser    string
	}{
		{
			name: "updates summary and preserves other fields",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				meta := NewSessionMeta("test-session", "user@test.com", "Ox1234", "claude-code",
					time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)).
					Summary("/ox-session-start\n\n5 user messages, 10 assistant responses").
					Build()
				require.NoError(t, WriteSessionMeta(dir, meta))
			},
			summary:     "Implemented auth system with OAuth2 flow",
			wantSummary: "Implemented auth system with OAuth2 flow",
			wantSession: "test-session",
			wantUser:    "user@test.com",
		},
		{
			name:       "returns error when meta.json missing",
			setup:      func(t *testing.T, dir string) { t.Helper() },
			summary:    "some summary",
			wantErr:    true,
			wantErrMsg: "meta.json not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session")
			require.NoError(t, os.MkdirAll(dir, 0755))
			tt.setup(t, dir)

			err := UpdateMetaSummary(dir, tt.summary)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)

			got, err := ReadSessionMeta(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSummary, got.Summary)
			assert.Equal(t, tt.wantSession, got.SessionName)
			assert.Equal(t, tt.wantUser, got.Username)
		})
	}
}

// TestSessionMeta_StorageRoundTrip verifies that the FileRef Storage tag
// survives a marshal+unmarshal cycle for all three encodings:
//
//  1. Storage="lfs" (new explicit form)
//  2. Storage="git" (new git-stored form)
//  3. Storage="" (legacy meta.json files written before the refactor)
//
// And — critically — that legacy entries do NOT gain a `"storage"` key on
// rewrite: the field is `omitempty`, so a freshly-marshaled legacy ref
// stays byte-equivalent to what we read. Without this guard, every rewrite
// of an old meta.json would churn `"storage":""` into the JSON, breaking
// any tooling that does byte-equality on ledger files.
//
// Failure prevented: silently rewriting every legacy meta.json on first
// write-after-refactor with a spurious storage key.
func TestSessionMeta_StorageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// build a manifest with one of each kind
	original := &SessionMeta{
		Version:     "1.0",
		SessionName: "test",
		Username:    "user@test",
		AgentID:     "Oxabc",
		AgentType:   "claude-code",
		CreatedAt:   time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		Files: map[string]FileRef{
			"raw.jsonl":    {Storage: StorageLFS, OID: "sha256:lfs1", Size: 100},
			"summary.json": {Storage: StorageGit, Size: 42},
			"legacy.md":    {OID: "sha256:lfs2", Size: 200}, // no Storage field
		},
	}

	// marshal → unmarshal → assert all three preserved through the typed reader
	require.NoError(t, WriteSessionMetaOnly(sessionDir, original))
	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)

	assert.Equal(t, StorageLFS, got.Files["raw.jsonl"].Storage)
	assert.Equal(t, StorageGit, got.Files["summary.json"].Storage)
	assert.Equal(t, "", got.Files["legacy.md"].Storage, "legacy entry must keep empty Storage on read")
	assert.True(t, got.Files["legacy.md"].IsLFS(), "legacy entry must be treated as LFS via EffectiveStorage")
	assert.Equal(t, "", got.Files["summary.json"].OID, "git-stored entry should have empty OID")

	// raw JSON inspection: the legacy entry must NOT gain a "storage" key
	rawBytes, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	var raw struct {
		Files map[string]map[string]any `json:"files"`
	}
	require.NoError(t, json.Unmarshal(rawBytes, &raw))

	_, legacyHasStorage := raw.Files["legacy.md"]["storage"]
	assert.False(t, legacyHasStorage,
		"legacy entry must NOT have a storage key on rewrite (omitempty regression)")

	_, lfsHasStorage := raw.Files["raw.jsonl"]["storage"]
	assert.True(t, lfsHasStorage, "explicit Storage=lfs must be persisted")

	_, gitHasStorage := raw.Files["summary.json"]["storage"]
	assert.True(t, gitHasStorage, "explicit Storage=git must be persisted")

	_, gitHasOID := raw.Files["summary.json"]["oid"]
	assert.False(t, gitHasOID, "git-stored entry must NOT persist an oid key (omitempty)")
}

// TestWriteSessionMeta_PreservesGitContent_ReplacesLFSContent verifies the
// load-bearing safety property at the public API level: when meta.Files
// contains a mix of Storage=lfs and Storage=git entries, WriteSessionMeta
// replaces the LFS files with pointers but leaves the git-stored file's
// real bytes untouched.
//
// TestWritePointerFiles_SkipsGitStorage covers the inner helper; this test
// covers the entrypoint that callers actually use (push-summary's later
// meta rewrites, redact's mid-flow rewrite, doctor repairs).
//
// Failure prevented: any future change to WriteSessionMeta that bypasses
// the WritePointerFiles guard (e.g., inlining the loop) would clobber
// summary.json with an empty pointer file every time meta.json is
// rewritten. Test asserts the user-visible invariant directly.
func TestWriteSessionMeta_PreservesGitContent_ReplacesLFSContent(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write real content for both files
	rawContent := []byte(`{"event":"recording"}`)
	summaryContent := []byte(`{"title":"hello","summary":"world"}`)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryContent, 0644))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "mixed",
		Username:    "user@test",
		AgentID:     "Oxmix",
		AgentType:   "claude-code",
		CreatedAt:   time.Now(),
		Files: map[string]FileRef{
			"raw.jsonl":    {Storage: StorageLFS, OID: "sha256:" + ComputeOID(rawContent), Size: int64(len(rawContent))},
			"summary.json": NewGitFileRef(int64(len(summaryContent))),
		},
	}
	require.NoError(t, WriteSessionMeta(sessionDir, meta))

	// LFS entry: replaced with a pointer file
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	assert.True(t, IsPointerFile(rawPath), "raw.jsonl must be replaced with an LFS pointer")

	// Git entry: real bytes intact
	summaryPath := filepath.Join(sessionDir, "summary.json")
	assert.False(t, IsPointerFile(summaryPath), "summary.json must NOT be a pointer file")
	gotSummary, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	assert.Equal(t, summaryContent, gotSummary, "summary.json content must survive WriteSessionMeta unchanged")
}

func TestWriteSessionMeta_ReplacesContentWithPointers(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write real content files that simulate post-upload state
	rawContent := []byte(`{"event":"test"}`)
	summaryContent := []byte("# Summary")
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "test",
		Username:    "test@example.com",
		AgentID:     "OxTest",
		AgentType:   "claude-code",
		CreatedAt:   time.Now(),
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:" + ComputeOID(rawContent), Size: 16},
			"summary.md": {OID: "sha256:" + ComputeOID(summaryContent), Size: 9},
		},
	}

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	// content files should now be pointer files
	assert.True(t, IsPointerFile(filepath.Join(sessionDir, "raw.jsonl")),
		"raw.jsonl should be replaced with a pointer file")
	assert.True(t, IsPointerFile(filepath.Join(sessionDir, "summary.md")),
		"summary.md should be replaced with a pointer file")

	// pointer files should round-trip correctly
	ref, err := ReadPointerFile(filepath.Join(sessionDir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "sha256:"+ComputeOID(rawContent), ref.OID)
	assert.Equal(t, int64(16), ref.Size)
}

func TestWriteSessionMeta_EmptyFiles_NoReplace(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write a content file
	contentPath := filepath.Join(sessionDir, "raw.jsonl")
	content := []byte(`{"event":"test"}`)
	require.NoError(t, os.WriteFile(contentPath, content, 0644))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "test",
		Username:    "test@example.com",
		AgentID:     "OxTest",
		AgentType:   "claude-code",
		CreatedAt:   time.Now(),
		Files:       map[string]FileRef{}, // empty
	}

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	// content file should NOT be replaced
	got, err := os.ReadFile(contentPath)
	require.NoError(t, err)
	assert.Equal(t, content, got, "content file should be unchanged when Files is empty")
}

func TestCheckHydrationStatus_WithPointers(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write pointer files (not real content)
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		AssertUploaded(FileRef{OID: "sha256:abc", Size: 100}),
	))
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "summary.md"),
		AssertUploaded(FileRef{OID: "sha256:def", Size: 200}),
	))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusDehydrated, status,
		"pointer files should be detected as dehydrated")
}

func TestCheckHydrationStatus_MixedPointerAndContent(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// one pointer file, one real content file
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		AssertUploaded(FileRef{OID: "sha256:abc", Size: 100}),
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "summary.md"),
		[]byte("# Summary with enough content to not be a pointer"), 0644,
	))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 50},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusPartial, status,
		"mix of pointer and content should be partial")
}

// --- CheckHydrationStatusWithCache ---

func TestCheckHydrationStatusWithCache_AllInCache(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "test-session")
	cacheDir := filepath.Join(dir, ".sageox", "cache", "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// pointer files in session dir
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		AssertUploaded(FileRef{OID: "sha256:abc", Size: 100}),
	))
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "summary.md"),
		AssertUploaded(FileRef{OID: "sha256:def", Size: 200}),
	))

	// real content in cache
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte("real content here"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "summary.md"), []byte("# Real summary content"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatusWithCache(sessionDir, cacheDir, meta)
	assert.Equal(t, HydrationStatusHydrated, status,
		"files in cache should count as hydrated")
}

func TestCheckHydrationStatusWithCache_MixedLocations(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "test-session")
	cacheDir := filepath.Join(dir, ".sageox", "cache", "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// real content in session dir (legacy hydration)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("real content here"), 0644))
	// real content in cache
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "summary.md"), []byte("# Cached summary"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatusWithCache(sessionDir, cacheDir, meta)
	assert.Equal(t, HydrationStatusHydrated, status,
		"mix of in-place and cached content should be hydrated")
}

func TestCheckHydrationStatusWithCache_PartialCacheOnly(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "test-session")
	cacheDir := filepath.Join(dir, ".sageox", "cache", "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// pointer in session dir, nothing else
	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		AssertUploaded(FileRef{OID: "sha256:abc", Size: 100}),
	))
	// only summary in cache, raw.jsonl missing from cache too
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "summary.md"), []byte("# Cached summary"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc", Size: 100},
			"summary.md": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatusWithCache(sessionDir, cacheDir, meta)
	assert.Equal(t, HydrationStatusPartial, status,
		"one file in cache, one missing should be partial")
}

func TestCheckHydrationStatusWithCache_EmptyCache(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	require.NoError(t, WritePointerFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		AssertUploaded(FileRef{OID: "sha256:abc", Size: 100}),
	))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl": {OID: "sha256:abc", Size: 100},
		},
	}

	// cache path doesn't exist
	status := CheckHydrationStatusWithCache(sessionDir, filepath.Join(dir, "nonexistent"), meta)
	assert.Equal(t, HydrationStatusDehydrated, status,
		"pointer in session dir with no cache should be dehydrated")
}

func TestCheckHydrationStatusWithCache_NilMeta(t *testing.T) {
	status := CheckHydrationStatusWithCache(t.TempDir(), t.TempDir(), nil)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestFileRef_BareOID(t *testing.T) {
	tests := []struct {
		oid      string
		expected string
	}{
		{"sha256:abc123", "abc123"},
		{"abc123", "abc123"},
		{"sha256:", "sha256:"},
		{"short", "short"},
	}

	for _, tt := range tests {
		ref := FileRef{OID: tt.oid}
		assert.Equal(t, tt.expected, ref.BareOID(), "BareOID for %q", tt.oid)
	}
}

func TestWriteSessionMetaOnly_DoesNotReplaceContentFiles(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	rawContent := []byte(`{"type":"header"}`)
	summaryContent := []byte("# Summary")
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "test",
		Username:    "test@example.com",
		AgentID:     "OxTest",
		AgentType:   "claude-code",
		CreatedAt:   time.Now(),
		Files: map[string]FileRef{
			"raw.jsonl":  {OID: "sha256:abc123", Size: int64(len(rawContent))},
			"summary.md": {OID: "sha256:def456", Size: int64(len(summaryContent))},
		},
	}

	err := WriteSessionMetaOnly(sessionDir, meta)
	require.NoError(t, err)

	// content files must remain intact — WriteSessionMetaOnly never writes pointer stubs
	assert.False(t, IsPointerFile(filepath.Join(sessionDir, "raw.jsonl")),
		"raw.jsonl should remain a content file after WriteSessionMetaOnly")
	assert.False(t, IsPointerFile(filepath.Join(sessionDir, "summary.md")),
		"summary.md should remain a content file after WriteSessionMetaOnly")

	got, err := os.ReadFile(filepath.Join(sessionDir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, rawContent, got, "raw.jsonl content must be unchanged")

	// meta.json should be written with correct OIDs
	readMeta, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, "sha256:abc123", readMeta.Files["raw.jsonl"].OID)
	assert.Equal(t, "sha256:def456", readMeta.Files["summary.md"].OID)
}

func TestWriteSessionMetaOnly_Nil(t *testing.T) {
	err := WriteSessionMetaOnly(t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil session meta")
}

// TestWriteSessionMeta_WritesPointers_WriteSessionMetaOnlyDoesNot is the key
// regression test for #291: WriteSessionMeta replaces content with pointer stubs
// while WriteSessionMetaOnly deliberately does not, keeping files intact for push.
func TestWriteSessionMeta_WritesPointers_WriteSessionMetaOnlyDoesNot(t *testing.T) {
	rawContent := []byte(`{"type":"header"}`)
	summaryContent := []byte("# Summary")

	// OIDs are the real content hashes — that is what the upload path stores
	// (meta.go builds each FileRef as "sha256:"+ComputeOID(content)), and
	// WritePointerFile now refuses to pointerize content an OID doesn't describe.
	files := map[string]FileRef{
		"raw.jsonl":  {OID: "sha256:" + ComputeOID(rawContent), Size: int64(len(rawContent))},
		"summary.md": {OID: "sha256:" + ComputeOID(summaryContent), Size: int64(len(summaryContent))},
	}
	meta := &SessionMeta{
		Version:   "1.0",
		AgentID:   "OxTest",
		AgentType: "claude-code",
		CreatedAt: time.Now(),
		Files:     files,
	}

	t.Run("WriteSessionMetaOnly preserves content files", func(t *testing.T) {
		sessionDir := filepath.Join(t.TempDir(), "session")
		require.NoError(t, os.MkdirAll(sessionDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644))

		require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

		assert.False(t, IsPointerFile(filepath.Join(sessionDir, "raw.jsonl")),
			"raw.jsonl must remain content after WriteSessionMetaOnly")
		assert.False(t, IsPointerFile(filepath.Join(sessionDir, "summary.md")),
			"summary.md must remain content after WriteSessionMetaOnly")
	})

	t.Run("WriteSessionMeta replaces content with pointers", func(t *testing.T) {
		sessionDir := filepath.Join(t.TempDir(), "session")
		require.NoError(t, os.MkdirAll(sessionDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644))

		require.NoError(t, WriteSessionMeta(sessionDir, meta))

		assert.True(t, IsPointerFile(filepath.Join(sessionDir, "raw.jsonl")),
			"raw.jsonl must become a pointer stub after WriteSessionMeta")
		assert.True(t, IsPointerFile(filepath.Join(sessionDir, "summary.md")),
			"summary.md must become a pointer stub after WriteSessionMeta")
	})
}

// TestUpdateMetaSummary_SetsTitleAndSummary locks in the ox-g5zw fix.
// Before this fix, the function only populated meta.Summary despite all
// callers passing an AI-generated title — which is why 91/155 sessions
// on the ox team ledger shipped with empty meta.Title. After the fix,
// both fields are set from the same source string so the bug can't recur.
func TestUpdateMetaSummary_SetsTitleAndSummary(t *testing.T) {
	dir := t.TempDir()

	// Write an initial meta with Title empty (simulating the state
	// immediately after processAgentSession before a summary exists).
	initial := &SessionMeta{
		Version:   "1.0",
		AgentID:   "Ox000",
		CreatedAt: timeFromString("2026-04-24T12:00:00Z"),
		Title:     "",
		Summary:   "",
	}
	if err := WriteSessionMeta(dir, initial); err != nil {
		t.Fatal(err)
	}

	// Simulate push-summary calling the function with an AI-generated title.
	const aiTitle = "Fix 19 golangci-lint issues across 10 files"
	if err := UpdateMetaSummary(dir, aiTitle); err != nil {
		t.Fatalf("UpdateMetaSummary: %v", err)
	}

	got, err := ReadSessionMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != aiTitle {
		t.Errorf("meta.Title: got %q, want %q (this is the ox-g5zw regression guard)", got.Title, aiTitle)
	}
	if got.Summary != aiTitle {
		t.Errorf("meta.Summary: got %q, want %q", got.Summary, aiTitle)
	}
}

func timeFromString(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
