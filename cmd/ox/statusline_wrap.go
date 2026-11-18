package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// `ox statusline --wrap -- <command>` runs an AI coworker's terminal app
// (Codex) inside ox and keeps the bottom row of that same terminal for ox:
// the status line, then the flow lane of the latest ox call, animating as it
// lands. It exists because Codex gives a command no place to draw: its status
// line takes built-in items only, and a hook's notice is printed once and
// never redrawn.
//
// How it holds the row: the wrapped app runs in a pseudo-terminal one row
// shorter than the real one, so every position it addresses is on the rows
// above; the real terminal's scroll region is set to those rows, so nothing
// the app scrolls can carry ox's row away. The app's output passes through
// untouched except for the few sequences that would undo that (wrapFilter).
//
// This file is the platform-neutral part: the output filter and the row's
// content. statusline_wrap_unix.go owns the pseudo-terminal.

// wrapFilter passes a terminal app's output through, keeping ox's bottom row
// out of the app's reach. It is a byte-level state machine, so a sequence
// split across two reads is handled, and it reports when it is between
// sequences: the only time ox may write its own.
type wrapFilter struct {
	// rows is the app's height: the real terminal's, less ox's row.
	rows int
	// dirty is set when the app did something that may have erased ox's row
	// (cleared the screen, switched screens, reset the terminal).
	dirty bool
	// regionPending is set when the terminal was resized while the app was
	// mid-sequence: the new scroll region is written once it finishes.
	regionPending bool
	// cursorSaved is set while the app has saved the cursor (DECSC, or the
	// ANSI.SYS save) and not yet restored it. The terminal keeps one saved
	// position; ox's own save and restore would replace the app's.
	cursorSaved bool
	// stringEndsOnBEL is set inside an OSC, the one string sequence BEL also
	// ends; a DCS, APC or PM runs to its ST.
	stringEndsOnBEL bool

	state wrapState
	seq   []byte
}

type wrapState int

const (
	wrapText      wrapState = iota
	wrapEsc                 // after ESC
	wrapEscInter            // after ESC and intermediate bytes (ESC ( B), until the final byte
	wrapCSI                 // after ESC [
	wrapString              // inside OSC / DCS / APC / PM, until BEL or ST
	wrapStringEsc           // ESC inside one of those: ST if followed by \
)

// maxWrapSeq bounds a control sequence held back for inspection. A longer
// one is not a sequence this filter rewrites; it is flushed as it stands.
const maxWrapSeq = 64

// region is the scroll region that leaves ox's row alone.
func (f *wrapFilter) region() string {
	return fmt.Sprintf("\x1b[1;%dr", max(1, f.rows))
}

// setRegion is the bytes that set the scroll region without moving the app's
// cursor.
func (f *wrapFilter) setRegion() string {
	return "\x1b7" + f.region() + "\x1b8"
}

// resized records the app's new height and returns what to write now: the
// new scroll region, or nothing while ox may not write (canDraw): the app is
// inside a control sequence (a title, a link, an image), where ox's bytes
// would become part of it, or holds a saved cursor that setting the region
// (which saves and restores one too) would replace. The region then comes
// from flushRegion. savedFor is how long the app has held its save.
func (f *wrapFilter) resized(rows int, savedFor time.Duration) string {
	f.rows, f.dirty = rows, true
	if !f.canDraw(savedFor) {
		f.regionPending = true
		return ""
	}
	f.regionPending = false
	return f.setRegion()
}

// flushRegion returns the scroll region a resize left pending, once ox may
// write it (canDraw); "" otherwise.
func (f *wrapFilter) flushRegion(savedFor time.Duration) string {
	if !f.regionPending || !f.canDraw(savedFor) {
		return ""
	}
	f.regionPending = false
	return f.setRegion()
}

// idle reports whether the app is between control sequences.
func (f *wrapFilter) idle() bool { return f.state == wrapText }

// wrapSaveHold is how long ox leaves its row undrawn for an app that holds a
// saved cursor. An app saves and restores within one piece of work; one that
// saved and never restores must not stop ox's row for good.
const wrapSaveHold = time.Second

// canDraw reports whether ox may draw its row now: the app is between
// sequences, and holds no saved cursor that ox's own save would replace.
// savedFor is how long the app has held its save.
func (f *wrapFilter) canDraw(savedFor time.Duration) bool {
	return f.idle() && (!f.cursorSaved || savedFor >= wrapSaveHold)
}

// write filters one chunk of the app's output.
func (f *wrapFilter) write(in []byte) []byte {
	out := make([]byte, 0, len(in)+16)
	for _, b := range in {
		switch f.state {
		case wrapText:
			if b == 0x1b {
				f.state, f.seq = wrapEsc, append(f.seq[:0], b)
				continue
			}
			out = append(out, b)
		case wrapEsc:
			f.seq = append(f.seq, b)
			switch b {
			case '[':
				f.state = wrapCSI
			case ']', 'P', '_', '^':
				out, f.state = append(out, f.seq...), wrapString
				f.stringEndsOnBEL = b == ']'
			case 'c':
				// RIS, a full reset: it drops the scroll region and clears
				// the screen. Let it through, then put the region back.
				out = append(append(out, f.seq...), f.region()...)
				f.dirty, f.state = true, wrapText
			case '7':
				f.cursorSaved = true
				out, f.state = append(out, f.seq...), wrapText
			case '8':
				f.cursorSaved = false
				out, f.state = append(out, f.seq...), wrapText
			default:
				if b >= 0x20 && b <= 0x2f {
					// An intermediate byte (ESC ( B, ESC # 8): the sequence
					// runs on to its final byte.
					f.state = wrapEscInter
					continue
				}
				out, f.state = append(out, f.seq...), wrapText
			}
		case wrapEscInter:
			f.seq = append(f.seq, b)
			if b < 0x20 || b > 0x2f || len(f.seq) > maxWrapSeq {
				out, f.state = append(out, f.seq...), wrapText
			}
		case wrapCSI:
			f.seq = append(f.seq, b)
			if b >= 0x40 && b <= 0x7e {
				out = append(out, f.csi()...)
				f.state = wrapText
			} else if len(f.seq) > maxWrapSeq {
				out, f.state = append(out, f.seq...), wrapText
			}
		case wrapString:
			out = append(out, b)
			switch b {
			case 0x07:
				if f.stringEndsOnBEL {
					f.state = wrapText
				}
			case 0x1b:
				f.state = wrapStringEsc
			}
		case wrapStringEsc:
			out = append(out, b)
			if b == '\\' {
				f.state = wrapText
			} else if b != 0x1b {
				f.state = wrapString
			}
		}
	}
	return out
}

// csi returns the complete CSI sequence in f.seq as it should reach the
// terminal.
func (f *wrapFilter) csi() []byte {
	final := f.seq[len(f.seq)-1]
	params := string(f.seq[2 : len(f.seq)-1])
	switch final {
	case 'r':
		// DECSTBM. "ESC [ r" resets the region to the whole screen, which
		// would put ox's row back inside it; so would a bottom past the
		// app's own height. Private forms (ESC [ ? … r) are something else.
		if strings.HasPrefix(params, "?") {
			break
		}
		top, bottom, _ := strings.Cut(params, ";")
		t, _ := strconv.Atoi(top)
		bt, err := strconv.Atoi(bottom)
		if err != nil || bt <= 0 || bt > f.rows {
			bt = f.rows
		}
		if t <= 0 || t >= bt {
			t = 1
		}
		return fmt.Appendf(nil, "\x1b[%d;%dr", t, bt)
	case 's':
		if params == "" {
			f.cursorSaved = true // the ANSI.SYS save; with parameters it is DECSLRM
		}
	case 'u':
		if params == "" {
			f.cursorSaved = false
		}
	case 'J':
		f.dirty = true // erase in display: ox's row may be among the erased
	case 'h', 'l':
		if strings.HasPrefix(params, "?") && (strings.Contains(params, "1049") || strings.Contains(params, "1047") || strings.Contains(params, "47")) {
			f.dirty = true // the other screen: ox's row is not on it yet
		}
	}
	return f.seq
}

// wrapRowSeparator sits between the status line and the lane on ox's row.
const wrapRowSeparator = "  "

// wrapRow joins the status line and the latest lane into ox's one row,
// fitted to width cells. The status line stays whole; the lane takes what is
// left and is dropped when that is too little to draw its track.
func wrapRow(status string, lane func(columns int) string, width int) string {
	if lane == nil {
		return status
	}
	room := 0
	if width > 0 {
		room = width - ansi.StringWidth(status) - len(wrapRowSeparator)
		if room < 20 {
			return status
		}
	}
	if l := lane(room); l != "" {
		return status + wrapRowSeparator + l
	}
	return status
}

// drawWrapRow is the bytes that paint ox's row at the bottom of a terminal
// of rows height without disturbing the app: save the cursor, go to the row,
// clear it, write, restore the cursor.
func drawWrapRow(rows int, content string) string {
	return "\x1b7" + fmt.Sprintf("\x1b[%d;1H", rows) + ansi.EraseEntireLine + ansi.ResetStyle + content + ansi.ResetStyle + "\x1b8"
}

// wrapQuiet is how long the app's output must have been still before ox
// draws its row. A terminal app saves and restores the cursor within one
// burst of output; drawing between bursts cannot land inside one.
const wrapQuiet = 40 * time.Millisecond

// envStatuslineWrapSession names the file through which a wrapped app's
// hooks tell `ox statusline --wrap` which harness session its terminal
// holds. ox sets it for the app it wraps; it is plumbing between two ox
// processes, not a setting.
const envStatuslineWrapSession = "OX_STATUSLINE_WRAP_SESSION"

const (
	wrapSessionPrefix = "ox-wrap-"
	wrapSessionSuffix = ".session"
	// maxWrapSessionID bounds what is read back: a session id, not a file.
	maxWrapSessionID = 256
)

// newWrapSessionFile creates the empty session file for one wrapped app and
// returns its path, or "" when it cannot (the row then finds its session
// from the worktree).
func newWrapSessionFile() string {
	f, err := os.CreateTemp("", wrapSessionPrefix+"*"+wrapSessionSuffix)
	if err != nil {
		return ""
	}
	_ = f.Close()
	return f.Name()
}

// noteWrapSession is the hook side: inside an app that `ox statusline
// --wrap` started, record the harness session so the wrapper's row follows
// it. It writes only to a file the wrapper already created and named the
// way it names them, so a stray or hostile value of the variable cannot
// make a hook create or overwrite anything else.
func noteWrapSession(sessionID string) {
	path := os.Getenv(envStatuslineWrapSession)
	sessionID = strings.TrimSpace(sessionID)
	if path == "" || sessionID == "" || len(sessionID) > maxWrapSessionID {
		return
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, wrapSessionPrefix) || !strings.HasSuffix(base, wrapSessionSuffix) {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == sessionID {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString(sessionID)
	_ = f.Close()
}

// readWrapSession is the wrapper side: the session its app's hooks last
// reported, or "" before any hook has run.
func readWrapSession(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxWrapSessionID {
		return ""
	}
	return strings.TrimSpace(string(data))
}
