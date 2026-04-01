package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// GetLegacyPaths Tests
// -----------------------------------------------------------------------------

func TestGetLegacyPaths(t *testing.T) {
	legacyPaths := GetLegacyPaths()

	// verify all paths are absolute
	assert.True(t, filepath.IsAbs(legacyPaths.ConfigDir), "ConfigDir should be absolute")
	assert.True(t, filepath.IsAbs(legacyPaths.GuidanceCache), "GuidanceCache should be absolute")
	assert.True(t, filepath.IsAbs(legacyPaths.SessionCache), "SessionCache should be absolute")

	// verify expected path components
	assert.Contains(t, legacyPaths.ConfigDir, ".config")
	assert.Contains(t, legacyPaths.ConfigDir, "sageox")

	assert.Contains(t, legacyPaths.GuidanceCache, ".sageox")
	assert.Contains(t, legacyPaths.GuidanceCache, "guidance")

	assert.Contains(t, legacyPaths.SessionCache, ".cache")
	assert.Contains(t, legacyPaths.SessionCache, "sageox")
}

// -----------------------------------------------------------------------------
// CheckMigrationStatus Tests
// -----------------------------------------------------------------------------

func TestCheckMigrationStatus_XDGModeDisablesMigration(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	status := CheckMigrationStatus()

	assert.False(t, status.Needed, "migration should not be needed in XDG mode")
	assert.False(t, status.LegacyConfigExists)
	assert.False(t, status.LegacyGuidanceCacheExists)
	assert.False(t, status.LegacySessionCacheExists)
	assert.False(t, status.NewStructureExists)
}

func TestCheckMigrationStatus_ReturnsValidStruct(t *testing.T) {
	// ensure XDG mode is disabled
	t.Setenv("OX_XDG_ENABLE", "")

	status := CheckMigrationStatus()

	// verify the function returns without panic and produces a valid struct
	// the actual values depend on system state, but structure should be valid
	assert.IsType(t, MigrationStatus{}, status)
}

// -----------------------------------------------------------------------------
// MigrationStatus Struct Tests
// -----------------------------------------------------------------------------

func TestMigrationStatus_Struct(t *testing.T) {
	status := MigrationStatus{
		Needed:                    true,
		LegacyConfigExists:        true,
		LegacyGuidanceCacheExists: false,
		LegacySessionCacheExists:  true,
		NewStructureExists:        false,
	}

	assert.True(t, status.Needed)
	assert.True(t, status.LegacyConfigExists)
	assert.False(t, status.LegacyGuidanceCacheExists)
	assert.True(t, status.LegacySessionCacheExists)
	assert.False(t, status.NewStructureExists)
}

// -----------------------------------------------------------------------------
// LegacyPaths Struct Tests
// -----------------------------------------------------------------------------

func TestLegacyPaths_Struct(t *testing.T) {
	paths := LegacyPaths{
		ConfigDir:     "/home/user/.config/sageox",
		GuidanceCache: "/home/user/.sageox/guidance/cache",
		SessionCache:  "/home/user/.cache/sageox",
	}

	assert.Equal(t, "/home/user/.config/sageox", paths.ConfigDir)
	assert.Equal(t, "/home/user/.sageox/guidance/cache", paths.GuidanceCache)
	assert.Equal(t, "/home/user/.cache/sageox", paths.SessionCache)
}

// -----------------------------------------------------------------------------
// DetectLegacyLedgerPath Tests
// -----------------------------------------------------------------------------

func TestDetectLegacyLedgerPath(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T) string // returns projectRoot
		wantFound bool
	}{
		{
			name: "empty project root returns empty",
			setup: func(t *testing.T) string {
				return ""
			},
			wantFound: false,
		},
		{
			name: "no legacy dir returns empty",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				return projectRoot
			},
			wantFound: false,
		},
		{
			name: "legacy dir exists returns path",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
				require.NoError(t, os.MkdirAll(legacyDir, 0755))
				return projectRoot
			},
			wantFound: true,
		},
		{
			name: "legacy file (not dir) returns empty",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				// create a file, not a directory
				legacyFile := filepath.Join(tmp, "myrepo_sageox_ledger")
				require.NoError(t, os.WriteFile(legacyFile, []byte("not a dir"), 0644))
				return projectRoot
			},
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot := tt.setup(t)
			result := DetectLegacyLedgerPath(projectRoot)
			if tt.wantFound {
				assert.NotEmpty(t, result)
				assert.Contains(t, result, "_sageox_ledger")
			} else {
				assert.Empty(t, result)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// NewLedgerPath Tests
// -----------------------------------------------------------------------------

func TestNewLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		projectRoot string
		endpoint    string
		wantSuffix  string
		wantPanic   bool
	}{
		{
			name:        "empty project root returns empty",
			projectRoot: "",
			endpoint:    "https://sageox.ai",
			wantSuffix:  "",
		},
		{
			name:        "production endpoint skips endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "ledger"),
		},
		{
			name:        "production api prefix also skips endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://api.sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "ledger"),
		},
		{
			name:        "staging endpoint includes endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://staging.sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "staging.sageox.ai", "ledger"),
		},
		{
			name:        "localhost endpoint includes endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "http://localhost:8080",
			wantSuffix:  "ledger",
		},
		{
			name:        "empty endpoint panics",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "",
			wantPanic:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPanic {
				assert.Panics(t, func() {
					NewLedgerPath(tt.projectRoot, tt.endpoint)
				})
				return
			}
			result := NewLedgerPath(tt.projectRoot, tt.endpoint)
			if tt.projectRoot == "" {
				assert.Empty(t, result)
				return
			}
			assert.True(t, strings.HasSuffix(result, tt.wantSuffix),
				"NewLedgerPath(%q, %q) = %q, want suffix %q", tt.projectRoot, tt.endpoint, result, tt.wantSuffix)
		})
	}
}

// -----------------------------------------------------------------------------
// LedgerNeedsMigration Tests
// -----------------------------------------------------------------------------

func TestLedgerNeedsMigration(t *testing.T) {
	t.Run("empty project root returns false", func(t *testing.T) {
		assert.False(t, LedgerNeedsMigration("", "https://sageox.ai"))
	})

	t.Run("no legacy dir returns false", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		assert.False(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})

	t.Run("legacy exists and new does not returns true", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))

		assert.True(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})

	t.Run("both exist returns false", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		assert.False(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})
}

// -----------------------------------------------------------------------------
// CheckLedgerMigrationStatus Tests
// -----------------------------------------------------------------------------

func TestCheckLedgerMigrationStatus(t *testing.T) {
	t.Run("empty project root returns zero struct", func(t *testing.T) {
		status := CheckLedgerMigrationStatus("", "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.False(t, status.LegacyExists)
		assert.False(t, status.NewExists)
	})

	t.Run("no legacy dir", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.False(t, status.LegacyExists)
		// LegacyPath should still be populated (for reporting)
		assert.NotEmpty(t, status.LegacyPath)
		assert.Contains(t, status.LegacyPath, "_sageox_ledger")
	})

	t.Run("legacy exists needs migration", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.True(t, status.NeedsMigration)
		assert.True(t, status.LegacyExists)
		assert.False(t, status.NewExists)
		assert.Equal(t, legacyDir, status.LegacyPath)
	})

	t.Run("new already exists no migration needed", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.True(t, status.LegacyExists)
		assert.True(t, status.NewExists)
	})
}

// -----------------------------------------------------------------------------
// CheckMigrationStatus with Legacy Mode Tests
// -----------------------------------------------------------------------------

func TestCheckMigrationStatus_LegacyMode(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	status := CheckMigrationStatus()
	// in legacy mode with OX_XDG_DISABLE, migration is possible
	// the result depends on filesystem state, but the function should not panic
	assert.IsType(t, MigrationStatus{}, status)
}
