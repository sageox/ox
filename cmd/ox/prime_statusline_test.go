package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// primeStatusLineRun is what a table case can observe about one prime run.
type primeStatusLineRun struct {
	gitRoot string
	// localBefore and localMod are settings.local.json before prime; nil when absent.
	localBefore []byte
	localMod    time.Time
	// sharedHooks is the shared settings.json "hooks" value before prime.
	sharedHooks json.RawMessage
	// localHooks is the settings.local.json "hooks" value before prime.
	localHooks json.RawMessage
	// logs is everything prime sent to slog at WARN and above.
	logs string
}

// TestEnsureClaudeHooks_SyncsStatusLineWhenHooksPresent covers the session-start
// path for a teammate whose repo already has ox hooks committed in the shared
// settings.json. Failure prevented: prime sees the hooks, returns early, and the
// coworker never gets the status line unless they happen to run `ox doctor`; or
// prime rewrites settings.local.json on every session start; or it replaces a
// coworker's own status line; or a sync failure is printed where the hook pipes
// it into the AI coworker's context on every session start.
func TestEnsureClaudeHooks_SyncsStatusLineWhenHooksPresent(t *testing.T) {
	const foreign = `{"statusLine":{"type":"command","command":"~/bin/my-status.sh"},"model":"opus"}`

	tests := []struct {
		name string
		// detected=false leaves HOME without ~/.claude (and cwd outside any
		// repo), so detectClaudeCode reports Claude Code is not in use.
		detected bool
		off      bool
		// local is the coworker's settings.local.json before prime; "" means absent.
		local string
		// current rewrites local to ox's current entry before prime.
		current bool
		// sharedStatusLine is a statusLine to put in the shared settings.json.
		sharedStatusLine string
		// richHooks adds a coworker's own PreToolUse guard, with the hook
		// fields ox does not model (args, timeout, onFailure), to the shared
		// file and, when local is set, to settings.local.json too.
		richHooks bool
		check     func(t *testing.T, run primeStatusLineRun)
	}{
		{
			name:     "hooks present, no status line: prime installs it",
			detected: true,
			check: func(t *testing.T, run primeStatusLineRun) {
				assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, run.gitRoot)))
			},
		},
		{
			name:             "legacy ox status line in shared settings.json is moved to settings.local.json",
			detected:         true,
			sharedStatusLine: `{"type":"command","command":"ox statusline"}`,
			check: func(t *testing.T, run primeStatusLineRun) {
				shared := readProjectSettingsMap(t, run.gitRoot)
				assert.NotContains(t, shared, "statusLine", "the committed file must stop forcing the row on every coworker")
				assert.JSONEq(t, string(run.sharedHooks), string(shared["hooks"]), "ox hooks in the shared file are kept")
				assert.JSONEq(t, `"opus"`, string(shared["model"]), "other shared keys are kept")
				assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, run.gitRoot)))
			},
		},
		{
			name:      "coworker's own hook fields survive the status line write",
			detected:  true,
			richHooks: true,
			local:     `{"model":"opus"}`,
			check: func(t *testing.T, run primeStatusLineRun) {
				local := readLocalSettingsMap(t, run.gitRoot)
				assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(local))
				assert.JSONEq(t, string(run.localHooks), string(local["hooks"]), "a guard's args, timeout and onFailure must not be stripped")
			},
		},
		{
			name:             "legacy move keeps every field of the shared hooks",
			detected:         true,
			richHooks:        true,
			sharedStatusLine: `{"type":"command","command":"ox statusline"}`,
			check: func(t *testing.T, run primeStatusLineRun) {
				shared := readProjectSettingsMap(t, run.gitRoot)
				assert.NotContains(t, shared, "statusLine")
				assert.JSONEq(t, string(run.sharedHooks), string(shared["hooks"]), "a guard's args, timeout and onFailure must not be stripped")
			},
		},
		{
			name:     "setting off: existing ox entry is removed",
			detected: true,
			off:      true,
			local:    `{"statusLine":{"type":"command","command":"ox statusline"},"model":"opus"}`,
			check: func(t *testing.T, run primeStatusLineRun) {
				assert.JSONEq(t, `{"model":"opus"}`, string(mustRead(t, getLocalClaudeSettingsPath(run.gitRoot))))
			},
		},
		{
			name:     "setting off, nothing installed: nothing is written",
			detected: true,
			off:      true,
			check: func(t *testing.T, run primeStatusLineRun) {
				assert.NoFileExists(t, getLocalClaudeSettingsPath(run.gitRoot))
			},
		},
		{
			name:     "coworker's own statusLine is left untouched",
			detected: true,
			local:    foreign,
			check: func(t *testing.T, run primeStatusLineRun) {
				assertFileUnchanged(t, getLocalClaudeSettingsPath(run.gitRoot), run.localBefore, run.localMod)
			},
		},
		{
			name:     "already current: file is not rewritten",
			detected: true,
			local:    `{"model":"opus"}`,
			current:  true,
			check: func(t *testing.T, run primeStatusLineRun) {
				assertFileUnchanged(t, getLocalClaudeSettingsPath(run.gitRoot), run.localBefore, run.localMod)
			},
		},
		{
			name:     "malformed settings.local.json: logged, file untouched, prime unaffected",
			detected: true,
			local:    `{"model":`,
			check: func(t *testing.T, run primeStatusLineRun) {
				assertFileUnchanged(t, getLocalClaudeSettingsPath(run.gitRoot), run.localBefore, run.localMod)
				assert.Contains(t, run.logs, "status line", "the failure is still recorded for diagnosis")
			},
		},
		{
			name:     "Claude Code not detected: nothing is written",
			detected: false,
			check: func(t *testing.T, run primeStatusLineRun) {
				assert.NoFileExists(t, getLocalClaudeSettingsPath(run.gitRoot))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateUserConfig(t)
			run := primeStatusLineRun{gitRoot: t.TempDir()}

			// a teammate's fresh clone: ox hooks committed in settings.json,
			// no settings.local.json of their own yet
			require.NoError(t, InstallProjectClaudeHooks(run.gitRoot))
			require.NoError(t, os.Remove(getLocalClaudeSettingsPath(run.gitRoot)))
			require.True(t, HasProjectClaudeHooks(run.gitRoot))

			if tt.sharedStatusLine != "" || tt.richHooks {
				shared := readProjectSettingsMap(t, run.gitRoot)
				if tt.sharedStatusLine != "" {
					shared["statusLine"] = json.RawMessage(tt.sharedStatusLine)
				}
				if tt.richHooks {
					shared["hooks"] = withGuardHook(t, shared["hooks"])
				}
				shared["model"] = json.RawMessage(`"opus"`)
				data, err := json.Marshal(shared)
				require.NoError(t, err)
				writeProjectSettings(t, run.gitRoot, string(data))
			}
			run.sharedHooks = readProjectSettingsMap(t, run.gitRoot)["hooks"]
			require.NotEmpty(t, run.sharedHooks)

			if tt.local != "" {
				local := tt.local
				if tt.richHooks {
					var m map[string]json.RawMessage
					require.NoError(t, json.Unmarshal([]byte(local), &m))
					m["hooks"] = withGuardHook(t, nil)
					data, err := json.Marshal(m)
					require.NoError(t, err)
					local = string(data)
				}
				writeLocalSettings(t, run.gitRoot, local)
				if tt.richHooks {
					run.localHooks = readLocalSettingsMap(t, run.gitRoot)["hooks"]
				}
			}
			if tt.current {
				_, err := syncProjectStatusLine(run.gitRoot)
				require.NoError(t, err)
				require.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, run.gitRoot)))
			}
			if tt.off {
				require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
			}

			localPath := getLocalClaudeSettingsPath(run.gitRoot)
			if _, err := os.Stat(localPath); err == nil {
				// backdate so any rewrite shows as an mtime change
				run.localMod = time.Now().Add(-time.Hour).Truncate(time.Second)
				require.NoError(t, os.Chtimes(localPath, run.localMod, run.localMod))
				run.localBefore = mustRead(t, localPath)
			}

			home := t.TempDir()
			t.Setenv("HOME", home)
			if tt.detected {
				require.NoError(t, os.Mkdir(filepath.Join(home, ".claude"), 0o755))
			}
			require.Equal(t, tt.detected, detectClaudeCode())

			// prime's stdout and stderr both reach the AI coworker's context
			// (hooks run it with 2>&1); its slog WARNs go to a file instead.
			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			var installed bool
			var stdout []byte
			stderr := captureStderr(t, func() {
				stdout = captureRealStdout(t, func() {
					installed = ensureClaudeHooks(run.gitRoot)
				})
			})
			run.logs = logs.String()

			assert.False(t, installed, "hooks were already installed, so none are newly installed")
			assert.Empty(t, string(stdout), "nothing may reach the AI coworker's context on stdout")
			assert.Empty(t, stderr, "nothing may reach the AI coworker's context on stderr")
			tt.check(t, run)
		})
	}
}

// withGuardHook adds a coworker's PreToolUse policy guard to a hooks value,
// carrying the fields Claude Code supports but ox's Hook struct does not
// (args, timeout, onFailure). hooks may be nil for a file with no hooks yet.
func withGuardHook(t *testing.T, hooks json.RawMessage) json.RawMessage {
	t.Helper()
	events := map[string]json.RawMessage{}
	if hooks != nil {
		require.NoError(t, json.Unmarshal(hooks, &events))
	}
	var pre []json.RawMessage
	if raw, ok := events["PreToolUse"]; ok {
		require.NoError(t, json.Unmarshal(raw, &pre))
	}
	pre = append(pre, json.RawMessage(`{"matcher":"Bash","hooks":[{"type":"command","command":"node","args":["guard.js"],"timeout":30,"onFailure":"block"}]}`))
	raw, err := json.Marshal(pre)
	require.NoError(t, err)
	events["PreToolUse"] = raw
	out, err := json.Marshal(events)
	require.NoError(t, err)
	return out
}

func assertFileUnchanged(t *testing.T, path string, before []byte, beforeMod time.Time) {
	t.Helper()
	assert.Equal(t, string(before), string(mustRead(t, path)))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(beforeMod), "file was rewritten: mtime %v, want %v", info.ModTime(), beforeMod)
}
