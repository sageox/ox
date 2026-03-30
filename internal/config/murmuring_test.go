package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidMurmuringMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode  string
		valid bool
	}{
		{MurmuringManual, true},
		{MurmuringAuto, true},
		{"", true},
		{"invalid", false},
		{"on", false},
		{"enabled", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, IsValidMurmuringMode(tt.mode))
		})
	}
}

func TestNormalizeMurmuring(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{MurmuringManual, MurmuringManual},
		{MurmuringAuto, MurmuringAuto},
		{"", MurmuringAuto},
		{"invalid", MurmuringAuto},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeMurmuring(tt.input))
		})
	}
}

func TestIsValidMurmurReceiveMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode  string
		valid bool
	}{
		{MurmurReceiveOff, true},
		{MurmurReceiveAuto, true},
		{"", true},
		{"invalid", false},
		{"on", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, IsValidMurmurReceiveMode(tt.mode))
		})
	}
}

func TestNormalizeMurmurReceive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{MurmurReceiveOff, MurmurReceiveOff},
		{MurmurReceiveAuto, MurmurReceiveAuto},
		{"", MurmurReceiveAuto},
		{"invalid", MurmurReceiveAuto},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeMurmurReceive(tt.input))
		})
	}
}

func TestResolveMurmuring_Default(t *testing.T) {
	// no user config, no project — should return auto
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	// invalidate cache by using a unique "project root"
	got := ResolveMurmuring("")
	assert.Equal(t, MurmuringAuto, got)
}

func TestResolveMurmuring_NoConfig(t *testing.T) {
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	dir := t.TempDir()
	got := ResolveMurmuring(dir)
	assert.Equal(t, MurmuringAuto, got)
}

func TestResolveMurmuring_UserOverride(t *testing.T) {
	// user=manual, repo=auto → manual
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("murmur_send: manual\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		Murmuring: "auto",
	})

	got := ResolveMurmuring(projectDir)
	assert.Equal(t, MurmuringManual, got)
}

func TestResolveMurmuring_RepoOnly(t *testing.T) {
	// user="", repo=manual → manual
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte(""), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		Murmuring: "manual",
	})

	got := ResolveMurmuring(projectDir)
	assert.Equal(t, MurmuringManual, got)
}

func TestResolveMurmurReceive_Default(t *testing.T) {
	// both empty → auto
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte(""), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	got := ResolveMurmurReceive("")
	assert.Equal(t, MurmurReceiveAuto, got)
}

func TestResolveMurmurReceive_UserOverride(t *testing.T) {
	// user=off, repo=auto → off
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("murmur_receive: \"off\"\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		MurmurReceive: "auto",
	})

	got := ResolveMurmurReceive(projectDir)
	assert.Equal(t, MurmurReceiveOff, got)
}

func TestResolveMurmurReceive_RepoOnly(t *testing.T) {
	// user="", repo=off → off
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte(""), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		MurmurReceive: "off",
	})

	got := ResolveMurmurReceive(projectDir)
	assert.Equal(t, MurmurReceiveOff, got)
}

func TestMurmuringEnabled_Default(t *testing.T) {
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	assert.True(t, MurmuringEnabled(""))
}

func TestMurmurReceiveEnabled(t *testing.T) {
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	// default: enabled
	assert.True(t, MurmurReceiveEnabled(""))

	// user sets off: disabled
	require.NoError(t, os.WriteFile(userCfgPath, []byte("murmur_receive: \"off\"\n"), 0644))
	assert.False(t, MurmurReceiveEnabled(""))
}

// TestResolveMurmuring_FullResolutionChain exercises User > Repo > Default
// with real config files at each level.
func TestResolveMurmuring_FullResolutionChain(t *testing.T) {
	tests := []struct {
		name     string
		userCfg  string // yaml content for user config ("" = empty file)
		repoCfg  string // murmuring value in project config ("" = no project config)
		want     string
		wantSrc  string // "user", "repo", or "default"
	}{
		{
			name:    "user=manual repo=auto → user wins",
			userCfg: "murmur_send: manual\n",
			repoCfg: "auto",
			want:    MurmuringManual,
			wantSrc: "user",
		},
		{
			name:    "user=auto repo=manual → user wins",
			userCfg: "murmur_send: auto\n",
			repoCfg: "manual",
			want:    MurmuringAuto,
			wantSrc: "user",
		},
		{
			name:    "user unset repo=manual → repo wins",
			userCfg: "",
			repoCfg: "manual",
			want:    MurmuringManual,
			wantSrc: "repo",
		},
		{
			name:    "both unset → default auto",
			userCfg: "",
			repoCfg: "",
			want:    MurmuringAuto,
			wantSrc: "default",
		},
		{
			name:    "user=manual repo unset → user wins",
			userCfg: "murmur_send: manual\n",
			repoCfg: "",
			want:    MurmuringManual,
			wantSrc: "user",
		},
		{
			name:    "user=invalid normalizes to auto, repo=manual → user auto wins",
			userCfg: "murmur_send: bogus\n",
			repoCfg: "manual",
			want:    MurmuringAuto, // "bogus" normalizes to auto
			wantSrc: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// isolate user config
			userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(userCfgPath, []byte(tt.userCfg), 0644))
			t.Setenv("OX_USER_CONFIG", userCfgPath)

			// set up project config
			var projectRoot string
			if tt.repoCfg != "" {
				projectRoot = CreateInitializedProjectWithConfig(t, &ProjectConfig{
					Murmuring: tt.repoCfg,
				})
			} else {
				projectRoot = t.TempDir() // no .sageox dir
			}

			got := ResolveMurmuring(projectRoot)
			assert.Equal(t, tt.want, got, "resolution mismatch (expected from %s)", tt.wantSrc)
		})
	}
}

// TestResolveMurmurReceive_FullResolutionChain exercises User > Repo > Default.
func TestResolveMurmurReceive_FullResolutionChain(t *testing.T) {
	tests := []struct {
		name    string
		userCfg string
		repoCfg string
		want    string
	}{
		{
			name:    "user=off repo=auto → user wins",
			userCfg: "murmur_receive: \"off\"\n",
			repoCfg: "auto",
			want:    MurmurReceiveOff,
		},
		{
			name:    "user=auto repo=off → user wins",
			userCfg: "murmur_receive: auto\n",
			repoCfg: "off",
			want:    MurmurReceiveAuto,
		},
		{
			name:    "user unset repo=off → repo wins",
			userCfg: "",
			repoCfg: "off",
			want:    MurmurReceiveOff,
		},
		{
			name:    "both unset → default auto",
			userCfg: "",
			repoCfg: "",
			want:    MurmurReceiveAuto,
		},
		{
			name:    "user=off repo unset → user wins",
			userCfg: "murmur_receive: \"off\"\n",
			repoCfg: "",
			want:    MurmurReceiveOff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(userCfgPath, []byte(tt.userCfg), 0644))
			t.Setenv("OX_USER_CONFIG", userCfgPath)

			var projectRoot string
			if tt.repoCfg != "" {
				projectRoot = CreateInitializedProjectWithConfig(t, &ProjectConfig{
					MurmurReceive: tt.repoCfg,
				})
			} else {
				projectRoot = t.TempDir()
			}

			got := ResolveMurmurReceive(projectRoot)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMurmuringEnabled_RespectsUserConfig verifies the convenience function
// returns false when user config says manual, even if repo says auto.
func TestMurmuringEnabled_RespectsUserConfig(t *testing.T) {
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("murmur_send: manual\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		Murmuring: "auto",
	})

	assert.False(t, MurmuringEnabled(projectDir), "user=manual should override repo=auto")
}

// TestMurmurReceiveEnabled_RespectsUserConfig verifies the convenience function
// returns false when user config says off, even if repo says auto.
func TestMurmurReceiveEnabled_RespectsUserConfig(t *testing.T) {
	userCfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("murmur_receive: \"off\"\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	projectDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		MurmurReceive: "auto",
	})

	assert.False(t, MurmurReceiveEnabled(projectDir), "user=off should override repo=auto")
}
