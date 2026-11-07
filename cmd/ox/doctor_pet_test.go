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

			// no Codex home: this table is the Claude Code half
			res := checkPetOxy(pet.Installer{Dir: dir, Frames: pettest.Frames}, pet.CodexInstaller{}, "2.0.0", tt.fix)

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

// codexOtherPet is a config.toml that selects a pet other than Oxy.
const codexOtherPet = "[tui]\npet = \"custom:other\"\n"

func writeCodexPet(version string) func(t *testing.T, home string) {
	return func(t *testing.T, home string) {
		t.Helper()
		require.NoError(t, pet.CodexInstaller{Home: home, Version: version, Sheet: pettest.CodexSheet}.WriteFiles())
	}
}

// TestCheckPetOxy_Codex verifies `ox doctor` refreshes Oxy's Codex files
// after an upgrade, alongside the Claude Code mod, without touching
// config.toml, and skips a Codex home where he was never enabled.
// Failure prevented: Codex keeps drawing an old ox's sheet until the coworker
// reruns enable, or doctor quietly reselects a pet they switched away from.
func TestCheckPetOxy_Codex(t *testing.T) {
	tests := []struct {
		name        string
		claude      string // version of the Claude Code mod; "" = never enabled
		codex       string // version of the Codex pet; "" = never enabled
		fix         bool
		wantSkipped bool
		wantWarning bool
		wantMessage []string
		wantCodex   string // version on the Codex pet after the check
	}{
		{name: "neither enabled", fix: true, wantSkipped: true},
		{name: "codex absent is skipped", claude: "2.0.0", fix: true, wantMessage: []string{"up to date"}},
		{name: "codex current", codex: "2.0.0", fix: true, wantMessage: []string{"up to date"}, wantCodex: "2.0.0"},
		{name: "codex stale without fix warns", codex: "1.0.0", wantWarning: true, wantMessage: []string{"Codex"}, wantCodex: "1.0.0"},
		{name: "codex stale is refreshed", codex: "1.0.0", fix: true, wantMessage: []string{"refreshed Codex to 2.0.0", "restart Codex"}, wantCodex: "2.0.0"},
		{
			name: "both stale are refreshed", claude: "1.0.0", codex: "1.0.0", fix: true,
			wantMessage: []string{"refreshed Claude Code and Codex to 2.0.0", "/reload-plugins", "restart Codex"},
			wantCodex:   "2.0.0",
		},
		{name: "both stale without fix names both", claude: "1.0.0", codex: "1.0.0", wantWarning: true, wantMessage: []string{"Claude Code, Codex"}, wantCodex: "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), pet.PluginName)
			home := t.TempDir()
			if tt.claude != "" {
				writePetMod(tt.claude)(t, dir)
			}
			if tt.codex != "" {
				writeCodexPet(tt.codex)(t, home)
			}
			cfg := filepath.Join(home, "config.toml")
			require.NoError(t, os.WriteFile(cfg, []byte(codexOtherPet), 0o600))

			res := checkPetOxy(
				pet.Installer{Dir: dir, Frames: pettest.Frames},
				pet.CodexInstaller{Home: home, Sheet: pettest.CodexSheet},
				"2.0.0", tt.fix)

			assert.Equal(t, tt.wantSkipped, res.skipped, res.message)
			assert.Equal(t, tt.wantWarning, res.warning, res.message)
			if !tt.wantSkipped && !tt.wantWarning {
				assert.True(t, res.passed, res.message)
			}
			for _, m := range tt.wantMessage {
				assert.Contains(t, res.message, m)
			}
			if tt.claude != "" && tt.fix {
				got, err := pet.WrittenVersion(dir)
				require.NoError(t, err)
				assert.Equal(t, "2.0.0", got)
			}
			if tt.wantCodex == "" {
				assert.NoDirExists(t, pet.CodexDir(home))
			} else {
				got, err := pet.CodexWrittenVersion(pet.CodexDir(home))
				require.NoError(t, err)
				assert.Equal(t, tt.wantCodex, got)
			}
			data, err := os.ReadFile(cfg)
			require.NoError(t, err)
			assert.Equal(t, codexOtherPet, string(data), "doctor must never edit config.toml")
		})
	}
}

// TestPetOxyCheck_IsRegistered verifies the doctor registry runs the Oxy
// check against the real data path, skipping a machine that never enabled it.
func TestPetOxyCheck_IsRegistered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	check := GetDoctorCheck(CheckSlugPetOxy)
	require.NotNil(t, check)
	res := check.Run(false)
	assert.True(t, res.skipped, res.message)
}
