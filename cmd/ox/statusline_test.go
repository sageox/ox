package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
)

// statuslineFixture is an initialized SageOx project with a primed Claude
// Code session and isolated user and Claude Code config — everything
// `ox statusline` reads.
type statuslineFixture struct {
	root       string
	ledgerDir  string
	teamDir    string
	claudeDir  string
	agentID    string
	sessionID  string
	userConfig string
}

// key is the state key the real hook and `ox statusline` agree on for this
// fixture's harness session.
func (f *statuslineFixture) key() string { return oxactivity.StateKey(f.agentID, f.sessionID) }

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

func (f *statuslineFixture) setUserStatusline(t *testing.T, command string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": command},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.claudeDir, "settings.json"), data, 0o644))
}

func testStatuslineOptions() statuslineOptions {
	return statuslineOptions{
		color:        false,
		chainTimeout: 2 * time.Second,
		getenv:       os.Getenv,
		stdinWait:    time.Second,
	}
}

// startRecording starts a live recording for the fixture's AI coworker, with
// the user cache (where recording state lives) isolated to this test.
func (f *statuslineFixture) startRecording(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	_, err := session.StartRecording(f.root, session.StartRecordingOptions{AgentID: f.agentID, AdapterName: "claude-code"})
	require.NoError(t, err)
}

// seed stores counts for this fixture's harness session, as hooks would.
func (f *statuslineFixture) seed(t *testing.T, c oxactivity.Counts) {
	t.Helper()
	require.NoError(t, oxactivity.SaveState(f.root, f.key(), oxactivity.State{
		Discussions: c.Discussions, Plans: c.Plans, Docs: c.Docs, Sessions: c.Sessions, Murmurs: c.Murmurs, Used: c.Used,
	}))
}

// --- A. What influenced this session ---

// TestStatusline_CountsLine shows the seeded counts for this session, byte
// for byte. Failure prevented: the bar shows the wrong numbers, wrong nouns,
// or anything but the bare mark before anything was used.
func TestStatusline_CountsLine(t *testing.T) {
	tests := []struct {
		name   string
		counts *oxactivity.Counts
		want   string
	}{
		{"no state yet", nil, "● ox\n"},
		{"zero counts", &oxactivity.Counts{}, "● ox\n"},
		{"mixed", &oxactivity.Counts{Discussions: 3, Plans: 1, Murmurs: 1}, "● ox  3 discussions · 1 plan · 1 murmur\n"},
		{"every bucket", &oxactivity.Counts{Discussions: 1, Plans: 2, Docs: 1, Sessions: 2, Murmurs: 4}, "● ox  1 discussion · 2 plans · 1 doc · 2 sessions · 4 murmurs\n"},
		{"used after the kinds", &oxactivity.Counts{Discussions: 3, Plans: 1, Used: 2}, "● ox  3 discussions · 1 plan · 2 used\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			if tt.counts != nil {
				f.seed(t, *tt.counts)
			}
			assert.Equal(t, tt.want, f.run(t, testStatuslineOptions()))
		})
	}
}

// TestStatusline_CountsAreNotAFeed keeps a count on the bar regardless of age.
// Failure prevented: the readout decaying like the old 10-minute activity
// feed, so value the session got disappears while the coworker is still in it.
func TestStatusline_CountsAreNotAFeed(t *testing.T) {
	f := newStatuslineFixture(t)
	require.NoError(t, oxactivity.SaveState(f.root, f.key(), oxactivity.State{
		Plans: 1, UpdatedAt: time.Now().Add(-6 * time.Hour),
	}))
	assert.Equal(t, "● ox  1 plan\n", f.run(t, testStatuslineOptions()))
}

// TestStatusline_UnknownSessionStillShowsMark covers a status line refreshed
// before prime wrote its marker. Failure prevented: an empty or error bar in
// the first seconds of every session.
func TestStatusline_UnknownSessionStillShowsMark(t *testing.T) {
	f := newStatuslineFixture(t)
	f.sessionID = "never-primed"
	assert.Equal(t, "● ox\n", f.run(t, testStatuslineOptions()))
}

// TestStatusline_PerSessionState shows each harness session only its own
// counts, even when both sessions share one ox agent ID in one repo.
// Failure prevented: plans another Claude Code window used (or a finished
// session's) inflating this window's bar.
func TestStatusline_PerSessionState(t *testing.T) {
	f := newStatuslineFixture(t)
	other := "some-other-window"
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, other, "plan", "p1"))
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, other, "plan", "p2"))
	require.NoError(t, oxactivity.SaveState(f.root, f.agentID, oxactivity.State{Docs: 5}))

	assert.Equal(t, "● ox\n", f.run(t, testStatuslineOptions()), "neither another window's nor the agent-wide state shows here")

	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "discussion", "cnv_1"))
	assert.Equal(t, "● ox  1 discussion\n", f.run(t, testStatuslineOptions()))
	assert.Equal(t, 2, oxactivity.LoadState(f.root, oxactivity.StateKey(f.agentID, other)).Plans, "the other window keeps its own")
}

// TestStatusline_FetchThenUse shows one fetched-and-used item as one item and
// one use. Failure prevented: the hook's retrieval ref and the use writer's
// ref disagreeing, so a discussion pulled and then acted on reads
// "2 discussions", or team context read once and cited once reads "2 docs".
func TestStatusline_FetchThenUse(t *testing.T) {
	f := newStatuslineFixture(t)
	const cnv = "cnv_019ffe10-0000-7000-8000-000000000011"
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "discussion", cnv))
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "team-context", oxactivity.TeamContextRef))
	assert.Equal(t, "● ox  1 discussion · 1 doc\n", f.run(t, testStatuslineOptions()))

	require.NoError(t, oxactivity.RecordUse(f.root, f.agentID, f.sessionID, "discussion", "https://sageox.ai/c/"+cnv))
	require.NoError(t, oxactivity.RecordUse(f.root, f.agentID, f.sessionID, "team-context", "team-ctx/acme"))
	assert.Equal(t, "● ox  1 discussion · 1 doc · 2 used\n", f.run(t, testStatuslineOptions()))
}

// TestStatusline_UsedFitsLast keeps "N used" when the bar narrows, after every
// kind count and before "● rec" gives way. Failure prevented: the most
// informative number dropping first on a narrow terminal.
func TestStatusline_UsedFitsLast(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1, Used: 2})
	f.startRecording(t)

	tests := []struct {
		width int
		want  string
	}{
		{0, "● ox  3 discussions · 1 plan · 2 used  ● rec"},
		{40, "● ox  3 discussions · 2 used  ● rec"},
		{25, "● ox  2 used  ● rec"},
		{12, "● ox  ● rec"},
	}
	for _, tt := range tests {
		opts := testStatuslineOptions()
		opts.width = tt.width
		got := strings.TrimSuffix(f.run(t, opts), "\n")
		assert.Equal(t, tt.want, got, "width %d", tt.width)
	}
}

// --- C. Off switch, project detection, failure paths ---

// TestStatusline_OffPrintsOnlyChained honors `ox config set statusline off`.
// Failure prevented: the setting is ignored and ox keeps drawing in the bar,
// or turning ox off also blanks the coworker's own status line.
func TestStatusline_OffPrintsOnlyChained(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Plans: 2})
	require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))

	assert.Equal(t, "", f.run(t, testStatuslineOptions()), "off with no chained command prints nothing")

	f.setUserStatusline(t, "printf 'my line'")
	assert.Equal(t, "my line\n", f.run(t, testStatuslineOptions()))
}

// TestStatusline_OutsideSageoxProject prints nothing of ox's outside an
// initialized project. Failure prevented: a "● ox" mark in repos that do not
// use SageOx (e.g. after `ox uninstall` left the setting behind).
func TestStatusline_OutsideSageoxProject(t *testing.T) {
	f := newStatuslineFixture(t)
	f.root = t.TempDir()
	assert.Equal(t, "", f.run(t, testStatuslineOptions()))
	f.setUserStatusline(t, "printf 'mine'")
	assert.Equal(t, "mine\n", f.run(t, testStatuslineOptions()))
}

// TestStatusline_GarbageInputStillShowsMark covers unreadable stdin and a
// corrupt state file. Failure prevented: an error message or a blank bar.
func TestStatusline_GarbageInputStillShowsMark(t *testing.T) {
	f := newStatuslineFixture(t)
	t.Chdir(f.root)
	statePath := oxactivity.StatePath(f.root, f.key())
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, os.WriteFile(statePath, []byte("{not json"), 0o600))

	var out bytes.Buffer
	runStatusline(strings.NewReader("this is not json"), &out, testStatuslineOptions())
	assert.Equal(t, "● ox\n", out.String())

	out.Reset()
	runStatusline(bytes.NewReader(f.stdin()), &out, testStatuslineOptions())
	assert.Equal(t, "● ox\n", out.String())
}

// --- D. Chaining the coworker's own status line ---

// TestStatusline_ChainsUserCommand runs the coworker's user-level statusLine
// (hidden by ox's project setting) and puts it on its own line beneath ox.
// Failure prevented: installing ox wipes out, or squeezes, a coworker's
// custom status bar.
func TestStatusline_ChainsUserCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"single line", "printf 'main ✓'", "● ox\nmain ✓\n"},
		{"trailing newline", "echo 'main ✓'", "● ox\nmain ✓\n"},
		{"multi-line keeps every line", "printf 'line one\\nline two\\n'", "● ox\nline one\nline two\n"},
		{"receives the same stdin", `sed -n 's/.*"session_id":"\([^"]*\)".*/sid=\1/p'`, "● ox\nsid=SESSION\n"},
		{"failing command keeps its output", "printf 'partial'; exit 3", "● ox\npartial\n"},
		{"ox's own command is not chained", "ox statusline", "● ox\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			f.setUserStatusline(t, tt.command)
			want := strings.ReplaceAll(tt.want, "SESSION", f.sessionID)
			assert.Equal(t, want, f.run(t, testStatuslineOptions()))
		})
	}
}

// TestStatusline_ChainedCommandTimeout drops a chained command that runs too
// long and still prints ox's line promptly. Failure prevented: a slow user
// script freezes the status bar.
func TestStatusline_ChainedCommandTimeout(t *testing.T) {
	f := newStatuslineFixture(t)
	f.setUserStatusline(t, "sleep 5; echo late")
	opts := testStatuslineOptions()
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
	assert.Equal(t, "● ox\n", f.run(t, testStatuslineOptions()))
}

// --- E. Rendering ---

// TestStatusline_NoColorIsByteExact keeps escape bytes out when color is off
// and in when it is on. Failure prevented: NO_COLOR ignored, or escape codes
// garbling a terminal that asked for none.
func TestStatusline_NoColorIsByteExact(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1})

	plain := f.run(t, testStatuslineOptions())
	assert.Equal(t, "● ox  3 discussions · 1 plan\n", plain)
	assert.NotContains(t, plain, "\x1b")

	opts := testStatuslineOptions()
	opts.color = true
	colored := f.run(t, opts)
	assert.Contains(t, colored, "\x1b[")
	assert.Equal(t, plain, ansi.Strip(colored), "color only adds escape sequences")
}

// TestStatusline_FitsColumns keeps ox's own line within COLUMNS by dropping
// the smallest count first, keeps "● rec", and leaves the chained line below
// it untouched. Failure prevented: ox's line wraps, a count is cut mid-word,
// or the recording mark disappears before a count does.
func TestStatusline_FitsColumns(t *testing.T) {
	f := newStatuslineFixture(t)
	f.setUserStatusline(t, "printf 'main ✓ 3 files'")
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1, Docs: 2, Murmurs: 1})
	f.startRecording(t)

	tests := []struct {
		width int
		want  string
	}{
		{0, "● ox  3 discussions · 1 plan · 2 docs · 1 murmur  ● rec"},
		// 46 + 7 for "  ● rec": the rightmost 1 (murmur) goes first
		{53, "● ox  3 discussions · 1 plan · 2 docs  ● rec"},
		{40, "● ox  3 discussions · 2 docs  ● rec"},
		{30, "● ox  3 discussions  ● rec"},
		{12, "● ox  ● rec"},
		{8, "● ox"},
	}
	for _, tt := range tests {
		opts := testStatuslineOptions()
		opts.width = tt.width
		lines := strings.Split(strings.TrimSuffix(f.run(t, opts), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, tt.want, lines[0], "width %d", tt.width)
		if tt.width > 0 {
			assert.LessOrEqual(t, ansi.StringWidth(lines[0]), tt.width)
		}
		assert.Equal(t, "main ✓ 3 files", lines[1])
	}
}

// TestComposeStatusline_IdleKeepsMarks shows "● ox  ● rec" when nothing has
// happened. Failure prevented: the bar going blank between ox calls, which
// reads as ox having switched off.
func TestComposeStatusline_IdleKeepsMarks(t *testing.T) {
	idle := oxStatusline{recording: true}
	assert.Equal(t, "● ox  ● rec\n", composeStatusline("", idle, true, statuslineOptions{}))
	assert.Equal(t, "● ox  ● rec\nme@host ~/x\n", composeStatusline("me@host ~/x\n", idle, true, statuslineOptions{}))
	assert.Equal(t, "● ox\n", composeStatusline("", oxStatusline{}, true, statuslineOptions{}))
}

// TestComposeStatusline_RecordingMark reserves "● rec" before the counts and
// drops it only when even the bare mark and it cannot share the width.
// Failure prevented: a long readout pushing the recording signal off the bar.
func TestComposeStatusline_RecordingMark(t *testing.T) {
	line := oxStatusline{counts: oxactivity.Counts{Plans: 1}, recording: true}
	opts := statuslineOptions{}
	assert.Equal(t, "● ox  1 plan  ● rec\n", composeStatusline("", line, true, opts))
	opts.width = 18
	assert.Equal(t, "● ox  ● rec\n", composeStatusline("", line, true, opts), "the count gives way first")
	opts.width = 8
	assert.Equal(t, "● ox\n", composeStatusline("", line, true, opts))
}

// TestComposeStatusline_RecordingDotIsRed colors only the dot red, keeping the
// label dim. Failure prevented: recording that doesn't read as recording.
func TestComposeStatusline_RecordingDotIsRed(t *testing.T) {
	line := oxStatusline{counts: oxactivity.Counts{Plans: 1}, recording: true}
	got := composeStatusline("", line, true, statuslineOptions{color: true})
	assert.Contains(t, got, statuslineRecDotStyle.Render("●")+statuslineDimStyle.Render(" rec"))
	assert.Equal(t, "● ox  1 plan  ● rec\n", ansi.Strip(got))
}

// TestIsStatuslineFastPath routes only the bare command around cobra, so
// `ox statusline --help` still gets help. Failure prevented: help text in the bar.
func TestIsStatuslineFastPath(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"statusline"}, true},
		{[]string{"statusline", "--json"}, true},
		{[]string{"statusline", "--help"}, false},
		{[]string{"statusline", "--json", "x"}, false},
		{[]string{"--json", "statusline"}, false},
		{[]string{"statusline", "--json=true"}, false},
		{[]string{"status"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isStatuslineFastPath(tt.args), "%q", tt.args)
	}
}

// TestStatusline_LatencyBudget measures one warm refresh in-process. Failure
// prevented: the bar lagging every keystroke. Process start-up (~30ms for the
// ox binary) is measured by the slow-tier binary test.
func TestStatusline_LatencyBudget(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1, Docs: 2, Sessions: 1, Murmurs: 4})
	f.startRecording(t)

	_ = f.run(t, testStatuslineOptions()) // warm the page cache
	var worst time.Duration
	for range 5 {
		start := time.Now()
		got := f.run(t, testStatuslineOptions())
		worst = max(worst, time.Since(start))
		require.Equal(t, "● ox  3 discussions · 1 plan · 2 docs · 1 session · 4 murmurs  ● rec\n", got)
	}
	t.Logf("statusline in-process (warm): worst of 5 %v", worst)
	assert.Less(t, worst, 20*time.Millisecond)
}

// --- F. `ox statusline --json` for the Claude desktop app band ---

// runJSON runs JSON mode with the given stdin and returns the raw output.
func (f *statuslineFixture) runJSON(t *testing.T, stdin []byte, opts statuslineOptions) string {
	t.Helper()
	var out bytes.Buffer
	runStatuslineJSON(bytes.NewReader(stdin), &out, opts)
	return out.String()
}

// decodeStatuslineJSON asserts out is exactly one JSON document of the
// documented shape, ending in one newline, and returns it.
func decodeStatuslineJSON(t *testing.T, out string) statuslineJSON {
	t.Helper()
	require.True(t, strings.HasSuffix(out, "}\n"), "one trailing newline: %q", out)
	require.False(t, strings.HasSuffix(out, "\n\n"), "exactly one trailing newline: %q", out)
	assert.NotContains(t, out, "\x1b", "JSON is never colored")
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got statuslineJSON
	require.NoError(t, dec.Decode(&got), out)
	assert.False(t, dec.More(), "exactly one JSON document: %q", out)
	return got
}

// envWith returns a getenv that answers CLAUDE_CODE_SESSION_ID with sid and
// everything else from the real environment.
func envWith(sid string) func(string) string {
	return func(k string) string {
		if k == envClaudeCodeSessionID {
			return sid
		}
		return os.Getenv(k)
	}
}

// TestRenderStatuslineJSON_Matrix covers what the desktop band reads in each
// state. Failure prevented: the band showing ox where the text bar would not
// (setting off, a non-SageOx repo), or dropping counts and "● rec" where it
// would.
func TestRenderStatuslineJSON_Matrix(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *statuslineFixture)
		want  statuslineJSON
	}{
		{
			name: "on: counts and recording",
			setup: func(t *testing.T, f *statuslineFixture) {
				require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "discussion", "cnv_019ffe10-0000-7000-8000-000000000012"))
				require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "plan", "p1"))
				require.NoError(t, oxactivity.RecordUse(f.root, f.agentID, f.sessionID, "discussion", "https://sageox.ai/c/cnv_019ffe10-0000-7000-8000-000000000012"))
				f.startRecording(t)
			},
			want: statuslineJSON{Show: true, Counts: oxactivity.Counts{Discussions: 1, Plans: 1, Used: 1}, Recording: true},
		},
		{
			name: "on: idle session shows the bare mark",
			want: statuslineJSON{Show: true},
		},
		{
			name: "statusline setting off",
			setup: func(t *testing.T, f *statuslineFixture) {
				f.seed(t, oxactivity.Counts{Plans: 2})
				require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
			},
			want: statuslineJSON{},
		},
		{
			name: "outside a SageOx project",
			setup: func(t *testing.T, f *statuslineFixture) {
				f.seed(t, oxactivity.Counts{Plans: 2})
				f.root = t.TempDir()
			},
			want: statuslineJSON{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatuslineFixture(t)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			got := decodeStatuslineJSON(t, f.runJSON(t, f.stdin(), testStatuslineOptions()))
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRenderStatuslineJSON_ExactShape pins the wire shape the desktop plugin
// parses. Failure prevented: a renamed or re-cased key silently blanking the
// band, or zero counts arriving as noise the plugin must filter.
func TestRenderStatuslineJSON_ExactShape(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1, Used: 2})
	assert.Equal(t, `{
  "show": true,
  "counts": {
    "discussions": 3,
    "plans": 1,
    "used": 2
  },
  "recording": false
}
`, f.runJSON(t, f.stdin(), testStatuslineOptions()))

	f.root = t.TempDir()
	assert.Equal(t, "{\n  \"show\": false,\n  \"counts\": {},\n  \"recording\": false\n}\n",
		f.runJSON(t, f.stdin(), testStatuslineOptions()))
}

// TestRenderStatuslineJSON_SessionFromEnv reads the session from
// CLAUDE_CODE_SESSION_ID when stdin is empty, as from the desktop plugin host.
// Failure prevented: the desktop band stuck on the bare mark because the host
// sends no status-line JSON.
func TestRenderStatuslineJSON_SessionFromEnv(t *testing.T) {
	f := newStatuslineFixture(t)
	t.Chdir(f.root) // no stdin cwd either: the plugin runs ox in the project
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "plan", "p1"))

	opts := testStatuslineOptions()
	opts.getenv = envWith("")
	assert.Equal(t, statuslineJSON{Show: true}, decodeStatuslineJSON(t, f.runJSON(t, nil, opts)),
		"without the env var the session is unknown")

	opts.getenv = envWith(f.sessionID)
	assert.Equal(t, statuslineJSON{Show: true, Counts: oxactivity.Counts{Plans: 1}},
		decodeStatuslineJSON(t, f.runJSON(t, nil, opts)))
}

// TestRenderStatuslineJSON_StdinSessionWinsOverEnv prefers the session Claude
// Code names on stdin. Failure prevented: an inherited CLAUDE_CODE_SESSION_ID
// from a parent session putting another window's counts on this band.
func TestRenderStatuslineJSON_StdinSessionWinsOverEnv(t *testing.T) {
	f := newStatuslineFixture(t)
	other := "some-other-window"
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: f.agentID, AgentSessionID: other, PrimedAt: time.Now()}))
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, other, "doc", "d1"))
	require.NoError(t, oxactivity.RecordInfluence(f.root, f.agentID, f.sessionID, "plan", "p1"))

	opts := testStatuslineOptions()
	opts.getenv = envWith(other)
	assert.Equal(t, statuslineJSON{Show: true, Counts: oxactivity.Counts{Plans: 1}},
		decodeStatuslineJSON(t, f.runJSON(t, f.stdin(), opts)))
}

// TestRenderStatuslineJSON_NeverRunsChainedCommand keeps the coworker's own
// statusLine command out of JSON mode, on and off. Failure prevented: the
// user's script running (side effects, latency) on every desktop band refresh,
// or its text corrupting the JSON document.
func TestRenderStatuslineJSON_NeverRunsChainedCommand(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "on", true: "off"}[off], func(t *testing.T) {
			f := newStatuslineFixture(t)
			ran := filepath.Join(t.TempDir(), "ran")
			f.setUserStatusline(t, "touch '"+ran+"'; printf 'mine'")
			if off {
				require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
			}

			out := f.runJSON(t, f.stdin(), testStatuslineOptions())
			decodeStatuslineJSON(t, out)
			assert.NotContains(t, out, "mine")
			assert.NoFileExists(t, ran, "the chained command must not run in JSON mode")

			// the same fixture does chain in text mode, so the probe works
			if !off {
				assert.Equal(t, "● ox\nmine\n", f.run(t, testStatuslineOptions()))
				assert.FileExists(t, ran)
			}
		})
	}
}

// TestStatuslineCmd_JSONFlag routes `ox statusline --json` through cobra to
// JSON mode. Failure prevented: the flag parsing but printing the text line,
// which the desktop plugin cannot parse.
func TestStatuslineCmd_JSONFlag(t *testing.T) {
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Plans: 1})
	require.NoError(t, statuslineCmd.Flags().Set("json", "true"))
	t.Cleanup(func() {
		_ = statuslineCmd.Flags().Set("json", "false")
		statuslineCmd.SetIn(nil)
		statuslineCmd.SetOut(nil)
	})
	var out bytes.Buffer
	statuslineCmd.SetIn(bytes.NewReader(f.stdin()))
	statuslineCmd.SetOut(&out)
	require.NoError(t, statuslineCmd.RunE(statuslineCmd, nil))
	assert.Equal(t, statuslineJSON{Show: true, Counts: oxactivity.Counts{Plans: 1}}, decodeStatuslineJSON(t, out.String()))
}

// swapStatuslineStdio points main's fast path at files: stdin holds in, and
// the returned func reads what was written to the real stdout.
func swapStatuslineStdio(t *testing.T, in []byte) func() string {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "stdin")
	require.NoError(t, os.WriteFile(inPath, in, 0o600))
	stdin, err := os.Open(inPath)
	require.NoError(t, err)
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	require.NoError(t, err)
	prevIn, prevOut := os.Stdin, statuslineStdout
	os.Stdin, statuslineStdout = stdin, stdout
	t.Cleanup(func() {
		os.Stdin, statuslineStdout = prevIn, prevOut
		_ = stdin.Close()
		_ = stdout.Close()
	})
	return func() string {
		b, err := os.ReadFile(stdout.Name())
		require.NoError(t, err)
		return string(b)
	}
}

// TestRunStatuslineFastPath_JSONAndText drives main's entry point, which
// skips cobra: `ox statusline --json` must print the JSON document and plain
// `ox statusline` the text line. Failure prevented: the desktop band's
// `ox statusline --json` getting the text line it cannot parse (or the
// terminal bar getting JSON) because the fast path ignored the flag.
func TestRunStatuslineFastPath_JSONAndText(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		f := newStatuslineFixture(t)
		f.seed(t, oxactivity.Counts{Plans: 1, Used: 1})
		read := swapStatuslineStdio(t, f.stdin())
		runStatuslineFastPath([]string{"statusline", "--json"})
		assert.Equal(t, statuslineJSON{Show: true, Counts: oxactivity.Counts{Plans: 1, Used: 1}}, decodeStatuslineJSON(t, read()))
	})
	t.Run("text", func(t *testing.T) {
		f := newStatuslineFixture(t)
		f.seed(t, oxactivity.Counts{Plans: 1, Used: 1})
		read := swapStatuslineStdio(t, f.stdin())
		t.Setenv("NO_COLOR", "1")
		runStatuslineFastPath([]string{"statusline"})
		assert.Equal(t, "● ox  1 plan · 1 used\n", ansi.Strip(read()))
	})
}

// TestRunStatuslineJSON_WriteErrorNeverFails: a closed stdout (the desktop
// host gave up on the run) is swallowed, like the text line's. Failure
// prevented: a panic or error exit the plugin host would surface.
func TestRunStatuslineJSON_WriteErrorNeverFails(t *testing.T) {
	f := newStatuslineFixture(t)
	assert.NotPanics(t, func() {
		runStatuslineJSON(bytes.NewReader(f.stdin()), failingWriter{}, testStatuslineOptions())
	})
}
