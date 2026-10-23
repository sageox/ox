package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
	claude "github.com/sageox/ox/internal/hooks/claude"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/theme"
)

// `ox statusline` draws ox's live activity in Claude Code's status bar — the
// only Claude Code surface that renders color. It runs on every status-line
// refresh, so it reads local files only: no network, no daemon IPC.

const (
	// statuslineActionTTL is how long a recorded action stays on the line.
	statuslineActionTTL = 10 * time.Minute
	// statuslineWhisperTTL is how long a teammate's murmur stays on the line.
	statuslineWhisperTTL = time.Hour
	// statuslineChainTimeout bounds the coworker's own status-line command.
	statuslineChainTimeout = 300 * time.Millisecond
	// statuslineMurmurCacheTTL is how long a murmur scan is reused.
	statuslineMurmurCacheTTL = 10 * time.Second
	// statuslineSelfCacheTTL is how long the coworker's own principal is reused.
	statuslineSelfCacheTTL = 10 * time.Minute
	// statuslineStdinWait bounds the wait for Claude Code's JSON on stdin.
	statuslineStdinWait = 200 * time.Millisecond
	// statuslineMaxInput caps how much stdin is read.
	statuslineMaxInput = 1 << 20
	// statuslineCachedMurmurs is how many teammate murmurs the cache keeps, so
	// filtering out the reading AI coworker's own still leaves one.
	statuslineCachedMurmurs = 5
	// envStatuslineChained marks a chained command's environment, so a user
	// command that itself calls `ox statusline` does not chain again.
	envStatuslineChained = "OX_STATUSLINE_CHAINED"
)

// main's init() replaces os.Stdout with an ANSI-stripping pipe and sets
// NO_COLOR whenever stdout is not a terminal — always true for a status line.
// Package-level variables initialize before any init() runs, so these hold the
// real stdout and the coworker's own NO_COLOR.
var (
	statuslineStdout                    = os.Stdout
	statuslineNoColor, statuslineHasNoC = os.LookupEnv("NO_COLOR")
)

var statuslineCmd = &cobra.Command{
	Use:    "statusline",
	Short:  "Print ox's activity line for the Claude Code status bar",
	Hidden: true,
	Args:   cobra.NoArgs,
	Long: `Print ox's live activity line for the Claude Code status bar.

Claude Code runs this command on every status-line refresh, piping its
status-line JSON (session_id, cwd, workspace) on stdin. ox prints one line:

  ● ox  posted murmur · whisper from ryan [wip] Refactoring auth… 3m

It shows what ox last did for this AI coworker (for 10 minutes) and the
newest teammate murmur (for an hour). It reads local files only — never the
network or the ox daemon — and never prints an error into the bar.

` + "`ox init`" + ` and ` + "`ox doctor`" + ` install it as the statusLine in the project's
.claude/settings.json. A statusLine already set there is never replaced. If
your own ~/.claude/settings.json sets a statusLine, ox runs it first and
appends its line after it, since the project setting would otherwise hide
yours.

Turn it off with: ox config set statusline off`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		runStatusline(cmd.InOrStdin(), cmd.OutOrStdout(), defaultStatuslineOptions())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statuslineCmd)
}

// isStatuslineFastPath reports whether argv is exactly `ox statusline`, which
// main runs before any command setup: the root pre-run pings the daemon and
// resolves feature flags, which alone would blow the status line's budget.
func isStatuslineFastPath(args []string) bool {
	return len(args) == 1 && args[0] == "statusline"
}

// runStatuslineFastPath is main's entry point for `ox statusline`.
func runStatuslineFastPath() {
	if statuslineHasNoC {
		_ = os.Setenv("NO_COLOR", statuslineNoColor)
	} else {
		_ = os.Unsetenv("NO_COLOR")
	}
	runStatusline(os.Stdin, statuslineStdout, defaultStatuslineOptions())
}

// statuslineOptions carries the environment, so tests can drive it.
type statuslineOptions struct {
	now          time.Time
	color        bool
	width        int // 0 = unlimited
	chainTimeout time.Duration
	getenv       func(string) string
	stdinWait    time.Duration
}

func defaultStatuslineOptions() statuslineOptions {
	width, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	return statuslineOptions{
		now:          time.Now(),
		color:        oxactivity.ColorAllowed(),
		width:        max(width, 0),
		chainTimeout: statuslineChainTimeout,
		getenv:       os.Getenv,
		stdinWait:    statuslineStdinWait,
	}
}

// statuslineInput is the part of Claude Code's status-line JSON ox reads.
type statuslineInput struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	Workspace      struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
}

// runStatusline writes the status line. It never fails: anything that goes
// wrong is logged at debug level and the line degrades, at worst to "● ox".
func runStatusline(stdin io.Reader, stdout io.Writer, opts statuslineOptions) {
	raw := readStatuslineStdin(stdin, opts.stdinWait)
	_, _ = io.WriteString(stdout, renderStatusline(raw, opts))
}

// readStatuslineStdin reads Claude Code's JSON, giving up after wait so a
// terminal or a never-closed pipe cannot hang the status bar.
func readStatuslineStdin(stdin io.Reader, wait time.Duration) []byte {
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
	}
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(stdin, statuslineMaxInput))
		done <- b
	}()
	select {
	case b := <-done:
		return b
	case <-time.After(wait):
		return nil
	}
}

// renderStatusline returns the full status-line output for Claude Code's
// JSON: the coworker's chained line(s) first, then ox's on the last line.
func renderStatusline(raw []byte, opts statuslineOptions) string {
	var in statuslineInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			slog.Debug("statusline: bad stdin JSON", "err", err)
		}
	}
	dir := firstNonEmpty(in.Workspace.CurrentDir, in.CWD)
	if dir == "" {
		dir, _ = os.Getwd()
	}

	// The coworker's own command runs while ox builds its segment.
	chained := make(chan string, 1)
	go func() { chained <- runChainedStatusline(raw, dir, opts) }()

	line, show := buildOxStatusline(in, dir, opts)
	return composeStatusline(<-chained, line, show, opts)
}

// oxStatusline is ox's part of the line before it is fitted to a width.
type oxStatusline struct {
	seg       oxactivity.Segment
	recording bool
}

// buildOxStatusline gathers what ox shows. show is false when the status line
// is off or dir is not in a SageOx project. A panic still shows "● ox".
func buildOxStatusline(in statuslineInput, dir string, opts statuslineOptions) (line oxStatusline, show bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("statusline: panic", "panic", r)
			line, show = oxStatusline{}, true
		}
	}()

	if cfg, err := config.LoadUserConfig(); err == nil && cfg != nil && !cfg.IsStatuslineEnabled() {
		return oxStatusline{}, false
	}
	root := statuslineProjectRoot(dir)
	if root == "" {
		return oxStatusline{}, false
	}

	agentID := ""
	if marker, err := ReadSessionMarker(in.SessionID); err != nil {
		slog.Debug("statusline: read session marker", "err", err)
	} else if marker != nil {
		agentID = marker.AgentID
	}

	var state oxactivity.State
	if agentID != "" {
		state = oxactivity.LoadState(root, agentID)
	}
	if state.LastActionAt.IsZero() || opts.now.Sub(state.LastActionAt) > statuslineActionTTL {
		state.LastAction, state.LastActionAt = "", time.Time{}
	}
	if state.LastWhisperAt.IsZero() || opts.now.Sub(state.LastWhisperAt) > statuslineWhisperTTL {
		state.LastWhisperFrom, state.LastWhisperTopic, state.LastWhisperContent = "", "", ""
		state.LastWhisperAt = time.Time{}
	}
	if m, ok := latestTeammateMurmur(root, agentID, opts.now); ok && m.Timestamp.After(state.LastWhisperAt) {
		state.LastWhisperFrom = m.PrincipalID
		if state.LastWhisperFrom == "" {
			state.LastWhisperFrom = m.AgentID
		}
		state.LastWhisperTopic = m.Topic
		state.LastWhisperContent = m.Content
		state.LastWhisperAt = m.Timestamp
	}

	line.seg = state.Segment(opts.now)
	if agentID != "" {
		if rec, err := session.LoadRecordingStateForAgent(root, agentID); err == nil && rec != nil {
			line.recording = true
		}
	}
	return line, true
}

// statuslineRecMark is appended while the session is being recorded.
const statuslineRecMark = "  ● rec"

var (
	statuslineDimStyle    = lipgloss.NewStyle().Foreground(theme.AdaptTo(colorprofile.ANSI256, theme.ColorDim))
	statuslineRecDotStyle = lipgloss.NewStyle().Foreground(ansi.IndexedColor(196)) // theme.AnsiError
)

// renderRecMark draws the recording mark with a red dot, the universal
// "recording" signal, and a dim label. It uses the palette's pure ANSI red:
// ColorError downsamples to a salmon that reads as orange in a status bar.
func renderRecMark() string {
	return "  " + statuslineRecDotStyle.Render("●") + statuslineDimStyle.Render(" rec")
}

// composeStatusline puts ox's segment on the chained output's last line.
func composeStatusline(chained string, line oxStatusline, show bool, opts statuslineOptions) string {
	chained = strings.TrimRight(chained, "\r\n")
	if !show {
		if chained == "" {
			return ""
		}
		return chained + "\n"
	}

	lines := []string{""}
	if chained != "" {
		lines = strings.Split(chained, "\n")
	}
	last := lines[len(lines)-1]
	sep := ""
	if chained != "" {
		sep = "  "
	}

	width := 0 // unlimited
	if opts.width > 0 {
		width = max(opts.width-ansi.StringWidth(last)-len(sep), ansi.StringWidth("● ox"))
	}
	rec := ""
	if line.recording && (width == 0 || width >= ansi.StringWidth("● ox")+ansi.StringWidth(statuslineRecMark)) {
		rec = statuslineRecMark
		if width > 0 {
			width -= ansi.StringWidth(rec)
		}
		if opts.color {
			rec = renderRecMark()
		}
	}

	lines[len(lines)-1] = last + sep + oxactivity.RenderWidth(line.seg, opts.color, width) + rec
	return strings.Join(lines, "\n") + "\n"
}

// statuslineProjectRoot walks up from dir to the initialized SageOx project,
// resolving symlinks the way hooks do so both name the same state file.
func statuslineProjectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		if config.IsInitialized(d) {
			if resolved, err := filepath.EvalSymlinks(d); err == nil {
				return resolved
			}
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// --- chaining the coworker's own status line ---

// userStatuslineCommand returns the statusLine command from the coworker's
// user-level Claude Code settings, or "" when there is none or it is ox's.
// Project settings outrank user settings, so ox's project statusLine would
// hide it; running it here keeps it visible.
func userStatuslineCommand(getenv func(string) string) string {
	dir := getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	cmd := strings.TrimSpace(s.StatusLine.Command)
	if cmd == "" || (s.StatusLine.Type != "" && s.StatusLine.Type != "command") || claude.IsOxStatuslineCommand(cmd) {
		return ""
	}
	return cmd
}

// runChainedStatusline runs the coworker's own status-line command with the
// same stdin and returns its output, or "" when there is none, it fails to
// start, or it runs past the timeout.
func runChainedStatusline(raw []byte, dir string, opts statuslineOptions) string {
	if opts.getenv(envStatuslineChained) != "" {
		return "" // already inside a chained command
	}
	command := userStatuslineCommand(opts.getenv)
	if command == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.chainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		cmd.Dir = dir
	}
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Env = append(os.Environ(), envStatuslineChained+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	// A grandchild can keep the pipe open after sh is killed; stop waiting.
	cmd.WaitDelay = 50 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		slog.Debug("statusline: chained command timed out", "timeout", opts.chainTimeout)
		return ""
	}
	if err != nil {
		slog.Debug("statusline: chained command failed", "err", err)
	}
	return out.String()
}

// --- teammate murmurs ---

// statuslineMurmur is one teammate murmur kept in the cache.
type statuslineMurmur struct {
	AgentID     string    `json:"agent_id,omitempty"`
	PrincipalID string    `json:"principal_id,omitempty"`
	Topic       string    `json:"topic,omitempty"`
	Content     string    `json:"content,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// statuslineMurmurCache is <root>/.sageox/cache/statusline/murmurs.json. It
// keeps the newest teammate murmurs (newest first) so frequent refreshes do
// not rescan the Ledger and Team Context checkouts. It is never a whisper
// cursor: reading murmurs here consumes nothing.
type statuslineMurmurCache struct {
	CheckedAt     time.Time          `json:"checked_at"`
	Murmurs       []statuslineMurmur `json:"murmurs"`
	SelfPrincipal string             `json:"self_principal,omitempty"`
	SelfAt        time.Time          `json:"self_at,omitzero"`
}

func statuslineMurmurCachePath(root string) string {
	return filepath.Join(root, ".sageox", "cache", "statusline", "murmurs.json")
}

// latestTeammateMurmur returns the newest murmur from someone other than the
// coworker reading the line, within statuslineWhisperTTL.
func latestTeammateMurmur(root, agentID string, now time.Time) (statuslineMurmur, bool) {
	path := statuslineMurmurCachePath(root)
	var c statuslineMurmurCache
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path is under the project's .sageox
		_ = json.Unmarshal(data, &c)
	}

	if age := now.Sub(c.CheckedAt); age < 0 || age > statuslineMurmurCacheTTL {
		// Resolving the coworker's own principal runs git; overlap it with the scan.
		var selfDone chan string
		if self := now.Sub(c.SelfAt); c.SelfAt.IsZero() || self < 0 || self > statuslineSelfCacheTTL {
			selfDone = make(chan string, 1)
			go func() { selfDone <- statuslineSelfPrincipal(root) }()
		}
		murmurs := scanRecentMurmurs(root, now)
		if selfDone != nil {
			c.SelfPrincipal, c.SelfAt = <-selfDone, now
		}
		c.Murmurs = teammateMurmurs(murmurs, c.SelfPrincipal)
		c.CheckedAt = now
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if err := fileutil.AtomicWriteJSON(path, c, 0o600); err != nil {
				slog.Debug("statusline: write murmur cache", "err", err)
			}
		}
	}

	for _, m := range c.Murmurs {
		if agentID != "" && m.AgentID == agentID {
			continue
		}
		if now.Sub(m.Timestamp) > statuslineWhisperTTL {
			continue
		}
		return m, true
	}
	return statuslineMurmur{}, false
}

// statuslineSelfPrincipal is the principal ox writes on this coworker's own
// murmurs, from local sources only. A team token's AI coworker is named by the
// server, so it yields "" and own murmurs are excluded by agent ID alone.
func statuslineSelfPrincipal(root string) string {
	if auth.EnvTokenIsTeamFamily(endpoint.GetForProject(root)) {
		return ""
	}
	return identity.LocalAttributionUsername()
}

// scanRecentMurmurs reads murmurs from the last statuslineWhisperTTL in the
// project's Ledger and Team Context checkouts, newest first, dropping daemon
// file-change notices and outside news.
func scanRecentMurmurs(root string, now time.Time) []statuslineMurmur {
	var bases []string
	if p := statuslineLedgerPath(root); p != "" {
		bases = append(bases, p)
	}
	if tc := config.FindRepoTeamContext(root); tc != nil && tc.Path != "" {
		bases = append(bases, tc.Path)
	}

	// two hourly directories cover a full statuslineWhisperTTL across an hour boundary
	var out []statuslineMurmur
	for _, base := range bases {
		murmurs, err := ledger.ReadMurmursInWindow(base, 2)
		if err != nil {
			slog.Debug("statusline: read murmurs", "base", base, "err", err)
		}
		for _, m := range murmurs {
			if m.Topic == "file-changes" || m.IsOutsideNews() || now.Sub(m.Timestamp) > statuslineWhisperTTL {
				continue
			}
			out = append(out, statuslineMurmur{
				AgentID:     m.AgentID,
				PrincipalID: m.PrincipalID,
				Topic:       m.Topic,
				Content:     m.Content,
				Timestamp:   m.Timestamp,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// teammateMurmurs drops the coworker's own murmurs (by principal) and keeps the
// newest statuslineCachedMurmurs.
func teammateMurmurs(murmurs []statuslineMurmur, selfPrincipal string) []statuslineMurmur {
	out := murmurs[:0]
	for _, m := range murmurs {
		if selfPrincipal != "" && strings.EqualFold(m.PrincipalID, selfPrincipal) {
			continue
		}
		out = append(out, m)
		if len(out) == statuslineCachedMurmurs {
			break
		}
	}
	return out
}

// statuslineLedgerPath resolves the project's Ledger checkout from its config
// files alone (no git subprocess): the configured path, else the default path
// for the committed repo ID.
func statuslineLedgerPath(root string) string {
	if lc, err := config.LoadLocalConfig(root); err == nil && lc != nil && lc.Ledger != nil && lc.Ledger.Path != "" {
		return lc.Ledger.Path
	}
	pc, err := config.LoadProjectConfig(root)
	if err != nil || pc == nil || pc.RepoID == "" {
		return ""
	}
	return config.DefaultLedgerPath(pc.RepoID, endpoint.GetForProject(root))
}
