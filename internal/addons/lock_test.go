package addons

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadLock_MissingIsEmpty proves the contract's central "absent means
// nothing installed, not broken" claim: a Team Context that has never run
// `ox addons install` has no .sageox/add-ons.lock.json at all, and that must
// load as an empty lock, never an error.
func TestLoadLock_MissingIsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	assert.Equal(t, LockSchemaVersion, lock.SchemaVersion)
	assert.Empty(t, lock.Addons)
}

// TestLoadLock_MalformedErrors proves the other half of that same claim: a
// lock file that exists but cannot be parsed must be a loud error, never
// silently treated as "nothing installed" — that would make Install miss a
// real collision and Remove believe it owns paths it does not.
func TestLoadLock_MalformedErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRawLock(t, dir, "{ not valid json")

	_, err := LoadLock(dir)
	require.Error(t, err)
}

// TestLoadLock_FutureSchemaVersionErrors proves an ox binary older than the
// lock's schema refuses to guess at fields it does not understand, rather
// than silently dropping or misreading them.
func TestLoadLock_FutureSchemaVersionErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRawLock(t, dir, `{"schema_version": 999, "addons": []}`)

	_, err := LoadLock(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade ox")
}

// TestWriteLock_SortsAddonsAndFilesDeterministically proves two machines
// installing the same selection produce the same file: WriteLock sorts
// add-ons by name and each add-on's files by path regardless of the order
// they were appended in, and the on-disk file is human-reviewable (indented)
// and newline-terminated.
func TestWriteLock_SortsAddonsAndFilesDeterministically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { _ = root.Close() }()

	lock := Lock{
		SchemaVersion: LockSchemaVersion,
		Addons: []LockedAddon{
			{
				Addon: "zebra", Source: SourceBuiltin, Version: "1.0.0",
				Files: []LockedFile{
					{Path: "agents/skills/zebra/z.md"},
					{Path: "agents/skills/zebra/a.md"},
				},
			},
			{
				Addon: "apple", Source: SourceBuiltin, Version: "1.0.0",
				Files: []LockedFile{{Path: "agents/skills/apple/SKILL.md"}},
			},
		},
	}
	require.NoError(t, WriteLock(root, lock))

	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(LockRelativePath)))
	require.NoError(t, err)
	content := string(raw)

	assert.True(t, strings.HasSuffix(content, "\n"), "lock file must end with a newline")
	assert.Contains(t, content, "\n  ", "lock file must be indented for human review")

	// "apple" must appear before "zebra", and within zebra, a.md before z.md.
	appleIdx := strings.Index(content, `"apple"`)
	zebraIdx := strings.Index(content, `"zebra"`)
	require.Greater(t, appleIdx, -1)
	require.Greater(t, zebraIdx, -1)
	assert.Less(t, appleIdx, zebraIdx, "add-ons must sort by name")

	aIdx := strings.Index(content, `"agents/skills/zebra/a.md"`)
	zIdx := strings.Index(content, `"agents/skills/zebra/z.md"`)
	require.Greater(t, aIdx, -1)
	require.Greater(t, zIdx, -1)
	assert.Less(t, aIdx, zIdx, "files within an add-on must sort by path")

	var decoded Lock
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Addons, 2)
	assert.Equal(t, "apple", decoded.Addons[0].Addon)
	assert.Equal(t, "zebra", decoded.Addons[1].Addon)
}

// TestLoadLock_RoundTrip proves WriteLock's output is exactly what LoadLock
// reads back — no field lost, no value transformed.
func TestLoadLock_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { _ = root.Close() }()

	want := Lock{
		SchemaVersion: LockSchemaVersion,
		Addons: []LockedAddon{{
			Addon: "hello", Source: SourceBuiltin, Version: "1.2.3", Digest: "sha256:abc",
			Files: []LockedFile{{Path: "agents/skills/hello/SKILL.md", Digest: "sha256:def", Mode: 0o644}},
		}},
	}
	require.NoError(t, WriteLock(root, want))

	got, err := LoadLock(dir)
	require.NoError(t, err)
	require.Len(t, got.Addons, 1)
	assert.Equal(t, want.Addons[0].Addon, got.Addons[0].Addon)
	assert.Equal(t, want.Addons[0].Version, got.Addons[0].Version)
	assert.Equal(t, want.Addons[0].Digest, got.Addons[0].Digest)
	require.Len(t, got.Addons[0].Files, 1)
	assert.Equal(t, want.Addons[0].Files[0], got.Addons[0].Files[0])
}

func writeRawLock(t *testing.T, teamPath, content string) {
	t.Helper()
	dir := filepath.Join(teamPath, ".sageox")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "add-ons.lock.json"), []byte(content), 0o644))
}
