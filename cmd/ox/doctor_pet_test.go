package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/pet/pettest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckPetOxy verifies `ox doctor` brings an enabled Oxy mod up to this
// ox's version and leaves a machine that never enabled it alone.
// Failure prevented: after an ox upgrade, Claude Code keeps running the old
// mod (old frames, old fixes) until the coworker happens to rerun enable.
func TestCheckPetOxy(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string)
		fix         bool
		wantSkipped bool
		wantWarning bool
		wantVersion string // stamped on the mod after the check; "" = no mod
	}{
		{name: "never enabled", setup: func(*testing.T, string) {}, fix: true, wantSkipped: true},
		{name: "current", setup: writePetMod("2.0.0"), fix: true, wantVersion: "2.0.0"},
		{name: "stale without fix warns", setup: writePetMod("1.0.0"), wantWarning: true, wantVersion: "1.0.0"},
		{name: "stale is refreshed", setup: writePetMod("1.0.0"), fix: true, wantVersion: "2.0.0"},
		{name: "interrupted refresh is restored, not read as never enabled", setup: func(t *testing.T, dir string) {
			writePetMod("2.0.0")(t, dir)
			require.NoError(t, os.Rename(dir, dir+".old"))
		}, fix: true, wantVersion: "2.0.0"},
		{name: "unreadable manifest is refreshed", setup: func(t *testing.T, dir string) {
			writePetMod("1.0.0")(t, dir)
			// a directory where the manifest should be: unreadable on every platform
			manifest := filepath.Join(dir, ".claude-plugin", "plugin.json")
			require.NoError(t, os.Remove(manifest))
			require.NoError(t, os.Mkdir(manifest, 0o755))
		}, fix: true, wantVersion: "2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), pet.PluginName)
			tt.setup(t, dir)

			res := checkPetOxy(pet.Installer{Dir: dir, Frames: pettest.Frames}, "2.0.0", tt.fix)

			assert.Equal(t, tt.wantSkipped, res.skipped, res.message)
			assert.Equal(t, tt.wantWarning, res.warning, res.message)
			if tt.wantVersion == "" {
				assert.NoDirExists(t, dir)
				return
			}
			assert.True(t, res.passed, res.message)
			got, err := pet.WrittenVersion(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.wantVersion, got)
		})
	}
}

func writePetMod(version string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		require.NoError(t, pet.Installer{Dir: dir, Version: version, Frames: pettest.Frames}.WriteMod())
	}
}

// TestPetOxyCheck_IsRegistered verifies the doctor registry runs the Oxy
// check against the real data path, skipping a machine that never enabled it.
func TestPetOxyCheck_IsRegistered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	check := GetDoctorCheck(CheckSlugPetOxy)
	require.NotNil(t, check)
	res := check.Run(false)
	assert.True(t, res.skipped, res.message)
}
