package teamconverge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/addons"
)

// --- OriginResolver.Resolve: pure function over an already-loaded Lock ---
// No disk I/O, no addons.LoadLock dependency — proves the ownership lookup
// itself is correct in isolation from how the lock got loaded.

func TestOriginResolver_Resolve(t *testing.T) {
	lock := addons.Lock{
		SchemaVersion: addons.LockSchemaVersion,
		Addons: []addons.LockedAddon{
			{
				Addon:       "post-cutoff",
				Source:      addons.SourceBuiltin,
				Version:     "1.2.0",
				Digest:      "sha256:addon-digest",
				InstalledAt: time.Now(),
				Files: []addons.LockedFile{
					{Path: "agents/skills/post-cutoff/SKILL.md", Digest: "sha256:file-digest", Mode: 0o644},
					{Path: "agents/rules/post-cutoff.md", Digest: "sha256:rule-digest", Mode: 0o644},
				},
			},
		},
	}

	tests := []struct {
		name       string
		sourcePath string
		want       Origin
	}{
		{
			name:       "path the lock owns resolves to the addon with version and digest",
			sourcePath: "agents/skills/post-cutoff/SKILL.md",
			want: Origin{
				Kind:         OriginAddon,
				Addon:        "post-cutoff",
				AddonVersion: "1.2.0",
				Digest:       "sha256:file-digest",
			},
		},
		{
			name:       "a second owned path in the same addon resolves independently",
			sourcePath: "agents/rules/post-cutoff.md",
			want: Origin{
				Kind:         OriginAddon,
				Addon:        "post-cutoff",
				AddonVersion: "1.2.0",
				Digest:       "sha256:rule-digest",
			},
		},
		{
			name:       "a hand-authored path the lock does not own resolves loose",
			sourcePath: "agents/rules/security.md",
			want:       Origin{Kind: OriginLoose},
		},
		{
			name:       "empty source path resolves loose rather than panicking",
			sourcePath: "",
			want:       Origin{Kind: OriginLoose},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewOriginResolver(lock).Resolve(tt.sourcePath)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestOriginResolver_Resolve_EmptyLockIsAllLoose covers the zero-value Lock a
// team that has never touched the Add-on Catalog produces: every artifact
// must resolve loose, not error, not panic.
func TestOriginResolver_Resolve_EmptyLockIsAllLoose(t *testing.T) {
	resolver := NewOriginResolver(addons.Lock{})
	got := resolver.Resolve("agents/skills/deploy/SKILL.md")
	assert.Equal(t, Origin{Kind: OriginLoose}, got)
}

// --- ResolveOrigin: end-to-end over a real Team Context path on disk ---
// Exercises addons.LoadLock for real, so these prove the "no lock" vs
// "malformed lock" distinction actually holds through the whole seam, not
// just in a mock.

// TestResolveOrigin_NoLockFileIsLooseNoError is the "there is no lock" half
// of the absent-match trap: a Team Context that has never run `ox addons`
// must resolve everything loose, with NO error — a team not using the
// catalog must never see add-on plumbing fail.
func TestResolveOrigin_NoLockFileIsLooseNoError(t *testing.T) {
	team := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(team, ".sageox"), 0o755))
	// deliberately no add-ons.lock.json written

	origin, err := ResolveOrigin(team, "agents/skills/deploy/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, Origin{Kind: OriginLoose}, origin)
}

// TestResolveOrigin_MalformedLockIsError is the "I could not read the lock"
// half: a lock file that exists but fails to parse must surface an error,
// NOT silently resolve everything loose. Silently going loose here would
// make every add-on-installed artifact look hand-authored — the same
// failure-renders-as-success class the contract calls the absent-match trap.
func TestResolveOrigin_MalformedLockIsError(t *testing.T) {
	team := t.TempDir()
	lockPath := filepath.Join(team, filepath.FromSlash(addons.LockRelativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	require.NoError(t, os.WriteFile(lockPath, []byte("{not valid json"), 0o644))

	origin, err := ResolveOrigin(team, "agents/skills/deploy/SKILL.md")
	require.Error(t, err, "a malformed lock must surface an error, not a silent loose resolution")
	assert.Equal(t, Origin{}, origin)
}

// TestResolveOrigin_OwnedPathEndToEnd proves the whole seam — a real lock
// file on disk, loaded for real, resolves an owned path to OriginAddon.
func TestResolveOrigin_OwnedPathEndToEnd(t *testing.T) {
	team := t.TempDir()
	lockPath := filepath.Join(team, filepath.FromSlash(addons.LockRelativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))

	lock := addons.Lock{
		SchemaVersion: addons.LockSchemaVersion,
		Addons: []addons.LockedAddon{
			{
				Addon:   "post-cutoff",
				Source:  addons.SourceBuiltin,
				Version: "1.0.0",
				Digest:  "sha256:addon",
				Files: []addons.LockedFile{
					{Path: "agents/skills/post-cutoff/SKILL.md", Digest: "sha256:file", Mode: 0o644},
				},
			},
		},
	}
	data, err := json.Marshal(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lockPath, data, 0o644))

	origin, err := ResolveOrigin(team, "agents/skills/post-cutoff/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, OriginAddon, origin.Kind)
	assert.Equal(t, "post-cutoff", origin.Addon)
	assert.Equal(t, "1.0.0", origin.AddonVersion)
	assert.Equal(t, "sha256:file", origin.Digest)
}
