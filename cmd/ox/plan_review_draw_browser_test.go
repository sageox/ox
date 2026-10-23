//go:build browser

// plan_review_draw_browser_test.go drives Draw around in real Chrome on the
// drawn-context fixture revisions (pkg/planreview/testdata/drawn-context),
// framed in an opaque origin under frameCSP. Each capture is made in the
// browser that places it, so a machine's fonts change offsets, not outcomes,
// as long as each stroke encloses the same elements on every machine. It
// reads the npm build: run `make plan-review` first; it skips without one.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
)

// drawProbe is the fixture page's own script, the test's and not the
// package's. It reports when pointer input first reaches the frame, scrolls a
// named part of the plan into view and reports its box in the frame's
// viewport, and opens the notes or checks a tab when told to. "table" is the
// rows under its header: a loop around the whole table bulges far enough
// above it to enclose the sentence there, which revision c rewords.
const drawProbe = `<script>
document.addEventListener('mousemove',function m(){document.removeEventListener('mousemove',m,true);parent.postMessage({input:true},'*')},true);
function node(t){return [].filter.call(document.querySelectorAll('svg g.node'),function(g){return g.textContent.trim()===t})}
function li(t){return [].filter.call(document.querySelectorAll('li'),function(l){return l.textContent.trim()===t})}
var named={
 sentence:function(){return [document.getElementById('double-fire')]},
 abort:function(){return li('Abort if the dead-letter rate doubles.').slice(0,1)},
 'retry-node':function(){return node('Retry budget')},
 'queue-worker':function(){return node('Queue').concat(node('Worker'))},
 notes:function(){return li('Add the budget counter beside the queue depth gauge.')},
 'full-tab':function(){return [document.querySelector('#panel-full p')]},
 figure:function(){return [document.querySelector('figure')]},
 table:function(){return [].slice.call(document.querySelectorAll('table tr'),1)}
};
window.addEventListener('message',function(e){
 var d=e.data||{};if(e.source!==parent)return;
 if(d.text)document.getElementById('double-fire').textContent=d.text;
 if(d.open)document.querySelector('details').open=true;
 if(d.check)document.getElementById(d.check).checked=true;
 if(d.which)parent.postMessage({which:document.querySelector('input[name=rollout]:checked').id},'*');
 if(d.ink){
  var root=document.querySelector('.rev-ink'),live=root.querySelector('.rev-ink-live'),hit=root.querySelector('.rev-ink-hit');
  function box(el){if(!el)return null;var r=el.getBoundingClientRect();return {x:r.left,y:r.top,w:r.width,h:r.height}}
  parent.postMessage({ink:{live:box(live),oval:box(hit),anchor:hit&&hit.getAttribute('data-edit'),cursor:hit&&getComputedStyle(hit).cursor,
   handles:[].map.call(root.querySelectorAll('.rev-ink-handle'),function(h){return {hx:+h.getAttribute('data-hx'),hy:+h.getAttribute('data-hy'),box:box(h)}}),
   polylines:root.querySelectorAll('polyline').length}},'*');
 }
 if(!d.want)return;
 var els=named[d.want]();els[0].scrollIntoView({block:'center'});
 requestAnimationFrame(function(){requestAnimationFrame(function(){
  var x0=1e9,y0=1e9,x1=-1e9,y1=-1e9;
  els.forEach(function(el){var r=el.getBoundingClientRect();x0=Math.min(x0,r.left);y0=Math.min(y0,r.top);x1=Math.max(x1,r.right);y1=Math.max(y1,r.bottom)});
  parent.postMessage({rect:{name:d.want,x:x0,y:y0,w:x1-x0,h:y1-y0}},'*');
 })});
});
</script>`

// drawTools is a host page around the plan's frame, at the page's top left.
// window.load(rev, width, height) frames a revision under a new instance and
// hears it through the package's connectFrame; every message it accepts lands
// in window.got.
const drawTools = `<!doctype html><html><body style="margin:0">` +
	`<iframe id="doc" sandbox="allow-scripts" style="position:absolute;left:0;top:0;border:0"></iframe>` +
	`<script type="module">import { connectFrame } from '/index.js';` +
	`const frame = document.getElementById('doc'); let port = null, loads = 0;` +
	`window.load = (rev, w, h) => { if (port) port.close(); window.got = []; window.input = false;` +
	` const inst = 'draw-test-' + String(++loads).padStart(4, '0'); port = connectFrame(frame, inst); port.listen((m) => window.got.push(m));` +
	` frame.style.width = w + 'px'; frame.style.height = h + 'px'; frame.src = '/doc/' + rev + '?load=' + loads + '#i=' + inst; };` +
	`window.send = (m) => port.send(m); window.ask = (m) => frame.contentWindow.postMessage(m, '*');` +
	`window.addEventListener('message', (e) => { const d = e.data || {}; if (d.rect) window.rect = d.rect; if (d.input) window.input = true; if (d.which) window.which = d.which; if (d.ink) window.ink = d.ink; });` +
	`window.ready = true;</script></body></html>`

var mermaidCDNTag = regexp.MustCompile(`<script[^>]+src="https://cdn\.jsdelivr\.net/npm/mermaid@[^"]+"[^>]*></script>`)

// revisionDigest is a revision's plan_revision: the SHA-256 of its stored bytes.
func revisionDigest(t *testing.T, rev string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "pkg", "planreview", "testdata", "drawn-context", "revisions", rev+".html"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// serveDrawFixture serves host at /, the npm build's index.js and host.css,
// and each fixture revision at /doc/<rev> the way a host frames it: under
// frameCSP, its Mermaid CDN tag swapped for ox's vendored build inlined (as
// framePlan does), and the document tools and drawProbe before </body>.
func serveDrawFixture(t *testing.T, host string) string {
	t.Helper()
	root := filepath.Join(packageDir, "..", "..")
	dist := filepath.Join(root, "packages", "plan-review", "dist")
	script, err := os.ReadFile(filepath.Join(dist, "document-tools.iife.js"))
	if err != nil {
		t.Skip("no packages/plan-review/dist — run `make plan-review` first")
	}
	index, err := os.ReadFile(filepath.Join(dist, "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join(dist, "host.css"))
	if err != nil {
		t.Fatal(err)
	}
	vendored, err := os.ReadFile(filepath.Join(root, "internal", "plan", "assets", "mermaid.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	mermaid := "<script>" + strings.NewReplacer("</script", `<\/script`, "<!--", `<\!--`).Replace(string(vendored)) + "</script>"
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
	mux.HandleFunc("/doc/", func(w http.ResponseWriter, r *http.Request) {
		rev := strings.TrimPrefix(r.URL.Path, "/doc/")
		if rev != "a" && rev != "b" && rev != "c" {
			http.NotFound(w, r)
			return
		}
		stored, err := os.ReadFile(filepath.Join(root, "pkg", "planreview", "testdata", "drawn-context", "revisions", rev+".html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// the tools go into the page as stored: the build inlined below carries a "</body>" of its own
		page := strings.Replace(string(stored), "</body>", "<script>"+string(script)+"</script>"+drawProbe+"</body>", 1)
		if r.URL.Query().Get("forge") == "1" {
			page = strings.Replace(page, "<head>", "<head>"+forgingHead, 1)
		}
		page = mermaidCDNTag.ReplaceAllLiteralString(page, mermaid)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", frameCSP)
		_, _ = w.Write([]byte(page))
	})
	ln := mustLoopbackListener(t)
	go serveUntilClosed(t, ln, mux)
	return "http://" + ln.Addr().String() + "/"
}

func drawBrowser(t *testing.T) context.Context {
	t.Helper()
	ctx := frameBrowser(t)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1400, 900)); err != nil {
		t.Fatalf("size the window: %v", err)
	}
	return ctx
}

// gotMessage waits for the first message of type kind in window.got from
// index after on, and decodes it into out; it returns the index after it.
func gotMessage(t *testing.T, ctx context.Context, after int, kind string, out any) int {
	t.Helper()
	js := fmt.Sprintf(`(function(){var g=window.got||[];for(var i=%d;i<g.length;i++)if(g[i].type===%q)return JSON.stringify([i,g[i]]);return '';})()`, after, kind)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var s string
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &s)); err == nil && s != "" {
			var pair [2]json.RawMessage
			if err := json.Unmarshal([]byte(s), &pair); err != nil {
				t.Fatalf("decode %s: %v", kind, err)
			}
			var at int
			_ = json.Unmarshal(pair[0], &at)
			if err := json.Unmarshal(pair[1], out); err != nil {
				t.Fatalf("decode %s: %v", kind, err)
			}
			return at + 1
		}
	}
	got := evalString(t, ctx, `JSON.stringify((window.got||[]).map(function(m){return m.type+(m.reason?':'+m.reason:'')}))`)
	t.Fatalf("no %q message after %d; got %s", kind, after, got)
	return 0
}

type box struct {
	Name       string
	X, Y, W, H float64
}

// partOf scrolls a named part of the plan into view and returns its box in
// page coordinates (the frame sits at the page's top left).
func partOf(t *testing.T, ctx context.Context, name string) box {
	t.Helper()
	var s string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.rect=null;window.ask({want:`+jsString(name)+`})`, nil),
		chromedp.Poll(`window.rect&&window.rect.name===`+jsString(name)+`&&JSON.stringify(window.rect)`, &s, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	var b box
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// reachFrame moves the pointer over the frame until its page hears it: Chrome
// drops pointer input to a frame process that has only just loaded.
func reachFrame(t *testing.T, ctx context.Context) {
	t.Helper()
	var reached bool
	for deadline := time.Now().Add(10 * time.Second); !reached && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved", 20, 20), chromedp.Evaluate(`!!window.input`, &reached)); err != nil {
			t.Fatalf("move over the frame: %v", err)
		}
	}
	if !reached {
		t.Fatal("pointer input never reached the framed plan")
	}
}

// stroke drags out an oval around b, enclosing most of its area.
func stroke(t *testing.T, ctx context.Context, b box) {
	t.Helper()
	cx, cy := b.X+b.W/2, b.Y+b.H/2
	rx, ry := b.W/2+6, b.H/2+6
	// Deselect the previous oval before creating another over the same area.
	acts := []chromedp.Action{
		chromedp.MouseClickXY(5, 5),
		chromedp.MouseEvent("mouseMoved", cx-rx, cy-ry),
		chromedp.MouseEvent("mousePressed", cx-rx, cy-ry, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", cx-rx+2, cy-ry+2, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseMoved", cx+rx, cy+ry, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseReleased", cx+rx, cy+ry, chromedp.ButtonLeft, chromedp.ClickCount(1)),
	}
	if err := chromedp.Run(ctx, acts...); err != nil {
		t.Fatalf("draw around %s: %v", b.Name, err)
	}
}

// captured is the part of a capture a test reads.
type captured struct {
	Targets []struct {
		Anchor, Section string
		Box             []float64
		Nth             *int `json:"nth,omitempty"`
		Of              *int `json:"of,omitempty"`
	}
	Ellipse json.RawMessage
	Layout  struct{ Open, Checked []string }
	Context struct {
		Items []struct{ Anchor, Text string }
	}
}

// paintOf is the drawing a host paints for a capture, named q-<name>.
func paintOf(name string, c captured) map[string]any {
	var targets []map[string]any
	for _, t := range c.Targets {
		m := map[string]any{"anchor": t.Anchor, "box": t.Box}
		if t.Nth != nil {
			m["nth"], m["of"] = *t.Nth, *t.Of
		}
		targets = append(targets, m)
	}
	return map[string]any{"anchor": "q-" + name + "-0001", "kind": "committed", "tint": "amber", "ellipse": c.Ellipse,
		"targets": targets, "open": c.Layout.Open, "checked": c.Layout.Checked}
}

// TestBrowser_DrawingsLandByTheirTargets draws around nine parts of revision
// a and places each drawing by spec section 3: exact on a, moved on b
// (content added above), missing or ambiguous on c where its text changed,
// reflowed at 600 px, hidden while collapsed or on another tab and exact once
// shown, unchanged at 2x device pixels, and placed again after a live resize.
// Failure prevented: a drawing lands on whatever text now resembles the
// original, paints over the wrong element after an edit, vanishes while its
// section is collapsed, or stays put when the page reflows.
func TestBrowser_DrawingsLandByTheirTargets(t *testing.T) {
	ctx := drawBrowser(t)
	base := serveDrawFixture(t, drawTools)
	load := func(rev string, w, h int) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`window.load(%q,%d,%d)`, rev, w, h), nil)); err != nil {
			t.Fatalf("frame %s: %v", rev, err)
		}
		var none struct{}
		gotMessage(t, ctx, 0, "ready", &none)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.Poll(`window.ready`, &ready)); err != nil {
		t.Fatalf("open the host: %v", err)
	}
	load("a", 1280, 800)
	reachFrame(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.send({type:'mode',on:true,open:false,draw:true})`, nil)); err != nil {
		t.Fatal(err)
	}
	caps := map[string]captured{}
	n := 0
	draw := func(name, part string) {
		t.Helper()
		stroke(t, ctx, partOf(t, ctx, part))
		var c struct{ Capture captured }
		n = gotMessage(t, ctx, n, "capture", &c)
		caps[name] = c.Capture
	}
	for _, p := range []string{"sentence", "abort", "retry-node", "queue-worker", "figure", "table"} {
		draw(p, p)
	}
	draw("sentence-again", "sentence")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ask({open:true})`, nil)); err != nil {
		t.Fatal(err)
	}
	draw("notes", "notes")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ask({check:'tab-full'})`, nil)); err != nil {
		t.Fatal(err)
	}
	draw("full-tab", "full-tab")

	// what each stroke enclosed, by the anchors ox computes for the same text
	sentence := planreview.AnchorFor("Risks", "The retry path can double-fire under load when the queue is saturated.")
	for name, want := range map[string]string{
		"sentence": sentence, "sentence-again": sentence,
		"abort":      planreview.AnchorFor("Risks", "Abort if the dead-letter rate doubles."),
		"retry-node": planreview.AnchorFor("Flow", "Retry budget"),
		"notes":      planreview.AnchorFor("", "Add the budget counter beside the queue depth gauge."),
	} {
		if c := caps[name]; len(c.Targets) != 1 || c.Targets[0].Anchor != want {
			t.Errorf("%s: drew around %+v, want only %s", name, c.Targets, want)
		}
	}
	if c := caps["queue-worker"]; len(c.Targets) < 2 {
		t.Errorf("queue-worker: two nodes drawn around enclose %+v, want each node's label", c.Targets)
	}

	var drawings []map[string]any
	for name, c := range caps {
		drawings = append(drawings, paintOf(name, c))
	}
	paint, _ := json.Marshal(map[string]any{"type": "paint", "items": []any{}, "drawings": drawings})
	type placedMsg struct {
		Items []struct{ Anchor, Outcome string }
	}
	place := func(after int) (map[string]string, int) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.send(`+string(paint)+`)`, nil)); err != nil {
			t.Fatal(err)
		}
		var p placedMsg
		next := gotMessage(t, ctx, after, "placed", &p)
		out := map[string]string{}
		for _, it := range p.Items {
			out[strings.TrimSuffix(strings.TrimPrefix(it.Anchor, "q-"), "-0001")] = it.Outcome
		}
		return out, next
	}
	check := func(where string, got map[string]string, want map[string][]string) {
		t.Helper()
		for name, ok := range want {
			if !slices.Contains(ok, got[name]) {
				t.Errorf("%s: %s placed %q, want %s", where, name, got[name], strings.Join(ok, " or "))
			}
		}
	}
	one := func(s string) []string { return []string{s} }
	onPage := []string{"exact", "moved", "reflowed"}

	load("a", 1280, 800)
	got, _ := place(0)
	check("a", got, map[string][]string{
		"sentence": one("exact"), "sentence-again": one("exact"), "abort": one("exact"), "retry-node": one("exact"),
		"queue-worker": one("exact"), "figure": one("exact"), "table": one("exact"), "notes": one("hidden"), "full-tab": one("hidden"),
	})
	load("b", 1280, 800)
	got, _ = place(0)
	check("b, content added above", got, map[string][]string{
		"sentence": one("moved"), "sentence-again": one("moved"), "abort": one("moved"), "retry-node": one("moved"),
		"queue-worker": one("moved"), "figure": one("moved"), "table": one("moved"), "notes": one("hidden"), "full-tab": one("hidden"),
	})
	load("c", 1280, 800)
	got, _ = place(0)
	check("c, drawn content changed", got, map[string][]string{
		"sentence": one("missing"), "sentence-again": one("missing"), "abort": one("ambiguous"), "retry-node": one("missing"),
		"queue-worker": onPage, "figure": onPage, "table": onPage, "notes": one("hidden"), "full-tab": one("hidden"),
	})
	load("a", 600, 800)
	got, _ = place(0)
	check("a at 600 px", got, map[string][]string{"sentence": one("reflowed"), "table": one("reflowed"), "notes": one("hidden")})
	// device pixels change no CSS pixel
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1400, 900, chromedp.EmulateScale(2))); err != nil {
		t.Fatal(err)
	}
	load("a", 1280, 800)
	got, _ = place(0)
	check("a at 2x device pixels", got, map[string][]string{"sentence": one("exact"), "retry-node": one("exact"), "table": one("exact")})
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1400, 900)); err != nil {
		t.Fatal(err)
	}

	// revealing a drawing that is on the page leaves the reader's tab alone,
	// though the drawing was made with the other tab checked
	load("a", 1280, 800)
	_, at := place(0)
	// which asks the frame which rollout tab is checked; this channel and the
	// tools' are not ordered with each other, so each step waits
	which := func() string {
		t.Helper()
		var id string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.which=null;window.ask({which:true})`, nil),
			chromedp.Poll(`window.which`, &id, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ask({check:'tab-full'})`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := which(); got != "tab-full" {
		t.Fatalf("the reader's tab is %s, want tab-full before the reveal", got)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.send({type:'reveal',anchor:'q-sentence-0001',nth:0})`, nil)); err != nil {
		t.Fatal(err)
	}
	place(at) // a paint after the reveal, on its channel: its placed answer means the reveal is done
	if got := which(); got != "tab-full" {
		t.Errorf("revealing a drawing on the page switched the reader's tab to %s", got)
	}

	// hidden, then shown: Show reopens what the capture had open or checked
	load("a", 1280, 800)
	_, at = place(0)
	for _, name := range []string{"notes", "full-tab"} {
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.send({type:'reveal',anchor:'q-`+name+`-0001',nth:0})`, nil)); err != nil {
			t.Fatal(err)
		}
		var p placedMsg
		at = gotMessage(t, ctx, at, "placed", &p)
		for _, it := range p.Items {
			if it.Anchor == "q-"+name+"-0001" && it.Outcome != "exact" {
				t.Errorf("%s after Show placed %q, want exact", name, it.Outcome)
			}
		}
	}
	// a live resize places every drawing again, without a reload
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('doc').style.width='1024px'`, nil)); err != nil {
		t.Fatal(err)
	}
	var p placedMsg
	gotMessage(t, ctx, at, "placed", &p)
	for _, it := range p.Items {
		if it.Anchor == "q-sentence-0001" && it.Outcome != "moved" {
			t.Errorf("after a live resize to 1024 px the sentence placed %q, want moved", it.Outcome)
		}
	}
}

// forgingHead runs before the document tools in a plan whose own script
// keeps their end of the channel, as any script in a plan can: whatever the
// host page forges (window.ask({forge: message})) reaches the host as if the
// tools had sent it. It tells the host page {connected} when the host's first
// message arrives on that channel.
const forgingHead = `<script>var MC=MessageChannel;window.MessageChannel=function(){var c=new MC();window.toolsPort=c.port1;` +
	`c.port1.addEventListener('message',function f(){c.port1.removeEventListener('message',f);parent.postMessage({connected:true},'*')});return c};` +
	`window.addEventListener('message',function(e){if(e.data&&e.data.forge&&window.toolsPort)` +
	`window.toolsPort.postMessage(Object.assign({ox:'review',v:3,instance:'draw-host-0001'},e.data.forge))})</script>`

// drawHost is a host page that mounts the review controls around the
// fixture's frame, at the page's top left, for Devon, signed in, reviewing
// the plan at doc (a fixture revision, with any query) whose revision is
// revision. Its transport answers every operation owned and keeps what it
// was sent in window.sent. committed is a CommittedItem[] literal; extra adds
// options.
func drawHost(doc, revision, committed, extra string) string {
	return `<!doctype html><html><head><link rel="stylesheet" href="/host.css"></head><body style="margin:0">` +
		`<iframe id="doc" sandbox="allow-scripts" style="position:absolute;left:0;top:0;width:1280px;height:800px;border:0"></iframe>` +
		`<script type="module">import { connectFrame, mountHostControls } from '/index.js';` +
		`const frame = document.getElementById('doc'); window.sent = [];` +
		`window.addEventListener('message', (e) => { const d = e.data || {}; if (d.rect) window.rect = d.rect; if (d.input) window.input = true; if (d.connected) window.connected = true; if (d.ink) window.ink = d.ink; });` +
		`window.ask = (m) => frame.contentWindow.postMessage(m, '*');` +
		`const port = connectFrame(frame, 'draw-host-0001'); window.got = [];` +
		`mountHostControls({ send: port.send, listen: (fn) => port.listen((m) => { window.got.push(m); fn(m); }) }, { account: 'acct-devon', plan: 'pln_draw', slug: 'retry-budget-rollout', reviewer: 'Devon',` +
		` frame, reviewing: true, revision: '` + revision + `', committed: ` + committed + `,` +
		` transport: { send: (op) => { window.sent.push(op); return Promise.resolve({ state: 'owned', id: op.id, synced: true }); } },` + extra + ` });` +
		`frame.src = '/doc/` + doc + `#i=draw-host-0001';</script></body></html>`
}

// TestBrowser_DrawAroundReachesTheHostStamped: Devon picks Draw in the host's
// controls, loops twice around one sentence and once around a list item,
// notes each, and submits. Each drawing is its own item under a q-<id> the
// host minted, stamped with the framed revision and a time, sent under marks.
// Esc puts the pen down before it leaves review mode.
// Failure prevented: a drawing never reaches the author, two drawings of one
// sentence merge, or a release that predates drawings stores the round
// without them.
func TestBrowser_DrawAroundReachesTheHostStamped(t *testing.T) {
	ctx := drawBrowser(t)
	revision := revisionDigest(t, "a")
	base := serveDrawFixture(t, drawHost("a", revision, "[]", ""))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the host: %v", err)
	}
	reachFrame(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-draw').getAttribute('aria-pressed')`); got != "true" {
		t.Fatalf("Draw is not shown selected: aria-pressed=%s", got)
	}
	notes := []struct{ part, note string }{{"sentence", "Both retries fire here."}, {"sentence", "And again on requeue."}, {"abort", "Abort sooner."}}
	for i, d := range notes {
		reachFrame(t, ctx)
		stroke(t, ctx, partOf(t, ctx, d.part))
		if !waitJS(ctx, `document.querySelector('.rev-pop .rev-note')`, 10*time.Second) {
			t.Fatalf("drawing %d around the %s opened no note: %s", i, d.part, evalString(t, ctx, `(document.querySelector('.rev-toast')||{}).textContent`))
		}
		about := evalString(t, ctx, `document.querySelector('.rev-pop .rev-about').textContent`)
		if !strings.HasPrefix(about, "Drawing: ") {
			t.Errorf("the note on drawing %d is about %q, want the drawing", i, about)
		}
		if err := chromedp.Run(ctx, chromedp.SendKeys(".rev-pop .rev-note", d.note, chromedp.ByQuery), chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
	}
	if !waitJS(ctx, `document.querySelectorAll('.rev-rail-orig').length===3`, 5*time.Second) {
		t.Fatalf("the rail does not list three drawings, each with its original: %s", evalString(t, ctx, `document.querySelector('.rev-rail').textContent`))
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.sent.length===1`, 5*time.Second) {
		t.Fatal("Submit sent nothing")
	}
	var round struct {
		Items, Marks []struct {
			Anchor, Label, Note string
			Region              struct {
				ID       string `json:"id"`
				Revision string `json:"plan_revision"`
				Created  string `json:"created_at"`
				Targets  []struct{ Anchor string }
			}
		}
	}
	if err := json.Unmarshal([]byte(evalString(t, ctx, `window.sent[0].body`)), &round); err != nil {
		t.Fatal(err)
	}
	if len(round.Items) != 0 || len(round.Marks) != 3 {
		t.Fatalf("round sent %d items and %d marks; a round with drawings sends them all under marks, items empty", len(round.Items), len(round.Marks))
	}
	seen := map[string]bool{}
	for i, m := range round.Marks {
		r := m.Region
		if m.Anchor != "q-"+r.ID || seen[r.ID] || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(r.ID) {
			t.Errorf("mark %d: anchor %s, region id %q: want a host-minted id of its own under q-<id>", i, m.Anchor, r.ID)
		}
		seen[r.ID] = true
		if r.Revision != revision || r.Created == "" {
			t.Errorf("mark %d stamped revision %q at %q, want %s", i, r.Revision, r.Created, revision)
		}
		if m.Note != notes[i].note || !strings.HasPrefix(m.Label, "Drawing: ") {
			t.Errorf("mark %d: label %q, note %q", i, m.Label, m.Note)
		}
	}
	if round.Marks[0].Region.Targets[0].Anchor != round.Marks[1].Region.Targets[0].Anchor {
		t.Error("the two loops around one sentence enclose different elements")
	}
	// Esc puts the pen down first, then leaves review mode
	if err := chromedp.Run(ctx, chromedp.KeyEvent(kb.Escape)); err != nil {
		t.Fatal(err)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-draw').getAttribute('aria-pressed')+' '+document.querySelector('.rev-toggle').textContent`); got != "false Exit review" {
		t.Errorf("after Esc: draw pressed and review toggle %q, want the pen down and review still on", got)
	}
}

// fixtureDrawings returns captures.json's records by id: drawings made on
// revision a, each stamped as a host stamps it.
func fixtureDrawings(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "pkg", "planreview", "testdata", "drawn-context", "captures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Records []map[string]any }
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, r := range f.Records {
		out[r["id"].(string)] = r
	}
	return out
}

// committedDrawing is the CommittedItem a host reads for a stored drawing.
func committedDrawing(region map[string]any, state, note string) map[string]any {
	return map[string]any{"anchor": "q-" + region["id"].(string), "section": "Risks", "label": "Drawing: The retry path can double-fire under load when the queue is …",
		"status": "request-change", "state": state, "note": note, "reviewer": "Devon", "region": region}
}

// View original must reconstruct drawings from their records after an edit,
// show when context was truncated, and render record text without running it.
func TestBrowser_ViewOriginalAfterAnEdit(t *testing.T) {
	ctx := drawBrowser(t)
	base := serveDrawFixture(t, drawHost("a", revisionDigest(t, "a"), "[]", ""))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the host for a bounded capture: %v", err)
	}
	reachFrame(t, ctx)
	const markup = `<img src=x onerror="window.pwned=1">`
	longText := markup + " " + strings.Repeat("Retained context ", 160) + "UNRETAINED TAIL"
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ask({text:`+jsString(longText)+`})`, nil), chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	// A loop within a long paragraph still targets the paragraph, without
	// requiring a screen tall enough to circle every line of its text.
	b := partOf(t, ctx, "sentence")
	b.X, b.Y, b.W, b.H = b.X+b.W/4, b.Y+80, b.W/2, 80
	stroke(t, ctx, b)
	if !waitJS(ctx, `document.querySelector('.rev-pop .rev-note')`, 10*time.Second) {
		t.Fatalf("drawing over long text opened no note: %s", evalString(t, ctx, `(document.querySelector('.rev-toast')||{}).textContent`))
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys(".rev-pop .rev-note", "Keep the bounded original.", chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery), chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.sent.length===1`, 5*time.Second) {
		t.Fatal("Submit sent no bounded drawing")
	}
	var round struct {
		Marks []struct{ Region map[string]any }
	}
	if err := json.Unmarshal([]byte(evalString(t, ctx, `window.sent[0].body`)), &round); err != nil || len(round.Marks) != 1 {
		t.Fatalf("decode the submitted bounded drawing: %v; %d marks", err, len(round.Marks))
	}
	bounded := round.Marks[0].Region
	capturedContext := bounded["context"].(map[string]any)
	items := capturedContext["items"].([]any)
	if capturedContext["truncated"] != true || len(items) != 1 || items[0].(map[string]any)["text"] != longText[:1999]+"…" {
		t.Fatalf("long paragraph was not retained as one bounded target with truncation: %s", evalString(t, ctx, `JSON.stringify(JSON.parse(window.sent[0].body).marks[0].region.context)`))
	}
	for _, part := range []struct {
		name  string
		value any
		limit int
	}{
		{"context", capturedContext, 16384},
		{"region", bounded, 65536},
	} {
		encoded, err := json.Marshal(part.value)
		if err != nil || len(encoded) > part.limit {
			t.Fatalf("submitted %s is %d bytes, limit %d: %v", part.name, len(encoded), part.limit, err)
		}
	}
	recs := fixtureDrawings(t)
	r1, r9 := recs["r1-double-fire"], recs["r9-mermaid-renamed-node"]
	// a neighbour's text that looks like markup
	r1["context"].(map[string]any)["items"].([]any)[1].(map[string]any)["text"] = `<img src=x onerror="window.pwned=1">`
	nodeItem := committedDrawing(r9, "open", "Rename it?")
	nodeItem["section"], nodeItem["label"] = "Flow", "Drawing: Retry budget"
	committed, _ := json.Marshal([]any{committedDrawing(r1, "open", "Both retries fire here."), nodeItem, committedDrawing(bounded, "open", "Keep the bounded original.")})
	base = serveDrawFixture(t, drawHost("c", revisionDigest(t, "c"), string(committed), ""))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-rail-orig", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the host: %v", err)
	}
	if !waitJS(ctx, `document.querySelectorAll('.rev-rail-item').length===3&&[].every.call(document.querySelectorAll('.rev-rail-item'),function(li){return li.textContent.indexOf('text changed')>=0})`, 10*time.Second) {
		t.Fatalf("the rail does not list all drawings with their text changed: %s", evalString(t, ctx, `document.querySelector('.rev-rail').textContent`))
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#doc').remove()`, nil)); err != nil {
		t.Fatal(err)
	}
	open := func(note string) string {
		t.Helper()
		js := `(function(){var li=[].filter.call(document.querySelectorAll('.rev-rail-item'),function(l){return l.textContent.indexOf(` + jsString(note) + `)>=0})[0];li.querySelector('.rev-rail-orig').click();return true})()`
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, nil), chromedp.WaitVisible(".rev-original svg", chromedp.ByQuery)); err != nil {
			t.Fatalf("View original of %q: %v", note, err)
		}
		return evalString(t, ctx, `document.querySelector('.rev-original').textContent`)
	}
	got := open("Both retries fire here.")
	for _, want := range []string{"Drawn on revision 95eb8beda729 at 1280 × 800, dark", "not the revision shown", "The retry path can double-fire", `<img src=x onerror="window.pwned=1">`} {
		if !strings.Contains(got, want) {
			t.Errorf("View original of the sentence: %q lacks %q", got, want)
		}
	}
	if n := evalString(t, ctx, `document.querySelectorAll('.rev-original img').length+':'+window.pwned`); n != "0:undefined" {
		t.Errorf("record text became markup in the host: img count and pwned = %s", n)
	}
	got = open("Keep the bounded original.")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(".rev-original .rev-orig-cut", chromedp.ByQuery)); err != nil {
		t.Fatalf("truncation notice is not visible: %v", err)
	}
	if !strings.Contains(got, "Context truncated: some of what lay here was not kept.") ||
		!strings.Contains(got, markup) || !strings.Contains(got, "Retained context") || strings.Contains(got, "UNRETAINED TAIL") {
		t.Errorf("View original lost the retained context or its truncation notice: %q", got)
	}
	if got := evalString(t, ctx, `document.querySelectorAll('.rev-original .rev-orig-box').length+':'+document.querySelectorAll('.rev-original .rev-orig-ink path').length+':'+document.querySelectorAll('.rev-original img').length+':'+window.pwned`); got != "1:2:0:undefined" {
		t.Errorf("bounded original did not reconstruct safe context and ink from its record alone: %s", got)
	}
	if got := open("Rename it?"); !strings.Contains(got, "Retry budget") {
		t.Errorf("View original of the renamed node: %q lacks the node as drawn", got)
	}
	if got := evalString(t, ctx, `document.querySelectorAll('.rev-original .rev-orig-cut').length`); got != "0" {
		t.Errorf("complete context has a truncation notice: %s", got)
	}
	if err := chromedp.Run(ctx, chromedp.KeyEvent(kb.Escape)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `!document.querySelector('.rev-original')`, 3*time.Second) {
		t.Error("Esc left View original open")
	}
}

// TestBrowser_DrawingsStayIndependentThroughEditAndReopen: of two stored
// drawings around one sentence, Devon edits the open one's note from its row
// and reopens the addressed one; each operation carries only its own drawing,
// whole, under marks.
// Failure prevented: editing or reopening one drawing changes the other, or
// changes submitted geometry, or sends a drawing without its region.
func TestBrowser_DrawingsStayIndependentThroughEditAndReopen(t *testing.T) {
	ctx := drawBrowser(t)
	recs := fixtureDrawings(t)
	addressed := committedDrawing(recs["r1-double-fire"], "addressed", "Both retries fire here.")
	addressed["resolutionNote"] = "Bounded the retries."
	committed, _ := json.Marshal([]any{addressed, committedDrawing(recs["r2-double-fire-again"], "open", "And again on requeue.")})
	base := serveDrawFixture(t, drawHost("a?forge=1", revisionDigest(t, "a"), string(committed), " resolve: true,"))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-rail-orig", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the host: %v", err)
	}
	row := func(note string) {
		t.Helper()
		js := `(function(){var li=[].filter.call(document.querySelectorAll('.rev-rail-item'),function(l){return l.textContent.indexOf(` + jsString(note) + `)>=0})[0];li.querySelector('.rev-rail-body').click();return true})()`
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, nil), chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery)); err != nil {
			t.Fatalf("open the row %q: %v", note, err)
		}
	}
	row("And again on requeue.")
	// a stored mark opens a new one of Devon's own, as for any comment
	if err := chromedp.Run(ctx, chromedp.SendKeys(".rev-pop .rev-note", "Still fires twice.", chromedp.ByQuery), chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	// A new note on a submitted drawing must not unlock its geometry, even
	// when the framed plan proposes an edit directly on the tools' channel.
	forged := map[string]any{}
	for k, v := range recs["r2-double-fire-again"] {
		forged[k] = v
	}
	forged["ellipse"] = map[string]any{"center": []int{10, 10}, "radii": []int{111, 111}, "rotation": 0, "start": 0, "dir": 1}
	msg, _ := json.Marshal(map[string]any{"type": "capture", "anchor": "q-r2-double-fire-again", "capture": forged, "rect": map[string]any{"left": 10, "bottom": 121}})
	if err := chromedp.Run(ctx, chromedp.Click(".rev-draw", chromedp.ByQuery), chromedp.Evaluate(`window.ask({forge:`+string(msg)+`})`, nil)); err != nil {
		t.Fatal(err)
	}
	var ignored struct{}
	gotMessage(t, ctx, 0, "capture", &ignored)
	row("Bounded the retries.")
	if err := chromedp.Run(ctx, chromedp.SendKeys(".rev-pop .rev-note", "Not bounded yet.", chromedp.ByQuery), chromedp.Click(".rev-pop .rev-reopen", chromedp.ByQuery)); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if !waitJS(ctx, `window.sent.length===1`, 5*time.Second) {
		t.Fatal("Reopen sent nothing")
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.sent.length===2`, 5*time.Second) {
		t.Fatal("Submit sent nothing")
	}
	type mark struct {
		Anchor, Status, Note string
		Region               struct {
			ID      string
			Ellipse json.RawMessage
		}
	}
	var sent []struct{ Items, Marks []mark }
	for i := range 2 {
		var r struct{ Items, Marks []mark }
		if err := json.Unmarshal([]byte(evalString(t, ctx, fmt.Sprintf(`window.sent[%d].body`, i))), &r); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, r)
	}
	want := []mark{
		{Anchor: "q-r1-double-fire", Status: "request-change", Note: "Not bounded yet."},
		{Anchor: "q-r2-double-fire-again", Status: "request-change", Note: "Still fires twice."},
	}
	for i, w := range want {
		if len(sent[i].Items) != 0 || len(sent[i].Marks) != 1 {
			t.Fatalf("operation %d sent items %+v, marks %+v; want its one drawing under marks", i, sent[i].Items, sent[i].Marks)
		}
		got := sent[i].Marks[0]
		if got.Anchor != w.Anchor || got.Status != w.Status || got.Note != w.Note || "q-"+got.Region.ID != w.Anchor {
			t.Errorf("operation %d sent %+v, want %+v with its own region", i, got, w)
		}
		var ellipse any
		if err := json.Unmarshal(got.Region.Ellipse, &ellipse); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ellipse, recs[got.Region.ID]["ellipse"]) {
			t.Errorf("operation %d changed submitted drawing geometry: %s", i, got.Region.Ellipse)
		}
	}
}

// TestBrowser_ForgedCapturesAreCheckedAndRestamped: the plan's own script
// forges captures on the document tools' channel. With the Draw tool down
// none opens a note; with it up a malformed or oversized one opens none, and
// a well-formed one is sent with only a region's fields, under the id,
// revision and time the host gave it.
// Failure prevented: a plan opens the trusted note box when the reviewer did
// not draw, stores an unbounded record, or names the drawing, its revision or
// its time.
func TestBrowser_ForgedCapturesAreCheckedAndRestamped(t *testing.T) {
	ctx := drawBrowser(t)
	revision := revisionDigest(t, "a")
	base := serveDrawFixture(t, drawHost("a?forge=1", revision, "[]", ""))
	if err := chromedp.Run(ctx, chromedp.Navigate(base), chromedp.WaitVisible(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the host: %v", err)
	}
	r1 := fixtureDrawings(t)["r1-double-fire"]
	forged := map[string]any{}
	for k, v := range r1 {
		forged[k] = v
	}
	forged["id"], forged["plan_revision"], forged["reviewer"] = "forged-0001", "sha256:"+strings.Repeat("0", 64), "Mallory"
	capture := func(c any) string {
		b, _ := json.Marshal(map[string]any{"type": "capture", "capture": c, "rect": map[string]any{"left": 300, "bottom": 300}})
		return string(b)
	}
	forge := func(msg string) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.ask({forge:`+msg+`})`, nil)); err != nil {
			t.Fatal(err)
		}
	}
	// the host holds the channel's other end only once the tools say ready,
	// after Mermaid draws: a forge sent before then would land late, after
	// the Draw tool is up
	if !waitJS(ctx, `window.connected&&document.querySelector('.rev-toggle').textContent==='Exit review'`, 10*time.Second) {
		t.Fatal("the host never connected to the plan in review mode")
	}
	forge(capture(forged))
	if waitJS(ctx, `document.querySelector('.rev-pop')`, time.Second) {
		t.Fatal("a capture opened a note while the Draw tool was down")
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	malformed := map[string]any{}
	for k, v := range forged {
		malformed[k] = v
	}
	malformed["v"] = 2
	big := map[string]any{}
	for k, v := range forged {
		big[k] = v
	}
	var items []any
	for range 48 {
		items = append(items, map[string]any{"anchor": "h21a13288", "tag": "td", "box": []int{0, 0, 1, 1}, "text": strings.Repeat("x", 600)})
	}
	big["context"] = map[string]any{"items": items, "truncated": false}
	for name, c := range map[string]any{"malformed": malformed, "oversized": big} {
		forge(capture(c))
		if waitJS(ctx, `document.querySelector('.rev-pop')`, time.Second) {
			t.Fatalf("a %s capture opened a note", name)
		}
	}
	forge(capture(forged))
	if !waitJS(ctx, `document.querySelector('.rev-pop .rev-save')`, 5*time.Second) {
		t.Fatal("a well-formed capture opened no note while the Draw tool was up")
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery), chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.sent.length===1`, 5*time.Second) {
		t.Fatal("Submit sent nothing")
	}
	var round struct {
		Marks []struct{ Region map[string]any }
	}
	if err := json.Unmarshal([]byte(evalString(t, ctx, `window.sent[0].body`)), &round); err != nil {
		t.Fatal(err)
	}
	if len(round.Marks) != 1 {
		t.Fatalf("sent %d marks", len(round.Marks))
	}
	got := round.Marks[0].Region
	if got["id"] == "forged-0001" || got["plan_revision"] != revision || got["reviewer"] != nil || got["created_at"] == r1["created_at"] {
		t.Errorf("the plan's claims reached the record: id %v, revision %v, reviewer %v, created_at %v", got["id"], got["plan_revision"], got["reviewer"], got["created_at"])
	}
	for k := range got {
		if _, ok := r1[k]; !ok {
			t.Errorf("the record holds %q, which a region does not name", k)
		}
	}
}

// TestBrowser_DrawOnTheLocalReviewPage is the `ox plan review` path: Devon
// loops twice around the first risk with a note on each and submits. The
// Ledger holds both drawings under their own q-<id> anchors, stamped with the
// revision of the plan.md the page was rendered from; the reloaded page
// paints them; resolving one leaves the other open.
// Failure prevented: a drawing on ox's own page never reaches the agent, is
// stored without what was drawn, names a revision the page was not rendered
// from, or merges with another drawing of the same item.
func TestBrowser_DrawOnTheLocalReviewPage(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	md := "# Draw Roundtrip\n\nShip the budget this week.\n\n## Risks\n\n- The retry path can double-fire under load.\n- Metrics lag the budget by one scrape.\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{Topic: "Draw Roundtrip", Slug: "draw-roundtrip"})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	base, planDir, _ := serveSavedPlanReview(t, gitRoot, "draw-roundtrip", dir)
	openReviewer(t, ctx, base)
	if err := chromedp.Run(ctx, chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatalf("the page offers no Draw: %v", err)
	}
	risks := `(function(){var p=[].filter.call(document.querySelectorAll('li'),function(p){return p.textContent.indexOf('double-fire')>=0})[0];` +
		`p.scrollIntoView({block:'center'});var r=p.getBoundingClientRect();return JSON.stringify({name:'risk',x:r.left,y:r.top,w:r.width,h:r.height})})()`
	for _, note := range []string{"Both retries fire here.", "And again on requeue."} {
		var b box
		if err := json.Unmarshal([]byte(evalString(t, ctx, risks)), &b); err != nil {
			t.Fatal(err)
		}
		stroke(t, ctx, b)
		if err := chromedp.Run(ctx, chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery),
			chromedp.SendKeys(".rev-pop .rev-note", note, chromedp.ByQuery), chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery)); err != nil {
			t.Fatalf("note the drawing %q: %v (toast %q)", note, err, evalString(t, ctx, `(document.querySelector('.rev-toast')||{}).textContent`))
		}
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil), chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	var rev plan.Review
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if rev = plan.ReadReview(planDir); len(rev.Items) == 2 {
			break
		}
	}
	stored, err := os.ReadFile(filepath.Join(planDir, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Items) != 2 {
		t.Fatalf("the Ledger holds %d items (failures %+v), want the two drawings", len(rev.Items), rev.Failures)
	}
	var anchors []string
	for _, it := range rev.Items {
		var r struct {
			ID       string `json:"id"`
			Revision string `json:"plan_revision"`
		}
		if err := json.Unmarshal(it.Region, &r); err != nil || it.Anchor != "q-"+r.ID {
			t.Fatalf("item %s carries region %s (%v), want its own record", it.Anchor, it.Region, err)
		}
		if r.Revision != planRevision(stored) || it.Label != "Drawing: The retry path can double-fire under load." || it.Section != "Risks" || it.Reviewer != "Devon" {
			t.Errorf("item %s: revision %s (plan.md is %s), label %q, section %q, reviewer %q", it.Anchor, r.Revision, planRevision(stored), it.Label, it.Section, it.Reviewer)
		}
		anchors = append(anchors, it.Anchor)
	}
	// the reloaded page paints both from the Ledger
	painted := `document.querySelectorAll('.rev-ink [data-anchor^="q-"]').length===2`
	if !waitForNewPage(ctx, painted, 10*time.Second) {
		t.Fatalf("the reloaded page does not paint the stored drawings: %s", evalString(t, ctx, `document.querySelectorAll('.rev-ink [data-anchor]').length`))
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil)); err != nil {
		t.Fatal(err)
	}
	resolveForTest(t, planDir, anchors[0], planreview.StateAddressed, "Bounded the retries.")
	if !waitForNewPage(ctx, `document.querySelectorAll('.rev-rail-tag.addressed').length===1`, 10*time.Second) {
		t.Fatal("the page does not reload with one drawing addressed")
	}
	for _, it := range plan.ReadReview(planDir).Items {
		if open := it.Anchor != anchors[0]; it.Open != open {
			t.Errorf("after resolving %s, %s open=%v", anchors[0], it.Anchor, it.Open)
		}
	}
}

// TestBrowser_DrawingNeverEnclosesTheReviewControls: on `ox plan review`'s
// own page the rail shares the plan's document. Devon leaves an unsent note
// and draws a loop around the rail that lists it: no drawing is made, and the
// note's text is in no draft but its own.
// Failure prevented: a drawing records the rail's rows, reviewer names and
// unsent notes as if they were the plan, and the next render reports it
// missing because the rail's text changed.
func TestBrowser_DrawingNeverEnclosesTheReviewControls(t *testing.T) {
	ctx := frameBrowser(t)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1500, 900)); err != nil {
		t.Fatal(err)
	}
	base, _, _ := serveLivePlanReview(t)
	openReviewer(t, ctx, base)
	const secret = "Rail-only note about the rollout."
	markSection(t, ctx, "section[id]:last-of-type", secret)
	if !waitJS(ctx, `getComputedStyle(document.querySelector('.rev-rail')).position==='fixed'&&document.querySelector('.rev-rail').textContent.indexOf(`+jsString(secret)+`)>=0`, 5*time.Second) {
		t.Fatalf("the rail does not list the note, fixed beside the plan: %s", evalString(t, ctx, `document.querySelector('.rev-rail').textContent`))
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-draw", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var b box
	if err := json.Unmarshal([]byte(evalString(t, ctx, `(function(){var r=document.querySelector('.rev-rail').getBoundingClientRect();return JSON.stringify({name:'rail',x:r.left-8,y:r.top-8,w:r.width+16,h:r.height+16})})()`)), &b); err != nil {
		t.Fatal(err)
	}
	stroke(t, ctx, b)
	if !waitJS(ctx, `(document.querySelector('.rev-toast')||{}).textContent.indexOf('Nothing in that oval')===0`, 5*time.Second) {
		t.Errorf("a loop around the rail did not say nothing can take a comment; note open: %s, toast: %q", evalString(t, ctx, `!!document.querySelector('.rev-pop')`),
			evalString(t, ctx, `(document.querySelector('.rev-toast')||{}).textContent`))
	}
	if got := evalString(t, ctx, jsMarks); strings.Count(got, secret) != 1 {
		t.Errorf("the note's text is stored %d times, want once, in its own draft: %s", strings.Count(got, secret), got)
	}
}
