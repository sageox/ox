package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// The editor's fallback must emit settings in the resolved output mode.
func TestConfigCommandListsSettings(t *testing.T) {
	dir := setupIsolatedUserConfig(t)
	t.Setenv("OX_USER_CONFIG", filepath.Join(dir, "user-config.yaml"))
	t.Chdir(dir)
	previousConfig := cfg
	t.Cleanup(func() { cfg = previousConfig })

	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			cfg = &config.Config{JSON: mode == "json"}
			var runErr error
			output := captureRealStdout(t, func() { runErr = configCmd.RunE(configCmd, nil) })
			require.NoError(t, runErr)
			if mode == "json" {
				var values []ConfigValue
				require.NoError(t, json.Unmarshal(output, &values), "output: %s", output)
				require.Len(t, values, len(AllSettings))
				return
			}
			require.Contains(t, string(output), "Configuration Settings")
			require.Contains(t, string(output), "session_recording:")
		})
	}
}

// A terminal on stdout must not open the editor when flags, CI, or stdin
// require non-interactive output. Exercise the real CLI and flag resolution.
func TestConfigCLIOutputModes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs the ox binary in a pseudo-terminal")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("pseudo-terminal integration is supported on macOS and Linux")
	}
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	for _, tt := range []struct {
		name       string
		args       []string
		env        []string
		stdinPipe  bool
		stdoutPipe bool
		wantJSON   bool
		wantTUI    bool
	}{
		{name: "no-interactive flag", args: []string{"--no-interactive"}},
		{name: "CI", env: []string{"CI=true"}},
		{name: "non-interactive environment", env: []string{"OX_NO_INTERACTIVE=1"}},
		{name: "JSON flag", args: []string{"--json", "--no-interactive=false"}, wantJSON: true},
		{name: "JSON environment", args: []string{"--no-interactive=false"}, env: []string{"OX_JSON=1"}, wantJSON: true},
		{name: "false JSON flag overrides environment", args: []string{"--json=false", "--no-interactive"}, env: []string{"OX_JSON=1"}},
		{name: "list JSON environment", args: []string{"list"}, env: []string{"OX_JSON=1"}, wantJSON: true},
		{name: "piped JSON output", args: []string{"--json"}, stdoutPipe: true, wantJSON: true},
		{name: "redirected stdin", args: []string{"--no-interactive=false"}, stdinPipe: true},
		{name: "redirected stdout", args: []string{"--no-interactive=false"}, stdoutPipe: true},
		{name: "interactive editor", wantTUI: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			env := []string{
				"TERM=xterm-256color",
				"NO_COLOR=1",
				"OX_XDG_ENABLE=1",
				"SAGEOX_ENDPOINT=http://127.0.0.1:1",
				"HTTP_PROXY=http://127.0.0.1:1",
				"HTTPS_PROXY=http://127.0.0.1:1",
				"NO_PROXY=localhost,127.0.0.1",
			}
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
				env = append(env, key+"="+filepath.Join(dir, key))
			}
			env = append(env, tt.env...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, dir, env, append([]string{"config"}, tt.args...)...)

			ptmx, tty, err := pty.Open()
			require.NoError(t, err)
			defer ptmx.Close()
			defer tty.Close()
			require.True(t, term.IsTerminal(int(tty.Fd())), "test must provide a real terminal")
			require.NoError(t, pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}))
			cmd.Stdin, cmd.Stdout = tty, tty
			if tt.stdinPipe {
				cmd.Stdin = strings.NewReader("")
			}
			var stdout, stderr bytes.Buffer
			if tt.stdoutPipe {
				cmd.Stdout = &stdout
			}
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			require.NoError(t, tty.Close()) // allow EOF once the child closes its copy

			const enterAltScreen = "\x1b[?1049h"
			var terminalOutput bytes.Buffer
			readErrors := make(chan error, 1)
			go func() {
				buf := make([]byte, 4096)
				backgroundAnswered := false
				quitSent := false
				for {
					n, readErr := ptmx.Read(buf)
					terminalOutput.Write(buf[:n])
					if !backgroundAnswered && bytes.Contains(terminalOutput.Bytes(), []byte(ansi.RequestPrimaryDeviceAttributes)) {
						// Answer Lip Gloss's startup probe as a terminal would.
						_, _ = ptmx.Write([]byte("\x1b]11;rgb:0000/0000/0000\a\x1b[?1;2c"))
						backgroundAnswered = true
					}
					if !quitSent && bytes.Contains(terminalOutput.Bytes(), []byte(enterAltScreen)) {
						// Quit an editor that opens so a regression fails on output,
						// rather than leaving a command waiting for keyboard input.
						_, _ = ptmx.Write([]byte("q"))
						quitSent = true
					}
					if readErr != nil {
						readErrors <- readErr
						return
					}
				}
			}()
			err = cmd.Wait()
			readErr := <-readErrors
			output := terminalOutput.Bytes()
			// PTY hangup is EOF on macOS and EIO on Linux.
			require.True(t, errors.Is(readErr, io.EOF) || errors.Is(readErr, syscall.EIO), "reading terminal: %v", readErr)
			require.NoError(t, err, "stdout: %s\nstderr: %s", output, stderr.String())
			if tt.stdoutPipe {
				output = stdout.Bytes()
			}
			require.Equal(t, tt.wantTUI, bytes.Contains(output, []byte(enterAltScreen)), "output: %s", output)
			if tt.wantTUI {
				return
			}
			if tt.wantJSON {
				if !tt.stdoutPipe {
					// A PTY also captures terminal queries written to stdin.
					// Check displayed JSON here, and raw JSON in the pipe case.
					output = []byte(ansi.Strip(string(output)))
				}
				var values []ConfigValue
				require.NoError(t, json.Unmarshal(output, &values), "output: %q", output)
				require.Len(t, values, len(AllSettings))
				return
			}
			require.Contains(t, string(output), "Configuration Settings")
			require.Contains(t, string(output), "session_recording:")
		})
	}
}

func TestWrapText_BasicWrapping(t *testing.T) {
	t.Parallel()

	m := configModel{}

	tests := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{
			name:  "short text fits in width",
			text:  "hello world",
			width: 40,
			want:  "hello world",
		},
		{
			name:  "long text wraps at word boundary",
			text:  "one two three four five six seven eight",
			width: 15,
			want:  "one two three\nfour five six\nseven eight",
		},
		{
			name:  "zero width uses default 60",
			text:  "short",
			width: 0,
			want:  "short",
		},
		{
			name:  "negative width uses default 60",
			text:  "short",
			width: -5,
			want:  "short",
		},
		{
			name:  "preserves indentation",
			text:  "  indented line here and more words follow",
			width: 20,
			want:  "  indented line here\n  and more words\n  follow",
		},
		{
			name:  "handles multiple newlines in input",
			text:  "line one\nline two",
			width: 40,
			want:  "line one\nline two",
		},
		{
			name:  "empty lines preserved as empty",
			text:  "before\n\nafter",
			width: 40,
			want:  "before\n\nafter",
		},
		{
			name:  "single word longer than width stays on one line",
			text:  "superlongword",
			width: 5,
			want:  "superlongword",
		},
		{
			name:  "empty text",
			text:  "",
			width: 40,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := m.wrapText(tt.text, tt.width)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatSourceArrow_AllLevels(t *testing.T) {
	t.Parallel()

	m := configModel{}

	tests := []struct {
		source ConfigLevel
		expect string
	}{
		{ConfigLevelUser, "user"},
		{ConfigLevelRepo, "repo"},
		{ConfigLevelTeam, "team"},
		{ConfigLevelDefault, "default"},
		{ConfigLevel("unknown"), "default"},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			t.Parallel()
			result := m.formatSourceArrow(tt.source)
			assert.Contains(t, result, tt.expect)
		})
	}
}

func TestParseValueDescriptions_ParsesPatterns(t *testing.T) {
	t.Parallel()

	m := configModel{}

	t.Run("dash separator", func(t *testing.T) {
		t.Parallel()
		desc := "auto - Recording starts automatically\nmanual - Recording starts on demand"
		vals := []string{"auto", "manual"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Recording starts automatically", result["auto"])
		assert.Equal(t, "Recording starts on demand", result["manual"])
	})

	t.Run("em dash separator", func(t *testing.T) {
		t.Parallel()
		desc := "enabled \u2014 Turns on the feature"
		vals := []string{"enabled"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Turns on the feature", result["enabled"])
	})

	t.Run("double space separator", func(t *testing.T) {
		t.Parallel()
		desc := "verbose  Extra detailed output"
		vals := []string{"verbose"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Equal(t, "Extra detailed output", result["verbose"])
	})

	t.Run("no matching pattern", func(t *testing.T) {
		t.Parallel()
		desc := "some unrelated text"
		vals := []string{"auto"}
		result := m.parseValueDescriptions(desc, vals)

		assert.Empty(t, result)
	})

	t.Run("empty description", func(t *testing.T) {
		t.Parallel()
		result := m.parseValueDescriptions("", []string{"a", "b"})
		assert.Empty(t, result)
	})

	t.Run("empty valid values", func(t *testing.T) {
		t.Parallel()
		result := m.parseValueDescriptions("auto - desc", []string{})
		assert.Empty(t, result)
	})
}

func TestBuildSettingsList_EmptyModel(t *testing.T) {
	t.Parallel()

	m := configModel{
		items:        []configItem{},
		categories:   map[string][]int{},
		catOrder:     []string{},
		displayOrder: []int{},
	}

	result := m.buildSettingsList(80)
	assert.Empty(t, result)
}

func TestBuildSettingsList_SingleItem(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:      "test_key",
					Category: "General",
				},
				value: &ConfigValue{
					Value:  "val1",
					Source: ConfigLevelDefault,
				},
			},
		},
		categories:   map[string][]int{"General": {0}},
		catOrder:     []string{"General"},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	assert.Contains(t, result, "GENERAL")
	assert.Contains(t, result, "test_key")
	assert.Contains(t, result, "val1")
}

func TestBuildSettingsList_SelectedVsUnselected(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "first", Category: "Cat"},
				value:   &ConfigValue{Value: "a", Source: ConfigLevelUser},
			},
			{
				setting: ConfigSetting{Key: "second", Category: "Cat"},
				value:   &ConfigValue{Value: "b", Source: ConfigLevelDefault},
			},
		},
		categories:   map[string][]int{"Cat": {0, 1}},
		catOrder:     []string{"Cat"},
		displayOrder: []int{0, 1},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	// selected item has source arrow for user level
	assert.Contains(t, result, "user")
	// both items present
	assert.Contains(t, result, "first")
	assert.Contains(t, result, "second")
}

func TestBuildDetailPanel_EmptyDisplayOrder(t *testing.T) {
	t.Parallel()

	m := configModel{
		displayOrder: []int{},
	}

	result := m.buildDetailPanel(80)
	assert.Empty(t, result)
}

func TestBuildDetailPanel_WithItem(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:             "session_recording",
					Description:     "Session recording mode",
					LongDescription: "Controls whether sessions are recorded.\nauto - starts automatically",
				},
				value: &ConfigValue{
					Value:   "auto",
					Source:  ConfigLevelUser,
					Default: "auto",
					UserVal: "auto",
				},
			},
		},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildDetailPanel(80)
	assert.Contains(t, result, "session_recording")
	assert.Contains(t, result, "Session recording mode")
	assert.Contains(t, result, "Override Chain")
	assert.Contains(t, result, "Effective")
}

func TestBuildDetailPanel_NoLongDescription(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{
					Key:         "simple_key",
					Description: "A simple setting",
				},
				value: &ConfigValue{
					Value:  "yes",
					Source: ConfigLevelDefault,
				},
			},
		},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildDetailPanel(80)
	assert.Contains(t, result, "simple_key")
	// should not have the long description section
	assert.NotContains(t, result, "Controls")
}

func TestBuildOverrideChain_UserActive(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "auto",
			Source:  ConfigLevelUser,
			Default: "manual",
			UserVal: "auto",
			RepoVal: "",
			TeamVal: "",
		},
	}

	result := m.buildOverrideChain(item)
	// headers present
	assert.Contains(t, result, "User")
	assert.Contains(t, result, "Repo")
	assert.Contains(t, result, "Team")
	assert.Contains(t, result, "Default")
}

func TestBuildOverrideChain_DefaultActive(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "auto",
			Source:  ConfigLevelDefault,
			Default: "auto",
		},
	}

	result := m.buildOverrideChain(item)
	assert.Contains(t, result, "User")
	assert.Contains(t, result, "Default")
}

func TestBuildOverrideChain_MultipleValuesSet(t *testing.T) {
	t.Parallel()

	m := configModel{}
	item := configItem{
		value: &ConfigValue{
			Value:   "disabled",
			Source:  ConfigLevelRepo,
			Default: "auto",
			UserVal: "",
			RepoVal: "disabled",
			TeamVal: "manual",
		},
	}

	result := m.buildOverrideChain(item)
	// repo is active
	assert.Contains(t, result, "disabled")
	// team value also shown
	assert.Contains(t, result, "manual")
}

func TestBuildSettingsList_MultipleCategories(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "a_key", Category: "Alpha"},
				value:   &ConfigValue{Value: "x", Source: ConfigLevelDefault},
			},
			{
				setting: ConfigSetting{Key: "b_key", Category: "Beta"},
				value:   &ConfigValue{Value: "y", Source: ConfigLevelUser},
			},
		},
		categories:   map[string][]int{"Alpha": {0}, "Beta": {1}},
		catOrder:     []string{"Alpha", "Beta"},
		displayOrder: []int{0, 1},
		cursor:       1,
	}

	result := m.buildSettingsList(80)
	assert.Contains(t, result, "ALPHA")
	assert.Contains(t, result, "BETA")
	// cursor on second item, so source arrow shows "user"
	assert.Contains(t, result, "user")
}

func TestBuildSettingsList_SkipsEmptyCategory(t *testing.T) {
	t.Parallel()

	m := configModel{
		items: []configItem{
			{
				setting: ConfigSetting{Key: "k", Category: "Filled"},
				value:   &ConfigValue{Value: "v", Source: ConfigLevelDefault},
			},
		},
		categories:   map[string][]int{"Filled": {0}, "Vacant": {}},
		catOrder:     []string{"Vacant", "Filled"},
		displayOrder: []int{0},
		cursor:       0,
	}

	result := m.buildSettingsList(80)
	assert.NotContains(t, result, "VACANT")
	assert.Contains(t, result, "FILLED")
}

func TestParseValueDescriptions_MultilineDescription(t *testing.T) {
	t.Parallel()

	m := configModel{}
	desc := `Controls session recording mode.

disabled - No sessions are recorded
manual - Recording starts only manually
auto - Recording starts automatically`

	vals := []string{"disabled", "manual", "auto"}
	result := m.parseValueDescriptions(desc, vals)

	require.Len(t, result, 3)
	assert.Equal(t, "No sessions are recorded", result["disabled"])
	assert.Equal(t, "Recording starts only manually", result["manual"])
	assert.Equal(t, "Recording starts automatically", result["auto"])
}

func TestWrapText_IndentedMultilineWrapping(t *testing.T) {
	t.Parallel()

	m := configModel{}
	text := "  first second third fourth fifth\nnormal line here"
	result := m.wrapText(text, 20)

	lines := strings.Split(result, "\n")
	assert.Greater(t, len(lines), 2, "wrapping should produce multiple lines")

	// find the boundary between indented paragraph and normal line
	normalIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "normal") {
			normalIdx = i
			break
		}
	}
	assert.Greater(t, normalIdx, 0, "normal line should appear after indented lines")

	// all lines before "normal" should preserve the two-space indent
	for i := 0; i < normalIdx; i++ {
		if lines[i] == "" {
			continue
		}
		assert.True(t, strings.HasPrefix(lines[i], "  "),
			"line %d should preserve indent: %q", i, lines[i])
	}
}
