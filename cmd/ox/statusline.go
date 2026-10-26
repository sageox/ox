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
	"strconv"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/sageox/ox/internal/config"
	claude "github.com/sageox/ox/internal/hooks/claude"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/theme"
)

// `ox statusline` draws, in Claude Code's status bar, how much team context
// influenced this harness session: "● ox  3 discussions · 1 plan". Hooks and
// use-evidence writers bump per-session counts (oxactivity.RecordInfluence);
// this command only reads them. It runs on every status-line refresh, so it
// reads local files only: no network, no daemon IPC.

const (
	// statuslineChainTimeout bounds the coworker's own status-line command.
	statuslineChainTimeout = 300 * time.Millisecond
	// statuslineStdinWait bounds the wait for Claude Code's JSON on stdin.
	statuslineStdinWait = 200 * time.Millisecond
	// statuslineMaxInput caps how much stdin is read.
	statuslineMaxInput = 1 << 20
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
	Short:  "Print ox's team-context counts for the Claude Code status bar",
	Hidden: true,
	Args:   cobra.NoArgs,
	Long: `Print how much team context influenced this AI coworker session, for the
Claude Code status bar.

Claude Code runs this command on every status-line refresh, piping its
status-line JSON (session_id, cwd, workspace) on stdin. ox prints one line:

  ● ox  3 discussions · 1 plan · 1 murmur  ● rec

Each count is the number of distinct discussions, plans, docs, sessions and
murmurs that shaped this session; nothing used yet reads "● ox". "● rec"
shows while the session is recording. When the bar is narrow, the smallest
count is dropped first. It reads local files only — never the network or the
ox daemon — and never prints an error into the bar.

` + "`ox init`" + ` and ` + "`ox doctor`" + ` install it as the statusLine in the project's
.claude/settings.local.json. A statusLine already set there is never
replaced. If your own ~/.claude/settings.json sets a statusLine, ox runs it
and prints its line beneath ox's, since the project setting would otherwise
hide yours.

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
	color        bool
	width        int // 0 = unlimited
	chainTimeout time.Duration
	getenv       func(string) string
	stdinWait    time.Duration
}

func defaultStatuslineOptions() statuslineOptions {
	width, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	return statuslineOptions{
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
// JSON: ox's line first, then the coworker's chained line(s) beneath it.
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

	line, show := buildOxStatusline(in, dir)
	return composeStatusline(<-chained, line, show, opts)
}

// oxStatusline is ox's part of the line before it is fitted to a width.
type oxStatusline struct {
	counts    oxactivity.Counts
	recording bool
}

// buildOxStatusline gathers what ox shows. show is false when the status line
// is off or dir is not in a SageOx project. A panic still shows "● ox".
func buildOxStatusline(in statuslineInput, dir string) (line oxStatusline, show bool) {
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
	if agentID == "" {
		return line, true
	}

	// Keyed by this harness session, not the agent ID alone: another
	// window's counts must not show up on this bar.
	line.counts = oxactivity.LoadState(root, oxactivity.StateKey(agentID, in.SessionID)).Counts()
	if rec, err := session.LoadRecordingStateForAgent(root, agentID); err == nil && rec != nil {
		line.recording = true
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

// composeStatusline puts ox's line first and the chained output on its own
// line(s) beneath, so ox is always in the same place and a coworker's status
// bar keeps its shape. Without a chained command ox's line is the whole
// output. The ox line is always present while the status line is on, even
// when nothing has been used: an idle session reads "● ox  ● rec". When the
// width is tight, "● rec" is reserved first and the counts give way (smallest
// first) before it does.
func composeStatusline(chained string, line oxStatusline, show bool, opts statuslineOptions) string {
	chained = strings.TrimRight(chained, "\r\n")
	if !show {
		if chained == "" {
			return ""
		}
		return chained + "\n"
	}

	width := 0 // unlimited
	if opts.width > 0 {
		width = max(opts.width, ansi.StringWidth(oxactivity.Mark))
	}
	rec := ""
	if line.recording && (width == 0 || width >= ansi.StringWidth(oxactivity.Mark)+ansi.StringWidth(statuslineRecMark)) {
		rec = statuslineRecMark
		if width > 0 {
			width -= ansi.StringWidth(rec)
		}
		if opts.color {
			rec = renderRecMark()
		}
	}

	ox, _ := oxactivity.RenderWidth(line.counts, opts.color, width)
	ox += rec
	if chained == "" {
		return ox + "\n"
	}
	return ox + "\n" + chained + "\n"
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
