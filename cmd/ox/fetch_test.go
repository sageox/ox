package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validFetchOID is a well-formed 64-char SHA256 hex string used across tests.
const validFetchOID = "a3f8c2d1deadbeef0011223344556677deadbeefa3f8c2d1deadbeef00112233"

// writeTestPointerFile writes an LFS pointer file and returns the path.
func writeTestPointerFile(t *testing.T, dir, name, oid string, size int64) string {
	t.Helper()
	content := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:" + oid + "\n" +
		"size " + formatInt64(size) + "\n"
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// --- detectRepo ---

func TestDetectRepo(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string // returns pointer path
		wantErr string
	}{
		{
			name: "finds git root from nested subdir",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
				subDir := filepath.Join(dir, "discussions", "2026-03-31", "keyframes")
				require.NoError(t, os.MkdirAll(subDir, 0o755))
				return writeTestPointerFile(t, subDir, "frame-003.jpg", validFetchOID, 204800)
			},
		},
		{
			name: "error when no .git directory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				return writeTestPointerFile(t, dir, "frame.jpg", validFetchOID, 1024)
			},
			wantErr: "not inside a git repository",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pointerPath := tt.setup(t)
			root, err := detectRepo(pointerPath)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.DirExists(t, filepath.Join(root, ".git"))
			}
		})
	}
}

// --- isCacheHit ---

func TestCacheHit(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string // returns path
		size     int64
		expected bool
	}{
		{
			name: "matching size returns true",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "frame.jpg")
				content := []byte("fake image content 12345")
				require.NoError(t, os.WriteFile(path, content, 0o644))
				return path
			},
			size:     int64(len("fake image content 12345")),
			expected: true,
		},
		{
			name: "wrong size returns false",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "frame.jpg")
				require.NoError(t, os.WriteFile(path, []byte("short"), 0o644))
				return path
			},
			size:     99999,
			expected: false,
		},
		{
			name: "missing file returns false",
			setup: func(t *testing.T) string {
				return "/no/such/file.jpg"
			},
			size:     1024,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			assert.Equal(t, tt.expected, isCacheHit(path, tt.size))
		})
	}
}

// --- isHex ---

func TestIsHex(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{validFetchOID, true},
		{"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ", false},
		{"not-hex-at-all!@#$%^&*()_+=-[]{}|;':\",./<>?", false},
		{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", true},
	}

	for _, tt := range tests {
		t.Run(tt.input[:16]+"...", func(t *testing.T) {
			assert.Equal(t, tt.expected, isHex(tt.input))
		})
	}
}

// --- cache path structure ---

func TestCachePath_PreservesOriginalFilename(t *testing.T) {
	repoRoot := "/fake/teams/team-id"
	pointerPath := filepath.Join(repoRoot, "discussions", "2026-03-31", "keyframes", "frame-003.jpg")

	relPath, err := filepath.Rel(repoRoot, pointerPath)
	require.NoError(t, err)

	cachePath := filepath.Join(repoRoot, ".sageox", "cache", relPath)

	assert.Equal(t,
		"/fake/teams/team-id/.sageox/cache/discussions/2026-03-31/keyframes/frame-003.jpg",
		cachePath,
	)
	assert.Equal(t, "frame-003.jpg", filepath.Base(cachePath),
		"cache path must preserve original filename")
}

func TestCachePath_PreservesDirectoryStructure(t *testing.T) {
	repoRoot := "/fake/ledgers/repo-id"
	pointerPath := filepath.Join(repoRoot, "sessions", "2026-03-31T14-32-ryan", "raw.jsonl")

	relPath, err := filepath.Rel(repoRoot, pointerPath)
	require.NoError(t, err)

	cachePath := filepath.Join(repoRoot, ".sageox", "cache", relPath)

	assert.Equal(t,
		"/fake/ledgers/repo-id/.sageox/cache/sessions/2026-03-31T14-32-ryan/raw.jsonl",
		cachePath,
	)
}

func TestCachePath_LivesInSourceRepoCache(t *testing.T) {
	repoRoot := "/data/teams/abc123"
	relPath := "discussions/meeting/frame.jpg"

	cachePath := filepath.Join(repoRoot, ".sageox", "cache", relPath)

	cacheDir := filepath.Join(repoRoot, ".sageox", "cache")
	assert.True(t,
		len(cachePath) > len(cacheDir) && cachePath[:len(cacheDir)] == cacheDir,
		"cache must be inside the source repo's .sageox/cache/",
	)
}
