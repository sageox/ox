package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/sageox/ox/internal/flowlane"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
)

// `ox statusline --watch` keeps ox's line live in a terminal pane, for hosts
// whose chat has no status bar: Conductor runs it as a run script in its Run
// pane. Nothing pipes it a session, so the session is found from the
// worktree the pane runs in.

// statuslineWatchInterval is how often the watched pane is rebuilt while
// nothing moves. Each rebuild reads a few small local files; nothing is
// redrawn unless it changed. Short enough that an ox call shows as running
// before it has finished.
const statuslineWatchInterval = 500 * time.Millisecond

// resolveStatuslineSessionForDir names the harness session recording in the
// worktree at root, for a caller that was given none. It returns "" unless
// exactly one live session records there: a Conductor workspace can hold
// several chats, and showing one of them on behalf of another would be wrong.
// A recording whose AI coworker process has exited (a crashed or closed chat
// never stopped) does not count, or it would leave the pane idle for good.
// Liveness is the recording's own PID check: local, no daemon round trip.
func resolveStatuslineSessionForDir(root string) string {
	if root == "" {
		return ""
	}
	states, err := session.LoadAllRecordingStates(root)
	if err != nil {
		return ""
	}
	want := evalPath(root)
	found := ""
	for _, s := range states {
		if s == nil || s.SourceRejected || s.WorkspacePath == "" || evalPath(s.WorkspacePath) != want || !s.IsAgentAlive() {
			continue
		}
		sid := strings.TrimSpace(s.AgentSessionID)
		if sid == "" || (found != "" && found != sid) {
			return ""
		}
		found = sid
	}
	return found
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// watchFrame is one draw of the watched pane: ox's status line, the flow
// lanes shown beneath it, and (for a pipe or a log, which cannot redraw)
// every finished lane as a settled row.
type watchFrame struct {
	status string
	lanes  []string
	log    []string
	// trace names the file the log rows were read from: a new one (a
	// recording started or ended) is a new list, printed from its start.
	trace string
	// moving is true while a lane is still animating: the caller redraws
	// soon rather than at the idle pace.
	moving bool
}

func (f watchFrame) text() string {
	return strings.Join(append([]string{f.status}, f.lanes...), "\n")
}

const (
	// watchMaxLanes caps the lanes drawn however tall the pane is.
	watchMaxLanes = 12
	// watchDefaultLanes is used when the pane's height is unknown.
	watchDefaultLanes = 6
)

// flowWatch remembers, between draws, when each finished ox call was first
// seen: that is when its lane starts moving. Calls already finished when the
// pane opened are drawn settled.
type flowWatch struct {
	agentID string
	// trace is the file the lanes were read from; a new one (a new
	// recording) starts the pane over.
	trace  string
	seenAt []time.Time
	primed bool
	// wantLog asks for the settled row of every finished call, for output
	// that cannot redraw (a pipe). A terminal never reads them.
	wantLog bool
	// The trace as last parsed, reused while the file is unchanged: a pane
	// redraws every 80ms while a lane moves, and a long recording's trace
	// must not be re-read and re-parsed for each frame.
	flowsStamp string
	flows      []finishedFlow
}

// traceStamp identifies a trace file's contents cheaply: its size and
// modification time. "" (missing or unreadable) never matches a cached stamp.
func traceStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// finished is the trace's finished calls, parsed again only when it changed.
func (w *flowWatch) finished(trace, agentID string) []finishedFlow {
	stamp := traceStamp(trace)
	if stamp == "" || stamp != w.flowsStamp {
		w.flows, w.flowsStamp = finishedFlowsAt(trace, agentID), stamp
	}
	return w.flows
}

// lanes draws agentID's flow lanes for the pane: the most recent finished
// calls that fit rows, then the calls still running.
func (w *flowWatch) lanes(root, agentID string, now time.Time, rows int, opts statuslineOptions) (shown, log []string, moving bool) {
	if agentID == "" || !flowLanesEnabled() {
		*w = flowWatch{wantLog: w.wantLog}
		return nil, nil, false
	}
	trace := flowTracePath(root, agentID)
	if agentID != w.agentID || trace != w.trace {
		*w = flowWatch{agentID: agentID, trace: trace, wantLog: w.wantLog}
	}
	flows := w.finished(trace, agentID)
	if len(flows) < len(w.seenAt) {
		w.seenAt, w.primed = nil, false // the trace was rewritten
	}
	for len(w.seenAt) < len(flows) {
		at := now
		if !w.primed {
			at = time.Time{} // there before the pane opened: settled
		}
		w.seenAt = append(w.seenAt, at)
	}
	w.primed = true

	lane := flowlane.Options{Columns: opts.width, Color: opts.color}
	running := runningFlowLanes(root, agentID, now)
	first := max(0, len(flows)-max(0, rows-len(running)))
	for i, f := range flows {
		since := flowlane.AnimDuration
		if !w.seenAt[i].IsZero() {
			since = now.Sub(w.seenAt[i])
		}
		if w.wantLog {
			if row := flowlane.Render(f.lane, flowlane.AnimDuration, flowlane.Options{Columns: opts.width, Still: true}); row != "" {
				log = append(log, row)
			}
		}
		if i < first {
			continue
		}
		// A pane too narrow for the track gets no lane rather than a wrapped one.
		if row := flowlane.Render(f.lane, since, lane); row != "" {
			moving = moving || flowlane.Moving(f.lane, since)
			shown = append(shown, row)
		}
	}
	for _, r := range running {
		if row := flowlane.Render(r.lane, r.elapsed, lane); row != "" {
			moving = moving || flowlane.Moving(r.lane, r.elapsed)
			shown = append(shown, row)
		}
	}
	if len(shown) > rows {
		shown = shown[len(shown)-rows:]
	}
	return shown, log, moving
}

// statuslineWatchFrame is ox's line for the worktree at dir, as one line
// without a trailing newline.
func statuslineWatchFrame(dir string, opts statuslineOptions) string {
	return buildWatchFrame(&flowWatch{}, dir, opts, 0, time.Now()).status
}

// buildWatchFrame gathers the pane for the worktree at dir: the status line
// of the one session recording there, and up to rows of its flow lanes.
func buildWatchFrame(w *flowWatch, dir string, opts statuslineOptions, rows int, now time.Time) watchFrame {
	return buildWatchFrameFor(w, dir, "", opts, rows, now)
}

// buildWatchFrameFor is buildWatchFrame for a caller that knows the harness
// session (`ox statusline --wrap` is told by the app's hooks); "" finds it
// from the worktree.
func buildWatchFrameFor(w *flowWatch, dir, sid string, opts statuslineOptions, rows int, now time.Time) watchFrame {
	root := statuslineProjectRoot(dir)
	if sid == "" {
		sid = resolveStatuslineSessionForDir(root)
	}
	line, show := buildOxStatusline(statuslineInput{SessionID: sid}, dir)
	f := watchFrame{status: strings.TrimRight(composeStatusline("", line, show, opts), "\n")}
	if !show || sid == "" || rows <= 0 {
		return f
	}
	agentID := ""
	if marker, err := ReadSessionMarker(sid); err == nil && marker != nil {
		agentID = marker.AgentID
	}
	f.lanes, f.log, f.moving = w.lanes(root, agentID, now, rows, opts)
	f.trace = w.trace
	return f
}

// watchStatusline writes frame() once, then again on every tick that
// changes it, until ctx ends. In place, a frame overwrites the last one
// where it stands, however many lines either has; otherwise (a pipe, a log)
// a changed status line is its own line and each finished lane is printed
// once, settled.
func watchStatusline(ctx context.Context, w io.Writer, frame func() watchFrame, tick <-chan time.Time, inPlace bool) {
	last, lastStatus, first := "", "", true
	lastLines, logged, loggedTrace := 1, 0, ""
	draw := func() {
		f := frame()
		if !inPlace {
			if first || f.status != lastStatus {
				_, _ = io.WriteString(w, f.status+"\n")
			}
			first, lastStatus = false, f.status
			if logged > len(f.log) || f.trace != loggedTrace {
				logged, loggedTrace = 0, f.trace // a new trace: its rows are all new
			}
			for _, row := range f.log[logged:] {
				_, _ = io.WriteString(w, row+"\n")
			}
			logged = len(f.log)
			return
		}
		cur := f.text()
		if !first && cur == last {
			return
		}
		var b strings.Builder
		if lastLines > 1 {
			b.WriteString(ansi.CursorUp(lastLines - 1))
		}
		b.WriteString("\r" + ansi.EraseEntireLine)
		if lastLines > 1 {
			b.WriteString(ansi.EraseScreenBelow)
		}
		b.WriteString(cur)
		_, _ = io.WriteString(w, b.String())
		first, last, lastLines = false, cur, strings.Count(cur, "\n")+1
	}
	draw()
	for {
		select {
		case <-ctx.Done():
			if inPlace {
				_, _ = io.WriteString(w, "\n")
			}
			return
		case <-tick:
			draw()
		}
	}
}

// statuslineWatchMotionInterval is how often the pane redraws while a lane
// is moving; statuslineWatchInterval is its idle pace.
const statuslineWatchMotionInterval = 80 * time.Millisecond

// runStatuslineWatch is `ox statusline --watch`: it runs until interrupted.
func runStatuslineWatch(stdout io.Writer) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := defaultStatuslineOptions()
	opts.color = false // only a terminal gets color; set below
	inPlace := false
	rows := watchDefaultLanes
	var size func() (int, int)
	if f, ok := stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		inPlace = true
		// main's init forced NO_COLOR for a piped stdout; on a terminal the
		// coworker's own NO_COLOR decides, as for the status bar.
		opts.color = oxactivity.ColorAllowedFor(statuslineNoColor)
		fixedWidth := opts.width
		size = func() (int, int) {
			width, height, err := term.GetSize(int(f.Fd()))
			if err != nil {
				return fixedWidth, 0
			}
			if fixedWidth > 0 {
				width = fixedWidth
			}
			return width, height
		}
	}
	dir, _ := os.Getwd()

	// One timer paces the pane: the idle interval while nothing moves, the
	// motion interval for the 2.4s after an ox call lands. Nothing is written
	// unless the frame changed.
	watch := flowWatch{wantLog: !inPlace}
	tick := make(chan time.Time)
	pace := make(chan bool, 1)
	go func() {
		for {
			wait := statuslineWatchInterval
			select {
			case moving := <-pace:
				if moving {
					wait = statuslineWatchMotionInterval
				}
			default:
			}
			select {
			case <-ctx.Done():
				return
			case now := <-time.After(wait):
				select {
				case tick <- now:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	frame := func() watchFrame {
		if size != nil {
			width, height := size()
			opts.width = max(width, 0)
			if height > 1 {
				rows = min(watchMaxLanes, height-1)
			}
		}
		f := buildWatchFrame(&watch, dir, opts, rows, time.Now())
		select {
		case pace <- f.moving:
		default:
		}
		return f
	}
	watchStatusline(ctx, stdout, frame, tick, inPlace)
}
