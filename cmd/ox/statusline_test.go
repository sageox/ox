package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/oxactivity"
)

// statuslineFixture is an initialized SageOx project with a Ledger and Team
// Context checkout, a primed Claude Code session, and isolated user and
// Claude Code config — everything `ox statusline` reads.
type statuslineFixture struct {
	root       string
	ledgerDir  string
	teamDir    string
	claudeDir  string
	agentID    string
	sessionID  string
	userConfig string
}

func newStatuslineFixture(t *testing.T) *statuslineFixture {
	t.Helper()
	isolateSessionMarkerDir(t)
	tmp := t.TempDir()
	f := &statuslineFixture{
		root:       filepath.Join(tmp, "repo"),
		ledgerDir:  filepath.Join(tmp, "ledger"),
		teamDir:    filepath.Join(tmp, "team"),
		claudeDir:  filepath.Join(tmp, "claude"),
		agentID:    "OxSt01",
		sessionID:  "claude-session-" + strings.ReplaceAll(t.Name(), "/", "_"),
		userConfig: filepath.Join(tmp, "config.yaml"),
	}
	for _, d := range []string{f.root, f.ledgerDir, f.teamDir, f.claudeDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	t.Setenv(config.EnvUserConfig, f.userConfig)
	t.Setenv("CLAUDE_CONFIG_DIR", f.claudeDir)
	t.Setenv(envStatuslineChained, "")
	t.Setenv("SAGEOX_TOKEN", "")

	require.NoError(t, config.SaveProjectConfig(f.root, &config.ProjectConfig{
		RepoID: "repo_statusline_test",
		TeamID: "team_statusline",
	}))
	require.NoError(t, config.SaveLocalConfig(f.root, &config.LocalConfig{
		Ledger:       &config.LedgerConfig{Path: f.ledgerDir},
		TeamContexts: []config.TeamContext{{TeamID: "team_statusline", Path: f.teamDir}},
	}))
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID:        f.agentID,
		AgentSessionID: f.sessionID,
		PrimedAt:       time.Now(),
	}))
	// the project root as hooks name it (symlinks resolved, e.g. /var → /private/var)
	resolved, err := filepath.EvalSymlinks(f.root)
	require.NoError(t, err)
	f.root = resolved
	return f
}

// stdin is the status-line JSON Claude Code pipes to the command.
func (f *statuslineFixture) stdin() []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id":      f.sessionID,
		"cwd":             f.root,
		"transcript_path": filepath.Join(f.root, "t.jsonl"),
		"workspace":       map[string]string{"current_dir": f.root, "project_dir": f.root},
	})
	return b
}

func (f *statuslineFixture) run(t *testing.T, opts statuslineOptions) string {
	t.Helper()
	var out bytes.Buffer
	runStatusline(bytes.NewReader(f.stdin()), &out, opts)
	return out.String()
}

func (f *statuslineFixture) writeMurmur(t *testing.T, base string, m ledger.MurmurFile) {
	t.Helper()
	if m.ID == "" {
		m.ID = fmt.Sprintf("m-%d", time.Now().UnixNano())
	}
	if m.Importance == "" {
		m.Importance = "normal"
	}
	path := filepath.Join(base, ledger.MurmurFilePath(m.Timestamp.UTC(), m.ID))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func (f *statuslineFixture) setUserStatusline(t *testing.T, command string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": command},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.claudeDir, "settings.json"), data, 0o644))
}

func testStatuslineOptions(now time.Time) statuslineOptions {
	return statuslineOptions{
		now:          now,
		color:        false,
		chainTimeout: 2 * time.Second,
		getenv:       os.Getenv,
		stdinWait:    time.Second,
	}
}

// --- A. What ox did for this AI coworker ---

// TestStatusline_ActionFreshness shows a recorded action only while it is
// recent. Failure prevented: the bar claims "posted murmur" an hour after the
// fact, or never shows the action at all.
func TestStatusline_ActionFreshness(t *testing.T) {
	tests := []struct {
		name  string
		state *oxactivity.State
		want  string
	}{
		{"no state yet", nil, "● ox\n"},
		{"recent action", &oxactivity.State{LastAction: "posted murmur", LastActionAt: time.Now().Add(-2 * time.Minute)}, "● ox  posted murmur 2m\n"},
		{"stale action (>10m)", &oxactivity.State{LastAction: "posted murmur", LastActionAt: time.Now().Add(-11 * time.Minute)}, "● ox\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			if tt.state != nil {
				require.NoError(t, oxactivity.SaveState(f.root, f.agentID, *tt.state))
			}
			assert.Equal(t, tt.want, f.run(t, testStatuslineOptions(time.Now())))
		})
	}
}

// TestStatusline_UnknownSessionStillShowsMark covers a status line refreshed
// before prime wrote its marker. Failure prevented: an empty or error bar in
// the first seconds of every session.
func TestStatusline_UnknownSessionStillShowsMark(t *testing.T) {
	f := newStatuslineFixture(t)
	f.sessionID = "never-primed"
	assert.Equal(t, "● ox\n", f.run(t, testStatuslineOptions(time.Now())))
}

// --- B. Teammate murmurs ---

// TestStatusline_TeammateMurmurs shows the newest teammate murmur from the
// Ledger or Team Context and never the coworker's own. Failure prevented: the
// bar echoes your own `ox murmur` back to you as "whisper from <you>", or
// misses a teammate's.
func TestStatusline_TeammateMurmurs(t *testing.T) {
	self := identity.LocalAttributionUsername()
	now := time.Now()
	tests := []struct {
		name    string
		murmurs func(f *statuslineFixture) []struct {
			base string
			m    ledger.MurmurFile
		}
		want string
	}{
		{
			name: "teammate murmur in the Ledger",
			murmurs: func(f *statuslineFixture) []struct {
				base string
				m    ledger.MurmurFile
			} {
				return []struct {
					base string
					m    ledger.MurmurFile
				}{{f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm01", PrincipalID: "160305533-ryan", Topic: "wip", Content: "refactoring auth", Timestamp: now.Add(-5 * time.Minute)}}}
			},
			want: "● ox  whisper from ryan [wip] refactoring auth 5m\n",
		},
		{
			name: "teammate murmur in Team Context",
			murmurs: func(f *statuslineFixture) []struct {
				base string
				m    ledger.MurmurFile
			} {
				return []struct {
					base string
					m    ledger.MurmurFile
				}{{f.teamDir, ledger.MurmurFile{AgentID: "OxTm02", PrincipalID: "avery", Topic: "decision", Content: "use sqlite", Timestamp: now.Add(-3 * time.Minute)}}}
			},
			want: "● ox  whisper from avery [decision] use sqlite 3m\n",
		},
		{
			name: "own murmur by principal is hidden",
			murmurs: func(f *statuslineFixture) []struct {
				base string
				m    ledger.MurmurFile
			} {
				return []struct {
					base string
					m    ledger.MurmurFile
				}{{f.ledgerDir, ledger.MurmurFile{AgentID: "OxOther", PrincipalID: self, Topic: "wip", Content: "my own work", Timestamp: now.Add(-time.Minute)}}}
			},
			want: "● ox\n",
		},
		{
			name: "own murmur by agent ID is hidden, older teammate one shows",
			murmurs: func(f *statuslineFixture) []struct {
				base string
				m    ledger.MurmurFile
			} {
				return []struct {
					base string
					m    ledger.MurmurFile
				}{
					{f.ledgerDir, ledger.MurmurFile{AgentID: f.agentID, PrincipalID: "someone-else", Topic: "wip", Content: "mine", Timestamp: now.Add(-time.Minute)}},
					{f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm03", PrincipalID: "sam", Topic: "wip", Content: "theirs", Timestamp: now.Add(-4 * time.Minute)}},
				}
			},
			want: "● ox  whisper from sam [wip] theirs 4m\n",
		},
		{
			name: "daemon file-change notices and old murmurs are hidden",
			murmurs: func(f *statuslineFixture) []struct {
				base string
				m    ledger.MurmurFile
			} {
				return []struct {
					base string
					m    ledger.MurmurFile
				}{
					{f.ledgerDir, ledger.MurmurFile{AgentID: "daemon", PrincipalID: "riley", Topic: "file-changes", Content: "3 files", Timestamp: now.Add(-time.Minute)}},
					{f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm04", PrincipalID: "quinn", Topic: "wip", Content: "ancient", Timestamp: now.Add(-90 * time.Minute)}},
				}
			},
			want: "● ox\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			for _, w := range tt.murmurs(f) {
				f.writeMurmur(t, w.base, w.m)
			}
			assert.Equal(t, tt.want, f.run(t, testStatuslineOptions(now)))
		})
	}
}

// TestStatusline_NewerWhisperWins prefers whichever is newer: the whisper a
// hook recorded or a murmur file. Failure prevented: an old delivered whisper
// masks a teammate's fresh murmur (or the reverse).
func TestStatusline_NewerWhisperWins(t *testing.T) {
	now := time.Now()
	f := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{
		LastWhisperFrom: "ryan", LastWhisperTopic: "wip", LastWhisperContent: "from the hook",
		LastWhisperAt: now.Add(-20 * time.Minute),
	}))
	f.writeMurmur(t, f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm05", PrincipalID: "avery", Topic: "wip", Content: "from the file", Timestamp: now.Add(-2 * time.Minute)})
	assert.Equal(t, "● ox  whisper from avery [wip] from the file 2m\n", f.run(t, testStatuslineOptions(now)))

	g := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(g.root, g.agentID, oxactivity.State{
		LastWhisperFrom: "ryan", LastWhisperTopic: "wip", LastWhisperContent: "from the hook",
		LastWhisperAt: now.Add(-time.Minute),
	}))
	g.writeMurmur(t, g.ledgerDir, ledger.MurmurFile{AgentID: "OxTm06", PrincipalID: "avery", Topic: "wip", Content: "from the file", Timestamp: now.Add(-30 * time.Minute)})
	assert.Equal(t, "● ox  whisper from ryan [wip] from the hook 1m\n", g.run(t, testStatuslineOptions(now)))
}

// TestStatusline_MurmurReadIsCachedAndConsumesNothing reads the same murmur on
// every refresh and serves repeats from the cache. Failure prevented: the
// status line acts as a whisper cursor and "uses up" a murmur, or rescans the
// Ledger on every keystroke.
func TestStatusline_MurmurReadIsCachedAndConsumesNothing(t *testing.T) {
	now := time.Now()
	f := newStatuslineFixture(t)
	f.writeMurmur(t, f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm07", PrincipalID: "ryan", Topic: "wip", Content: "hello", Timestamp: now.Add(-time.Minute)})

	want := "● ox  whisper from ryan [wip] hello 1m\n"
	assert.Equal(t, want, f.run(t, testStatuslineOptions(now)))
	assert.Equal(t, want, f.run(t, testStatuslineOptions(now.Add(time.Second))), "a second refresh still shows it")
	assert.FileExists(t, statuslineMurmurCachePath(f.root))

	// within the cache TTL a new murmur is not yet read...
	f.writeMurmur(t, f.ledgerDir, ledger.MurmurFile{AgentID: "OxTm08", PrincipalID: "avery", Topic: "wip", Content: "newer", Timestamp: now})
	assert.Equal(t, want, f.run(t, testStatuslineOptions(now.Add(2*time.Second))))
	// ...and after it, it is
	assert.Contains(t, f.run(t, testStatuslineOptions(now.Add(statuslineMurmurCacheTTL+time.Second))), "whisper from avery [wip] newer")
}

// --- C. Off switch, project detection, failure paths ---

// TestStatusline_OffPrintsOnlyChained honors `ox config set statusline off`.
// Failure prevented: the setting is ignored and ox keeps drawing in the bar,
// or turning ox off also blanks the coworker's own status line.
func TestStatusline_OffPrintsOnlyChained(t *testing.T) {
	f := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{LastAction: "posted murmur", LastActionAt: time.Now()}))
	require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))

	assert.Equal(t, "", f.run(t, testStatuslineOptions(time.Now())), "off with no chained command prints nothing")

	f.setUserStatusline(t, "printf 'my line'")
	assert.Equal(t, "my line\n", f.run(t, testStatuslineOptions(time.Now())))
}

// TestStatusline_OutsideSageoxProject prints nothing of ox's outside an
// initialized project. Failure prevented: a "● ox" mark in repos that do not
// use SageOx (e.g. after `ox uninstall` left the setting behind).
func TestStatusline_OutsideSageoxProject(t *testing.T) {
	f := newStatuslineFixture(t)
	f.root = t.TempDir()
	assert.Equal(t, "", f.run(t, testStatuslineOptions(time.Now())))
	f.setUserStatusline(t, "printf 'mine'")
	assert.Equal(t, "mine\n", f.run(t, testStatuslineOptions(time.Now())))
}

// TestStatusline_GarbageInputStillShowsMark covers unreadable stdin and a
// corrupt state file. Failure prevented: an error message or a blank bar.
func TestStatusline_GarbageInputStillShowsMark(t *testing.T) {
	f := newStatuslineFixture(t)
	t.Chdir(f.root)
	statePath := oxactivity.StatePath(f.root, f.agentID)
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, os.WriteFile(statePath, []byte("{not json"), 0o600))
	require.NoError(t, os.WriteFile(statuslineMurmurCachePath(f.root), []byte("garbage"), 0o600))

	var out bytes.Buffer
	runStatusline(strings.NewReader("this is not json"), &out, testStatuslineOptions(time.Now()))
	assert.Equal(t, "● ox\n", out.String())

	out.Reset()
	runStatusline(bytes.NewReader(f.stdin()), &out, testStatuslineOptions(time.Now()))
	assert.Equal(t, "● ox\n", out.String())
}

// --- D. Chaining the coworker's own status line ---

// TestStatusline_ChainsUserCommand runs the coworker's user-level statusLine
// (hidden by ox's project setting) and appends ox on its last line. Failure
// prevented: installing ox wipes out a coworker's custom status bar.
func TestStatusline_ChainsUserCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"single line", "printf 'main ✓'", "main ✓  ● ox\n"},
		{"trailing newline", "echo 'main ✓'", "main ✓  ● ox\n"},
		{"multi-line puts ox on the last line", "printf 'line one\\nline two\\n'", "line one\nline two  ● ox\n"},
		{"receives the same stdin", `sed -n 's/.*"session_id":"\([^"]*\)".*/sid=\1/p'`, "sid=SESSION  ● ox\n"},
		{"failing command keeps its output", "printf 'partial'; exit 3", "partial  ● ox\n"},
		{"ox's own command is not chained", "ox statusline", "● ox\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			f.setUserStatusline(t, tt.command)
			want := strings.ReplaceAll(tt.want, "SESSION", f.sessionID)
			assert.Equal(t, want, f.run(t, testStatuslineOptions(time.Now())))
		})
	}
}

// TestStatusline_ChainedCommandTimeout drops a chained command that runs too
// long and still prints ox's line promptly. Failure prevented: a slow user
// script freezes the status bar.
func TestStatusline_ChainedCommandTimeout(t *testing.T) {
	f := newStatuslineFixture(t)
	f.setUserStatusline(t, "sleep 5; echo late")
	opts := testStatuslineOptions(time.Now())
	opts.chainTimeout = 100 * time.Millisecond

	start := time.Now()
	got := f.run(t, opts)
	assert.Equal(t, "● ox\n", got)
	assert.Less(t, time.Since(start), 2*time.Second, "the timeout must bound the whole status line")
}

// TestStatusline_ChainRecursionGuard refuses to chain from inside a chained
// command. Failure prevented: a user script that calls `ox statusline` forks
// forever.
func TestStatusline_ChainRecursionGuard(t *testing.T) {
	f := newStatuslineFixture(t)
	f.setUserStatusline(t, "printf 'mine'")
	t.Setenv(envStatuslineChained, "1")
	assert.Equal(t, "● ox\n", f.run(t, testStatuslineOptions(time.Now())))
}

// --- E. Rendering ---

// TestStatusline_NoColorIsByteExact keeps escape bytes out when color is off
// and in when it is on. Failure prevented: NO_COLOR ignored, or escape codes
// garbling a terminal that asked for none.
func TestStatusline_NoColorIsByteExact(t *testing.T) {
	now := time.Now()
	f := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{
		LastAction: "loaded team context", LastActionAt: now.Add(-time.Minute),
		LastWhisperFrom: "160305533-ryan", LastWhisperTopic: "wip", LastWhisperContent: "auth \x1b[31mred\x1b[0m",
		LastWhisperAt: now.Add(-time.Minute),
	}))

	plain := f.run(t, testStatuslineOptions(now))
	assert.Equal(t, "● ox  loaded team context · whisper from ryan [wip] auth [31mred[0m 1m\n", plain)
	assert.NotContains(t, plain, "\x1b")

	opts := testStatuslineOptions(now)
	opts.color = true
	colored := f.run(t, opts)
	assert.Contains(t, colored, "\x1b[")
	assert.Equal(t, plain, ansi.Strip(colored), "color only adds escape sequences")
}

// TestStatusline_FitsColumns keeps the chained line plus ox within COLUMNS.
// Failure prevented: ox pushes the coworker's own line off the bar.
func TestStatusline_FitsColumns(t *testing.T) {
	now := time.Now()
	f := newStatuslineFixture(t)
	f.setUserStatusline(t, "printf 'main ✓ 3 files'")
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{
		LastAction: "posted murmur", LastActionAt: now,
		LastWhisperFrom: "ryan", LastWhisperContent: strings.Repeat("long whisper ", 20), LastWhisperAt: now,
	}))
	opts := testStatuslineOptions(now)
	opts.width = 60
	got := strings.TrimSuffix(f.run(t, opts), "\n")
	assert.True(t, strings.HasPrefix(got, "main ✓ 3 files  ● ox  posted murmur"), got)
	assert.LessOrEqual(t, ansi.StringWidth(got), 60)
}

// TestComposeStatusline_RecordingMark appends the dim recording mark only when
// it fits. Failure prevented: "● rec" crowding out the activity itself.
func TestComposeStatusline_RecordingMark(t *testing.T) {
	line := oxStatusline{seg: oxactivity.Segment{Action: "posted murmur"}, recording: true}
	opts := statuslineOptions{}
	assert.Equal(t, "● ox  posted murmur  ● rec\n", composeStatusline("", line, true, opts))
	opts.width = 8
	assert.Equal(t, "● ox  p…\n", composeStatusline("", line, true, opts))
}

// TestIsStatuslineFastPath routes only the bare command around cobra, so
// `ox statusline --help` still gets help. Failure prevented: help text in the bar.
func TestIsStatuslineFastPath(t *testing.T) {
	assert.True(t, isStatuslineFastPath([]string{"statusline"}))
	assert.False(t, isStatuslineFastPath([]string{"statusline", "--help"}))
	assert.False(t, isStatuslineFastPath([]string{"status"}))
	assert.False(t, isStatuslineFastPath(nil))
}

// TestStatusline_LatencyBudget measures one refresh in-process against a
// Ledger holding a day of murmurs. Failure prevented: the bar lagging every
// keystroke. Process start-up (~30ms for the ox binary) is measured by the
// slow-tier binary test.
func TestStatusline_LatencyBudget(t *testing.T) {
	now := time.Now()
	f := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{LastAction: "posted murmur", LastActionAt: now}))
	for i := range 200 {
		f.writeMurmur(t, f.ledgerDir, ledger.MurmurFile{
			ID: fmt.Sprintf("m%03d", i), AgentID: "OxTm09", PrincipalID: "ryan", Topic: "wip",
			Content: "x", Timestamp: now.Add(-time.Duration(i) * 7 * time.Minute),
		})
	}

	measure := func(prep func()) time.Duration {
		var worst time.Duration
		for range 5 {
			prep()
			start := time.Now()
			_ = f.run(t, testStatuslineOptions(time.Now()))
			worst = max(worst, time.Since(start))
		}
		return worst
	}
	cold := measure(func() { _ = os.Remove(statuslineMurmurCachePath(f.root)) })
	warm := measure(func() {})
	t.Logf("statusline in-process: cold (murmur scan + identity) %v, warm (cached) %v", cold, warm)
	assert.Less(t, warm, 20*time.Millisecond)
	// cold runs git for the coworker's own identity (once per 10 minutes); keep slack for loaded CI
	assert.Less(t, cold, 150*time.Millisecond)
}
