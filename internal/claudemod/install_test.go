package claudemod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPlugin = "ox-cli-test"
	testMarket = "ox-cli-tests"
	testRef    = testPlugin + "@" + testMarket
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
			return f.notice + `[{"name":"` + testMarket + `"}]`, nil
		}
		return f.notice + `[]`, nil
	case strings.HasPrefix(call, "plugin marketplace add"):
		f.hasMarketplace = true
	case strings.HasPrefix(call, "plugin marketplace remove"):
		f.hasMarketplace = false
	case call == "plugin list --json":
		if f.installed {
			return f.notice + `[{"id":"` + testRef + `","version":"1.0.0"}]`, nil
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

// testMod is a minimal mod: a plugin.json carrying the version placeholder
// and one hook file, under the "mod" root.
func testMod() fstest.MapFS {
	return fstest.MapFS{
		"mod/.claude-plugin/plugin.json":      {Data: []byte(`{"name":"ox-cli-test","version":"{{VERSION}}"}`)},
		"mod/.claude-plugin/marketplace.json": {Data: []byte(`{"name":"ox-cli-tests"}`)},
		"mod/hooks/register.tsx":              {Data: []byte("export {}\n")},
	}
}

func newTestInstaller(t *testing.T, c Claude) Installer {
	t.Helper()
	return Installer{
		Dir: filepath.Join(t.TempDir(), testPlugin), Plugin: testPlugin, Marketplace: testMarket,
		Version: "1.0.0", Claude: c, FS: testMod(), Root: "mod",
	}
}

func TestInstaller_Ref(t *testing.T) {
	assert.Equal(t, testRef, newTestInstaller(t, nil).Ref())
}

// TestWriteMod_StampsVersionWritesGeneratedAndDropsStaleFiles verifies a
// write replaces the whole folder: the version is stamped, generated files
// land after (and over) the static ones, and files the new mod no longer
// ships are gone. Failure prevented: a stale hooks module from an old ox
// keeps loading.
func TestWriteMod_StampsVersionWritesGeneratedAndDropsStaleFiles(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(in.Dir, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(in.Dir, "hooks", "stale.ts"), []byte("old"), 0o644))
	in.Version = "9.9.9"
	in.Generated = func() (map[string][]byte, error) {
		return map[string][]byte{"hooks/gen/data.json": []byte("{}"), "hooks/register.tsx": []byte("generated\n")}, nil
	}

	require.NoError(t, in.WriteMod())

	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "9.9.9", v)
	plugin, err := os.ReadFile(filepath.Join(in.Dir, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(plugin), versionPlaceholder)
	assert.FileExists(t, filepath.Join(in.Dir, ".claude-plugin", "marketplace.json"))
	assert.FileExists(t, filepath.Join(in.Dir, "hooks", "gen", "data.json"))
	reg, err := os.ReadFile(filepath.Join(in.Dir, "hooks", "register.tsx"))
	require.NoError(t, err)
	assert.Equal(t, "generated\n", string(reg), "generated files are written after FS files")
	assert.NoFileExists(t, filepath.Join(in.Dir, "hooks", "stale.ts"))

	entries, err := os.ReadDir(filepath.Dir(in.Dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "staging and backup folders must not be left behind")
}

// TestEnable_IsIdempotent verifies a second enable (the refresh after an ox
// upgrade) rewrites the files without adding the marketplace or installing twice.
// Failure prevented: a re-run errors on "already added" and blocks refreshes.
func TestEnable_IsIdempotent(t *testing.T) {
	c := &fakeClaude{notice: "Checking for updates…\n"}
	in := newTestInstaller(t, c)

	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.True(t, res.Installed)
	assert.Equal(t, testRef, res.Plugin)
	assert.Equal(t, []string{
		"plugin validate " + in.Dir,
		"plugin marketplace add " + in.Dir,
		"plugin install " + testRef + " --scope user",
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
	assert.Contains(t, err.Error(), "the "+testPlugin+" mod did not validate")
	for _, call := range c.calls {
		assert.NotContains(t, call, "install")
		assert.NotContains(t, call, "marketplace add")
	}
}

// TestEnable_WithoutClaudeWritesModAndExplains verifies a machine without the
// claude CLI still gets the mod plus the exact steps to finish in Claude Code.
func TestEnable_WithoutClaudeWritesModAndExplains(t *testing.T) {
	in := newTestInstaller(t, nil)
	res, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.False(t, res.Installed)
	assert.False(t, res.ClaudeFound)
	assert.True(t, res.FilesPresent)
	assert.Equal(t, []string{"/plugin marketplace add " + in.Dir, "/plugin install " + testRef}, res.ManualSteps)
	assert.FileExists(t, filepath.Join(in.Dir, ".claude-plugin", "plugin.json"))
}

// TestEnable_ReinstallsWhenClaudeRecordsAnOlderVersion verifies enable after
// an ox upgrade reinstalls the plugin, since Claude Code records a plugin's
// version at install time. Failure prevented: status keeps reporting the mod
// as stale after the files were refreshed.
func TestEnable_ReinstallsWhenClaudeRecordsAnOlderVersion(t *testing.T) {
	c := &fakeClaude{hasMarketplace: true, installed: true}
	in := newTestInstaller(t, c)
	in.Version = "2.0.0"
	_, err := in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"plugin validate " + in.Dir,
		"plugin uninstall " + testRef + " --scope user",
		"plugin install " + testRef + " --scope user",
	}, c.mutations())
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
		"plugin uninstall " + testRef + " --scope user",
		"plugin marketplace remove " + testMarket,
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

// TestDisable_WithoutClaudeRemovesFilesAndExplains verifies disable on a
// machine without the claude CLI still removes the folder and returns the
// two Claude Code commands that finish the job.
func TestDisable_WithoutClaudeRemovesFilesAndExplains(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())
	res, err := in.Disable(context.Background())
	require.NoError(t, err)
	assert.NoDirExists(t, in.Dir)
	assert.Equal(t, []string{"/plugin uninstall " + testRef, "/plugin marketplace remove " + testMarket}, res.ManualSteps)
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

type garbleClaude struct{}

func (garbleClaude) Run(context.Context, ...string) (string, error) { return "not json", nil }

// TestInstaller_UnreadableListsAreErrors verifies an unparseable claude
// answer is reported, not read as "not installed". Failure prevented: status
// says the mod is missing when ox simply could not tell.
func TestInstaller_UnreadableListsAreErrors(t *testing.T) {
	in := newTestInstaller(t, garbleClaude{})
	_, err := in.Status(context.Background())
	assert.ErrorContains(t, err, "plugin list")
	_, err = in.Enable(context.Background())
	assert.ErrorContains(t, err, "marketplace list")
}

// TestInstaller_ClaudeFailuresAreReported verifies every claude CLI step that
// fails surfaces as an error instead of a half-done success.
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

// TestRecoverSwap_RestoresAnInterruptedRefresh verifies a refresh that died
// between WriteMod's renames is undone: the previous copy comes back, and a
// machine that never wrote the mod stays that way.
func TestRecoverSwap_RestoresAnInterruptedRefresh(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())
	require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))

	require.NoError(t, RecoverSwap(in.Dir))
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v)
	assert.NoDirExists(t, in.Dir+".old")

	never := filepath.Join(t.TempDir(), "mod")
	require.NoError(t, RecoverSwap(never))
	assert.NoDirExists(t, never)
	// a live copy wins over a leftover backup
	require.NoError(t, os.MkdirAll(in.Dir+".old", 0o755))
	require.NoError(t, RecoverSwap(in.Dir))
	assert.DirExists(t, in.Dir+".old")
}

// TestWriteMod_RecoversBeforeRewriting verifies WriteMod itself restores an
// interrupted swap before replacing the folder, so enable heals it.
func TestWriteMod_RecoversBeforeRewriting(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())
	require.NoError(t, os.Rename(in.Dir, in.Dir+".old"))
	in.Version = "2.0.0"
	require.NoError(t, in.WriteMod())
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", v)
	assert.NoDirExists(t, in.Dir+".old")
}

// TestWriteMod_FailuresLeaveTheOldCopy verifies a generate or filesystem
// failure aborts the write without touching an installed mod.
// Failure prevented: a failed refresh deletes the working mod.
func TestWriteMod_FailuresLeaveTheOldCopy(t *testing.T) {
	in := newTestInstaller(t, nil)
	require.NoError(t, in.WriteMod())

	broken := in
	broken.Version = "2.0.0"
	broken.Generated = func() (map[string][]byte, error) { return nil, errors.New("no art") }
	assert.ErrorContains(t, broken.WriteMod(), "no art")
	v, err := WrittenVersion(in.Dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v, "the installed copy survives")

	noFS := in
	noFS.FS = nil
	assert.Error(t, noFS.WriteMod())

	// a regular file where the parent folder should be
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	blocked := in
	blocked.Dir = filepath.Join(blocker, testPlugin)
	assert.Error(t, blocked.WriteMod())
}

func TestWrittenVersion_Errors(t *testing.T) {
	dir := t.TempDir()
	_, err := WrittenVersion(dir)
	assert.Error(t, err, "no mod")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte("{"), 0o644))
	_, err = WrittenVersion(dir)
	assert.Error(t, err, "unreadable plugin.json")
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
	d := Dir("pets", testPlugin)
	assert.Equal(t, testPlugin, filepath.Base(d))
	assert.Equal(t, "claude-code", filepath.Base(filepath.Dir(d)))
	assert.Equal(t, "pets", filepath.Base(filepath.Dir(filepath.Dir(d))))
}
