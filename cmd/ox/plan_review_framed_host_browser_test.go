//go:build browser

// plan_review_framed_host_browser_test.go proves, in real Chrome, the review
// controls a host mounts around a plan it frames inside its own page: where
// they sit, and what the plan's own scripts and a member-written Ledger cannot
// make them do. It reads the npm build, so run `make plan-review` first; it
// skips without one.
package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sageox/ox/pkg/planreview"
)

// framedHost is a host page with chrome of its own, a left column and a top
// strip, around the plan's frame. It mounts the controls with the given
// options. window.forge has the plan's own scripts send a message on the
// document tools' channel (forgingDoc), and window.frameOn mirrors review mode
// as the plan's document sees it.
func framedHost(opts string) string {
	return `<!doctype html><html><head><link rel="stylesheet" href="/host.css"><style>body{margin:0}` +
		`#nav{position:fixed;left:0;top:0;bottom:0;width:240px}#strip{position:fixed;left:240px;right:0;top:0;height:100px}` +
		`#doc{position:fixed;left:240px;top:100px;width:calc(100vw - 240px);height:calc(100vh - 100px);border:0}</style></head>` +
		`<body><div id="nav"></div><div id="strip"></div><iframe id="doc" sandbox="allow-scripts"></iframe>` +
		`<script type="module">import { connectFrame, mountHostControls } from '/index.js';` +
		`const frame = document.getElementById('doc');` +
		`window.addEventListener('message', (e) => { const d = e.data || {}; if ('revOn' in d) window.frameOn = d.revOn; });` +
		`window.forge = (m) => frame.contentWindow.postMessage({ forge: m }, '*');` +
		`window.controls = mountHostControls(connectFrame(frame, '` + frameInstance + `'), { account: 'acct-devon', plan: 'pln_framed', slug: 'framed',` +
		` reviewer: 'Devon', frame, transport: { send: () => Promise.resolve({ state: 'offline' }) }, ` + opts + ` });` +
		`frame.src = '/doc';</script></body></html>`
}

// Changing the host's Review toggle must not replace its controls or presence
// link, discard saved feedback, or resend an operation already in flight.
func TestBrowser_HostReviewModePreservesPresenceAndSubmission(t *testing.T) {
	ctx := frameBrowser(t)
	host := framedHost(`committed: [], revision: 'sha256:` + strings.Repeat("a", 64) + `',
		presence: { join(revision, on) {
			window.joins = (window.joins || 0) + 1; window.leaves = 0;
			on({ type: 'welcome', self: 'devon', peers: [{ id: 'avery', user: 'avery', name: 'Avery', revision, at: null }] });
			return { move() {}, leave() { window.leaves++; } };
		} }, transport: { send() { window.sends = (window.sends || 0) + 1; return new Promise(resolve => window.delivered = resolve); } }`)
	base := serveFrameHost(t, []byte(forgingDoc), host)
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-peer", chromedp.ByQuery),
		chromedp.Evaluate(`window.bar = document.querySelector('.rev-bar'); window.peer = document.querySelector('.rev-peer'); controls.setReviewing(true)`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.frameOn === true`, 5*time.Second) {
		t.Fatal("the frame must enter review mode")
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-rail-plan", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-note", chromedp.ByQuery),
		chromedp.SendKeys(".rev-note", "Keep this feedback", chromedp.ByQuery),
		chromedp.Evaluate(`controls.setReviewing(true)`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-note').value`); got != "Keep this feedback" {
		t.Fatalf("applying the same mode must leave the open note alone: %q", got)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-save", chromedp.ByQuery),
		chromedp.Evaluate(`controls.setReviewing(false); controls.setReviewing(true)`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-rail').textContent`); !strings.Contains(got, "Keep this feedback") {
		t.Fatalf("saved feedback must survive mode changes: %q", got)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery),
		chromedp.Evaluate(`controls.setReviewing(false); controls.setReviewing(true); controls.setReviewing(false)`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.frameOn === false`, 5*time.Second) {
		t.Fatal("the frame must return to reading mode")
	}
	if got := evalString(t, ctx, `JSON.stringify([joins, leaves, sends, bar === document.querySelector('.rev-bar'), peer === document.querySelector('.rev-peer')])`); got != `[1,0,1,true,true]` {
		t.Fatalf("mode changes must preserve the controls, peer and in-flight submission: %s", got)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`controls.whenIdle(() => window.idle = true); delivered({state:'offline'})`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.idle === true`, 5*time.Second) {
		t.Fatal("the original submission must still settle and release the idle guard")
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`controls.destroy(); controls.setReviewing(true); controls.destroy()`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `JSON.stringify([leaves, !!document.querySelector('.rev-bar'), document.body.classList.contains('rev-on')])`); got != `[1,false,false]` {
		t.Fatalf("destroy must leave once and subsequent mode changes must do nothing: %s", got)
	}
}

// forgingDoc is a plan whose own script runs before the document tools and
// keeps their end of the channel, as any script in the plan can: whatever the
// host page forges goes to the host as if the tools had sent it.
const forgingDoc = `<!doctype html><html><head><script>var MC=MessageChannel;` +
	`window.MessageChannel=function(){var c=new MC();window.toolsPort=c.port1;return c};` +
	`window.addEventListener('message',function(e){if(e.data&&e.data.forge&&window.toolsPort)` +
	`window.toolsPort.postMessage(Object.assign({ox:'review',v:3,instance:'` + frameInstance + `'},e.data.forge))})` +
	`</script></head><body><p>Retry budget is three</p></body></html>`

// insideFrame reports whether the element matching sel lies within the plan's
// frame, with both rects for the failure message.
func insideFrame(sel string) string {
	return `(function(){var f=document.getElementById('doc').getBoundingClientRect(),r=document.querySelector('` + sel + `').getBoundingClientRect();` +
		`return (r.left>=f.left&&r.right<=f.right&&r.top>=f.top&&r.bottom<=f.bottom)+' '+JSON.stringify({el:[r.left,r.top,r.right,r.bottom],frame:[f.left,f.top,f.right,f.bottom]})})()`
}

// Dragging across the plan's frame must keep moving the bar, not consume button clicks.
func TestBrowser_ReviewBarDrag(t *testing.T) {
	ctx := frameBrowser(t)
	base := serveFrameHost(t, []byte(`<!doctype html><html><body><p>A plan</p></body></html>`), framedHost(`committed: [], reviewing: true`))
	var start box
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base),
		chromedp.WaitVisible(".rev-bar", chromedp.ByQuery),
		chromedp.Evaluate(`window.bar=document.querySelector('.rev-bar');window.before=bar.getBoundingClientRect();
			const r=bar.querySelector('.rev-conn').getBoundingClientRect();({X:r.left+r.width/2,Y:r.top+r.height/2})`, &start),
	); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `getComputedStyle(bar).cursor+'|'+getComputedStyle(bar.querySelector('button')).cursor`); got != "grab|pointer" {
		t.Fatalf("hover cursors: %q", got)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mouseMoved", start.X, start.Y),
		chromedp.MouseEvent("mousePressed", start.X, start.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", start.X+120, start.Y-100, chromedp.ButtonLeft),
	); err != nil {
		t.Fatal(err)
	}
	const position = `Math.round(bar.getBoundingClientRect().left-before.left)+','+Math.round(bar.getBoundingClientRect().top-before.top)`
	if got := evalString(t, ctx, position+`+'|'+getComputedStyle(bar).cursor`); got != "120,-100|grabbing" {
		t.Fatalf("drag position and cursor: %q", got)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mouseReleased", start.X+120, start.Y-100, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", 900, 200),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, position+`+'|'+getComputedStyle(bar).cursor+'|'+bar.querySelector('.rev-toggle').textContent`); got != "120,-100|grab|Review" {
		t.Fatalf("release and button click: %q", got)
	}
}

// TestBrowser_FramedControlsStayOverTheFrame mounts the controls around a
// plan framed beside a host's own navigation, as a hosted viewer does, at a
// laptop width and a desktop one. The bar and the comments rail sit over the
// plan's frame, review mode is on in the host and in the plan because the
// host mounted the controls for the reviewer to review, and an addressed
// item's row shows the author's answer.
// Failure prevented: the controls cover the host's own navigation and
// actions, the rail drops into the host's page flow below 1400px where no one
// sees it, a reviewer who asked to review lands in reading mode, or the
// author's answer to a comment is never shown.
func TestBrowser_FramedControlsStayOverTheFrame(t *testing.T) {
	ctx := frameBrowser(t)
	host := framedHost(`reviewing: true, committed: [{ anchor: 'h00000000', section: 'Risks', label: 'Retry budget was two',` +
		` status: 'request-change', state: 'addressed', resolutionNote: 'Raised it to three' }]`)
	base := serveFrameHost(t, []byte(`<!doctype html><html><body><p>Retry budget is three</p></body></html>`), host)
	for _, width := range []int64{1280, 1440} {
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(width, 900), chromedp.Navigate(base), chromedp.WaitVisible(".rev-rail-item", chromedp.ByQuery)); err != nil {
			t.Fatalf("%d px: load the framed plan: %v", width, err)
		}
		if !waitJS(ctx, `document.querySelector('.rev-toggle').textContent==='Exit review'&&window.frameOn===true`, 5*time.Second) {
			t.Fatalf("%d px: controls mounted to review must open in review mode, in the host and in the plan", width)
		}
		for _, sel := range []string{".rev-bar", ".rev-rail"} {
			if got := evalString(t, ctx, insideFrame(sel)); !strings.HasPrefix(got, "true") {
				t.Errorf("%d px: %s must sit over the plan's frame, clear of the host's own chrome: %s", width, sel, got)
			}
		}
		if got := evalString(t, ctx, `document.querySelector('.rev-rail').textContent`); !strings.Contains(got, "Raised it to three") {
			t.Errorf("%d px: the rail must show the author's answer to an addressed item: %q", width, got)
		}
	}
}

// TestBrowser_PlanCannotDriveTheComposer has the plan's own scripts forge
// messages on the document tools' channel. A target outside review mode opens
// nothing. Once the reviewer is writing a note, nothing the plan sends can
// close it, swap what it is about, or end review mode, and the note always
// names what it is about. The reviewer's own Esc still closes it, a note the
// plan asks for far outside its frame opens over the frame, a repeated
// placement lists its comment once, and a note opened from the rail stays open
// even before the reviewer types.
// Failure prevented: a plan opens the trusted note box with its own text
// outside review mode, files a reviewer's note under another part of the
// plan, throws away what the reviewer typed so no objection can be written,
// places the note box over the host's own controls, floods the host's rail
// with copies of one comment, or closes every note before a word is typed.
func TestBrowser_PlanCannotDriveTheComposer(t *testing.T) {
	ctx := frameBrowser(t)
	host := framedHost(`committed: [{ anchor: 'h33333333', section: 'S', label: 'Watched', status: 'comment', state: 'open' }]`)
	base := serveFrameHost(t, []byte(forgingDoc), host)
	forge := func(msg string) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.forge(`+msg+`)`, nil)); err != nil {
			t.Fatalf("forge %s: %v", msg, err)
		}
	}
	textChanged := `(document.querySelector('.rev-rail')||{textContent:''}).textContent.indexOf('text changed')>=0`
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	// The channel works: the plan says its one mark is on the page.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		forge(`{type:'placed',items:[{anchor:'h33333333',section:'S',label:'Watched'}]}`)
		if waitJS(ctx, `document.querySelector('.rev-rail-item')&&!(`+textChanged+`)`, 300*time.Millisecond) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the forged channel never reached the host")
		}
	}

	target := func(anchor, label string, left, bottom int) string {
		return `{type:'target',target:{anchor:'` + anchor + `',section:'Risks',label:'` + label + `',rect:{left:` + strconv.Itoa(left) + `,bottom:` + strconv.Itoa(bottom) + `}}}`
	}
	// a placement repeated 500 times; then, outside review mode, a target that
	// opens nothing; the forged r that follows shows all three were handled
	forge(`{type:'placed',items:Array.from({length:500},function(){return {anchor:'h33333333',section:'S',label:'Watched'}})}`)
	forge(target("h11111111", "session expired: paste your token", 40, 40))
	forge(`{type:'key',key:'r'}`)
	if !waitJS(ctx, `document.querySelector('.rev-toggle').textContent==='Exit review'`, 5*time.Second) {
		t.Fatal("the forged r never arrived")
	}
	if waitJS(ctx, `document.querySelector('.rev-pop')`, 300*time.Millisecond) {
		t.Fatal("a target outside review mode must open no note")
	}
	if n := evalString(t, ctx, `document.querySelectorAll('.rev-rail-item').length`); n != "1" {
		t.Errorf("one comment must be one rail row, however often the plan places its anchor: %s rows", n)
	}

	// in review mode a target opens the note, which names what it is about
	forge(target("h11111111", "Real element", 40, 40))
	if err := chromedp.Run(ctx, chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery), chromedp.SendKeys(".rev-pop .rev-note", "keep this", chromedp.ByQuery)); err != nil {
		t.Fatalf("write a note: %v", err)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-pop .rev-about').textContent`); got != "Real element" {
		t.Errorf("an element's note must name the element: %q", got)
	}
	for _, msg := range []string{`{type:'dismiss'}`, `{type:'key',key:'Escape'}`, `{type:'key',key:'r'}`,
		target("h22222222", "Elsewhere", 60, 60), `{type:'refused',reason:'cross-section'}`} {
		forge(msg)
	}
	// a last message the controls answer visibly: every earlier one was handled
	forge(`{type:'placed',items:[]}`)
	if !waitJS(ctx, textChanged, 5*time.Second) {
		t.Fatal("the forged messages never arrived")
	}
	state := evalString(t, ctx, `JSON.stringify([document.querySelector('.rev-pop .rev-about')&&document.querySelector('.rev-pop .rev-about').textContent,`+
		`document.querySelector('.rev-pop .rev-note')&&document.querySelector('.rev-pop .rev-note').value,document.querySelector('.rev-toggle').textContent])`)
	if state != `["Real element","keep this","Exit review"]` {
		t.Fatalf("nothing the plan sends may close, retarget or end a note the reviewer is writing: [about, note, toggle] = %s", state)
	}

	// the reviewer's own Esc closes it
	if err := chromedp.Run(ctx, chromedp.KeyEvent(kb.Escape)); err != nil {
		t.Fatalf("press Esc: %v", err)
	}
	if !waitJS(ctx, `!document.querySelector('.rev-pop')`, 5*time.Second) {
		t.Fatal("the reviewer's own Esc must close the note")
	}
	// a note the plan asks for far outside its frame opens over the frame
	forge(target("h11111111", "Far away", -5000, 99999))
	if !waitJS(ctx, `document.querySelector('.rev-pop')`, 5*time.Second) {
		t.Fatal("a target in review mode must open the note")
	}
	corner := `(function(){var f=document.getElementById('doc').getBoundingClientRect(),r=document.querySelector('.rev-pop').getBoundingClientRect();` +
		`return (r.left>=f.left&&r.left<=f.right&&r.top>=f.top&&r.top<=f.bottom)+' '+JSON.stringify({pop:[r.left,r.top],frame:[f.left,f.top,f.right,f.bottom]})})()`
	if got := evalString(t, ctx, corner); !strings.HasPrefix(got, "true") {
		t.Errorf("the note must open over the plan's frame wherever the plan asks: %s", got)
	}

	// a note the reviewer opens from the rail stays open, empty, whatever the
	// plan sends. The rail holds still while a note is open in it, so the
	// banner of notes no longer anchored, which the placed answer last clears,
	// shows every earlier message was handled.
	if err := chromedp.Run(ctx, chromedp.KeyEvent(kb.Escape), chromedp.Click(".rev-rail-plan", chromedp.ByQuery), chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the whole-plan note from the rail: %v", err)
	}
	if !waitJS(ctx, `document.querySelector('.rev-orphans')`, 5*time.Second) {
		t.Fatal("the mark placed nowhere must show in the banner, or its clearing proves nothing")
	}
	for _, msg := range []string{`{type:'dismiss'}`, `{type:'key',key:'Escape'}`, `{type:'key',key:'r'}`,
		target("h22222222", "Elsewhere", 60, 60), `{type:'refused',reason:'cross-section'}`} {
		forge(msg)
	}
	forge(`{type:'placed',items:[{anchor:'h33333333',section:'S',label:'Watched'}]}`)
	if !waitJS(ctx, `!document.querySelector('.rev-orphans')`, 5*time.Second) {
		t.Fatal("the forged messages never arrived")
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-pop .rev-about')&&document.querySelector('.rev-pop .rev-about').textContent`); got != "Whole plan" {
		t.Errorf("nothing the plan sends may close or retarget a note opened from the rail: it is about %q", got)
	}
}

// TestBrowser_LedgerTextCannotStyleOrScriptTheRail mounts committed items as a
// member could write them into the Ledger: markup in a note, a label and a
// reviewer's name, a state made of class names, a status that breaks out of
// its attribute, and a note and label that are not text. The markup shows as
// text, the three items outside the contract are left out and counted as
// problems in a banner that stays within the frame, and the valid item still
// paints.
// Failure prevented: a member's record runs script or lays an invisible layer
// over the host's page, one bad record stops every mark from painting or the
// rail from drawing, or the banner listing them covers the host's own page.
func TestBrowser_LedgerTextCannotStyleOrScriptTheRail(t *testing.T) {
	ctx := frameBrowser(t)
	anchor := planreview.AnchorFor("", "Retry budget is three")
	payload := `<img src=x onerror=\"window.pwned=1\">\'\"><svg onload=\"window.pwned=2\">`
	host := framedHost(`committed: [` +
		`{ anchor: '` + anchor + `', section: '', label: 'Retry budget is three', status: 'comment', state: 'open', note: '` + payload + `', reviewer: '` + payload + `' },` +
		`{ anchor: 'h22222222', label: 'x', status: 'flag', state: 'fixed inset-0 rev-bar' },` +
		`{ anchor: 'h44444444', label: 'y', status: 'x\" onmouseover=\"window.pwned=3', state: 'open' },` +
		`{ anchor: 'h55555555', label: { toString: null }, status: 'comment', state: 'open', note: 42 }]`)
	base := serveFrameHost(t, []byte(`<!doctype html><html><body><p>Retry budget is three</p></body></html>`), host)
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-rail-item", chromedp.ByQuery)); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	// the valid item painted: the document placed it, so its row has no "text changed"
	if !waitJS(ctx, `document.querySelectorAll('.rev-rail-item').length===1&&document.querySelector('.rev-rail').textContent.indexOf('text changed')<0`, 10*time.Second) {
		t.Fatalf("only the valid item must be listed, painted on the page: %s", evalString(t, ctx, `document.querySelector('.rev-rail').textContent`))
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-orphans').textContent`); !strings.Contains(got, "3 problem(s)") {
		t.Errorf("the three items outside the contract must count as problems reading the review: %q", got)
	}
	if got := evalString(t, ctx, `Math.abs(parseFloat(getComputedStyle(document.querySelector('.rev-orphans')).maxHeight)-0.4*document.getElementById('doc').getBoundingClientRect().height)<1`); got != "true" {
		t.Errorf("the problems banner must stay within the frame's height, not the window's")
	}
	if got := evalString(t, ctx, `JSON.stringify([window.pwned===undefined,document.querySelectorAll('.rev-rail img,.rev-rail svg,.rev-orphans img').length,`+
		`document.querySelectorAll('.fixed,.inset-0').length,document.querySelector('.rev-rail').textContent.indexOf('<img src=x')>=0])`); got != `[true,0,0,true]` {
		t.Errorf("Ledger text must render as text and never as markup or classes: [no script ran, injected elements, injected classes, shown as text] = %s", got)
	}
}

// With autoSubmit a reviewer never has to press Submit: a comment waits while
// they review, a send never closes a note they are typing, and what they saved
// posts as one round when they hide the page, leave review mode or navigate
// away inside the host, and on the next visit when an earlier one left it.
func TestBrowser_AutoSubmitPostsWhenTheReviewerIsDone(t *testing.T) {
	ctx := frameBrowser(t)
	host := framedHost(`committed: [], autoSubmit: !location.search.includes('manual'), transport: { send(op) {
		(window.sent = window.sent || []).push(JSON.parse(op.body).items.map((i) => i.note));
		const owned = { state: 'owned', id: op.id, synced: true };
		return window.hold ? new Promise((resolve) => { window.release = () => resolve(owned); }) : Promise.resolve(owned);
	} }`)
	base := serveFrameHost(t, []byte(forgingDoc), host)
	run := func(what string, actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// The rail redraws each time the plan answers a paint, and a click that
	// lands mid-redraw opens nothing, so click until the note is open.
	openNote := func() {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !waitJS(ctx, `document.querySelector('.rev-pop .rev-note')`, 300*time.Millisecond); {
			if time.Now().After(deadline) {
				t.Fatal("the whole-plan note never opened")
			}
			run("open a whole-plan note", chromedp.Click(".rev-rail-plan", chromedp.ByQuery))
		}
	}
	comment := func(note string) {
		t.Helper()
		openNote()
		run("comment "+note, chromedp.SetValue(".rev-note", note, chromedp.ByQuery), chromedp.Click(".rev-save", chromedp.ByQuery))
	}
	posted := func(n int, want string) {
		t.Helper()
		if !waitJS(ctx, `(window.sent || []).length === `+strconv.Itoa(n), 5*time.Second) {
			t.Fatalf("want round %d posted: %s", n, evalString(t, ctx, `JSON.stringify(window.sent || [])`))
		}
		if got := evalString(t, ctx, `JSON.stringify(window.sent[`+strconv.Itoa(n-1)+`])`); got != want {
			t.Fatalf("round %d = %s, want %s", n, got, want)
		}
	}

	// a comment saved where only Submit sent it
	run("load without autoSubmit", chromedp.Navigate(base+"?manual"), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery),
		chromedp.Evaluate(`controls.setReviewing(true)`, nil))
	comment("Left from yesterday")
	run("load with autoSubmit", chromedp.Navigate(base), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery))
	posted(1, `["Left from yesterday"]`)

	run("review", chromedp.Evaluate(`controls.setReviewing(true)`, nil))
	comment("Retry budget")
	if got := evalString(t, ctx, `JSON.stringify([window.sent.length, document.querySelector('.rev-count').textContent, document.querySelector('.rev-submit').textContent])`); got != `[1,"1 comment posts when you are done","Post now"]` {
		t.Fatalf("a comment must wait while the reviewer reviews, and the bar say when it posts: %s", got)
	}
	openNote()
	run("hide the page mid-note", chromedp.SetValue(".rev-note", "Retry budget is three", chromedp.ByQuery),
		chromedp.Evaluate(`Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' }); document.dispatchEvent(new Event('visibilitychange'))`, nil))
	if got := evalString(t, ctx, `JSON.stringify([window.sent.length, (document.querySelector('.rev-note') || {}).value || null])`); got != `[1,"Retry budget is three"]` {
		t.Fatalf("a send must never close the note the reviewer is typing: %s", got)
	}
	run("save, then hide", chromedp.Click(".rev-save", chromedp.ByQuery), chromedp.Evaluate(`document.dispatchEvent(new Event('visibilitychange'))`, nil))
	posted(2, `["Retry budget is three"]`)
	if !waitJS(ctx, `document.querySelector('.rev-submit').hidden && !document.querySelector('.rev-count').textContent`, 5*time.Second) {
		t.Fatal("with nothing waiting, the bar must offer no Post now")
	}

	comment("Second thought")
	run("leave review mode", chromedp.Evaluate(`controls.setReviewing(false)`, nil))
	posted(3, `["Second thought"]`)

	run("review again", chromedp.Evaluate(`controls.setReviewing(true)`, nil))
	comment("On the way out")
	run("navigate away inside the host", chromedp.Evaluate(`controls.destroy()`, nil))
	posted(4, `["On the way out"]`)

	// a comment saved while a send is still out goes too, after the controls are gone
	run("reload", chromedp.Navigate(base), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery), chromedp.Evaluate(`window.hold = true; controls.setReviewing(true)`, nil))
	comment("Held")
	run("leave review mode while the host is slow", chromedp.Evaluate(`controls.setReviewing(false); controls.setReviewing(true)`, nil))
	posted(1, `["Held"]`)
	comment("Saved during the send")
	run("navigate away, then the host answers", chromedp.Evaluate(`controls.destroy(); window.hold = false; window.release()`, nil))
	posted(2, `["Saved during the send"]`)
	if got := evalString(t, ctx, `document.querySelectorAll('.rev-sync, .rev-bar, .rev-pop').length`); got != "0" {
		t.Fatalf("delivery after destroy() must draw nothing on the host page: %s left", got)
	}
}
