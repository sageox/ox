package pet

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaude answers the claude CLI from memory and records each call.
type fakeClaude struct {
	calls          []string
	hasMarketplace bool
	installed      bool
	failValidate   bool
	failOn         string // a call prefix that fails
	notice         string // printed before JSON, as the real CLI sometimes does
}

func (f *fakeClaude) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return "boom", errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(call, "plugin validate"):
		if f.failValidate {
			return "✘ Validation failed", errors.New("exit status 1")
		}
	case call == "plugin marketplace list --json":
		if f.hasMarketplace {
			return f.notice + `[{"name":"ox-cli-pets"}]`, nil
		}
		return f.notice + `[]`, nil
	case strings.HasPrefix(call, "plugin marketplace add"):
		f.hasMarketplace = true
	case strings.HasPrefix(call, "plugin marketplace remove"):
		f.hasMarketplace = false
	case call == "plugin list --json":
		if f.installed {
			return f.notice + `[{"id":"ox-cli-oxy@ox-cli-pets","version":"1.0.0"}]`, nil
		}
		return f.notice + `[]`, nil
	case strings.HasPrefix(call, "plugin install"):
		f.installed = true
	case strings.HasPrefix(call, "plugin uninstall"):
		f.installed = false
	}
	return "", nil
}

func (f *fakeClaude) mutations() []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasSuffix(c, "--json") {
			out = append(out, c)
		}
	}
	return out
}

func newTestInstaller(t *testing.T, c *fakeClaude) Installer {
	t.Helper()
	return Installer{Dir: filepath.Join(t.TempDir(), "oxy"), Version: "1.0.0", Claude: c, Frames: tinyFrames}
}

// tinyFrames is a one-frame-per-animation sheet, so writing the mod in a
// test takes milliseconds. (pettest has the same for other packages; it
// imports pet, so pet's own tests can't use it.)
var tinyFrames = sync.OnceValues(func() (*Frames, error) {
	sheet := image.NewNRGBA(image.Rect(0, 0, 4, 8))
	for y := 2; y < 8; y++ {
		for x := 1; x < 3; x++ {
			sheet.SetNRGBA(x, y, color.NRGBA{R: 0xe0, G: 0x70, B: 0x20, A: 0xff})
		}
	}
	m := Manifest{Cell: 4, CellHeight: 8, Columns: 1}
	for _, name := range Animations {
		m.Animations = append(m.Animations, Animation{Name: name, Row: 0, Frames: 1, MS: []int{100}})
	}
	return BuildFrames(sheet, m)
})

// TestEnable_IsIdempotent verifies a second enable (the refresh after an ox
// upgrade) rewrites the files without adding the marketplace or installing twice.
// Failure prevented: a re-run errors on "already added" and blocks refreshes.
func TestEnable_IsIdempotent(t *testing.T) {
	c := &fakeClaude{notice: "Checking for updates…\n"}
	in := newTestInstaller(t, c)

	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.True(t, res.Installed)
	assert.Equal(t, []string{
		"plugin validate " + in.Dir,
		"plugin marketplace add " + in.Dir,
		"plugin install ox-cli-oxy@ox-cli-pets --scope user",
	}, c.mutations())

	c.calls = nil
	_, err = in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"plugin validate " + in.Dir}, c.mutations())
}

// TestEnable_InvalidModInstallsNothing verifies a mod Claude Code refuses is
// never registered. Failure prevented: a broken mod is installed and every
// session logs a load error.
func TestEnable_InvalidModInstallsNothing(t *testing.T) {
	c := &fakeClaude{failValidate: true}
	_, err := newTestInstaller(t, c).Enable(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not validate")
	for _, call := range c.calls {
		assert.NotContains(t, call, "install")
		assert.NotContains(t, call, "marketplace add")
	}
}

// TestEnable_WithoutClaudeWritesModAndExplains verifies a machine without the
// claude CLI still gets the mod plus the exact steps to finish in Claude Code.
func TestEnable_WithoutClaudeWritesModAndExplains(t *testing.T) {
	in := Installer{Dir: filepath.Join(t.TempDir(), "oxy"), Version: "1.0.0", Frames: tinyFrames}
	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.False(t, res.Installed)
	assert.True(t, res.FilesPresent)
	assert.Equal(t, []string{"/plugin marketplace add " + in.Dir, "/plugin install ox-cli-oxy@ox-cli-pets"}, res.ManualSteps)
	assert.FileExists(t, filepath.Join(in.Dir, ".claude-plugin", "plugin.json"))
}

// TestDisable_UninstallsAndRemovesFolder verifies disable undoes all of enable.
func TestDisable_UninstallsAndRemovesFolder(t *testing.T) {
	c := &fakeClaude{}
	in := newTestInstaller(t, c)
	_, err := in.Enable(context.Background())
	require.NoError(t, err)

	c.calls = nil
	_, err = in.Disable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"plugin uninstall ox-cli-oxy@ox-cli-pets --scope user",
		"plugin marketplace remove ox-cli-pets",
	}, c.mutations())
	assert.NoDirExists(t, in.Dir)
	assert.False(t, c.installed)
	assert.False(t, c.hasMarketplace)
}

// TestDisable_WhenNeverEnabledIsANoOp verifies disable on a clean machine
// succeeds without asking Claude Code to remove anything.
func TestDisable_WhenNeverEnabledIsANoOp(t *testing.T) {
	c := &fakeClaude{}
	_, err := newTestInstaller(t, c).Disable(context.Background())
	require.NoError(t, err)
	assert.Empty(t, c.mutations())
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name        string
		enable      bool
		installed   bool
		wantFiles   bool
		wantInstall bool
	}{
		{"never enabled", false, false, false, false},
		{"enabled", true, true, true, true},
		{"files but not installed", true, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClaude{}
			in := newTestInstaller(t, c)
			if tt.enable {
				require.NoError(t, in.WriteMod())
			}
			c.installed = tt.installed
			res, err := in.Status(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantFiles, res.FilesPresent)
			assert.Equal(t, tt.wantInstall, res.Installed)
		})
	}
}

// TestStatus_UnreadableListIsAnError verifies an unparseable claude answer is
// reported, not read as "not installed". Failure prevented: status says Oxy is
// missing when ox simply could not tell.
func TestStatus_UnreadableListIsAnError(t *testing.T) {
	in := newTestInstaller(t, &fakeClaude{})
	in.Claude = garbleClaude{}
	_, err := in.Status(context.Background())
	assert.Error(t, err)
}

type garbleClaude struct{}

func (garbleClaude) Run(context.Context, ...string) (string, error) { return "not json", nil }

// TestEnable_ReinstallsWhenClaudeRecordsAnOlderVersion verifies enable after
// an ox upgrade reinstalls the plugin, since Claude Code records a plugin's
// version at install time.
// Failure prevented: `ox pet status` keeps reporting Oxy as stale after the
// files were refreshed, because `plugin list` still shows the old version.
func TestEnable_ReinstallsWhenClaudeRecordsAnOlderVersion(t *testing.T) {
	c := &fakeClaude{hasMarketplace: true, installed: true}
	in := newTestInstaller(t, c)
	in.Version = "2.0.0"
	_, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"plugin validate " + in.Dir,
		"plugin uninstall ox-cli-oxy@ox-cli-pets --scope user",
		"plugin install ox-cli-oxy@ox-cli-pets --scope user",
	}, c.mutations())
}

// TestRecoverSwap_RestoresAnInterruptedRefresh verifies a refresh that died
// between WriteMod's renames is undone: the previous copy comes back, and a
// machine that never enabled Oxy stays that way.
// Failure prevented: doctor reads the missing folder as "never enabled" and
// Oxy silently disappears until the coworker re-enables him.
func TestRecoverSwap_RestoresAnInterruptedRefresh(t *testing.T) {
	in := newTestInstaller(t, &fakeClaude{})
	require.NoError(t, in.WriteMod())
	require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))

	require.NoError(t, RecoverSwap(in.Dir))
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v)
	assert.NoDirExists(t, in.Dir+".old")

	never := filepath.Join(t.TempDir(), "oxy")
	require.NoError(t, RecoverSwap(never))
	assert.NoDirExists(t, never)
	// a live copy wins over a leftover backup
	require.NoError(t, os.MkdirAll(in.Dir+".old", 0o755))
	require.NoError(t, RecoverSwap(in.Dir))
	assert.DirExists(t, in.Dir+".old")
}

// TestWriteMod_RecoversBeforeRewriting verifies WriteMod itself restores an
// interrupted swap before replacing the folder, so `ox pet enable` heals it.
func TestWriteMod_RecoversBeforeRewriting(t *testing.T) {
	in := newTestInstaller(t, &fakeClaude{})
	require.NoError(t, in.WriteMod())
	require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))
	in.Version = "2.0.0"
	require.NoError(t, in.WriteMod())
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", v)
	assert.NoDirExists(t, in.Dir+".old")
}

// TestInstaller_ClaudeFailuresAreReported verifies every claude CLI step that
// fails surfaces as an error instead of a half-done success.
// Failure prevented: enable or disable reports Oxy installed or removed while
// Claude Code never took the change.
func TestInstaller_ClaudeFailuresAreReported(t *testing.T) {
	type op func(Installer) error
	enable := func(in Installer) error { _, err := in.Enable(context.Background()); return err }
	disable := func(in Installer) error { _, err := in.Disable(context.Background()); return err }
	status := func(in Installer) error { _, err := in.Status(context.Background()); return err }
	tests := []struct {
		name      string
		failOn    string
		installed bool
		market    bool
		version   string
		run       op
	}{
		{"enable: marketplace list", "plugin marketplace list", false, false, "1.0.0", enable},
		{"enable: marketplace add", "plugin marketplace add", false, false, "1.0.0", enable},
		{"enable: plugin list", "plugin list", false, true, "1.0.0", enable},
		{"enable: install", "plugin install", false, true, "1.0.0", enable},
		{"enable: reinstall's uninstall", "plugin uninstall", true, true, "2.0.0", enable},
		{"disable: plugin list", "plugin list", true, true, "1.0.0", disable},
		{"disable: uninstall", "plugin uninstall", true, true, "1.0.0", disable},
		{"disable: marketplace list", "plugin marketplace list", true, true, "1.0.0", disable},
		{"disable: marketplace remove", "plugin marketplace remove", false, true, "1.0.0", disable},
		{"status: plugin list", "plugin list", true, true, "1.0.0", status},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClaude{failOn: tt.failOn, installed: tt.installed, hasMarketplace: tt.market}
			in := newTestInstaller(t, c)
			in.Version = tt.version
			assert.Error(t, tt.run(in))
		})
	}
}

// TestInstaller_UnreadableMarketplaceListIsAnError mirrors the plugin-list
// case for the marketplace list.
func TestInstaller_UnreadableMarketplaceListIsAnError(t *testing.T) {
	in := newTestInstaller(t, &fakeClaude{})
	in.Claude = garbleClaude{}
	_, err := in.Enable(context.Background())
	assert.Error(t, err)
}

// TestDisable_WithoutClaudeRemovesFilesAndExplains verifies disable on a
// machine without the claude CLI still removes the folder and returns the
// two Claude Code commands that finish the job.
func TestDisable_WithoutClaudeRemovesFilesAndExplains(t *testing.T) {
	in := Installer{Dir: filepath.Join(t.TempDir(), "oxy"), Version: "1.0.0", Frames: tinyFrames}
	require.NoError(t, in.WriteMod())
	res, err := in.Disable(context.Background())
	require.NoError(t, err)
	assert.NoDirExists(t, in.Dir)
	assert.Equal(t, []string{"/plugin uninstall ox-cli-oxy@ox-cli-pets", "/plugin marketplace remove ox-cli-pets"}, res.ManualSteps)
}

// TestWriteMod_FailuresLeaveTheOldCopy verifies a render or filesystem
// failure aborts the write without touching an installed mod.
// Failure prevented: a failed refresh deletes the working Oxy.
func TestWriteMod_FailuresLeaveTheOldCopy(t *testing.T) {
	in := newTestInstaller(t, &fakeClaude{})
	require.NoError(t, in.WriteMod())

	broken := in
	broken.Version = "2.0.0"
	broken.Frames = func() (*Frames, error) { return nil, errors.New("no art") }
	assert.Error(t, broken.WriteMod())
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v, "the installed copy survives")

	// a regular file where the parent folder should be
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	assert.Error(t, Installer{Dir: filepath.Join(blocker, "oxy"), Version: "1.0.0", Frames: tinyFrames}.WriteMod())
}

// TestClaudeCLI_RunsTheBinaryOnPath verifies the real CLI wrapper finds
// claude on PATH, passes arguments through, and reports a failing exit with
// its output.
func TestClaudeCLI_RunsTheBinaryOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a POSIX shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = fail ]; then echo nope; exit 3; fi\necho \"ran $*\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", bin)

	c := FindClaude()
	require.NotNil(t, c)
	out, err := c.Run(context.Background(), "plugin", "list")
	require.NoError(t, err)
	assert.Equal(t, "ran plugin list\n", out)
	_, err = c.Run(context.Background(), "fail")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	t.Setenv("PATH", t.TempDir())
	assert.Nil(t, FindClaude())
}

func TestDir_IsUnderOxData(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	assert.Equal(t, PluginName, filepath.Base(Dir()))
	assert.Equal(t, "claude-code", filepath.Base(filepath.Dir(Dir())))
}

// TestFramesFiles_RejectsBadFrames verifies a frame that isn't base64 or a
// cells file over what the mod can read fails the write instead of shipping.
func TestFramesFiles_RejectsBadFrames(t *testing.T) {
	bad := &Frames{Animations: map[string]AnimationData{"blink": {MS: []int{100}, PNG: []string{"%%%"}, Cells: map[string][]string{}}}}
	_, err := FramesFiles(bad)
	assert.Error(t, err)

	huge := &Frames{Animations: map[string]AnimationData{"blink": {
		MS: []int{100}, PNG: []string{""}, Cells: map[string][]string{"small": {strings.Repeat("A", fsReadLimit)}},
	}}}
	_, err = FramesFiles(huge)
	assert.ErrorContains(t, err, "over the")
}
