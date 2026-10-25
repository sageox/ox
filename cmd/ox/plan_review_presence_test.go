package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/plan"
)

const (
	presenceTestPlan     = "pln_0199e7c6-1f2a-7b3c-8d4e-5f6a7b8c9d0e"
	presenceTestRevision = "sha256:95eb8bed00000000000000000000000000000000000000000000000000000000"
)

// fakePresence is a presence service for one plan as
// docs/specs/plan-review-presence.md describes it: it names each connection
// from the token it joined with, relays every join and move to the room, and
// says leave when a stream ends. Like a plain pub/sub it also tells each
// connection about itself, which a client must ignore. Browser tests reach it
// with the token in ?access_token=, since an EventSource sends no headers.
type fakePresence struct {
	plan   string
	people map[string]fakePerson // by token
	mux    *http.ServeMux

	mu     sync.Mutex
	refuse int // answer every join with this status, when set
	conns  map[string]*fakeConn
	seq    int
	joins  int
	moves  []fakeMove
}

type fakePerson struct{ name, user string }

type fakeConn struct {
	id, token, revision string
	who                 fakePerson
	at                  json.RawMessage
	out                 chan string
	kill                chan struct{}
}

type fakeMove struct {
	conn string
	at   string
}

// newFakePresence serves a fakePresence for plan and returns it with its base URL.
func newFakePresence(t *testing.T, plan string, people map[string]fakePerson) (*fakePresence, string) {
	t.Helper()
	f := fakePresenceFor(plan, people)
	srv := httptest.NewServer(f.mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// fakePresenceFor is a fakePresence for plan, for a test to serve.
func fakePresenceFor(plan string, people map[string]fakePerson) *fakePresence {
	f := &fakePresence{plan: plan, people: people, conns: map[string]*fakeConn{}, mux: http.NewServeMux()}
	f.mux.HandleFunc("GET /api/v1/plans/{plan}/presence", f.join)
	f.mux.HandleFunc("POST /api/v1/plans/{plan}/presence/{conn}", f.move)
	return f
}

func fakeBearer(r *http.Request) string {
	if t := r.URL.Query().Get("access_token"); t != "" {
		return t
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (c *fakeConn) peer() map[string]any {
	return map[string]any{"id": c.id, "name": c.who.name, "user": c.who.user, "revision": c.revision, "at": c.at}
}

// broadcast queues an event for every connection; f.mu is held.
func (f *fakePresence) broadcast(name string, v any) {
	b, _ := json.Marshal(v)
	for _, c := range f.conns {
		select {
		case c.out <- "event: " + name + "\ndata: " + string(b) + "\n\n":
		default:
		}
	}
}

func (f *fakePresence) join(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.joins++
	refuse := f.refuse
	f.mu.Unlock()
	who, ok := f.people[fakeBearer(r)]
	switch {
	case refuse != 0:
		http.Error(w, "refused", refuse)
		return
	case !ok:
		http.Error(w, "who are you", http.StatusUnauthorized)
		return
	case r.PathValue("plan") != f.plan:
		http.NotFound(w, r)
		return
	case !presenceRevisionRE.MatchString(r.URL.Query().Get("revision")):
		http.Error(w, "revision", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f.mu.Lock()
	f.seq++
	c := &fakeConn{id: fmt.Sprintf("conn-%d", f.seq), token: fakeBearer(r), revision: r.URL.Query().Get("revision"), who: who,
		at: json.RawMessage("null"), out: make(chan string, 64), kill: make(chan struct{})}
	f.conns[c.id] = c
	peers := []map[string]any{}
	for _, o := range f.conns {
		peers = append(peers, o.peer())
	}
	welcome, _ := json.Marshal(map[string]any{"self": c.id, "peers": peers})
	f.broadcast("peer", c.peer())
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.conns, c.id)
		f.broadcast("leave", map[string]string{"id": c.id})
		f.mu.Unlock()
	}()
	fmt.Fprint(w, "event: welcome\ndata: "+string(welcome)+"\n\n")
	w.(http.Flusher).Flush()
	for {
		select {
		case s := <-c.out:
			fmt.Fprint(w, s)
			w.(http.Flusher).Flush()
		case <-c.kill:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakePresence) move(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.conns[r.PathValue("conn")]
	if c == nil {
		http.NotFound(w, r)
		return
	}
	if fakeBearer(r) != c.token {
		http.Error(w, "not yours", http.StatusForbidden)
		return
	}
	var body struct {
		At json.RawMessage `json:"at"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.At == nil {
		http.Error(w, "at", http.StatusBadRequest)
		return
	}
	c.at = body.At
	f.moves = append(f.moves, fakeMove{c.id, string(body.At)})
	f.broadcast("peer", c.peer())
	w.WriteHeader(http.StatusNoContent)
}

// drop ends conn's stream as a dropped connection would.
func (f *fakePresence) drop(conn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.conns[conn]; c != nil {
		close(c.kill)
	}
}

func (f *fakePresence) count() (joins int, moves []fakeMove) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.joins, append([]fakeMove(nil), f.moves...)
}

type sseEvent struct {
	name string
	data map[string]any
	raw  string
}

// sseStream reads the event stream at url into a channel until the test ends
// or stop is called.
func sseStream(t *testing.T, url, token string) (events <-chan sseEvent, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	ch := make(chan sseEvent, 64)
	go func() {
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(name string, data []byte) {
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			ch <- sseEvent{name, m, string(data)}
		})
		close(ch)
	}()
	return ch, cancel
}

// nextEvent returns the first event on ch that want accepts, failing the test
// after a few seconds.
func nextEvent(t *testing.T, ch <-chan sseEvent, what string, want func(sseEvent) bool) sseEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended before %s", what)
			}
			if want(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s within 5s", what)
		}
	}
}

// presenceOf picks the page's presence events of one type.
func presenceOf(typ string) func(sseEvent) bool {
	return func(e sseEvent) bool { return e.name == "presence" && e.data["type"] == typ }
}

// presenceReview serves the live review handler with a bridge that joins the
// room at base as the holder of token, and returns its live-reload broadcaster.
func presenceReview(t *testing.T, base, token string) (*httptest.Server, *broadcaster) {
	t.Helper()
	b := &presenceBridge{base: base, planID: presenceTestPlan, tabs: map[string]*presenceTab{},
		token: func(context.Context) (string, error) { return token, nil }}
	bc := newBroadcaster()
	h := liveReviewHandlerWithActivity("", "p", t.TempDir(), "http://x", "secret", bc, make(chan int, 8), make(chan struct{}, 1), nil, b)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, bc
}

func pageEvents(t *testing.T, review, key string) (<-chan sseEvent, func()) {
	t.Helper()
	return sseStream(t, review+"/events?t=secret&rev="+presenceTestRevision+"&k="+key, "")
}

func pointerPOST(t *testing.T, review, key, at string) int {
	t.Helper()
	return reviewPOST(t, review+"/presence", "secret", `{"key":"`+key+`","at":`+at+`}`)
}

func roomURL(base string) string {
	return base + "/api/v1/plans/" + presenceTestPlan + "/presence"
}

// --- A. The room, both ways ---

// TestReviewLoop_PresenceRelaysTheRoomBothWays joins a local review page and
// a hosted viewer to one plan's room. Each hears the other join, move and
// leave, a second tab of the viewer's is a connection of its own, the page's
// moves reach the room under the name the service stamped from the user's
// login, and live reloads still reach the page on the stream it shares.
// Failure prevented: a local reviewer and a hosted one never see each other,
// a page can name itself whatever it likes, closing one tab drops another's
// cursor or a closed tab leaves a ghost, or presence breaks live reload.
func TestReviewLoop_PresenceRelaysTheRoomBothWays(t *testing.T) {
	fake, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}, "tok-avery": {"Avery", "u-avery"}})
	review, bc := presenceReview(t, base, "tok-devon")
	from := func(name string) func(sseEvent) bool {
		return func(e sseEvent) bool { return e.name == "peer" && e.data["name"] == name }
	}

	avery, _ := sseStream(t, roomURL(base)+"?revision="+presenceTestRevision, "tok-avery")
	nextEvent(t, avery, "Avery's welcome", func(e sseEvent) bool { return e.name == "welcome" })

	page, closePage := pageEvents(t, review.URL, "page-devon-1")
	w := nextEvent(t, page, "the page's welcome", presenceOf("welcome"))
	self, _ := w.data["self"].(string)
	if !strings.Contains(fmt.Sprint(w.data["peers"]), "name:Avery") {
		t.Fatalf("the page must be told who is already here: %v", w.data)
	}
	joined := nextEvent(t, avery, "Devon joining", from("Devon"))
	if joined.data["id"] != self || joined.data["at"] != nil {
		t.Fatalf("the room must hear the page join as the signed-in user, pointing nowhere yet: %v", joined.data)
	}

	if code := pointerPOST(t, review.URL, "page-devon-1", `{"anchor":"h1234abcd","path":[1],"x":0.5,"y":0.25,"text":7}`); code != http.StatusNoContent {
		t.Fatalf("a well-formed pointer: HTTP %d", code)
	}
	moved := nextEvent(t, avery, "Devon's pointer", func(e sseEvent) bool { return from("Devon")(e) && e.data["at"] != nil })
	if at := moved.data["at"].(map[string]any); at["anchor"] != "h1234abcd" || at["text"] != float64(7) {
		t.Fatalf("the room must get the page's pointer under the service's name: %v", moved.data)
	}

	req, _ := http.NewRequest(http.MethodPost, roomURL(base)+"/"+peerID(t, fake, "Avery"), strings.NewReader(`{"at":{"anchor":"hfeedbeef","x":0.1,"y":0.9}}`))
	req.Header.Set("Authorization", "Bearer tok-avery")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Avery's move: %v %v", resp, err)
	}
	seen := nextEvent(t, page, "Avery's pointer", func(e sseEvent) bool {
		p, _ := e.data["peer"].(map[string]any)
		return presenceOf("peer")(e) && p["name"] == "Avery" && p["at"] != nil
	})
	if p := seen.data["peer"].(map[string]any); p["at"].(map[string]any)["anchor"] != "hfeedbeef" {
		t.Fatalf("the page must hear the hosted viewer's pointer: %v", seen.data)
	}

	bc.broadcast()
	nextEvent(t, page, "a live reload on the stream presence shares", func(e sseEvent) bool { return e.name == "" && e.raw == "reload" })

	avery2, closeAvery2 := sseStream(t, roomURL(base)+"?revision="+presenceTestRevision, "tok-avery")
	second, _ := nextEvent(t, avery2, "Avery's second welcome", func(e sseEvent) bool { return e.name == "welcome" }).data["self"].(string)
	nextEvent(t, page, "Avery's second tab joining", func(e sseEvent) bool {
		p, _ := e.data["peer"].(map[string]any)
		return presenceOf("peer")(e) && p["id"] == second
	})
	closeAvery2()
	if gone := nextEvent(t, page, "Avery's second tab leaving", presenceOf("leave")); gone.data["id"] != second {
		t.Fatalf("closing one tab must leave only its own connection: %v", gone.data)
	}

	closePage()
	nextEvent(t, avery, "closing the page leaving the room", func(e sseEvent) bool { return e.name == "leave" && e.data["id"] == self })
}

// peerID is the fake's connection id for the named person.
func peerID(t *testing.T, f *fakePresence, name string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, c := range f.conns {
		if c.who.name == name {
			return id
		}
	}
	t.Fatalf("%s is not in the room", name)
	return ""
}

// --- B. What a page may send ---

// TestReviewLoop_PresenceMovesAreCheckedAndScoped sends /presence everything
// but a well-formed pointer for a connected page.
// Failure prevented: the review server forwards whatever the page posts to
// the user's account upstream (a name, a note, an unbounded blob), or a
// caller without the review token moves the user's pointer.
func TestReviewLoop_PresenceMovesAreCheckedAndScoped(t *testing.T) {
	fake, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}})
	review, _ := presenceReview(t, base, "tok-devon")
	page, _ := pageEvents(t, review.URL, "page-devon-1")
	nextEvent(t, page, "the page's welcome", presenceOf("welcome"))

	good := `{"anchor":"h1234abcd","x":0.5,"y":0.5}`
	for name, c := range map[string]struct {
		token, body string
		want        int
	}{
		"no review token":   {"", `{"key":"page-devon-1","at":` + good + `}`, http.StatusForbidden},
		"a name of its own": {"secret", `{"key":"page-devon-1","at":` + good + `,"name":"Mallory"}`, http.StatusBadRequest},
		"a field in at":     {"secret", `{"key":"page-devon-1","at":{"anchor":"h1234abcd","x":0.5,"y":0.5,"note":"hi"}}`, http.StatusBadRequest},
		"off the element":   {"secret", `{"key":"page-devon-1","at":{"anchor":"h1234abcd","x":2,"y":0.5}}`, http.StatusBadRequest},
		"another page":      {"secret", `{"key":"page-avery-9","at":` + good + `}`, http.StatusNotFound},
		"too large":         {"secret", `{"key":"page-devon-1","at":` + good + `,"pad":"` + strings.Repeat("x", 5000) + `"}`, http.StatusBadRequest},
	} {
		if got := reviewPOST(t, review.URL+"/presence", c.token, c.body); got != c.want {
			t.Errorf("%s: HTTP %d, want %d", name, got, c.want)
		}
	}
	if resp, err := http.Get(review.URL + "/presence"); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /presence must be refused: %v %v", resp, err)
	}
	if got := pointerPOST(t, review.URL, "page-devon-1", "null"); got != http.StatusNoContent {
		t.Fatalf("the pointer leaving the plan: HTTP %d", got)
	}
	waitFor(t, "the pointer leaving the plan to reach the room", func() bool { _, m := fake.count(); return len(m) == 1 && m[0].at == "null" })
	if _, moves := fake.count(); len(moves) != 1 {
		t.Fatalf("only the well-formed pointer may reach the room: %v", moves)
	}

	// a review server with no bridge (no login) has no presence to move
	bare := httptest.NewServer(liveReviewHandler("", "p", t.TempDir(), "http://x", "secret", newBroadcaster(), make(chan int, 1), make(chan struct{}, 1)))
	t.Cleanup(bare.Close)
	if got := pointerPOST(t, bare.URL, "page-devon-1", good); got != http.StatusNotFound {
		t.Fatalf("no presence: HTTP %d", got)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func setPresenceTimings(t *testing.T, first, refused, silence time.Duration) {
	t.Helper()
	prev := [4]time.Duration{presenceRetryFirst, presenceRetryRefused, presenceSilence, presenceMoveEvery}
	presenceRetryFirst, presenceRetryRefused, presenceSilence, presenceMoveEvery = first, refused, silence, 10*time.Millisecond
	t.Cleanup(func() {
		presenceRetryFirst, presenceRetryRefused, presenceSilence, presenceMoveEvery = prev[0], prev[1], prev[2], prev[3]
	})
}

// --- C. Lifecycle ---

// TestPresenceBridge_RejoinSendsOnlyTheCurrentPointer drops the page's
// connection, keeps the service down while the pointer moves three times,
// and brings it back.
// Failure prevented: a reconnect replays a queued cursor trail into the
// room, a dropped connection is never rejoined, or the page is not told its
// peers are gone while it is disconnected.
func TestPresenceBridge_RejoinSendsOnlyTheCurrentPointer(t *testing.T) {
	setPresenceTimings(t, 20*time.Millisecond, time.Hour, time.Hour)
	fake, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}})
	review, _ := presenceReview(t, base, "tok-devon")
	page, _ := pageEvents(t, review.URL, "page-devon-1")
	first := nextEvent(t, page, "the page's welcome", presenceOf("welcome")).data["self"].(string)

	fake.mu.Lock()
	fake.refuse = http.StatusServiceUnavailable
	fake.mu.Unlock()
	fake.drop(first)
	nextEvent(t, page, "the page told it lost the room", presenceOf("lost"))
	for i := 1; i <= 3; i++ {
		if got := pointerPOST(t, review.URL, "page-devon-1", fmt.Sprintf(`{"anchor":"h1234abcd","x":0.%d,"y":0.5}`, i)); got != http.StatusNoContent {
			t.Fatalf("move %d while disconnected: HTTP %d", i, got)
		}
	}
	fake.mu.Lock()
	fake.refuse = 0
	fake.mu.Unlock()
	again := nextEvent(t, page, "the page's second welcome", presenceOf("welcome")).data["self"].(string)
	if again == first {
		t.Fatalf("a rejoin is a new connection: %s", again)
	}
	time.Sleep(50 * time.Millisecond) // several send intervals: any queued trail would be out by now
	if _, moves := fake.count(); len(moves) != 0 {
		t.Fatalf("nothing moved while disconnected may reach the room on rejoin: %v", moves)
	}
	// the page says where its pointer is now, as it does on every welcome
	current := `{"anchor":"h1234abcd","x":0.3,"y":0.5}`
	pointerPOST(t, review.URL, "page-devon-1", current)
	waitFor(t, "the current pointer", func() bool { _, m := fake.count(); return len(m) == 1 })
	if _, moves := fake.count(); moves[0].conn != again || moves[0].at != current {
		t.Fatalf("the rejoined connection must get the current pointer, once: %v", moves)
	}
}

// TestPresenceBridge_RetriesOnlyWhatMayRecover covers the ways a join goes
// nowhere: the service declines it, fails for now, or its stream goes silent
// without closing.
// Failure prevented: every open review page re-asks a service that declined
// it (signed out, no access, presence not offered) as fast as a transient
// error; or a half-open connection leaves the page believing in a room that
// is gone, with ghosts it will never hear leave.
func TestPresenceBridge_RetriesOnlyWhatMayRecover(t *testing.T) {
	setPresenceTimings(t, 10*time.Millisecond, time.Hour, 100*time.Millisecond)
	for _, c := range []struct {
		status    int
		atMostOne bool
	}{{http.StatusNotFound, true}, {http.StatusUnauthorized, true}, {http.StatusForbidden, true}, {http.StatusBadGateway, false}} {
		fake, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}})
		fake.refuse = c.status
		review, _ := presenceReview(t, base, "tok-devon")
		_, stop := pageEvents(t, review.URL, "page-devon-1")
		time.Sleep(300 * time.Millisecond)
		stop()
		joins, _ := fake.count()
		if c.atMostOne && joins != 1 {
			t.Errorf("HTTP %d: a declined join was asked again %d times", c.status, joins-1)
		}
		if !c.atMostOne && joins < 3 {
			t.Errorf("HTTP %d: a transient failure must be retried; joined %d times", c.status, joins)
		}
	}

	// the fake sends no keepalives: after its welcome the stream is silent
	_, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}})
	review, _ := presenceReview(t, base, "tok-devon")
	page, _ := pageEvents(t, review.URL, "page-devon-1")
	nextEvent(t, page, "the page's welcome", presenceOf("welcome"))
	nextEvent(t, page, "a silent stream ended", presenceOf("lost"))
	nextEvent(t, page, "the room joined again", presenceOf("welcome"))
}

// TestNewPresenceBridge_OnlyForASavedPlanAndASignedInUser builds the bridge
// `ox plan review` uses from what is on disk.
// Failure prevented: a reviewer who is not signed in, or a plan that has no
// saved id, makes requests to SageOx on every open page, or a signed-in
// reviewer of a saved plan never joins its room.
func TestNewPresenceBridge_OnlyForASavedPlanAndASignedInUser(t *testing.T) {
	fake, base := newFakePresence(t, presenceTestPlan, map[string]fakePerson{"tok-devon": {"Devon", "u-devon"}})
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("SAGEOX_ENDPOINT", base)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitRoot := t.TempDir()

	unsaved := t.TempDir()
	saved := t.TempDir()
	if err := plan.AppendEvent(context.Background(), saved, plan.Event{Kind: plan.EventCreated, PlanID: presenceTestPlan, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if b := newPresenceBridge(gitRoot, saved); b != nil {
		t.Fatal("no one is signed in: no bridge")
	}
	if err := auth.SaveTokenForEndpoint(base, &auth.StoredToken{AccessToken: "tok-devon", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if b := newPresenceBridge(gitRoot, unsaved); b != nil {
		t.Fatal("a plan with no pln_ id: no bridge")
	}
	b := newPresenceBridge(gitRoot, saved)
	if b == nil || b.planID != presenceTestPlan {
		t.Fatalf("a signed-in reviewer of a saved plan joins its room: %+v", b)
	}
	if tok, err := b.token(context.Background()); err != nil || tok != "tok-devon" {
		t.Fatalf("the bridge joins with the user's own login: %q %v", tok, err)
	}
	// a login that expired and cannot refresh declines presence, as signed out
	if err := auth.SaveTokenForEndpoint(base, &auth.StoredToken{AccessToken: "tok-old", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := newPresenceBridge(gitRoot, saved).token(context.Background()); !errors.Is(err, errPresenceRefused) {
		t.Fatalf("an expired login is declined, not retried as an outage: %v", err)
	}

	// the no-login server answers its pages exactly as before: no request
	bare := httptest.NewServer(liveReviewHandlerWithActivity("", "p", unsaved, "http://x", "secret", newBroadcaster(), make(chan int, 1), make(chan struct{}, 1), nil, nil))
	t.Cleanup(bare.Close)
	page, stop := pageEvents(t, bare.URL, "page-devon-1")
	time.Sleep(100 * time.Millisecond)
	stop()
	for e := range page {
		if e.name == "presence" {
			t.Fatalf("no bridge, no presence events: %v", e)
		}
	}
	if joins, _ := fake.count(); joins != 0 {
		t.Fatalf("no bridge, no request to the service: %d joins", joins)
	}
}

// --- D. The wire ---

// TestPresenceAt_SharedCases runs the pointer cases every reader shares.
// Failure prevented: the review server forwards a pointer the package and
// the service refuse, or refuses one they accept.
func TestPresenceAt_SharedCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "pkg", "planreview", "testdata", "presence", "pointers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Valid   []json.RawMessage          `json:"valid"`
		Invalid map[string]json.RawMessage `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	parse := func(b []byte) (*presenceAt, bool) {
		var a presenceAt
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		return &a, dec.Decode(&a) == nil && a.valid()
	}
	for _, v := range cases.Valid {
		a, ok := parse(v)
		if !ok {
			t.Errorf("refused a valid pointer: %s", v)
			continue
		}
		var got, want any
		out, _ := json.Marshal(a)
		if json.Unmarshal(out, &got) != nil || json.Unmarshal(v, &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("a valid pointer must pass through unchanged: %s became %s", v, out)
		}
	}
	for name, v := range cases.Invalid {
		if _, ok := parse(v); ok {
			t.Errorf("accepted %s: %s", name, v)
		}
	}
}

// TestReadSSE_FramesEvents reads the event stream shapes a service can send.
// Failure prevented: a keepalive, a CRLF line ending or a multi-line data
// field is read as an event, or splits or drops one.
func TestReadSSE_FramesEvents(t *testing.T) {
	stream := ": keepalive\n\nevent: peer\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\nevent: leave\nid: 7\ndata:{\"id\":\"c1\"}\n\nevent: welcome\n\ndata: unterminated"
	var got []string
	if err := readSSE(strings.NewReader(stream), func(name string, data []byte) { got = append(got, name+"="+string(data)) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"peer={\"a\":\n1}", `leave={"id":"c1"}`}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events: %q, want %q", got, want)
	}
	if err := readSSE(strings.NewReader("data: "+strings.Repeat("x", presenceMaxEvent+1)+"\n\n"), func(string, []byte) {}); err == nil {
		t.Fatal("an event past the limit must end the stream")
	}
}
