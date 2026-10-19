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
		`mountHostControls(connectFrame(frame, '` + frameInstance + `'), { account: 'acct-devon', plan: 'pln_framed', slug: 'framed',` +
		` reviewer: 'Devon', frame, transport: { send: () => Promise.resolve({ state: 'offline' }) }, ` + opts + ` });` +
		`frame.src = '/doc';</script></body></html>`
}

// forgingDoc is a plan whose own script runs before the document tools and
// keeps their end of the channel, as any script in the plan can: whatever the
// host page forges goes to the host as if the tools had sent it.
const forgingDoc = `<!doctype html><html><head><script>var MC=MessageChannel;` +
	`window.MessageChannel=function(){var c=new MC();window.toolsPort=c.port1;return c};` +
	`window.addEventListener('message',function(e){if(e.data&&e.data.forge&&window.toolsPort)` +
	`window.toolsPort.postMessage(Object.assign({ox:'review',v:2,instance:'` + frameInstance + `'},e.data.forge))})` +
	`</script></head><body><p>Retry budget is three</p></body></html>`

// insideFrame reports whether the element matching sel lies within the plan's
// frame, with both rects for the failure message.
func insideFrame(sel string) string {
	return `(function(){var f=document.getElementById('doc').getBoundingClientRect(),r=document.querySelector('` + sel + `').getBoundingClientRect();` +
		`return (r.left>=f.left&&r.right<=f.right&&r.top>=f.top&&r.bottom<=f.bottom)+' '+JSON.stringify({el:[r.left,r.top,r.right,r.bottom],frame:[f.left,f.top,f.right,f.bottom]})})()`
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
// plan asks for far outside its frame opens over the frame, and a repeated
// placement lists its comment once.
// Failure prevented: a plan opens the trusted note box with its own text
// outside review mode, files a reviewer's note under another part of the
// plan, throws away what the reviewer typed so no objection can be written,
// places the note box over the host's own controls, or floods the host's
// rail with copies of one comment.
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
}

// TestBrowser_LedgerTextCannotStyleOrScriptTheRail mounts committed items as a
// member could write them into the Ledger: markup in a note, a label and a
// reviewer's name, a state made of class names, and a status that breaks out
// of its attribute. The markup shows as text, the two items outside the
// contract are left out and counted as problems, and the valid item still
// paints.
// Failure prevented: a member's record runs script or lays an invisible layer
// over the host's page, or one bad record stops every mark from painting.
func TestBrowser_LedgerTextCannotStyleOrScriptTheRail(t *testing.T) {
	ctx := frameBrowser(t)
	anchor := planreview.AnchorFor("", "Retry budget is three")
	payload := `<img src=x onerror=\"window.pwned=1\">\'\"><svg onload=\"window.pwned=2\">`
	host := framedHost(`committed: [` +
		`{ anchor: '` + anchor + `', section: '', label: 'Retry budget is three', status: 'comment', state: 'open', note: '` + payload + `', reviewer: '` + payload + `' },` +
		`{ anchor: 'h22222222', label: 'x', status: 'flag', state: 'fixed inset-0 rev-bar' },` +
		`{ anchor: 'h44444444', label: 'y', status: 'x\" onmouseover=\"window.pwned=3', state: 'open' }]`)
	base := serveFrameHost(t, []byte(`<!doctype html><html><body><p>Retry budget is three</p></body></html>`), host)
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-rail-item", chromedp.ByQuery)); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	// the valid item painted: the document placed it, so its row has no "text changed"
	if !waitJS(ctx, `document.querySelectorAll('.rev-rail-item').length===1&&document.querySelector('.rev-rail').textContent.indexOf('text changed')<0`, 10*time.Second) {
		t.Fatalf("only the valid item must be listed, painted on the page: %s", evalString(t, ctx, `document.querySelector('.rev-rail').textContent`))
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-orphans').textContent`); !strings.Contains(got, "2 problem(s)") {
		t.Errorf("the two items outside the contract must count as problems reading the review: %q", got)
	}
	if got := evalString(t, ctx, `JSON.stringify([window.pwned===undefined,document.querySelectorAll('.rev-rail img,.rev-rail svg,.rev-orphans img').length,`+
		`document.querySelectorAll('.fixed,.inset-0').length,document.querySelector('.rev-rail').textContent.indexOf('<img src=x')>=0])`); got != `[true,0,0,true]` {
		t.Errorf("Ledger text must render as text and never as markup or classes: [no script ran, injected elements, injected classes, shown as text] = %s", got)
	}
}
