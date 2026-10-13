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
const frameProbe = `<script>document.addEventListener('mousemove',function m(){document.removeEventListener('mousemove',m,true);parent.postMessage({input:true},'*')},true);
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
	dist := filepath.Join(packageDir, "..", "..", "packages", "plan-review", "dist")
	script, err := os.ReadFile(filepath.Join(dist, "document-tools.iife.js"))
	if err != nil {
		t.Skip("no packages/plan-review/dist — run `make plan-review` first")
	}
	index, err := os.ReadFile(filepath.Join(dist, "index.js"))
	if err != nil {
		t.Fatalf("read dist/index.js: %v", err)
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
		`parent.postMessage({ox:'review',v:1,instance:i,type:'ready'},'*',[c.port2]);` +
		`parent.postMessage({ox:'review',v:1,instance:i,type:'target',target:{anchor:'h00000000',section:'',label:'forged',rect:{left:1,bottom:1}}},'*');` +
		`parent.postMessage({evil:true},'*')</script></body></html>`
	host := `<!doctype html><html><body style="margin:0">` +
		`<iframe id="doc" sandbox="allow-scripts" style="position:absolute;left:0;top:0;width:1000px;height:1000px;border:0"></iframe>` +
		`<script type="module">import { connectFrame } from '/index.js';` +
		`const frame = document.getElementById('doc'); const port = connectFrame(frame, '` + frameInstance + `');` +
		`window.got = []; port.listen((m) => window.got.push(m)); window.send = (m) => port.send(m);` +
		`window.leaks = []; window.addEventListener('message', (e) => { const d = e.data || {}; if (d.probe) window.probe = d.probe; if (d.input) window.input = true;` +
		` if (d.evil) window.evil = true; if (d.pong) window.pong = true; if (d.leak) window.leaks.push(d.leak); });` +
		`frame.src = '/doc';</script></body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(host))
	})
	mux.HandleFunc("/index.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(index)
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
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1100, 1100))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
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
	frameSend(t, ctx, `{type:'mode', on:true, open:false}`)
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
	want := plan.AnchorFor("Risks", "Retry budget is three")
	if target.Target.Anchor != want || target.Target.Section != "Risks" || target.Target.Label != "Retry budget is three" || target.Target.Quote != "" {
		t.Fatalf("clicking the item named %+v, want anchor %s in Risks", target.Target, want)
	}

	// painting it: the document places the mark the host sent
	frameSend(t, ctx, `{type:'paint', items:[{anchor:'`+want+`', kind:'unsent', status:'request-change', glyph:'✎', tint:'amber'}]}`)
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
	wantQ := "q" + strings.TrimPrefix(plan.AnchorFor("Risks", "retry path"), "h")
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
	frameSend(t, ctx, `{type:'paint', items:[`+strings.Join(items, ",")+`]}`)
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
	frameSend(t, ctx, `{type:'paint', items:[{anchor:'h1234abcd', kind:'committed', status:'flag', state:'open', glyph:'⚑', tint:'amber'}]}`)
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
