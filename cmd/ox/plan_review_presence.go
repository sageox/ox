package main

// plan_review_presence.go joins each open `ox plan review` page to its plan's
// live presence room on SageOx, as the user signed in to ox, so teammates on
// the same saved plan see this reviewer's pointer and selected words, and the
// page sees theirs. The page never holds the user's credentials: it reaches
// only this server, with the review token, and this server joins only the
// plan it serves, by its pln_ id. Pointers and selections are transient:
// nothing is written to disk or the Ledger, and only the latest of each is
// sent. The wire contract is docs/specs/plan-review-presence.md.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/planid"
	"github.com/sageox/ox/internal/useragent"
)

const presenceMaxEvent = 256 << 10

// Timings; variables so tests can shorten them.
var (
	// presenceMoveEvery is the most often one page's pointer is sent to the
	// service: a moving pointer is coalesced to its latest place.
	presenceMoveEvery = 200 * time.Millisecond
	// A dropped connection is joined again after presenceRetryFirst, doubling
	// to presenceRetryMax while it keeps failing.
	presenceRetryFirst = time.Second
	presenceRetryMax   = 30 * time.Second
	// presenceRetryRefused is the wait after the service declines the join:
	// no login, no access to the plan, or no presence for it yet.
	presenceRetryRefused = 5 * time.Minute
	// presenceSilence ends a stream that sent nothing, not even a keepalive,
	// for this long: a dropped connection can stay open without failing.
	presenceSilence = 75 * time.Second
)

var (
	presenceKeyRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	presenceRevisionRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	presenceAnchorRE   = regexp.MustCompile(`^h[0-9a-f]{8}$`)
	errPresenceRefused = errors.New("presence refused")
	// errPresenceSelRefused is a move carrying a selection the service
	// refused, as one from before selections refuses every such move.
	errPresenceSelRefused = errors.New("presence refused the selection")
	// presenceStream reads the room's event stream, which stays open for the
	// whole review; presenceSilence ends a dead one instead of a timeout.
	presenceStream = &http.Client{}
	presenceMoves  = &http.Client{Timeout: 5 * time.Second}
)

// presenceAt is a pointer location (PointerAt in packages/plan-review): the
// content under a reviewer's pointer, never screen coordinates.
type presenceAt struct {
	Anchor string   `json:"anchor"`
	Nth    *int     `json:"nth,omitempty"`
	Of     *int     `json:"of,omitempty"`
	Path   []int    `json:"path,omitempty"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Text   *int     `json:"text,omitempty"`
}

// valid applies pointerAt's rules in packages/plan-review/src/protocol.ts;
// pkg/planreview/testdata/presence/pointers.json holds the cases.
func (a *presenceAt) valid() bool {
	unit := func(f *float64) bool { return f != nil && *f >= 0 && *f <= 1 }
	if !presenceAnchorRE.MatchString(a.Anchor) || !unit(a.X) || !unit(a.Y) || !presenceOccurrence(a.Nth, a.Of) {
		return false
	}
	if a.Path != nil && (len(a.Path) == 0 || len(a.Path) > 16) {
		return false
	}
	for _, i := range a.Path {
		if i < 0 || i > 10000 {
			return false
		}
	}
	return a.Text == nil || (*a.Text >= 0 && *a.Text <= 1<<20)
}

// presenceOccurrence checks which occurrence of a repeated anchor a location
// names: nth and of come together, and only when the anchor repeats.
func presenceOccurrence(nth, of *int) bool {
	if (nth == nil) != (of == nil) {
		return false
	}
	return of == nil || (*of >= 2 && *of <= 10000 && *nth >= 0 && *nth < *of)
}

// presenceTextAt is a place in the plan's text (TextAt in packages/plan-review).
type presenceTextAt struct {
	Anchor string `json:"anchor"`
	Nth    *int   `json:"nth,omitempty"`
	Of     *int   `json:"of,omitempty"`
	Text   *int   `json:"text"`
}

func (t *presenceTextAt) valid() bool {
	return t != nil && presenceAnchorRE.MatchString(t.Anchor) && presenceOccurrence(t.Nth, t.Of) &&
		t.Text != nil && *t.Text >= 0 && *t.Text <= 1<<20
}

// presenceSel is the words a reviewer has selected (SelectionAt in
// packages/plan-review), never the words themselves.
type presenceSel struct {
	Start *presenceTextAt `json:"start"`
	End   *presenceTextAt `json:"end"`
}

// valid applies selectionAt's rules in packages/plan-review/src/protocol.ts;
// pkg/planreview/testdata/presence/selections.json holds the cases.
func (s *presenceSel) valid() bool { return s.Start.valid() && s.End.valid() }

// presencePeer is another connection in the room, as the service names it.
type presencePeer struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	User     string       `json:"user"`
	Revision string       `json:"revision"`
	At       *presenceAt  `json:"at"`
	Sel      *presenceSel `json:"sel,omitempty"`
}

func presenceText(s string, n int) bool { return s != "" && utf8.RuneCountInString(s) <= n }

// parsePresencePeer keeps a peer whose every field the page reads is well formed.
func parsePresencePeer(raw []byte) (presencePeer, bool) {
	var p presencePeer
	if json.Unmarshal(raw, &p) != nil {
		return p, false
	}
	ok := presenceText(p.ID, 128) && presenceText(p.Name, 200) && presenceText(p.User, 128) && presenceText(p.Revision, 128) &&
		(p.At == nil || p.At.valid()) && (p.Sel == nil || p.Sel.valid())
	return p, ok
}

// presenceBridge joins one plan's room, once per open page.
type presenceBridge struct {
	base   string // the SageOx endpoint the user signed in to
	planID string
	// token is the user's access token; fresh reads the login again
	token func(ctx context.Context, fresh bool) (string, error)

	mu   sync.Mutex
	tabs map[string]*presenceTab // by the key each page names itself with
}

// newPresenceBridge returns the bridge for the plan in planDir, or nil when
// the plan has no pln_ id or no one is signed in to the project's endpoint.
// Without one, `ox plan review` makes no presence request at all.
func newPresenceBridge(gitRoot, planDir string) *presenceBridge {
	id := planIDForDir(planDir)
	if !planid.IsPlanID(id) {
		return nil
	}
	ep := endpoint.GetForProject(gitRoot)
	if t, err := auth.GetTokenForEndpoint(ep); err != nil || t == nil || t.AccessToken == "" {
		return nil
	}
	var mu sync.Mutex
	var held *auth.StoredToken
	return &presenceBridge{
		base:   strings.TrimSuffix(ep, "/"),
		planID: id,
		// A join reads the login again, so signing in anew takes effect at the
		// next one; a move, far more frequent, reuses it until it nears expiry.
		token: func(ctx context.Context, fresh bool) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if fresh || held == nil || (!held.ExpiresAt.IsZero() && held.IsExpired(300)) {
				t, err := auth.EnsureValidTokenForEndpointContext(ctx, ep, 300)
				// a refresh that could not reach the server is retried soon;
				// one the server answered (a revoked login) waits like a
				// declined join
				var offline net.Error
				if errors.As(err, &offline) {
					return "", fmt.Errorf("refresh the login: %w", err)
				}
				if err != nil {
					return "", fmt.Errorf("%w: %w", errPresenceRefused, err)
				}
				if t == nil || t.AccessToken == "" {
					return "", fmt.Errorf("%w: not signed in", errPresenceRefused)
				}
				held = t
			}
			return held.AccessToken, nil
		},
		tabs: map[string]*presenceTab{},
	}
}

func (b *presenceBridge) roomURL() string {
	return b.base + "/api/v1/plans/" + url.PathEscape(b.planID) + "/presence"
}

// presenceTab is one page's connection to the room.
type presenceTab struct {
	b        *presenceBridge
	key      string
	revision string
	emit     func([]byte) // writes one presence event to the page
	cancel   context.CancelFunc
	done     sync.WaitGroup
	wake     chan struct{}

	mu    sync.Mutex
	conn  string          // the service's id for this connection, while joined
	at    json.RawMessage // where the page's pointer rests
	sel   json.RawMessage // the words the page has selected
	fresh bool            // at and sel are not yet sent on conn
	bare  bool            // conn refused a selection: send it pointers alone
}

// open joins the room for a page that shows revision and names itself key,
// writing the room's events to it through emit until close; nil when the
// bridge is off or either value is missing or malformed.
func (b *presenceBridge) open(ctx context.Context, key, revision string, emit func([]byte)) *presenceTab {
	if b == nil || !presenceKeyRE.MatchString(key) || !presenceRevisionRE.MatchString(revision) {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &presenceTab{b: b, key: key, revision: revision, emit: emit, cancel: cancel, wake: make(chan struct{}, 1), at: json.RawMessage("null"), sel: json.RawMessage("null")}
	b.mu.Lock()
	// a page reconnecting under its key replaces its old connection, which
	// leaves the room now, not when the server notices its stream is gone
	if old := b.tabs[key]; old != nil {
		old.cancel()
	}
	b.tabs[key] = t
	b.mu.Unlock()
	t.done.Add(2)
	go func() { defer t.done.Done(); t.run(ctx) }()
	go func() { defer t.done.Done(); t.send(ctx) }()
	return t
}

// close leaves the room and returns once nothing more is written to the page.
func (t *presenceTab) close() {
	t.cancel()
	t.done.Wait()
	t.b.mu.Lock()
	if t.b.tabs[t.key] == t {
		delete(t.b.tabs, t.key)
	}
	t.b.mu.Unlock()
}

// move records where the page named key has its pointer and what it has
// selected; false when no page of that name is connected.
func (b *presenceBridge) move(key string, at, sel json.RawMessage) bool {
	b.mu.Lock()
	t := b.tabs[key]
	b.mu.Unlock()
	if t == nil {
		return false
	}
	t.mu.Lock()
	t.at, t.sel, t.fresh = at, sel, true
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
	return true
}

// run keeps the page in the room: it joins, relays the room's events, and
// after a drop says `lost` and joins again.
func (t *presenceTab) run(ctx context.Context) {
	wait := presenceRetryFirst
	for {
		joined, err := t.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if joined {
			t.emit([]byte(`{"type":"lost"}`))
			wait = presenceRetryFirst
		}
		next := wait
		if errors.Is(err, errPresenceRefused) {
			next = presenceRetryRefused
		} else if wait *= 2; wait > presenceRetryMax {
			wait = presenceRetryMax
		}
		slog.Debug("plan review: presence disconnected", "plan_id", t.b.planID, "retry_in", next, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// session joins the room once and relays its events until the stream ends.
// joined reports whether the service welcomed this connection.
func (t *presenceTab) session(ctx context.Context) (joined bool, err error) {
	tok, err := t.b.token(ctx, true)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := useragent.NewRequest(ctx, http.MethodGet, t.b.roomURL()+"?revision="+url.QueryEscape(t.revision), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	silent := time.AfterFunc(presenceSilence, cancel)
	defer silent.Stop()
	resp, err := presenceStream.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, fmt.Errorf("%w: HTTP %d", errPresenceRefused, resp.StatusCode)
	default:
		return false, fmt.Errorf("presence join: HTTP %d", resp.StatusCode)
	}
	defer t.setConn("")
	err = readSSE(alive{resp.Body, silent}, func(name string, data []byte) {
		switch name {
		case "welcome":
			var w struct {
				Self  string            `json:"self"`
				Peers []json.RawMessage `json:"peers"`
			}
			if json.Unmarshal(data, &w) != nil || !presenceText(w.Self, 128) {
				return
			}
			peers := []presencePeer{}
			for _, raw := range w.Peers {
				if p, ok := parsePresencePeer(raw); ok {
					peers = append(peers, p)
				}
			}
			// the connection is ready for moves before the page hears it
			// joined and sends where its pointer is
			t.setConn(w.Self)
			joined = true
			t.forward(map[string]any{"type": "welcome", "self": w.Self, "peers": peers})
		case "peer":
			if p, ok := parsePresencePeer(data); ok && joined {
				t.forward(map[string]any{"type": "peer", "peer": p})
			}
		case "leave":
			var l struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(data, &l) == nil && presenceText(l.ID, 128) && joined {
				t.forward(map[string]any{"type": "leave", "id": l.ID})
			}
		}
	})
	return joined, err
}

func (t *presenceTab) forward(v any) {
	b, _ := json.Marshal(v) // maps of strings and checked peers: always encodes
	t.emit(b)
}

// setConn records the service's id for this page's connection; a new
// connection is asked to take selections again.
func (t *presenceTab) setConn(id string) {
	t.mu.Lock()
	t.conn, t.bare = id, false
	t.mu.Unlock()
}

// send passes the page's latest pointer and selection to its connection, at
// most once every presenceMoveEvery. A move made while no connection is joined
// waits for the next: the page sends where it is when it hears it joined.
func (t *presenceTab) send(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
		}
		t.mu.Lock()
		conn, at, sel, fresh := t.conn, t.at, t.sel, t.fresh
		if conn != "" {
			t.fresh = false
		}
		if t.bare {
			sel = json.RawMessage("null")
		}
		t.mu.Unlock()
		if conn == "" || !fresh {
			continue
		}
		err := t.b.post(ctx, conn, at, sel)
		if errors.Is(err, errPresenceSelRefused) {
			// a service from before selections refuses every move carrying
			// one: from the next interval on, this connection gets the
			// pointer alone, as it did before them, this move included
			t.mu.Lock()
			if t.conn == conn {
				t.bare, t.fresh = true, true
			}
			t.mu.Unlock()
			select {
			case t.wake <- struct{}{}:
			default:
			}
		}
		if err != nil {
			slog.Debug("plan review: presence move not sent", "plan_id", t.b.planID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(presenceMoveEvery):
		}
	}
}

// post sends one move. A move with nothing selected carries no sel, as moves
// did before selections were shared.
func (b *presenceBridge) post(ctx context.Context, conn string, at, sel json.RawMessage) error {
	tok, err := b.token(ctx, false)
	if err != nil {
		return err
	}
	move := `{"at":` + string(at)
	if string(sel) != "null" {
		move += `,"sel":` + string(sel)
	}
	body := strings.NewReader(move + `}`)
	req, err := useragent.NewRequest(ctx, http.MethodPost, b.roomURL()+"/"+url.PathEscape(conn), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := presenceMoves.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest && string(sel) != "null" {
		return errPresenceSelRefused
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// alive restarts the silence timer on every byte the stream delivers.
type alive struct {
	r io.Reader
	t *time.Timer
}

func (a alive) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.t.Reset(presenceSilence)
	}
	return n, err
}

// readSSE calls on for each event of a server-sent event stream until it
// ends: nil at its end, else why it could not be read.
func readSSE(r io.Reader, on func(name string, data []byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), presenceMaxEvent)
	name, data := "", []byte(nil)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data != nil {
				on(name, data)
			}
			name, data = "", nil
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(line[len("data:"):], " ")...)
			if len(data) > presenceMaxEvent {
				return errors.New("presence event too large")
			}
		}
	}
	return sc.Err()
}
