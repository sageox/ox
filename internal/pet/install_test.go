package pet

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The generic install flow (validate, marketplace, version-drift reinstall,
// swap recovery, failure reporting) is covered in internal/claudemod. These
// tests cover what pet adds: Oxy's identity, its folder, and its frames.

// fakeClaude answers the claude CLI from memory and records each call.
type fakeClaude struct {
	calls          []string
	hasMarketplace bool
	installed      bool
}

func (f *fakeClaude) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case call == "plugin marketplace list --json":
		if f.hasMarketplace {
			return `[{"name":"ox-cli-pets"}]`, nil
		}
		return `[]`, nil
	case strings.HasPrefix(call, "plugin marketplace add"):
		f.hasMarketplace = true
	case strings.HasPrefix(call, "plugin marketplace remove"):
		f.hasMarketplace = false
	case call == "plugin list --json":
		if f.installed {
			return `[{"id":"ox-cli-oxy@ox-cli-pets","version":"1.0.0"}]`, nil
		}
		return `[]`, nil
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

func newTestInstaller(t *testing.T, c Claude) Installer {
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

// TestInstaller_RegistersOxyUnderItsOwnNames verifies the pet wrapper hands
// Claude Code Oxy's plugin and marketplace, end to end through enable,
// status, and disable. Failure prevented: the extraction installs Oxy under
// the wrong id, so `ox pet disable` cannot find him.
func TestInstaller_RegistersOxyUnderItsOwnNames(t *testing.T) {
	c := &fakeClaude{}
	in := newTestInstaller(t, c)

	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, PluginRef, res.Plugin)
	assert.True(t, res.Installed)
	assert.Equal(t, []string{
		"plugin validate " + in.Dir,
		"plugin marketplace add " + in.Dir,
		"plugin install ox-cli-oxy@ox-cli-pets --scope user",
	}, c.mutations())

	st, err := in.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, st.FilesPresent)
	assert.True(t, st.Installed)

	c.calls = nil
	_, err = in.Disable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"plugin uninstall ox-cli-oxy@ox-cli-pets --scope user",
		"plugin marketplace remove ox-cli-pets",
	}, c.mutations())
	assert.NoDirExists(t, in.Dir)
}

// TestEnable_WithoutClaudeNamesOxy verifies the manual steps name Oxy's
// plugin and marketplace.
func TestEnable_WithoutClaudeNamesOxy(t *testing.T) {
	in := newTestInstaller(t, nil)
	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"/plugin marketplace add " + in.Dir, "/plugin install ox-cli-oxy@ox-cli-pets"}, res.ManualSteps)
}

// TestWriteMod_FramesFailureLeavesTheOldCopy verifies a frame render failure
// aborts the write without touching an installed Oxy.
// Failure prevented: a failed refresh deletes the working Oxy.
func TestWriteMod_FramesFailureLeavesTheOldCopy(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())

	broken := in
	broken.Version = "2.0.0"
	broken.Frames = func() (*Frames, error) { return nil, errors.New("no art") }
	assert.Error(t, broken.WriteMod())
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v, "the installed copy survives")
}

// TestRecoverSwap_DelegatesForOxy verifies pet's RecoverSwap restores an
// interrupted Oxy refresh (doctor and integrate call it through pet).
func TestRecoverSwap_DelegatesForOxy(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())
	require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))
	require.NoError(t, RecoverSwap(in.Dir))
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v)
}

func TestDir_IsUnderOxData(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	assert.Equal(t, PluginName, filepath.Base(Dir()))
	assert.Equal(t, "claude-code", filepath.Base(filepath.Dir(Dir())))
	assert.Equal(t, "pets", filepath.Base(filepath.Dir(filepath.Dir(Dir()))))
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
