//go:build browser

package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sageox/ox/pkg/planreview"
)

// Failure prevented: drawing stays freehand until release, editing makes a
// second comment, loses the note, or submits stale geometry and context.
func TestBrowser_OvalsDrawMoveAndResize(t *testing.T) {
	ctx := drawBrowser(t)
	base := serveDrawFixture(t, drawHost("a", revisionDigest(t, "a"), "[]", ""))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-draw", chromedp.ByQuery), chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	reachFrame(t, ctx)
	type inkState struct {
		Live, Oval     *box
		Anchor, Cursor string
		Polylines      int
		Handles        []struct {
			HX, HY int
			Box    box
		}
	}
	inspect := func() inkState {
		t.Helper()
		var s string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ink=null;window.ask({ink:true})`, nil),
			chromedp.Poll(`window.ink&&JSON.stringify(window.ink)`, &s)); err != nil {
			t.Fatal(err)
		}
		var state inkState
		if err := json.Unmarshal([]byte(s), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	waitSize := func(width, height float64) inkState {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			state := inspect()
			if state.Oval != nil && math.Abs(state.Oval.W-width) <= 1 && math.Abs(state.Oval.H-height) <= 1 {
				return state
			}
			if time.Now().After(deadline) {
				t.Fatalf("oval never reached %.1fx%.1f: %+v", width, height, state.Oval)
			}
		}
	}
	drag := func(x, y, dx, dy float64) {
		t.Helper()
		if err := chromedp.Run(ctx,
			chromedp.MouseEvent("mouseMoved", x, y),
			chromedp.MouseEvent("mousePressed", x, y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
			chromedp.MouseEvent("mouseMoved", x+dx, y+dy, chromedp.ButtonLeft),
			chromedp.MouseEvent("mouseReleased", x+dx, y+dy, chromedp.ButtonLeft, chromedp.ClickCount(1))); err != nil {
			t.Fatal(err)
		}
	}
	b := partOf(t, ctx, "sentence")
	x, y, w, h := b.X+40, b.Y-5, 200.0, b.H+10
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mousePressed", x, y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		// A detour must not turn into a freehand path.
		chromedp.MouseEvent("mouseMoved", x+w, y, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseMoved", x+w, y+h, chromedp.ButtonLeft)); err != nil {
		t.Fatal(err)
	}
	state := inspect()
	if state.Live == nil || state.Polylines != 0 || math.Abs(state.Live.W-w) > 16 || math.Abs(state.Live.H-h) > 16 {
		t.Fatalf("while dragging: %+v; want a live oval about %.0fx%.0f, without a freehand line", state, w, h)
	}
	if got := evalString(t, ctx, `String(!!document.querySelector('.rev-pop'))`); got != "false" {
		t.Fatal("note opened before the drag ended")
	}
	if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseReleased", x+w, y+h, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	state = waitSize(w, h)
	anchor := state.Anchor
	if state.Oval == nil || state.Cursor != "grab" || len(state.Handles) != 8 {
		t.Fatalf("oval has no grab outline and eight handles: %+v", state)
	}
	// An invalid edit must also preserve a pending drawing with an empty note.
	o := state.Oval
	drag(o.X+o.W/2+o.W/2/math.Sqrt2, o.Y+o.H/2-o.H/2/math.Sqrt2, 20-(o.X+o.W/2), 20-(o.Y+o.H/2))
	if !waitJS(ctx, `(document.querySelector('.rev-toast')||{}).textContent.indexOf('Nothing in that oval')===0&&document.querySelector('.rev-pop .rev-note')`, 5*time.Second) {
		t.Fatal("invalid move discarded the pending drawing or its empty note")
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys(".rev-pop .rev-note", "Keep this note while editing.", chromedp.ByQuery),
		chromedp.Click(`.rev-pop [data-s="comment"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	// Move the pending oval onto a different element while its note is typed.
	b = partOf(t, ctx, "abort")
	state = inspect()
	o = state.Oval
	dx, dy := b.X+140-(o.X+o.W/2), b.Y+b.H/2-(o.Y+o.H/2)
	drag(o.X+o.W/2+o.W/2/math.Sqrt2, o.Y+o.H/2-o.H/2/math.Sqrt2, dx, dy)
	if !waitJS(ctx, `document.querySelector('.rev-pop .rev-about').textContent.indexOf('Abort if')>=0`, 5*time.Second) {
		t.Fatal("moving the oval did not refresh its target")
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-note').value`); got != "Keep this note while editing." {
		t.Fatalf("move lost note: %q", got)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	state = inspect()
	// Resize the saved draft horizontally using the right edge handle.
	for _, handle := range state.Handles {
		if handle.HX == 1 && handle.HY == 0 {
			drag(handle.Box.X+5, handle.Box.Y+5, 60, 0)
		}
	}
	state = waitSize(w+60, h)
	if state.Anchor != anchor {
		t.Fatalf("edge resize changed identity: %+v", state)
	}
	// Esc during a corner resize restores the saved geometry.
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mousePressed", state.Oval.X+state.Oval.W, state.Oval.Y+state.Oval.H, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", state.Oval.X+state.Oval.W+40, state.Oval.Y+state.Oval.H+30, chromedp.ButtonLeft),
		chromedp.KeyEvent(kb.Escape),
		chromedp.MouseEvent("mouseReleased", state.Oval.X+state.Oval.W+40, state.Oval.Y+state.Oval.H+30, chromedp.ButtonLeft, chromedp.ClickCount(1))); err != nil {
		t.Fatal(err)
	}
	state = inspect()
	if state.Oval == nil || math.Abs(state.Oval.W-(w+60)) > 1 || math.Abs(state.Oval.H-h) > 1 {
		t.Fatalf("cancelled resize changed the draft: %+v", state.Oval)
	}
	// A corner can reduce both dimensions independently of the original ratio.
	drag(state.Oval.X+state.Oval.W, state.Oval.Y+state.Oval.H, -40, -10)
	waitSize(w+20, h-10)
	// Reload proves edits reached the durable draft, not just its preview.
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `document.querySelector('.rev-count').textContent==='1 unsent'`, 5*time.Second) {
		t.Fatal("edited oval did not survive reload as one draft")
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.sent.length===1`, 5*time.Second) {
		t.Fatal("Submit sent nothing")
	}
	var round struct {
		Marks []struct {
			Anchor, Note, Status string
			Region               struct {
				Ellipse struct{ Radii [2]float64 }
				Targets []struct{ Anchor string }
			}
		}
	}
	if err := json.Unmarshal([]byte(evalString(t, ctx, `window.sent[0].body`)), &round); err != nil {
		t.Fatal(err)
	}
	if len(round.Marks) != 1 {
		t.Fatalf("edits created %d drawings", len(round.Marks))
	}
	m := round.Marks[0]
	if m.Anchor != anchor || m.Note != "Keep this note while editing." || m.Status != "comment" {
		t.Fatalf("edits lost identity, note, or status: %+v", m)
	}
	if math.Abs(m.Region.Ellipse.Radii[0]*2-(w+20)) > 1 || math.Abs(m.Region.Ellipse.Radii[1]*2-(h-10)) > 1 {
		t.Fatalf("submitted stale ellipse: %+v", m.Region.Ellipse)
	}
	want := planreview.AnchorFor("Risks", "Abort if the dead-letter rate doubles.")
	if len(m.Region.Targets) != 1 || m.Region.Targets[0].Anchor != want {
		t.Fatalf("submitted stale targets: %+v", m.Region.Targets)
	}
}
