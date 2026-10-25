//go:build browser

// plan_review_presence_browser_test.go proves, in real Chrome, live pointers
// between review pages: a local `ox plan review` page and a host that frames
// the plan with the npm build, as a hosted viewer does, and two local pages on
// different revisions. fakePresence stands in for SageOx's presence service.
// The framed host reads the npm build: run `make plan-review` first; it skips
// without one.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/plan"
)

// presencePlan is an authored plan whose words wrap differently at each
// width, with a collapsed section, and a spacer so the page scrolls.
// data-probe names the parts the test points at.
const presencePlan = `<!doctype html><html><head><meta charset="utf-8"><title>Presence</title>` +
	`<meta name="ox-plan-slug" content="presence"></head><body style="max-width:900px;margin:40px auto;padding:0 60px;font:16px/1.6 sans-serif">` +
	`<h1>Presence</h1><section id="risks"><h2>Risks</h2>` +
	`<p>The retry path can fire twice when the queue backs up and two workers pick the same job, and the second write lands on a ` +
	`<span data-probe="word">double-fire</span> that this plan exists to prevent before the rollout reaches every region.</p>` +
	`<details><summary data-probe="summary">Rollback</summary><p>Restore the old <span data-probe="inside">consumer</span> first.</p></details>` +
	`<p>Ship behind a flag.</p></section><div style="height:1600px"></div></body></html>`

// presenceProbe runs in the framed plan before the document tools: it reports
// when pointer input first reaches the frame, where each data-probe part is,
// on load and when asked, and every message the host sends the document,
// which a plan's own script can read.
const presenceProbe = `<script>document.addEventListener('mousemove',function m(){document.removeEventListener('mousemove',m,true);parent.postMessage({input:true},'*')},true);var MC=MessageChannel;window.MessageChannel=function(){var c=new MC();` +
	`c.port1.addEventListener('message',function(e){parent.postMessage({heard:JSON.stringify(e.data)},'*')});return c};` +
	`function probe(){var p={};document.querySelectorAll('[data-probe]').forEach(function(el){var r=el.getBoundingClientRect();` +
	`p[el.getAttribute('data-probe')]={x:r.left+r.width/2,y:r.top+r.height/2,left:r.left,top:r.top,right:r.right,bottom:r.bottom}});parent.postMessage({probe:p},'*')}` +
	`window.addEventListener('load',probe);window.addEventListener('message',function(e){if(e.source===parent&&e.data&&e.data.reprobe)probe()})</script>`

// presenceHost serves, on one origin, the presence service f and a host page
// that frames doc at an offset beside its own navigation, mounting the review
// controls for reviewer with live presence through f, the way a hosted viewer
// would: an EventSource for the room's events and a POST for each move.
func presenceHost(t *testing.T, f *fakePresence, doc []byte, revision, token, reviewer string) string {
	t.Helper()
	dist := filepath.Join(packageDir, "..", "..", "packages", "plan-review", "dist")
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
	room := "/api/v1/plans/" + f.plan + "/presence"
	host := `<!doctype html><html><head><link rel="stylesheet" href="/host.css"><style>body{margin:0}` +
		`#doc{position:fixed;left:200px;top:60px;width:760px;height:800px;border:12px solid #333}</style></head><body>` +
		`<iframe id="doc" sandbox="allow-scripts"></iframe><script type="module">import { connectFrame, mountHostControls } from '/index.js';` +
		`const frame = document.getElementById('doc'); window.heard = [];` +
		`window.addEventListener('message', (e) => { const d = e.data || {}; if (d.probe) window.probe = d.probe; if (d.heard) window.heard.push(d.heard); if (d.input) window.input = true; });` +
		`window.reprobe = () => { window.probe = null; frame.contentWindow.postMessage({ reprobe: true }, '*'); };` +
		`const room = '` + room + `', auth = 'access_token=` + token + `';` +
		`const presence = { join(revision, on) { let self = '';` +
		` const es = new EventSource(room + '?revision=' + encodeURIComponent(revision) + '&' + auth);` +
		` es.addEventListener('welcome', (e) => { const d = JSON.parse(e.data); self = d.self; on({ type: 'welcome', self: d.self, peers: d.peers }); });` +
		` es.addEventListener('peer', (e) => on({ type: 'peer', peer: JSON.parse(e.data) }));` +
		` es.addEventListener('leave', (e) => on({ type: 'leave', id: JSON.parse(e.data).id }));` +
		` es.onerror = () => on({ type: 'lost' });` +
		` return { move(at) { if (self) fetch(room + '/' + self + '?' + auth, { method: 'POST', body: JSON.stringify({ at }) }); }, leave() { es.close(); } }; } };` +
		`mountHostControls(connectFrame(frame, '` + frameInstance + `'), { account: 'acct', plan: '` + f.plan + `', slug: 'presence', committed: [], reviewer: '` + reviewer + `', frame,` +
		` transport: { send: () => Promise.resolve({ state: 'offline' }) }, revision: '` + revision + `', presence });` +
		`frame.src = '/doc';</script></body></html>`
	framed := strings.Replace(string(doc), "</body>", `<script data-instance="`+frameInstance+`">`+string(script)+`</script>`+presenceProbe+"</body>", 1)

	mux := http.NewServeMux()
	mux.Handle("/api/", f.mux)
	serve := func(path, kind, body string, csp bool) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", kind)
			if csp {
				w.Header().Set("Content-Security-Policy", frameCSP)
			}
			_, _ = w.Write([]byte(body))
		})
	}
	serve("/{$}", "text/html; charset=utf-8", host, false)
	serve("/index.js", "text/javascript", string(index), false)
	serve("/host.css", "text/css", string(css), false)
	serve("/doc", "text/html; charset=utf-8", framed, true)
	ln := mustLoopbackListener(t)
	go serveUntilClosed(t, ln, mux)
	return "http://" + ln.Addr().String()
}

// servePresenceReview serves the live review loop for the plan in dir, with a
// bridge that joins the room of planID at base as the holder of token, and
// returns its URL and how to stop it, as if the process exited.
func servePresenceReview(t *testing.T, gitRoot, slug, dir, planID, base, token string) (url string, stop func()) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	b := &presenceBridge{base: base, planID: planID, tabs: map[string]*presenceTab{},
		token: func(context.Context, bool) (string, error) { return token, nil }}
	srv.Config.Handler = liveReviewHandlerWithActivity(gitRoot, slug, dir, "http://"+srv.Listener.Addr().String(), "secret",
		newBroadcaster(), make(chan int, 8), make(chan struct{}, 1), nil, b)
	srv.Start()
	// Close waits for open requests, and a page's event stream stays open
	// until its tab closes: end the connections first
	stop = func() { srv.Listener.Close(); srv.CloseClientConnections(); srv.Close() }
	t.Cleanup(stop)
	return srv.URL, stop
}

// saveAuthoredPresencePlan saves html as an authored plan and returns its
// dir, its pln_ id, and the bytes stored for it, which a host frames.
func saveAuthoredPresencePlan(t *testing.T, gitRoot, html string) (dir, planID string, stored []byte) {
	t.Helper()
	dir = savePlanArtifacts(gitRoot, plan.Input{Raw: "# Presence\n\n## Risks\n\nThe retry path can fire twice.\n"}, plan.Result{}, []byte(html), plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("the authored plan was not saved")
	}
	if planID = planIDForDir(dir); planID == "" {
		t.Fatal("a saved plan has a pln_ id")
	}
	path, _, _, ok := plan.PlanHTMLPath(dir)
	stored, err := os.ReadFile(path)
	if !ok || err != nil {
		t.Fatalf("read the stored plan: %v", err)
	}
	return dir, planID, stored
}

type spot struct{ X, Y, Left, Top, Right, Bottom float64 }

// cursorOf is where the cursor named name is drawn, in its page's viewport,
// with how many cursors carry that name: its arrow's tip is the element's
// top-left corner.
func cursorOf(t *testing.T, ctx context.Context, name string) (at [2]float64, n int) {
	t.Helper()
	var s string
	quoted, _ := json.Marshal(name)
	js := `JSON.stringify([...document.querySelectorAll('.rev-cursor')].filter((c)=>c.textContent===` + string(quoted) + `).map((c)=>{const r=c.getBoundingClientRect();return [r.left,r.top]}))`
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &s)); err != nil {
		t.Fatal(err)
	}
	var all [][2]float64
	_ = json.Unmarshal([]byte(s), &all)
	if len(all) > 0 {
		at = all[0]
	}
	return at, len(all)
}

// waitCursor waits until the cursor named name is drawn within the box (in
// its page's viewport), and fails with where it was otherwise.
func waitCursor(t *testing.T, ctx context.Context, name, what string, within func() spot) {
	t.Helper()
	var at [2]float64
	var n int
	var b spot
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		at, n = cursorOf(t, ctx, name)
		b = within()
		if n == 1 && at[0] >= b.Left-1 && at[0] <= b.Right+1 && at[1] >= b.Top-1 && at[1] <= b.Bottom+1 {
			return
		}
	}
	t.Fatalf("%s: %d cursor(s) named %s, at %v; want one within %+v; cursors %s", what, n, name, at, b, evalString(t, ctx, `[...document.querySelectorAll('.rev-cursor')].map((c)=>c.textContent+'@'+c.style.transform).join(' ')+' | here '+document.querySelector('.rev-here').textContent+' | vw '+innerWidth+'x'+innerHeight`))
}

func localSpot(t *testing.T, ctx context.Context, part string) spot {
	t.Helper()
	var s spot
	js := `(()=>{const r=document.querySelector('[data-probe=` + part + `]').getBoundingClientRect();return {X:r.left+r.width/2,Y:r.top+r.height/2,Left:r.left,Top:r.top,Right:r.right,Bottom:r.bottom}})()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &s)); err != nil {
		t.Fatal(err)
	}
	return s
}

// framedSpot is where a part of the framed plan is, in the host page's
// viewport: the probe's frame coordinates shifted by the frame's box inside
// its border.
func framedSpot(t *testing.T, ctx context.Context, part string) spot {
	t.Helper()
	if !waitJS(ctx, `window.probe&&window.probe.`+part, 5*time.Second) {
		t.Fatalf("the framed plan never reported where %s is", part)
	}
	var s spot
	js := `(()=>{const d=document.getElementById('doc'),f=d.getBoundingClientRect(),l=f.left+d.clientLeft,t=f.top+d.clientTop,p=window.probe.` + part + `;` +
		`return {X:l+p.x,Y:t+p.y,Left:l+p.left,Top:t+p.top,Right:l+p.right,Bottom:t+p.bottom}})()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &s)); err != nil {
		t.Fatal(err)
	}
	return s
}

func pointAt(t *testing.T, ctx context.Context, s spot) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved", s.X-3, s.Y), chromedp.MouseEvent("mouseMoved", s.X, s.Y)); err != nil {
		t.Fatal(err)
	}
}

// pointIntoFrame is pointAt for a part of the framed plan. Chrome drops
// pointer input to a frame process that has only just loaded, so it moves
// over the part until the frame hears it.
func pointIntoFrame(t *testing.T, ctx context.Context, part string) {
	t.Helper()
	var reached bool
	for deadline := time.Now().Add(10 * time.Second); !reached && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		s := framedSpot(t, ctx, part)
		if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved", s.X-3, s.Y), chromedp.Evaluate(`!!window.input`, &reached)); err != nil {
			t.Fatal(err)
		}
	}
	if !reached {
		t.Fatal("pointer input never reached the framed plan")
	}
	pointAt(t, ctx, framedSpot(t, ctx, part))
}

// TestBrowser_LocalAndFramedReviewersSeeEachOthersPointers puts Devon on the
// local review page at a desktop width and Avery on a host that frames the
// same revision at another width, offset in its page. Each sees the other's
// pointer, with their name, on the same word, through a scroll; a pointer in
// a section collapsed on the other side is not drawn until it is opened
// there; the framed plan never hears a name; and Devon reloading leaves one
// cursor of his, not a ghost.
// Failure prevented: cursors are shared as screen positions and land on other
// words at another width or scroll, a pointer is painted over a collapsed
// section as if on what follows it, a plan's own script learns who is
// reviewing it, or a reload leaves a stale cursor behind.
func TestBrowser_LocalAndFramedReviewersSeeEachOthersPointers(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	dir, planID, stored := saveAuthoredPresencePlan(t, gitRoot, presencePlan)
	f := fakePresenceFor(planID, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}, "tok-avery": {"Avery", "u-avery"}})
	hosted := presenceHost(t, f, stored, planRevision(stored), "tok-avery", "Avery")
	review, stopReview := servePresenceReview(t, gitRoot, "presence", dir, planID, hosted, "tok-devon")

	// each reviewer has a browser of their own, as on two machines: a second
	// tab would sit in the background, where a page renders no frames
	local := frameBrowser(t)
	if err := chromedp.Run(local, chromedp.EmulateViewport(1280, 900), chromedp.Navigate(review+"/"), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the local review page: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1000, 900), chromedp.Navigate(hosted+"/"), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
		t.Fatalf("open the framed host: %v", err)
	}
	if !waitJS(local, `document.querySelector('.rev-peer[title="Avery is here"]')`, 8*time.Second) ||
		!waitJS(ctx, `document.querySelector('.rev-peer[title="Devon is here"]')`, 8*time.Second) {
		t.Fatalf("each reviewer must see the other is here: local %s, framed %s",
			evalString(t, local, `document.querySelector('.rev-here')?.outerHTML`), evalString(t, ctx, `document.querySelector('.rev-here')?.outerHTML`))
	}

	pointAt(t, local, localSpot(t, local, "word"))
	waitCursor(t, ctx, "Devon", "Devon's pointer in the framed plan", func() spot { return framedSpot(t, ctx, "word") })

	if err := chromedp.Run(local, chromedp.Evaluate(`window.chip = document.querySelector('.rev-peer[title="Avery is here"]')`, nil)); err != nil {
		t.Fatal(err)
	}
	pointIntoFrame(t, ctx, "word")
	waitCursor(t, local, "Avery", "Avery's pointer on the local page", func() spot { return localSpot(t, local, "word") })
	if got := evalString(t, local, `document.querySelector('.rev-peer[title="Avery is here"]') === window.chip`); got != "true" {
		t.Fatal("a teammate's moves must not redraw their chip: its name, in its title, would vanish under the reader's hover")
	}
	if err := chromedp.Run(local, chromedp.Evaluate(`window.scrollBy(0, 120)`, nil)); err != nil {
		t.Fatal(err)
	}
	waitCursor(t, local, "Avery", "Avery's pointer after the local page scrolls", func() spot { return localSpot(t, local, "word") })

	// Devon opens the collapsed section and points into it: closed on Avery's
	// side, there is nothing to draw there until Avery opens it too
	if err := chromedp.Run(local, chromedp.Evaluate(`document.querySelector('details').open = true`, nil)); err != nil {
		t.Fatal(err)
	}
	pointAt(t, local, localSpot(t, local, "inside"))
	time.Sleep(time.Second)
	if _, n := cursorOf(t, ctx, "Devon"); n != 0 {
		t.Fatal("a pointer in a section collapsed on this page must not be drawn")
	}
	summary := framedSpot(t, ctx, "summary")
	if err := chromedp.Run(ctx, chromedp.MouseClickXY(summary.X, summary.Y), chromedp.Evaluate(`window.reprobe()`, nil)); err != nil {
		t.Fatal(err)
	}
	waitCursor(t, ctx, "Devon", "Devon's pointer once the section opens", func() spot { return framedSpot(t, ctx, "inside") })

	var heard []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.heard`, &heard)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(heard, "\n")
	if !strings.Contains(all, `"locate"`) {
		t.Fatalf("the probe must hear the host's locate messages for this check to mean anything: %s", all)
	}
	for _, secret := range []string{"Devon", "Avery", "tok-", "u-devon", "conn-"} {
		if strings.Contains(all, secret) {
			t.Fatalf("the framed plan heard %q: names and connections stay in the host: %s", secret, all)
		}
	}

	// a reload is a new connection; the old one leaves, so Devon has one cursor
	if err := chromedp.Run(local, chromedp.Reload(), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	pointAt(t, local, localSpot(t, local, "word"))
	waitCursor(t, ctx, "Devon", "Devon's one cursor after he reloads", func() spot { return framedSpot(t, ctx, "word") })

	// the plan reloads in Avery's frame: its old document says her pointer
	// left as it goes, since the reloaded one cannot reach her controls
	pointIntoFrame(t, ctx, "word")
	waitCursor(t, local, "Avery", "Avery's pointer before her frame reloads", func() spot { return localSpot(t, local, "word") })
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('doc').src = '/doc?again'`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(local, `![...document.querySelectorAll('.rev-cursor')].some((c) => c.textContent === 'Avery')`, 5*time.Second) {
		t.Fatal("a reloaded frame must not leave its reviewer's last pointer on teammates' pages")
	}

	// the review server goes away: the page stops showing who was here
	stopReview()
	if !waitJS(local, `document.querySelector('.rev-here').hidden&&!document.querySelector('.rev-cursor')`, 8*time.Second) {
		t.Fatalf("with its review server gone, the page must not keep showing who was here: %s", evalString(t, local, `document.querySelector('.rev-here').outerHTML`))
	}
}

// TestBrowser_PointerOnAnotherRevisionIsNeverDrawn joins two local review
// pages to one saved plan: Devon's shows the plan, Avery's a later revision
// of it. Each lists the other as on another revision, and neither draws the
// other's pointer, wherever it moves.
// Failure prevented: a pointer from one revision is painted onto changed
// content in another, where the same place means different words.
func TestBrowser_PointerOnAnotherRevisionIsNeverDrawn(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	dir, planID, _ := saveAuthoredPresencePlan(t, gitRoot, presencePlan)
	later := strings.Replace(strings.Replace(presencePlan, `content="presence"`, `content="presence-later"`, 1), "Ship behind a flag.", "Ship behind a flag, region by region.", 1)
	laterDir, _, _ := saveAuthoredPresencePlan(t, gitRoot, later)
	f, base := newFakePresence(t, planID, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}, "tok-avery": {"Avery", "u-avery"}})
	devon, _ := servePresenceReview(t, gitRoot, "presence", dir, planID, base, "tok-devon")
	// Avery's loop serves the later page, joined to the same plan's room
	avery, _ := servePresenceReview(t, gitRoot, "presence-later", laterDir, planID, base, "tok-avery")

	second := frameBrowser(t)
	for _, p := range []struct {
		ctx context.Context
		url string
	}{{ctx, devon}, {second, avery}} {
		if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1100, 900), chromedp.Navigate(p.url+"/"), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
	}
	if r1, r2 := evalString(t, ctx, `document.body.getAttribute('data-review-revision')`), evalString(t, second, `document.body.getAttribute('data-review-revision')`); r1 == r2 || r1 == "" {
		t.Fatalf("the two pages must show different revisions: %q %q", r1, r2)
	}
	for _, c := range []struct {
		ctx  context.Context
		peer string
	}{{ctx, "Avery"}, {second, "Devon"}} {
		if !waitJS(c.ctx, `document.querySelector('.rev-peer.rev-peer-other[title^="`+c.peer+`"]')&&document.querySelector('.rev-here-note').textContent==='`+c.peer+` on another revision'`, 8*time.Second) {
			t.Fatalf("each page must say %s is on another revision: %s", c.peer, evalString(t, c.ctx, `document.querySelector('.rev-here')?.outerHTML`))
		}
	}
	pointAt(t, ctx, localSpot(t, ctx, "word"))
	pointAt(t, second, localSpot(t, second, "word"))
	time.Sleep(1500 * time.Millisecond)
	for _, c := range []struct {
		ctx  context.Context
		peer string
	}{{ctx, "Avery"}, {second, "Devon"}} {
		if _, n := cursorOf(t, c.ctx, c.peer); n != 0 {
			t.Fatalf("%s's pointer from another revision must never be drawn", c.peer)
		}
	}
	if _, moves := f.count(); len(moves) < 2 {
		t.Fatalf("both pointers must still reach the room, so a test of their absence means something: %v", moves)
	}
}

// TestBrowser_PointerMovesNeverOverlap mounts the review controls with a
// presence transport whose moves the test settles by hand.
// Failure prevented: a slow move is overtaken by a newer one, so the service
// can keep an older pointer, such as a place the reviewer has left, as the
// latest.
func TestBrowser_PointerMovesNeverOverlap(t *testing.T) {
	ctx := frameBrowser(t)
	index, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "packages", "plan-review", "dist", "index.js"))
	if err != nil {
		t.Skip("no packages/plan-review/dist — run `make plan-review` first")
	}
	page := `<!doctype html><html><body><script type="module">import { mountHostControls } from '/index.js';` +
		`let fn; const port = { send() {}, listen(f) { fn = f; } };` +
		`window.moves = []; window.busy = 0; window.most = 0; window.settle = [];` +
		`const presence = { join(revision, on) { window.on = on; return { move(at) { window.moves.push(at && at.x);` +
		` window.most = Math.max(window.most, ++window.busy); return new Promise((done) => window.settle.push(() => { window.busy--; done(); })); }, leave() {} }; } };` +
		`mountHostControls(port, { account: 'a', plan: 'p', slug: 'p', committed: [], reviewer: 'Avery', revision: 'sha256:` + strings.Repeat("0", 64) + `', presence });` +
		`window.point = (x) => fn({ type: 'pointer', at: { anchor: 'h1234abcd', x, y: 0.5 } });` +
		`window.on({ type: 'welcome', self: 'me', peers: [] });</script></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(page)) })
	mux.HandleFunc("/index.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(index)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/")); err != nil {
		t.Fatal(err)
	}
	// a welcome sends where the pointer is now: nowhere, and that move stays unsettled
	if !waitJS(ctx, `window.moves && window.moves.length === 1`, 5*time.Second) {
		t.Fatalf("the welcome must send the pointer: %s", evalString(t, ctx, `JSON.stringify(window.moves)`))
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.point(0.1); window.point(0.2)`, nil)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // well past the 200 ms between sends
	if got := evalString(t, ctx, `JSON.stringify(window.moves)`); got != "[null]" {
		t.Fatalf("no move may start while one is unsettled: %s", got)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.settle.shift()()`, nil)); err != nil {
		t.Fatal(err)
	}
	if !waitJS(ctx, `window.moves.length === 2`, 5*time.Second) {
		t.Fatal("the latest pointer must follow once the move before it settles")
	}
	time.Sleep(300 * time.Millisecond)
	if got := evalString(t, ctx, `JSON.stringify(window.moves)+' '+window.most`); got != "[null,0.2] 1" {
		t.Fatalf("moves sent, then the most ever in flight: %s; want [null,0.2] 1", got)
	}
}

// TestBrowser_ChangingPlanIsNotRehashedEveryFrame serves a plan whose own
// script rewrites a clock every 16 ms while a teammate's pointer is on it.
// Failure prevented: every change hashes every anchor in the plan again, once
// a frame, so a review page with a teammate present stalls on a plan with a
// live widget.
func TestBrowser_ChangingPlanIsNotRehashedEveryFrame(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	// window.hashes counts the anchor index being built: each build lists the markable elements
	ticking := strings.Replace(presencePlan, "<title>Presence</title>", `<title>Presence</title><script>window.hashes=0;`+
		`var qsa=Document.prototype.querySelectorAll;Document.prototype.querySelectorAll=function(s){if(s===window.OX_REVIEW_SELECTOR)window.hashes++;return qsa.call(this,s)};`+
		`setInterval(function(){var c=document.getElementById('clock');if(c)c.textContent=String(Date.now())},16)</script>`, 1)
	ticking = strings.Replace(ticking, "<p>Ship behind a flag.</p>", `<p>Ship behind a flag at <span id="clock">0</span>.</p>`, 1)
	dir, planID, _ := saveAuthoredPresencePlan(t, gitRoot, ticking)
	f, base := newFakePresence(t, planID, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}, "tok-avery": {"Avery", "u-avery"}})
	devon, _ := servePresenceReview(t, gitRoot, "presence", dir, planID, base, "tok-devon")
	if err := chromedp.Run(ctx, chromedp.Navigate(devon+"/"), chromedp.WaitVisible(".rev-bar", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	revision := evalString(t, ctx, `document.body.getAttribute('data-review-revision')`)
	avery, _ := sseStream(t, base+"/api/v1/plans/"+f.plan+"/presence?revision="+revision, "tok-avery")
	self, _ := nextEvent(t, avery, "Avery's welcome", func(e sseEvent) bool { return e.name == "welcome" }).data["self"].(string)
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/plans/"+f.plan+"/presence/"+self, strings.NewReader(`{"at":{"anchor":"h1234abcd","x":0.5,"y":0.5}}`))
	req.Header.Set("Authorization", "Bearer tok-avery")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Avery's move: %v %v", resp, err)
	}
	if !waitJS(ctx, `document.querySelector('.rev-peer[title="Avery is here"]')`, 8*time.Second) {
		t.Fatal("Avery must be here, so the page places her pointer")
	}
	time.Sleep(300 * time.Millisecond)
	var before, after int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.hashes`, &before), chromedp.Sleep(time.Second), chromedp.Evaluate(`window.hashes`, &after)); err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("the clock must keep the plan changing, or this test measures nothing")
	}
	if after-before > 6 {
		t.Fatalf("a plan rewriting itself every 16 ms had its anchors hashed %d times in a second; want a few at most", after-before)
	}
}
