package pet

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tinyCodexSheet is a 1x1 PNG and a minimal pet.json, so installing in a
// test takes microseconds.
func tinyCodexSheet(version string) (*CodexSheet, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		return nil, err
	}
	manifest := []byte(`{"id":"oxy","spritesheetPath":"spritesheet.png","oxVersion":"` + version + `"}` + "\n")
	return &CodexSheet{PNG: buf.Bytes(), Manifest: manifest}, nil
}

func newCodexInstaller(t *testing.T) CodexInstaller {
	t.Helper()
	return CodexInstaller{Home: t.TempDir(), Version: "1.0.0", Sheet: tinyCodexSheet}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestCodexEnable_WritesPetSelectsAndSkill verifies enable leaves Codex able
// to show Oxy on its next start. Failure prevented: the pet is written but
// never selected, or selected with no files, and Codex shows nothing.
func TestCodexEnable_WritesPetSelectsAndSkill(t *testing.T) {
	in := newCodexInstaller(t)
	res, err := in.Enable(context.Background())
	require.NoError(t, err)

	dir := CodexDir(in.Home)
	assert.Equal(t, filepath.Join(in.Home, "pets", "oxy"), dir)
	assert.FileExists(t, filepath.Join(dir, "spritesheet.png"))
	assert.FileExists(t, filepath.Join(dir, "pet.json"))
	v, err := CodexWrittenVersion(dir)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", v)
	assert.True(t, res.Selected)
	assert.Contains(t, res.Guidance, "Restart Codex")

	cfg := filepath.Join(in.Home, "config.toml")
	assert.Equal(t, "[tui]\npet = \"custom:oxy\"\n", readFile(t, cfg))
	info, err := os.Stat(cfg)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	skill := readFile(t, filepath.Join(in.Home, "skills", "oxy", "SKILL.md"))
	assert.True(t, len(skill) > 4 && skill[:4] == "---\n", "frontmatter opens the skill")
	assert.Contains(t, skill, "name: oxy\n")
	assert.Contains(t, skill, "disable-model-invocation: true\n")
	assert.Contains(t, skill, "<!-- ox-cli-oxy ver: 1.0.0 -->")
	assert.Contains(t, skill, "ox pet enable --host codex --json")

	st, err := in.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, st.FilesPresent)
	assert.True(t, st.Selected)
	assert.Equal(t, "custom:oxy", st.SelectedPet)
	assert.Equal(t, "1.0.0", st.InstalledVersion)
	assert.Contains(t, st.Guidance, "current terminal pet")
	assert.Contains(t, st.Guidance, "Claude Code options")
}

// TestCodexEnable_RealSheet installs the real Oxy sheet end to end.
func TestCodexEnable_RealSheet(t *testing.T) {
	if testing.Short() {
		t.Skip("short: renders Oxy's Codex sheet")
	}
	in := CodexInstaller{Home: t.TempDir(), Version: "2.0.0"}
	_, err := in.Enable(context.Background())
	require.NoError(t, err)
	f, err := os.Open(filepath.Join(CodexDir(in.Home), "spritesheet.png"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	cfg, err := png.DecodeConfig(f)
	require.NoError(t, err)
	assert.Equal(t, CodexSheetWidth, cfg.Width)
	assert.Equal(t, CodexSheetHeight, cfg.Height)
	v, err := CodexWrittenVersion(CodexDir(in.Home))
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", v)
}

const codexConfigTail = `
[tui.model_availability_nux]
pet = "not-this-one"
seen = true

[features]
hooks = true # keep me
`

// TestSelectCodexPet edits config.toml at the line level. Failure prevented:
// enabling Oxy rewrites a coworker's hand-tuned Codex config (comments,
// ordering, quoting) or edits a sub-table's `pet` instead of [tui]'s.
func TestSelectCodexPet(t *testing.T) {
	tests := []struct {
		name   string
		before string // "" with missing=true means no file
		want   string
	}{
		{
			name: "a: replaces the [tui] pet line, keeping its comment",
			before: "# my codex\nmodel = \"o3\"\n\n[tui]\nanimations = false\n" +
				"pet = \"custom:oxy.fake-copies\" # comment\n" + codexConfigTail,
			want: "# my codex\nmodel = \"o3\"\n\n[tui]\nanimations = false\n" +
				"pet = \"custom:oxy\" # comment\n" + codexConfigTail,
		},
		{
			name:   "a: replaces a literal-string value",
			before: "[tui]\npet = 'dewey'   # mine\n",
			want:   "[tui]\npet = \"custom:oxy\"   # mine\n",
		},
		{
			name:   "b: inserts after the [tui] header",
			before: "model = \"o3\"\n[tui] # ui\nanimations = false\n" + codexConfigTail,
			want:   "model = \"o3\"\n[tui] # ui\npet = \"custom:oxy\"\nanimations = false\n" + codexConfigTail,
		},
		{
			name:   "b: [tui] header at end of file without newline",
			before: "model = \"o3\"\n[tui]",
			want:   "model = \"o3\"\n[tui]\npet = \"custom:oxy\"\n",
		},
		{
			name:   "c: appends a [tui] table; a sub-table is not [tui]",
			before: "model = \"o3\"\n" + codexConfigTail,
			want:   "model = \"o3\"\n" + codexConfigTail + "\n[tui]\npet = \"custom:oxy\"\n",
		},
		{
			name:   "c: appends a newline first when the file lacks one",
			before: "model = \"o3\"",
			want:   "model = \"o3\"\n\n[tui]\npet = \"custom:oxy\"\n",
		},
		{
			name:   "c: empty file",
			before: "",
			want:   "[tui]\npet = \"custom:oxy\"\n",
		},
		{
			name:   "already selected is untouched",
			before: "[tui]\npet   =   \"custom:oxy\"  # same\n",
			want:   "[tui]\npet   =   \"custom:oxy\"  # same\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tt.before), 0o644))
			require.NoError(t, selectCodexPet(path, CodexPetSelector))
			assert.Equal(t, tt.want, readFile(t, path))
		})
	}

	t.Run("d: missing file is created 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "codex", "config.toml")
		require.NoError(t, selectCodexPet(path, CodexPetSelector))
		assert.Equal(t, "[tui]\npet = \"custom:oxy\"\n", readFile(t, path))
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})
}

// TestSelectCodexPet_RefusesLayoutsItCannotEditSafely verifies a config the
// line editor does not understand is reported, not corrupted.
func TestSelectCodexPet_RefusesLayoutsItCannotEditSafely(t *testing.T) {
	for name, before := range map[string]string{
		"inline tui table": "tui = { pet = \"dewey\" }\n",
		"dotted key":       "tui.pet = \"dewey\"\n",
		"unparseable":      "[tui\npet = \n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(before), 0o644))
			err := selectCodexPet(path, CodexPetSelector)
			require.Error(t, err)
			assert.Contains(t, err.Error(), path)
			assert.Equal(t, before, readFile(t, path))
		})
	}
}

// TestDeselectCodexPet verifies disable only ever removes Oxy. Failure
// prevented: turning Oxy off clears a coworker's chosen pet.
func TestDeselectCodexPet(t *testing.T) {
	tests := []struct {
		name, before, want string
	}{
		{
			name:   "removes our line only",
			before: "# top\n[tui]\nanimations = false\npet = \"custom:oxy\" # ox\n" + codexConfigTail,
			want:   "# top\n[tui]\nanimations = false\n" + codexConfigTail,
		},
		{
			name:   "removes the bare id",
			before: "[tui]\npet = \"oxy\"\n",
			want:   "[tui]\n",
		},
		{
			name:   "leaves another pet alone",
			before: "[tui]\npet = \"dewey\"\n" + codexConfigTail,
			want:   "[tui]\npet = \"dewey\"\n" + codexConfigTail,
		},
		{
			name:   "no-op when the key is absent",
			before: "[tui]\nanimations = false\n" + codexConfigTail,
			want:   "[tui]\nanimations = false\n" + codexConfigTail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tt.before), 0o644))
			require.NoError(t, deselectCodexPet(path, CodexPetSelector))
			assert.Equal(t, tt.want, readFile(t, path))
		})
	}

	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(t, deselectCodexPet(path, CodexPetSelector))
		assert.NoFileExists(t, path)
	})
}

// TestCodexDisable_RemovesEverythingOxyOwns verifies disable undoes enable
// and never touches another pet.
func TestCodexDisable_RemovesEverythingOxyOwns(t *testing.T) {
	in := newCodexInstaller(t)
	other := filepath.Join(in.Home, "pets", "dewey", "pet.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o755))
	require.NoError(t, os.WriteFile(other, []byte("{}"), 0o644))
	cfg := filepath.Join(in.Home, "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte("model = \"o3\"\n"), 0o600))

	_, err := in.Enable(context.Background())
	require.NoError(t, err)
	res, err := in.Disable(context.Background())
	require.NoError(t, err)

	assert.NoDirExists(t, CodexDir(in.Home))
	assert.NoDirExists(t, filepath.Join(in.Home, "skills", "oxy"))
	assert.FileExists(t, other)
	assert.Equal(t, "model = \"o3\"\n\n[tui]\n", readFile(t, cfg))
	assert.False(t, res.Selected)

	st, err := in.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, st.FilesPresent)
	assert.False(t, st.Selected)
}

func TestCodexDisable_WhenNeverEnabledIsANoOp(t *testing.T) {
	in := newCodexInstaller(t)
	_, err := in.Disable(context.Background())
	require.NoError(t, err)
	entries, err := os.ReadDir(in.Home)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestCodexEnable_IsIdempotent verifies the refresh after an ox upgrade
// rewrites the same bytes and leaves no swap debris.
func TestCodexEnable_IsIdempotent(t *testing.T) {
	in := newCodexInstaller(t)
	_, err := in.Enable(context.Background())
	require.NoError(t, err)
	snapshot := func() map[string]string {
		out := map[string]string{}
		require.NoError(t, filepath.WalkDir(in.Home, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			out[p] = readFile(t, p)
			return nil
		}))
		return out
	}
	first := snapshot()
	_, err = in.Enable(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first, snapshot())
	assert.NoDirExists(t, CodexDir(in.Home)+".old")
	entries, err := os.ReadDir(filepath.Join(in.Home, "pets"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no staging folder left behind")
}

// TestCodexStatus_Guidance covers what status tells an AI coworker to say.
func TestCodexStatus_Guidance(t *testing.T) {
	t.Run("stale after an ox upgrade", func(t *testing.T) {
		in := newCodexInstaller(t)
		_, err := in.Enable(context.Background())
		require.NoError(t, err)
		in.Version = "1.1.0"
		st, err := in.Status(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", st.InstalledVersion)
		assert.Contains(t, st.Guidance, "refresh")
	})
	t.Run("installed but another pet selected", func(t *testing.T) {
		in := newCodexInstaller(t)
		_, err := in.Enable(context.Background())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(in.Home, "config.toml"), []byte("[tui]\npet = \"dewey\"\n"), 0o600))
		st, err := in.Status(context.Background())
		require.NoError(t, err)
		assert.False(t, st.Selected)
		assert.Equal(t, "dewey", st.SelectedPet)
		assert.Contains(t, st.Guidance, "/pets")
	})
	t.Run("never enabled", func(t *testing.T) {
		st, err := newCodexInstaller(t).Status(context.Background())
		require.NoError(t, err)
		assert.False(t, st.FilesPresent)
		assert.False(t, st.Selected)
	})
}

// TestCodexWriteFiles_RecoversAnInterruptedSwap verifies a refresh that died
// between renames puts the previous copy back before rewriting.
func TestCodexWriteFiles_RecoversAnInterruptedSwap(t *testing.T) {
	in := newCodexInstaller(t)
	require.NoError(t, in.WriteFiles())
	dir := CodexDir(in.Home)
	require.NoError(t, os.Rename(dir, dir+".old"))

	require.NoError(t, recoverCodexSwap(dir))
	assert.FileExists(t, filepath.Join(dir, "pet.json"))
	assert.NoDirExists(t, dir+".old")

	require.NoError(t, os.Rename(dir, dir+".old"))
	in.Version = "1.1.0"
	require.NoError(t, in.WriteFiles())
	v, err := CodexWrittenVersion(dir)
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", v)
	assert.NoDirExists(t, dir+".old")
}

// TestCodexEnable_UnparseableConfigChangesNothing verifies a broken
// config.toml stops enable before any file is written.
func TestCodexEnable_UnparseableConfigChangesNothing(t *testing.T) {
	in := newCodexInstaller(t)
	cfg := filepath.Join(in.Home, "config.toml")
	broken := "[tui\npet = \n"
	require.NoError(t, os.WriteFile(cfg, []byte(broken), 0o600))
	_, err := in.Enable(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), cfg)
	assert.Equal(t, broken, readFile(t, cfg))
	assert.NoDirExists(t, CodexDir(in.Home))
	assert.NoDirExists(t, filepath.Join(in.Home, "skills"))
}

func TestTOMLValueEnd(t *testing.T) {
	for in, want := range map[string]int{
		`"a\"b" # c`: 6,
		`'x' #`:      3,
		"true # c\n": 4,
		"42\n":       2,
		"":           0,
	} {
		assert.Equal(t, want, tomlValueEnd(in), in)
	}
}
