//go:build browser

// plan_review_frame_browser_test.go proves, in real Chrome, the document tools
// a host injects into a plan it frames (@sageox/plan-review's
// dist/document-tools.iife.js): an iframe with sandbox="allow-scripts" and
// the CSP hosts frame plans under (docs/specs/plan-review-drawn-context.md §6),
// driven through the package's own host end, connectFrame. It reads the npm
// build, so run `make plan-review` first; it skips without one.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
)

// frameCSP is the policy a host serves a framed plan under: an opaque origin
// with no storage, cookies, network or eval.
const frameCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; " +
	"img-src data:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'"

const frameInstance = "frame-test-0001"

// frameProbe is the framed page's own script, run after the document tools:
// it reports to the parent what the frame may do and where things are, so the
// test can click them, and when pointer input first reaches it; and it
// navigates the frame when told to, as a hostile plan would. It is the
// test's, not a hook in the package.
const frameProbe = `<script>new MutationObserver(function(){parent.postMessage({revOn:document.body.classList.contains('rev-on')},'*')}).observe(document.body,{attributes:true});document.addEventListener('mousemove',function m(){document.removeEventListener('mousemove',m,true);parent.postMessage({input:true},'*')},true);
window.addEventListener('message',function(e){if(e.source===parent&&e.data&&e.data.navigate)location.href=e.data.navigate});window.addEventListener('load',function(){var p={origin:self.origin,bar:!!document.querySelector('.rev-bar'),rects:{}};
try{localStorage.length;p.storage='allowed'}catch(e){p.storage='denied'}
function box(r){return{x:r.left+r.width/2,y:r.top+r.height/2,left:r.left,right:r.right}}
document.querySelectorAll('[data-probe]').forEach(function(el){p.rects[el.getAttribute('data-probe')]=box(el.getBoundingClientRect())});
var w=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);for(var n=w.nextNode();n;n=w.nextNode()){var i=n.data.indexOf('retry path');
if(i>=0){var r=document.createRange();r.setStart(n,i);r.setEnd(n,i+10);p.rects.phrase=box(r.getBoundingClientRect());break}}
parent.postMessage({probe:p},'*')})</script>`

// serveFramedPlan serves a host page at / that frames doc, with the document
// tools script injected and frameCSP on it, and returns the host URL. The
// host page imports connectFrame from the npm build, records every message it
// accepts in window.got, and exposes window.send.
func serveFramedPlan(t *testing.T, doc []byte) string {
	t.Helper()
	host := `<!doctype html><html><body style="margin:0">` +
		`<iframe id="doc" sandbox="allow-scripts" style="position:absolute;left:0;top:0;width:1000px;height:1000px;border:0"></iframe>` +
		`<script type="module">import { connectFrame } from '/index.js';` +
		`const frame = document.getElementById('doc'); const port = connectFrame(frame, '` + frameInstance + `');` +
		`window.got = []; port.listen((m) => window.got.push(m)); window.send = (m) => port.send(m);` +
		`window.leaks = []; window.addEventListener('message', (e) => { const d = e.data || {}; if (d.probe) window.probe = d.probe; if (d.input) window.input = true;` +
		` if (d.evil) window.evil = true; if (d.pong) window.pong = true; if (d.leak) window.leaks.push(d.leak); });` +
		`frame.src = '/doc';</script></body></html>`
	return serveFrameHost(t, doc, host)
}

// serveFrameHost serves host at /, the npm build's index.js and host.css, and
// at /doc the plan doc with the document tools injected, under frameCSP.
func serveFrameHost(t *testing.T, doc []byte, host string) string {
	t.Helper()
	dist := filepath.Join(packageDir, "..", "..", "packages", "plan-review", "dist")
	script, err := os.ReadFile(filepath.Join(dist, "document-tools.iife.js"))
	if err != nil {
		t.Skip("no packages/plan-review/dist — run `make plan-review` first")
	}
	index, err := os.ReadFile(filepath.Join(dist, "index.js"))
	if err != nil {
		t.Fatalf("read dist/index.js: %v", err)
	}
	css, err := os.ReadFile(filepath.Join(dist, "host.css"))
	if err != nil {
		t.Fatalf("read dist/host.css: %v", err)
	}
	injected := strings.Replace(string(doc), "</body>",
		`<script data-instance="`+frameInstance+`">`+string(script)+`</script>`+frameProbe+"</body>", 1)
	// evil is a page a hostile plan navigates its frame to. It read the
	// instance off the plan and tries to take over: it says ready with a
	// channel of its own, sends a target, and reports back whatever the host
	// sends it. A ping it answers with a pong, so the test knows when the
	// host's messages have arrived.
	evil := `<!doctype html><html><body><script>var i=location.hash.slice(3),c=new MessageChannel();` +
		`c.port1.onmessage=function(e){parent.postMessage({leak:e.data},'*')};` +
		`window.addEventListener('message',function(e){parent.postMessage(e.data&&e.data.ping?{pong:true}:{leak:e.data},'*')});` +
		`parent.postMessage({ox:'review',v:3,instance:i,type:'ready'},'*',[c.port2]);` +
		`parent.postMessage({ox:'review',v:3,instance:i,type:'target',target:{anchor:'h00000000',section:'',label:'forged',rect:{left:1,bottom:1}}},'*');` +
		`parent.postMessage({evil:true},'*')</script></body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(host))
	})
	mux.HandleFunc("/index.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(index)
	})
	mux.HandleFunc("/host.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write(css)
	})
	mux.HandleFunc("/evil", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(evil))
	})
	mux.HandleFunc("/doc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", frameCSP)
		_, _ = w.Write([]byte(injected))
	})
	ln := mustLoopbackListener(t)
	go serveUntilClosed(t, ln, mux)
	return "http://" + ln.Addr().String() + "/"
}

func frameBrowser(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}
	// A cold Chrome on a CI runner took over chromedp's default 20 s to open
	// its DevTools socket; a minute keeps a slow start from failing the test.
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1100, 1100),
		chromedp.WSURLReadTimeout(time.Minute))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(cancelTimeout)
	return ctx
}

// frameMessage waits for the first message of type kind the host accepted
// after the first `after` messages, and decodes it into out.
func frameMessage(t *testing.T, ctx context.Context, after int, kind string, out any) int {
	t.Helper()
	js := fmt.Sprintf(`(function(){for(var i=%d;i<window.got.length;i++)if(window.got[i].type===%q)return JSON.stringify([i,window.got[i]]);return '';})()`, after, kind)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var s string
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &s)); err == nil && s != "" {
			var pair [2]json.RawMessage
			if err := json.Unmarshal([]byte(s), &pair); err != nil {
				t.Fatalf("decode %s message: %v", kind, err)
			}
			var at int
			_ = json.Unmarshal(pair[0], &at)
			if err := json.Unmarshal(pair[1], out); err != nil {
				t.Fatalf("decode %s message: %v", kind, err)
			}
			return at + 1
		}
	}
	var got string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify(window.got)`, &got))
	t.Fatalf("the host never accepted a %q message from the frame; it accepted %s", kind, got)
	return 0
}

func frameSend(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.send(`+msg+`)`, nil)); err != nil {
		t.Fatalf("send %s: %v", msg, err)
	}
}

// TestBrowser_DocumentToolsInOpaqueFrame frames a plan as ox renders it —
// with the CLI's own review layer inside — and drives the injected document
// tools from the host. The frame really is opaque (origin "null", storage
// denied), the CLI layer stays out of it, and the tools still pick an
// element, paint it, comment on a highlighted phrase and pass the review keys
// up, with the anchors ox computes for the same text.
// Failure prevented: the shipped document tools need storage, network or a
// weaker sandbox to run in a host's viewer, or name elements with anchors the
// Ledger's other readers do not compute.
func TestBrowser_DocumentToolsInOpaqueFrame(t *testing.T) {
	ctx := frameBrowser(t)
	md := "# Framed\n\nShip the fix this week.\n\n## Risks\n\nThe retry path can double-fire under load.\n\n- Retry budget is three\n- Backoff doubles\n"
	page, err := plan.RenderHTMLOpts(plan.Parse(md), plan.Result{}, plan.RenderOptions{Slug: "framed"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	page = []byte(strings.Replace(string(page), "<li>Retry budget is three</li>", `<li data-probe="item">Retry budget is three</li>`, 1))
	base := serveFramedPlan(t, page)

	var probe struct {
		Origin, Storage string
		Bar             bool
		Rects           map[string]struct{ X, Y, Left, Right float64 }
	}
	var probeJSON string
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.Poll(`window.probe && JSON.stringify(window.probe)`, &probeJSON, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	if err := json.Unmarshal([]byte(probeJSON), &probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if probe.Origin != "null" || probe.Storage != "denied" {
		t.Fatalf("the frame is not an opaque origin without storage, so this proves nothing: %+v", probe)
	}
	if probe.Bar {
		t.Fatal("the CLI's review layer drew its bar inside the opaque frame; there it must stay out of the host's way")
	}
	item, phrase := probe.Rects["item"], probe.Rects["phrase"]
	if item.X == 0 || phrase.X == 0 {
		t.Fatalf("probe found no click targets: %+v", probe.Rects)
	}
	var none struct{}
	n := frameMessage(t, ctx, 0, "ready", &none)
	frameSend(t, ctx, `{type:'mode', on:true, open:false, draw:false}`)
	// Chrome drops pointer input to a frame process that has only just
	// loaded; move over the frame until the page hears it, then click.
	var reached bool
	for deadline := time.Now().Add(10 * time.Second); !reached && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved", item.X, item.Y), chromedp.Evaluate(`!!window.input`, &reached)); err != nil {
			t.Fatalf("move over the frame: %v", err)
		}
	}
	if !reached {
		t.Fatal("pointer input never reached the framed page")
	}

	// an element: its anchor is the one ox computes for the same heading and text
	var target struct {
		Target struct{ Anchor, Section, Label, Quote string } `json:"target"`
	}
	if err := chromedp.Run(ctx, chromedp.MouseClickXY(item.X, item.Y)); err != nil {
		t.Fatalf("click the item: %v", err)
	}
	n = frameMessage(t, ctx, n, "target", &target)
	want := planreview.AnchorFor("Risks", "Retry budget is three")
	if target.Target.Anchor != want || target.Target.Section != "Risks" || target.Target.Label != "Retry budget is three" || target.Target.Quote != "" {
		t.Fatalf("clicking the item named %+v, want anchor %s in Risks", target.Target, want)
	}

	// painting it: the document places the mark the host sent
	frameSend(t, ctx, `{type:'paint', items:[{anchor:'`+want+`', kind:'unsent', status:'request-change', glyph:'✎', tint:'amber'}], drawings:[]}`)
	var placed struct{ Items []struct{ Anchor string } }
	n = frameMessage(t, ctx, n, "placed", &placed)
	if len(placed.Items) != 1 || placed.Items[0].Anchor != want {
		t.Fatalf("painting %s placed %+v", want, placed.Items)
	}

	// a highlighted phrase: a drag over "retry path" comments on exactly those words
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mousePressed", phrase.Left+1, phrase.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", phrase.Right-1, phrase.Y, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseReleased", phrase.Right-1, phrase.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
	); err != nil {
		t.Fatalf("drag over the phrase: %v", err)
	}
	n = frameMessage(t, ctx, n, "target", &target)
	wantQ := "q" + strings.TrimPrefix(planreview.AnchorFor("Risks", "retry path"), "h")
	if target.Target.Anchor != wantQ || target.Target.Quote != "retry path" {
		t.Fatalf("highlighting the phrase named %+v, want %s with its words", target.Target, wantQ)
	}

	// the review keys reach the host from inside the frame
	var key struct{ Key string }
	if err := chromedp.Run(ctx, chromedp.KeyEvent("r")); err != nil {
		t.Fatalf("press r: %v", err)
	}
	if frameMessage(t, ctx, n, "key", &key); key.Key != "r" {
		t.Fatalf("r reached the host as %q", key.Key)
	}
}

// TestBrowser_DocumentToolsMatchSharedPageVectors places a mark on every
// element of the shared page vectors (pkg/planreview/testdata/anchors.json),
// framed, with the markable set the Go side extracts. Failure prevented: the
// browser package names an element, or its section, differently from the
// anchors already in every Ledger.
func TestBrowser_DocumentToolsMatchSharedPageVectors(t *testing.T) {
	ctx := frameBrowser(t)
	raw, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "pkg", "planreview", "testdata", "anchors.json"))
	if err != nil {
		t.Fatalf("read anchors.json: %v", err)
	}
	var vectors struct {
		Page struct {
			HTML    string
			Targets []struct{ Anchor, Section, Label string }
		}
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse anchors.json: %v", err)
	}
	// the selector extractReviewTargets (internal/plan/anchor.go) matches
	doc := `<!doctype html><html><head><meta charset="utf-8"></head><body>` + vectors.Page.HTML +
		`<script>window.OX_REVIEW_SELECTOR='section[id], li, tr, .ox-chip, .stat, .bar-row, [data-ox-section]'</script></body></html>`
	base := serveFramedPlan(t, []byte(doc))
	if err := chromedp.Run(ctx, chromedp.Navigate(base)); err != nil {
		t.Fatalf("load the framed vectors page: %v", err)
	}
	var none struct{}
	n := frameMessage(t, ctx, 0, "ready", &none)
	var items []string
	seen := map[string]bool{}
	for _, v := range vectors.Page.Targets {
		if !seen[v.Anchor] {
			seen[v.Anchor] = true
			items = append(items, `{anchor:'`+v.Anchor+`', kind:'committed', status:'comment', state:'open', glyph:'◌', tint:'amber'}`)
		}
	}
	frameSend(t, ctx, `{type:'paint', items:[`+strings.Join(items, ",")+`], drawings:[]}`)
	var placed struct {
		Items []struct{ Anchor, Section, Label string }
	}
	frameMessage(t, ctx, n, "placed", &placed)
	if len(placed.Items) != len(vectors.Page.Targets) {
		t.Fatalf("placed %d marks, the vectors have %d markable elements: %+v", len(placed.Items), len(vectors.Page.Targets), placed.Items)
	}
	for i, v := range vectors.Page.Targets {
		if got := placed.Items[i]; got != v {
			t.Errorf("element %d: placed %+v, vectors say %+v", i, got, v)
		}
	}
}

// Failure prevented: a mark on a Mermaid diagram never paints, because the
// host painted before the diagram finished drawing and looked it up by its
// source or a half-drawn copy, or the drawn diagram's anchor changed with the
// per-render id in its <style>.
func TestBrowser_DocumentToolsAnchorADiagramOnceItDraws(t *testing.T) {
	ctx := frameBrowser(t)
	// the plan draws its diagram after load, as Mermaid does: first into a
	// temporary <div>, half drawn, then the finished <svg>
	doc := `<!doctype html><html><head><meta charset="utf-8"></head><body>` +
		`<section data-ox-section="Flow"><pre class="mermaid">graph TD; A--&gt;B</pre></section>` +
		`<script>var d=document.querySelector('.mermaid');` +
		`setTimeout(function(){d.innerHTML='<div id="dmermaid-1"><svg><g><text>A</text></g></svg></div>'},200);` +
		`setTimeout(function(){d.innerHTML='<svg><style>#mermaid-1760000000000{}</style><g><text>A</text><text>B</text></g></svg>'},700)` +
		`</script></body></html>`
	base := serveFramedPlan(t, []byte(doc))
	if err := chromedp.Run(ctx, chromedp.Navigate(base)); err != nil {
		t.Fatalf("load the framed diagram page: %v", err)
	}
	var none struct{}
	n := frameMessage(t, ctx, 0, "ready", &none)
	drawn := planreview.AnchorFor("Flow", "AB")
	frameSend(t, ctx, `{type:'paint', items:[{anchor:'`+drawn+`', kind:'committed', status:'comment', state:'open', glyph:'◌', tint:'amber'}], drawings:[]}`)
	var placed struct{ Items []struct{ Anchor string } }
	frameMessage(t, ctx, n, "placed", &placed)
	if len(placed.Items) == 0 || placed.Items[0].Anchor != drawn {
		t.Fatalf("placed %+v, want the drawn diagram's anchor %s", placed.Items, drawn)
	}
}

// TestBrowser_NavigatedFrameIsNotHeard: a hostile plan navigates its frame,
// after the document tools said ready, to a page it controls that knows the
// instance. The host hears none of that page's messages and sends it nothing.
// Failure prevented: the page forges the reviewer's targets, and every later
// paint the host sends leaves through a frame no CSP keeps off the network.
func TestBrowser_NavigatedFrameIsNotHeard(t *testing.T) {
	ctx := frameBrowser(t)
	base := serveFramedPlan(t, []byte(`<!doctype html><html><head><meta charset="utf-8"></head><body><section id="a"><h2>Risks</h2><p>Retry storms</p></section></body></html>`))
	if err := chromedp.Run(ctx, chromedp.Navigate(base)); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	var none struct{}
	frameMessage(t, ctx, 0, "ready", &none)
	var arrived bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('doc').contentWindow.postMessage({navigate:'/evil#i=`+frameInstance+`'},'*')`, nil),
		chromedp.Poll(`!!window.evil`, &arrived, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("the plan never navigated its frame: %v", err)
	}
	frameSend(t, ctx, `{type:'paint', items:[{anchor:'h1234abcd', kind:'committed', status:'flag', state:'open', glyph:'⚑', tint:'amber'}], drawings:[]}`)
	var got, leaks string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('doc').contentWindow.postMessage({ping:true},'*')`, nil),
		chromedp.Poll(`!!window.pong`, &arrived, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`JSON.stringify(window.got)`, &got),
		chromedp.Evaluate(`JSON.stringify(window.leaks)`, &leaks),
	); err != nil {
		t.Fatalf("ping the navigated frame: %v", err)
	}
	if got != `[{"type":"ready"}]` {
		t.Errorf("the host heard the page the frame navigated to: %s", got)
	}
	if leaks != `[]` {
		t.Errorf("the page the frame navigated to received the host's messages: %s", leaks)
	}
}

// TestBrowser_HostControlsAroundAFramedPlan mounts the package's review
// controls in a host page around a plan framed in an opaque origin, as a
// hosted viewer does, with a transport that stands in for the host's server.
// Devon presses r inside the plan and review mode turns on in the host; his
// click inside the plan opens the composer in the host, below the click; his
// notes are kept under his account, and each Submit freezes them and sends
// them as his. While they wait for the server, Avery signs in on the same
// browser: she sees none of it and sends none of it, even when she signs in
// while one of Devon's operations is in flight. When Devon is back, his
// operations go out with the bytes they were frozen with, and clear.
// Failure prevented: the shared controls only work in ox's own page, keys
// pressed in the plan are lost, or a reviewer's queued feedback is shown to or
// sent as whoever signs in next.
func TestBrowser_HostControlsAroundAFramedPlan(t *testing.T) {
	ctx := frameBrowser(t)
	md := "# Framed\n\nShip the fix this week.\n\n## Risks\n\nThe retry path can double-fire under load.\n\n- Retry budget is three\n- Backoff doubles\n"
	page, err := plan.RenderHTMLOpts(plan.Parse(md), plan.Result{}, plan.RenderOptions{Slug: "framed"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	page = []byte(strings.NewReplacer("<li>Retry budget is three</li>", `<li data-probe="item">Retry budget is three</li>`,
		"<li>Backoff doubles</li>", `<li data-probe="item2">Backoff doubles</li>`).Replace(string(page)))
	// the frame sits away from the page's corner, so the composer must add its offset
	const left, top = 40, 60
	// window.answer picks the stand-in server's reply; 'hold' keeps the send
	// in flight until window.release(reply)
	host := `<!doctype html><html><head><link rel="stylesheet" href="/host.css"></head><body style="margin:0">` +
		fmt.Sprintf(`<iframe id="doc" sandbox="allow-scripts" style="position:absolute;left:%dpx;top:%dpx;width:1000px;height:900px;border:0"></iframe>`, left, top) +
		`<script type="module">import { connectFrame, mountHostControls } from '/index.js';` +
		`const frame = document.getElementById('doc'); const port = connectFrame(frame, '` + frameInstance + `');` +
		`window.sent = []; window.answer = 'offline';` +
		`const reply = (op, a) => a === 'owned' ? { state: 'owned', id: op.id, synced: true } : { state: 'offline' };` +
		`const transport = { send: (op) => { window.sent.push(op); if (window.answer !== 'hold') return Promise.resolve(reply(op, window.answer));` +
		` return new Promise((done) => { window.release = (a) => done(reply(op, a)); }); } };` +
		`window.mount = (account, reviewer) => mountHostControls(port, { account, plan: 'pln_framed', slug: 'framed', committed: [], transport, reviewer, frame });` +
		`window.controls = window.mount('acct-devon', 'Devon');` +
		`window.addEventListener('message', (e) => { const d = e.data || {}; if (d.probe) window.probe = d.probe; if (d.input) window.input = true; if ('revOn' in d) window.frameOn = d.revOn; });` +
		`frame.src = '/doc';</script></body></html>`
	base := serveFrameHost(t, page, host)

	var probe struct {
		Rects map[string]struct{ X, Y float64 }
	}
	var probeJSON string
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.Poll(`window.probe && JSON.stringify(window.probe)`, &probeJSON, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatalf("load the framed plan: %v", err)
	}
	if err := json.Unmarshal([]byte(probeJSON), &probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	x, y := probe.Rects["item"].X+left, probe.Rects["item"].Y+top
	var reached bool
	for deadline := time.Now().Add(10 * time.Second); !reached && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved", x, y), chromedp.Evaluate(`!!window.input`, &reached)); err != nil {
			t.Fatalf("move over the frame: %v", err)
		}
	}
	if !reached {
		t.Fatal("pointer input never reached the framed page")
	}

	// r pressed inside the plan turns review mode on in the host
	if err := chromedp.Run(ctx, chromedp.MouseClickXY(x, y), chromedp.KeyEvent("r")); err != nil {
		t.Fatalf("press r in the frame: %v", err)
	}
	// the frame's own report: Chrome can deliver the next click before the host's message
	if !waitJS(ctx, `document.querySelector('.rev-toggle').textContent==='Exit review'&&window.frameOn===true`, 5*time.Second) {
		t.Fatal("r pressed inside the framed plan must turn review mode on in the host and in the plan")
	}
	// a click inside the plan opens the composer in the host, just below it
	var popTop, popLeft float64
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(x, y),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.rev-pop').getBoundingClientRect().top`, &popTop),
		chromedp.Evaluate(`document.querySelector('.rev-pop').getBoundingClientRect().left`, &popLeft),
		chromedp.SendKeys(".rev-pop .rev-note", "bound the budget", chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("mark the item from the host: %v", err)
	}
	if popTop < y+4 || popTop > y+12 || popLeft < x-2 || popLeft > x+2 {
		t.Fatalf("the composer must open just below the click in the host's coordinates (%v,%v), opened at (%v,%v)", x, y, popLeft, popTop)
	}
	anchor := planreview.AnchorFor("Risks", "Retry budget is three")
	if !waitJS(ctx, `localStorage.getItem('ox-review:acct-devon:pln_framed:draft:`+anchor+`')`, 5*time.Second) {
		t.Fatal("Devon's note must be stored under his account and the plan")
	}

	// Submit freezes it and sends it as Devon; the server is unreachable. A
	// second note's Submit waits behind the first.
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !waitJS(ctx, `window.sent.length===1&&!!document.querySelector('.rev-offline-bar')`, 5*time.Second) {
		t.Fatalf("Submit must send once and say the server is offline, sent=%s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}
	first := evalString(t, ctx, `JSON.stringify(window.sent[0])`)
	if !strings.Contains(first, `"account":"acct-devon"`) || !strings.Contains(first, "bound the budget") || strings.Contains(first, `\"reviewer\"`) {
		t.Fatalf("the operation must go as Devon's, carrying his note and no name of its own: %s", first)
	}
	x2, y2 := probe.Rects["item2"].X+left, probe.Rects["item2"].Y+top
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(x2, y2),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", "cap the backoff", chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Click(".rev-submit", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("mark and submit a second note: %v", err)
	}
	if !waitJS(ctx, `window.sent.length===2&&window.sent[1].id===window.sent[0].id&&Object.keys(localStorage).filter(function(k){return k.indexOf(':op:')>0}).length===2`, 5*time.Second) {
		t.Fatalf("the second Submit must wait behind the first, sent=%s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}

	// Avery signs in on this browser: none of Devon's review is hers
	switchTo := func(account, reviewer, answer string) {
		t.Helper()
		js := fmt.Sprintf(`window.answer = %q; window.controls.destroy(); window.controls = window.mount(%q, %q)`, answer, account, reviewer)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, nil)); err != nil {
			t.Fatalf("sign in as %s: %v", reviewer, err)
		}
	}
	// a mount sends synchronously, before Evaluate returns, so no wait is needed
	switchTo("acct-avery", "Avery", "owned")
	if n := evalString(t, ctx, `window.sent.length`); n != "2" {
		t.Fatalf("Avery's controls must not send Devon's queued review, sent %s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}
	if got := evalString(t, ctx, `document.querySelectorAll('.rev-bar').length+'/'+`+jsCount+`+'/'+((document.querySelector('.rev-rail')||{}).textContent||'')`); got != "1//" {
		t.Fatalf("Avery must see one bar and none of Devon's drafts (bars/count/rail): %q", got)
	}

	// Devon is back, and Avery signs in again while his first operation is in
	// flight: once it is answered, his second is not sent under her session
	switchTo("acct-devon", "Devon", "hold")
	if !waitJS(ctx, `window.sent.length===3`, 5*time.Second) {
		t.Fatalf("Devon's controls must resend his first operation, sent=%s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}
	switchTo("acct-avery", "Avery", "owned")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.release('owned')`, nil)); err != nil {
		t.Fatalf("answer the held send: %v", err)
	}
	if n := evalString(t, ctx, `window.sent.length`); n != "3" {
		t.Fatalf("controls taken down mid-delivery must send nothing more, sent %s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}

	// Devon again: his second operation goes out, and nothing of his is left
	switchTo("acct-devon", "Devon", "owned")
	if !waitJS(ctx, `window.sent.length===4&&`+jsCount+`===''`, 5*time.Second) {
		t.Fatalf("Devon's queued review must go out and clear, sent=%s", evalString(t, ctx, `JSON.stringify(window.sent)`))
	}
	if again := evalString(t, ctx, `JSON.stringify(window.sent[2])`); again != first {
		t.Fatalf("a resend must be the operation as frozen:\n%s\n%s", first, again)
	}
	if last := evalString(t, ctx, `JSON.stringify(window.sent[3])`); !strings.Contains(last, "cap the backoff") || !strings.Contains(last, `"account":"acct-devon"`) {
		t.Fatalf("the last send must be Devon's second operation: %s", last)
	}
	if left := evalString(t, ctx, `Object.keys(localStorage).filter(function(k){return k.indexOf('ox-review:acct-devon:')===0}).join(',')`); left != "" {
		t.Fatalf("owned operations must leave nothing of Devon's behind: %s", left)
	}
}

// TestBrowser_ResolveOfferedOnlyToReviewersWhoMay mounts the review controls
// around a framed plan with an item the author addressed, once as a host that
// does not let this reviewer resolve and once with resolve. Devon opens the
// item from its rail row: without resolve the composer opens to mark it up,
// with neither Accept nor Reopen; with it, Accept and Reopen are offered.
// Failure prevented: a host that authorizes resolutions per reviewer, or
// stores none yet, offers Accept and Reopen to everyone, and its server can
// only refuse them after the reviewer has clicked.
func TestBrowser_ResolveOfferedOnlyToReviewersWhoMay(t *testing.T) {
	ctx := frameBrowser(t)
	// the author rewrote the item's text when addressing it: the rail lists
	// it, and its row opens its composer
	host := `<!doctype html><html><head><link rel="stylesheet" href="/host.css"></head><body><iframe id="doc" sandbox="allow-scripts"></iframe>` +
		`<script type="module">import { connectFrame, mountHostControls } from '/index.js';` +
		`const frame = document.getElementById('doc');` +
		`mountHostControls(connectFrame(frame, '` + frameInstance + `'), { account: 'acct-devon', plan: 'pln_framed', slug: 'framed', reviewer: 'Devon',` +
		` committed: [{ anchor: 'h00000000', section: 'Risks', label: 'Retry budget was two', status: 'request-change', state: 'addressed' }],` +
		` transport: { send: () => Promise.resolve({ state: 'offline' }) }, resolve: location.search === '?resolve' });` +
		`frame.src = '/doc';</script></body></html>`
	base := serveFrameHost(t, []byte(`<!doctype html><html><body><p>Retry budget is three</p></body></html>`), host)
	offered := func(query string) string {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Navigate(base+query), chromedp.Click(".rev-rail-item", chromedp.ByQuery), chromedp.WaitVisible(".rev-pop", chromedp.ByQuery)); err != nil {
			t.Fatalf("open the addressed item from its row: %v", err)
		}
		return evalString(t, ctx, `['rev-accept','rev-reopen','rev-save'].filter(function(c){return document.querySelector('.rev-pop .'+c)}).join(' ')`)
	}
	if got := offered(""); got != "rev-save" {
		t.Errorf("without resolve, an addressed item must open the mark-up composer and offer neither Accept nor Reopen; it offered %q", got)
	}
	if got := offered("?resolve"); got != "rev-accept rev-reopen" {
		t.Errorf("with resolve, an addressed item must offer Accept and Reopen; it offered %q", got)
	}
}
