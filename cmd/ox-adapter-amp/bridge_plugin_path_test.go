package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Amp loads system plugins from $XDG_CONFIG_HOME/amp/plugins/ when
// XDG_CONFIG_HOME is set, and from ~/.config/amp/plugins/ only otherwise
// ("System plugins", https://ampcode.com/docs/customize/plugins). A bridge
// plugin anywhere else is never loaded, so no Amp session is recorded.

// setHome points the home directory at dir on every platform: HOME on unix,
// USERPROFILE on Windows.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// unsetEnv removes key for the rest of the test; t.Setenv restores it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

func pluginUnder(configDir string) string {
	return filepath.Join(configDir, "amp", "plugins", "ox-bridge.ts")
}

// --- A. Where the plugin goes ---

// TestUserBridgePluginPath_FollowsAmpSystemPluginDir pins the path to Amp's
// own rule for every HOME / XDG_CONFIG_HOME combination.
// Failure prevented: install and diagnose agreeing on a directory Amp does not
// read, which looks healthy and records nothing.
func TestUserBridgePluginPath_FollowsAmpSystemPluginDir(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	homeConfig := filepath.Join(home, ".config")

	tests := []struct {
		name    string
		home    string
		xdgSet  bool
		xdg     string
		want    string
		wantErr bool
	}{
		{name: "XDG_CONFIG_HOME unset uses ~/.config", home: home, want: pluginUnder(homeConfig)},
		{name: "XDG_CONFIG_HOME empty uses ~/.config", home: home, xdgSet: true, xdg: "", want: pluginUnder(homeConfig)},
		{name: "XDG_CONFIG_HOME wins over HOME", home: home, xdgSet: true, xdg: xdg, want: pluginUnder(xdg)},
		{name: "XDG_CONFIG_HOME trailing separator is cleaned", home: home, xdgSet: true, xdg: xdg + string(filepath.Separator), want: pluginUnder(xdg)},
		{name: "XDG_CONFIG_HOME set to ~/.config", home: home, xdgSet: true, xdg: homeConfig, want: pluginUnder(homeConfig)},
		{name: "relative XDG_CONFIG_HOME is ignored", home: home, xdgSet: true, xdg: filepath.Join("relative", "config"), want: pluginUnder(homeConfig)},
		{name: "XDG_CONFIG_HOME needs no home directory", home: "", xdgSet: true, xdg: xdg, want: pluginUnder(xdg)},
		{name: "no home and no XDG_CONFIG_HOME is an error", home: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setHome(t, tt.home)
			if tt.xdgSet {
				t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			} else {
				unsetEnv(t, "XDG_CONFIG_HOME")
			}

			got, err := userBridgePluginPath()
			if tt.wantErr {
				require.Error(t, err, "an unresolvable path must not become a cwd-relative one")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestAmpInstall_WritesBridgeWhereAmpLoadsIt is the customer promise: with
// XDG_CONFIG_HOME set, `ox integrate install --amp` puts ox-bridge.ts in
// $XDG_CONFIG_HOME/amp/plugins/, the only system plugin dir Amp then reads.
// The expected path is spelled out rather than asked of userBridgePluginPath,
// which would agree with itself however wrong it was.
// Failure prevented: an install that reports success while Amp never loads the
// plugin, so no Amp session is captured (sageox/ox#1314, #1320).
func TestAmpInstall_WritesBridgeWhereAmpLoadsIt(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	setHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"})
	require.NoError(t, err)
	require.True(t, resp.Installed)

	want := pluginUnder(xdg)
	data, err := os.ReadFile(want)
	require.NoError(t, err, "the bridge plugin must be in $XDG_CONFIG_HOME/amp/plugins/, where Amp loads it")
	assert.Equal(t, oxBridgePluginSrc, data)
	assert.Contains(t, resp.FilesWritten, want)
	assert.NoFileExists(t, pluginUnder(filepath.Join(home, ".config")),
		"Amp does not read ~/.config/amp/plugins/ while XDG_CONFIG_HOME is set")
}

// --- B. What diagnose reports ---

// bridgeIssue returns the single amp:bridge-plugin-* issue, or nil.
func bridgeIssue(t *testing.T, issues []adapterprotocol.DiagnoseIssue) *adapterprotocol.DiagnoseIssue {
	t.Helper()
	var found *adapterprotocol.DiagnoseIssue
	for i := range issues {
		if strings.HasPrefix(issues[i].Slug, "amp:bridge-plugin-") {
			require.Nil(t, found, "diagnose reported more than one bridge plugin issue: %+v", issues)
			found = &issues[i]
		}
	}
	return found
}

func seedPlugin(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, oxBridgePluginSrc, 0o644))
}

// TestDiagnose_BridgePluginWhereAmpLoadsIt covers every place the plugin can
// be, against every XDG_CONFIG_HOME state.
// Failure prevented: ox doctor calling a capture-less install healthy, or
// telling a coworker the plugin is "missing" when it is sitting in a directory
// Amp stopped reading.
func TestDiagnose_BridgePluginWhereAmpLoadsIt(t *testing.T) {
	const (
		xdgUnset = iota
		xdgSeparate
		xdgIsHomeConfig
	)

	tests := []struct {
		name     string
		xdgMode  int
		seedHome bool
		seedXDG  bool
		wantSlug string // "" means no bridge issue
	}{
		{name: "no XDG, plugin in ~/.config", xdgMode: xdgUnset, seedHome: true},
		{name: "no XDG, no plugin", xdgMode: xdgUnset, wantSlug: "amp:bridge-plugin-missing"},
		{name: "XDG set, plugin in XDG dir", xdgMode: xdgSeparate, seedXDG: true},
		{name: "XDG set, plugin in both", xdgMode: xdgSeparate, seedHome: true, seedXDG: true},
		{name: "XDG set, plugin only in ~/.config", xdgMode: xdgSeparate, seedHome: true, wantSlug: "amp:bridge-plugin-misplaced"},
		{name: "XDG set, no plugin", xdgMode: xdgSeparate, wantSlug: "amp:bridge-plugin-missing"},
		{name: "XDG is ~/.config, plugin there", xdgMode: xdgIsHomeConfig, seedHome: true},
		{name: "XDG is ~/.config, no plugin is missing not misplaced", xdgMode: xdgIsHomeConfig, wantSlug: "amp:bridge-plugin-missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			setHome(t, home)
			homeConfig := filepath.Join(home, ".config")
			xdg := homeConfig
			switch tt.xdgMode {
			case xdgUnset:
				unsetEnv(t, "XDG_CONFIG_HOME")
			case xdgSeparate:
				xdg = t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", xdg)
			case xdgIsHomeConfig:
				t.Setenv("XDG_CONFIG_HOME", homeConfig)
			}
			if tt.seedHome {
				seedPlugin(t, pluginUnder(homeConfig))
			}
			if tt.seedXDG {
				seedPlugin(t, pluginUnder(xdg))
			}

			res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: t.TempDir(), Scope: "project"})
			require.NoError(t, err)
			issue := bridgeIssue(t, res.Issues)

			if tt.wantSlug == "" {
				assert.Nil(t, issue, "Amp can load the plugin; diagnose must not report it")
				return
			}
			require.NotNil(t, issue, "want %s, got %+v", tt.wantSlug, res.Issues)
			assert.Equal(t, tt.wantSlug, issue.Slug)
			// the detail must name the directory Amp actually reads, so the
			// coworker can check it themselves
			assert.Contains(t, issue.Detail, filepath.Dir(pluginUnder(xdg)))
			if tt.wantSlug == "amp:bridge-plugin-misplaced" {
				assert.Contains(t, issue.Detail, pluginUnder(homeConfig), "the detail must name the copy Amp ignores")
				assert.Contains(t, issue.Detail, "XDG_CONFIG_HOME")
			}
		})
	}
}

// TestDiagnose_MisplacedBridgeRepairedByInstall drives the repair the issue
// offers: install-hooks puts the plugin where Amp loads it, diagnose goes
// quiet, and the copy Amp ignores is left alone.
// Failure prevented: a fix that "succeeds" and leaves the issue in place, or a
// repair that deletes a file in the coworker's config directory.
func TestDiagnose_MisplacedBridgeRepairedByInstall(t *testing.T) {
	home, xdg, repo := t.TempDir(), t.TempDir(), t.TempDir()
	setHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	stale := pluginUnder(filepath.Join(home, ".config"))
	seedPlugin(t, stale)

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	issue := bridgeIssue(t, res.Issues)
	require.NotNil(t, issue)
	require.Equal(t, "amp:bridge-plugin-misplaced", issue.Slug)

	_, err = handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)

	res, err = handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	assert.Nil(t, bridgeIssue(t, res.Issues), "install-hooks must resolve the misplaced plugin")
	assert.FileExists(t, pluginUnder(xdg))
	assert.FileExists(t, stale, "the repair must not delete the copy Amp ignores")
}

// TestDiagnose_SafeFixesRunUnderDoctorFix checks every repair diagnose marks
// safe is one `ox doctor --fix` will actually execute. Doctor auto-runs only a
// FixArgv whose argv[0] is git or ox (adapterFixArgvAllowlist in
// cmd/ox/doctor_adapters.go); anything else is shown and never run.
// Failure prevented: FixSafe=true on a repair that silently never happens.
func TestDiagnose_SafeFixesRunUnderDoctorFix(t *testing.T) {
	home, xdg, repo := t.TempDir(), t.TempDir(), t.TempDir()
	setHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	seedPlugin(t, pluginUnder(filepath.Join(home, ".config")))
	// AGENTS.md without the marker raises amp:hooks-missing alongside the
	// misplaced plugin, so both repairs are checked
	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Agents\n"), 0o644))

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)

	checked := map[string]bool{}
	for _, issue := range res.Issues {
		if !issue.FixSafe {
			continue
		}
		checked[issue.Slug] = true
		assert.Equal(t, []string{"ox", "integrate", "install", "--amp"}, issue.FixArgv,
			"%s: a safe fix must be an argv ox doctor --fix will run", issue.Slug)
		assert.Equal(t, strings.Join(issue.FixArgv, " "), issue.Fix,
			"%s: the shown fix must be the command that runs", issue.Slug)
	}
	assert.True(t, checked["amp:hooks-missing"], "want amp:hooks-missing, got %+v", res.Issues)
	assert.True(t, checked["amp:bridge-plugin-misplaced"], "want amp:bridge-plugin-misplaced, got %+v", res.Issues)

	// outside a repository `ox integrate install --amp` cannot run, so the fix
	// is shown, not offered to the auto-fix path
	res, err = handleDiagnose(adapterprotocol.DiagnoseParams{Scope: "project"})
	require.NoError(t, err)
	issue := bridgeIssue(t, res.Issues)
	require.NotNil(t, issue)
	assert.Empty(t, issue.FixArgv)
	assert.Equal(t, "ox integrate install --amp", issue.Fix)
}

// TestDiagnose_UnreadableBridgePathIsReported covers a plugin path Amp cannot
// load from and install-hooks cannot write to. Diagnose must report it, never
// call it healthy, and must not offer install as a "safe" fix that would fail.
// Failure prevented: "I could not look" or "something else is there" reading as
// "all checks passed".
func TestDiagnose_UnreadableBridgePathIsReported(t *testing.T) {
	tests := []struct {
		name string
		// block puts something in the way and returns what the detail must say
		block func(t *testing.T, xdg string) string
	}{
		{
			name: "file where the plugins directory should be",
			block: func(t *testing.T, xdg string) string {
				blocker := filepath.Dir(pluginUnder(xdg))
				require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o755))
				require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))
				return blocker + " is a file"
			},
		},
		{
			name: "file where the amp directory should be",
			block: func(t *testing.T, xdg string) string {
				blocker := filepath.Join(xdg, "amp")
				require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))
				return blocker + " is a file"
			},
		},
		{
			name: "directory where the plugin file should be",
			block: func(t *testing.T, xdg string) string {
				require.NoError(t, os.MkdirAll(pluginUnder(xdg), 0o755))
				return "is a directory"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdg, repo := t.TempDir(), t.TempDir()
			setHome(t, t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", xdg)
			wantInDetail := tt.block(t, xdg)

			res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"})
			require.NoError(t, err)
			issue := bridgeIssue(t, res.Issues)
			require.NotNil(t, issue, "an unusable plugin path must not read as healthy, got %+v", res.Issues)
			assert.Equal(t, "amp:bridge-plugin-unreadable", issue.Slug)
			assert.Contains(t, issue.Detail, pluginUnder(xdg), "the detail must name the path")
			assert.Contains(t, issue.Detail, wantInDetail, "the detail must say what is in the way")

			// the premise for offering no fix: install fails on this path too
			_, installErr := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
			require.Error(t, installErr, "install-hooks repaired this path; diagnose could offer it as a fix")
			assert.False(t, issue.FixSafe, "install-hooks fails on this path, so it is not a safe fix")
			assert.Empty(t, issue.FixArgv)
		})
	}
}

// TestFileBlockingPluginDir pins the walk that finds a file standing where a
// directory of the plugin path must be. It is what keeps Windows honest: stat
// through a file component there says "not found", so without the walk a
// blocked path reads as missing and diagnose offers an install that fails.
// Failure prevented: a FixSafe repair offered for a path install cannot create.
func TestFileBlockingPluginDir(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) (path, want string)
	}{
		{
			name: "nothing exists below a directory",
			setup: func(t *testing.T, root string) (string, string) {
				return pluginUnder(root), ""
			},
		},
		{
			name: "plugins directory exists",
			setup: func(t *testing.T, root string) (string, string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(pluginUnder(root)), 0o755))
				return pluginUnder(root), ""
			},
		},
		{
			name: "file at the plugins directory",
			setup: func(t *testing.T, root string) (string, string) {
				blocker := filepath.Dir(pluginUnder(root))
				require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o755))
				require.NoError(t, os.WriteFile(blocker, nil, 0o644))
				return pluginUnder(root), blocker
			},
		},
		{
			name: "file two levels up",
			setup: func(t *testing.T, root string) (string, string) {
				blocker := filepath.Join(root, "amp")
				require.NoError(t, os.WriteFile(blocker, nil, 0o644))
				return pluginUnder(root), blocker
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, want := tt.setup(t, t.TempDir())
			assert.Equal(t, want, fileBlockingPluginDir(path))
		})
	}
}
