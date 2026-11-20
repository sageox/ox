// Package livesessiontest is an in-process fake of the api-go live-capture
// routes, built from the vendored wire contract (sessionstream.Append over a
// MemoryStore, real signature verification). It enforces what the real server
// enforces and exposes named fault knobs and call recording so client, cmd and
// daemon tests share one server.
package livesessiontest

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

const (
	// Token is the only bearer token the fake accepts.
	Token = "live-test-token"
	// TeamID owns every stored object (object keys lead with it).
	TeamID = "team_livetest0001"
)

// Call is one request the fake received.
type Call struct {
	Method string
	Path   string
	// Offset is the append offset, -1 for other routes.
	Offset int64
	// Status is the HTTP status sent; 0 means the connection was dropped or hung.
	Status  int
	BodyLen int
}

// Closed is what the server saw when a session was closed.
type Closed struct {
	sessionstream.CloseRequest
	// StoredAtClose is the layer's stored_through at the moment of close:
	// it equals StoredThrough only if the client closed after a full ack.
	StoredAtClose int64
}

type layerState struct{ cur sessionstream.Cursor }

type conversation struct {
	sessionID string
	layers    map[string]*layerState
	deleted   bool
	closed    *Closed
}

// Server is the fake api-go. Create it with New.
type Server struct {
	*httptest.Server
	// Store holds every stream object; reassemble a layer with Stored.
	Store *sessionstream.MemoryStore

	mu       sync.Mutex
	keys     map[string]ed25519.PublicKey
	loginKey string
	convs    map[string]*conversation
	calls    []Call

	// knobs; all one-shot counters unless noted
	dropAfterWrite   int
	forceNewLayer    int
	forceResync      *int64
	refuseOpen       int
	dropOpenReply    int
	refuseDelete     bool
	appendRefusal    *reply
	closeRefusal     *reply
	deferRecovery    bool
	rateLimit        int
	rateLimitMS      int64
	rejectAuth       bool
	forceKeyConflict bool
	down             bool
	hang             bool
	latency          time.Duration
	openDelay        time.Duration
	edgeMethod       string
	edgeBlocks       int
	edgeType         string
	edgeBody         string
	hangAppendsOver  int
	// teammate comments, in the order the owner queued them; delivered holds the
	// first-wins delivered_at_seq per comment id.
	comments                    []sessionstream.TeammateComment
	delivered                   map[string]int64
	ackRefusal                  *reply
	ackRefusals                 int
	ackDropBefore, ackDropAfter int
	ackIgnored                  int
	listFails                   bool
	nameTaken                   bool
	ackBodies                   []sessionstream.AckTeammateCommentsRequest

	inflight, maxInflight int
}

// New starts a fake server and registers its cleanup.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		Store:     sessionstream.NewMemoryStore(),
		keys:      map[string]ed25519.PublicKey{},
		convs:     map[string]*conversation{},
		delivered: map[string]int64{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+sessionstream.PathKeys, s.route(s.registerKey))
	mux.HandleFunc("PUT "+sessionstream.PathSessions+"/{cnv}", s.route(s.open))
	mux.HandleFunc("DELETE "+sessionstream.PathSessions+"/{cnv}", s.route(s.remove))
	mux.HandleFunc("POST "+sessionstream.PathSessions+"/{cnv}/close", s.route(s.closeSession))
	mux.HandleFunc("POST "+sessionstream.PathSessions+"/{cnv}/layers/{layer}/lines", s.route(s.appendLines))
	mux.HandleFunc("POST "+sessionstream.PathSessions+"/{cnv}/teammate-comments/acks", s.route(s.ackTeammateComments))
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Config is a livesession.Config pointed at this server, with a static token and
// private temp dirs for keys and the pending-deletes queue.
func (s *Server) Config(t testing.TB) livesession.Config {
	t.Helper()
	return livesession.Config{
		Endpoint: s.URL,
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		Token:    func(_ context.Context, _ string) (string, error) { return Token, nil },
		KeyDir:   t.TempDir(),
		DataDir:  t.TempDir(),
		Logger:   slog.New(slog.NewTextHandler(testLogWriter{t}, nil)),
	}
}

// testLogWriter routes client logs into the test log, shown only on failure.
type testLogWriter struct{ t testing.TB }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

type reply struct {
	status int
	body   any
	// drop closes the connection without answering (the work already happened).
	drop bool
}

func refusal(status int, reason string, action sessionstream.Action, stored *int64) reply {
	return reply{status: status, body: sessionstream.ErrorResponse{Error: reason, Reason: reason, Action: action, StoredThrough: stored}}
}

// route wraps a handler with call recording, latency, hang and down knobs, auth
// and signature verification.
func (s *Server) route(h func(r *http.Request, body []byte, call *Call) reply) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := Call{Method: r.Method, Path: r.URL.Path, Offset: -1, BodyLen: len(body)}
		s.enter()
		defer s.leave()
		if s.knob(func() bool { return s.hang || s.hangsAppend(r, body) }) {
			<-r.Context().Done()
			s.record(call)
			return
		}
		if typ, edge, ok := s.takeEdgeBlock(r); ok {
			// the edge answers before api-go sees anything: no auth, no JSON refusal
			call.Status = http.StatusForbidden
			s.record(call)
			w.Header().Set("Content-Type", typ)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, edge)
			return
		}
		if d := s.latencyValue(); d > 0 {
			time.Sleep(d)
		}
		if d := s.openDelayValue(); d > 0 && r.Method == http.MethodPut {
			time.Sleep(d)
		}
		s.mu.Lock()
		rep := s.guard(r, body)
		if rep == nil {
			x := h(r, body, &call)
			rep = &x
		}
		call.Status = rep.status
		if rep.drop {
			call.Status = 0
		}
		s.calls = append(s.calls, call)
		s.mu.Unlock()
		if rep.drop {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_ = json.NewEncoder(w).Encode(rep.body)
	}
}

// guard applies the down, auth and signature checks; nil means proceed. mu held.
func (s *Server) guard(r *http.Request, body []byte) *reply {
	if s.down {
		x := refusal(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry, nil)
		return &x
	}
	if s.rejectAuth || r.Header.Get("Authorization") != "Bearer "+Token {
		x := refusal(http.StatusUnauthorized, sessionstream.ReasonUnauthenticated, sessionstream.ActionReauth, nil)
		return &x
	}
	if r.Method == http.MethodPost && r.URL.Path == sessionstream.PathKeys {
		return nil // self-signed by the key being registered; checked in registerKey
	}
	return s.verify(r, body, nil)
}

// verify checks the request signature against pub, or against the registered
// key named in the header when pub is nil. mu held.
func (s *Server) verify(r *http.Request, body []byte, pub ed25519.PublicKey) *reply {
	sig, err := sessionstream.ParseSignature(r.Header.Get(sessionstream.HeaderSignature))
	if err != nil {
		x := refusal(http.StatusUnauthorized, sessionstream.ReasonBadSignature, sessionstream.ActionReauth, nil)
		return &x
	}
	if pub == nil {
		var ok bool
		if pub, ok = s.keys[sig.KeyID]; !ok {
			x := refusal(http.StatusUnauthorized, sessionstream.ReasonUnknownKey, sessionstream.ActionReauth, nil)
			return &x
		}
	}
	if err := sessionstream.Verify(pub, sig, r.Method, r.URL.RequestURI(), body, time.Now()); err != nil {
		reason := sessionstream.ReasonBadSignature
		if errors.Is(err, sessionstream.ErrSignatureExpired) {
			reason = sessionstream.ReasonSignatureExpired
		}
		x := refusal(http.StatusUnauthorized, reason, sessionstream.ActionReauth, nil)
		return &x
	}
	return nil
}

func (s *Server) registerKey(r *http.Request, body []byte, _ *Call) reply {
	var req sessionstream.RegisterKeyRequest
	if json.Unmarshal(body, &req) != nil {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	pub, err := sessionstream.DecodePublicKey(req.PublicKey)
	if err != nil {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	if bad := s.verify(r, body, pub); bad != nil {
		return *bad
	}
	id := sessionstream.KeyID(pub)
	if s.forceKeyConflict || (s.loginKey != "" && s.loginKey != id) {
		return refusal(http.StatusConflict, sessionstream.ReasonForbidden, sessionstream.ActionStop, nil)
	}
	s.keys[id], s.loginKey = pub, id
	return reply{status: http.StatusOK, body: sessionstream.RegisterKeyResponse{KeyID: id, CreatedAt: time.Now()}}
}

func (s *Server) open(r *http.Request, body []byte, _ *Call) reply {
	if s.refuseOpen > 0 {
		s.refuseOpen--
		return refusal(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry, nil)
	}
	if s.nameTaken {
		return refusal(http.StatusConflict, sessionstream.ReasonSessionNameTaken, sessionstream.ActionStop, nil)
	}
	cnv := r.PathValue("cnv")
	var req sessionstream.OpenSessionRequest
	if json.Unmarshal(body, &req) != nil || !sessionstream.ValidConversationID(cnv) || !sessionstream.ValidLayerID(req.LayerID) ||
		strings.TrimPrefix(req.SessionID, "ses_") != strings.TrimPrefix(cnv, "cnv_") {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	c := s.convs[cnv]
	if c == nil {
		c = &conversation{sessionID: req.SessionID, layers: map[string]*layerState{}}
		s.convs[cnv] = c
	}
	if rep := s.liveGuard(c); rep != nil {
		return *rep
	}
	l := c.layers[req.LayerID]
	if l == nil {
		l = &layerState{cur: sessionstream.NewCursor()}
		c.layers[req.LayerID] = l
	}
	if s.dropOpenReply > 0 {
		s.dropOpenReply--
		return reply{drop: true} // opened, but the client never hears the 200
	}
	return reply{status: http.StatusOK, body: sessionstream.OpenSessionResponse{
		ConversationID: cnv, LayerID: req.LayerID, Status: sessionstream.StatusLive,
		StoredThrough: l.cur.StoredThrough, PrefixSHA256: l.cur.PrefixSHA256,
	}}
}

// liveGuard refuses writes to a deleted or closed conversation.
func (s *Server) liveGuard(c *conversation) *reply {
	switch {
	case c.deleted:
		x := refusal(http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe, nil)
		return &x
	case c.closed != nil:
		x := refusal(http.StatusConflict, sessionstream.ReasonSessionClosed, sessionstream.ActionStop, nil)
		return &x
	}
	return nil
}

func (s *Server) appendLines(r *http.Request, body []byte, call *Call) reply {
	cnv, layer := r.PathValue("cnv"), r.PathValue("layer")
	c := s.convs[cnv]
	if c == nil || c.layers[layer] == nil {
		return refusal(http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop, nil)
	}
	if rep := s.liveGuard(c); rep != nil {
		return *rep
	}
	l := c.layers[layer]
	off, err := sessionstream.ParseOffset(r.URL.Query().Get("offset"))
	if err != nil {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	call.Offset = off
	if rep := s.appendKnobs(l); rep != nil {
		return *rep
	}
	key := func(o int64) (string, error) { return sessionstream.BytesKey(TeamID, cnv, layer, o) }
	req := sessionstream.AppendRequest{
		Offset: off, PrefixSHA256: r.Header.Get(sessionstream.HeaderPrefixHash), Body: body,
		Signature: r.Header.Get(sessionstream.HeaderSignature),
	}
	next, ref, err := sessionstream.Append(r.Context(), s.Store, key, l.cur, req)
	// As api-go's appendRecovering: only an offset past the row can mean the
	// row lost progress the stored objects still hold.
	if err == nil && ref != nil && ref.Body.Reason == sessionstream.ReasonOffsetGap && off > l.cur.StoredThrough {
		cur, deferred := s.recoveredCursor(r.Context(), cnv, layer, l.cur)
		if deferred != nil {
			return *deferred
		}
		if cur.StoredThrough != l.cur.StoredThrough {
			next, ref, err = sessionstream.Append(r.Context(), s.Store, key, cur, req)
		}
	}
	if err != nil {
		return refusal(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry, nil)
	}
	l.cur = next // kept on a refusal too, as api-go does: it is always a checked stored end
	if ref != nil {
		return reply{status: ref.Status, body: ref.Body}
	}
	if s.dropAfterWrite > 0 {
		s.dropAfterWrite--
		return reply{drop: true} // stored, but the client never hears the 200
	}
	return reply{status: http.StatusOK, body: sessionstream.AppendResponse{
		StoredThrough: next.StoredThrough, PrefixSHA256: next.PrefixSHA256, TeammateComments: s.undelivered(),
		TeammateCommentsListed: !s.listFails,
	}}
}

// undelivered is what api-go puts in an append answer: queued comments with no
// recorded ack, oldest first, at most MaxTeammateCommentsPerAppend. mu held.
func (s *Server) undelivered() []sessionstream.TeammateComment {
	if s.listFails {
		return nil
	}
	var out []sessionstream.TeammateComment
	for _, c := range s.comments {
		if _, done := s.delivered[c.ID]; done || len(out) == sessionstream.MaxTeammateCommentsPerAppend {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ackTeammateComments records the first ack per comment id, as api-go does.
func (s *Server) ackTeammateComments(r *http.Request, body []byte, _ *Call) reply {
	var req sessionstream.AckTeammateCommentsRequest
	if json.Unmarshal(body, &req) != nil || len(req.Acks) == 0 || len(req.Acks) > sessionstream.MaxTeammateCommentAcks {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	if s.ackDropBefore > 0 { // the connection dies before the server acts
		s.ackDropBefore--
		return reply{drop: true}
	}
	if s.ackRefusal != nil && s.ackRefusals > 0 {
		x := *s.ackRefusal
		if s.ackRefusals--; s.ackRefusals == 0 {
			s.ackRefusal = nil
		}
		return x
	}
	s.ackBodies = append(s.ackBodies, req)
	if s.ackIgnored > 0 { // answered 200, recorded nothing: a lost write
		s.ackIgnored--
		return reply{status: http.StatusOK, body: sessionstream.AckTeammateCommentsResponse{}}
	}
	recorded := 0
	for _, a := range req.Acks {
		if _, done := s.delivered[a.ID]; !done && a.DeliveredAtSeq >= 1 {
			s.delivered[a.ID] = a.DeliveredAtSeq
			recorded++
		}
	}
	if s.ackDropAfter > 0 { // recorded, but the caller never hears
		s.ackDropAfter--
		return reply{drop: true}
	}
	return reply{status: http.StatusOK, body: sessionstream.AckTeammateCommentsResponse{Recorded: recorded}}
}

// appendKnobs applies the one-shot refusals before the append runs. mu held.
func (s *Server) appendKnobs(l *layerState) *reply {
	stored := l.cur.StoredThrough
	switch {
	case s.appendRefusal != nil:
		x := *s.appendRefusal
		s.appendRefusal = nil
		return &x
	case s.forceNewLayer > 0:
		s.forceNewLayer--
		x := refusal(http.StatusConflict, sessionstream.ReasonBytesConflict, sessionstream.ActionNewLayer, &stored)
		return &x
	case s.forceResync != nil:
		at := *s.forceResync
		s.forceResync = nil
		x := refusal(http.StatusConflict, sessionstream.ReasonOffsetGap, sessionstream.ActionResync, &at)
		return &x
	case s.rateLimit > 0:
		s.rateLimit--
		x := refusal(http.StatusTooManyRequests, sessionstream.ReasonRateLimited, sessionstream.ActionRetry, nil)
		x.body = sessionstream.ErrorResponse{Error: "slow down", Reason: sessionstream.ReasonRateLimited, Action: sessionstream.ActionRetry, RetryAfterMS: &s.rateLimitMS}
		return &x
	}
	return nil
}

func (s *Server) closeSession(r *http.Request, body []byte, _ *Call) reply {
	cnv := r.PathValue("cnv")
	var req sessionstream.CloseRequest
	c := s.convs[cnv]
	if json.Unmarshal(body, &req) != nil || c == nil || c.layers[req.LayerID] == nil {
		return refusal(http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop, nil)
	}
	if c.deleted {
		return *s.liveGuard(c)
	}
	if s.closeRefusal != nil {
		x := *s.closeRefusal
		s.closeRefusal = nil
		return x
	}
	// As api-go's closeLocked (write.go): a close claiming more than the row
	// holds is judged against the end recovery proves (kept even if the close
	// is refused), and the request is compared with the layer before anything
	// else, closed or not. A mismatch answers resync from that proven end.
	// Answering new_layer here once hid a client that ignored stored_through
	// on close.
	l := c.layers[req.LayerID]
	if req.StoredThrough > l.cur.StoredThrough {
		cur, deferred := s.recoveredCursor(r.Context(), cnv, req.LayerID, l.cur)
		if deferred != nil {
			return *deferred
		}
		l.cur = cur
	}
	cur := l.cur
	if req.StoredThrough != cur.StoredThrough || req.StreamSHA256 != cur.PrefixSHA256 {
		stored := cur.StoredThrough
		return refusal(http.StatusConflict, sessionstream.ReasonStreamMismatch, sessionstream.ActionResync, &stored)
	}
	if c.closed != nil && c.closed.LayerID == req.LayerID {
		return reply{status: http.StatusOK, body: struct{}{}} // idempotent repeat
	}
	if rep := s.liveGuard(c); rep != nil {
		return *rep
	}
	c.closed = &Closed{CloseRequest: req, StoredAtClose: cur.StoredThrough}
	return reply{status: http.StatusOK, body: struct{}{}}
}

// storageRetryAfterMS is api-go's retry hint when storage cannot answer
// (handler.go storageRetryAfterMS).
const storageRetryAfterMS = 2000

// unavailable is api-go's errUnavailable: nothing is proven right now, retry.
func unavailable(msg string) reply {
	ms := int64(storageRetryAfterMS)
	return reply{status: http.StatusServiceUnavailable, body: sessionstream.ErrorResponse{
		Error: msg, Reason: sessionstream.ReasonUnavailable, Action: sessionstream.ActionRetry, RetryAfterMS: &ms,
	}}
}

// recoveredCursor mirrors api-go's: where a layer really ends, for a request
// that claims more than its row holds. The row stands when the store holds
// nothing past it, or bytes that disagree with it. A rebuild that cannot run
// proves nothing and answers a retry, never a resync that could send a client
// behind bytes it freed on a 200. mu held.
func (s *Server) recoveredCursor(ctx context.Context, cnv, layer string, row sessionstream.Cursor) (sessionstream.Cursor, *reply) {
	if s.deferRecovery {
		x := unavailable("the stored end cannot be verified yet; retry")
		return sessionstream.Cursor{}, &x
	}
	prefix, err := sessionstream.BytesPrefix(TeamID, cnv, layer)
	if err != nil {
		x := unavailable("storage is temporarily unavailable")
		return sessionstream.Cursor{}, &x
	}
	keys := s.Store.Keys(prefix)
	var extent int64
	for _, k := range keys {
		if off, ok := sessionstream.ParseBytesKeyOffset(k); ok {
			if b, _, err := s.Store.Get(ctx, k); err == nil {
				extent = max(extent, off+int64(len(b)))
			}
		}
	}
	if extent <= row.StoredThrough {
		return row, nil
	}
	cur, err := sessionstream.RecoverCursor(ctx, s.Store, keys, row)
	switch {
	case errors.Is(err, sessionstream.ErrRecoveryDiverged):
		return row, nil
	case err != nil:
		x := unavailable("storage is temporarily unavailable")
		return sessionstream.Cursor{}, &x
	case cur.StoredThrough <= row.StoredThrough:
		return row, nil
	}
	return cur, nil
}

func (s *Server) remove(r *http.Request, _ []byte, _ *Call) reply {
	cnv := r.PathValue("cnv")
	c := s.convs[cnv]
	if c == nil {
		return refusal(http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop, nil)
	}
	if s.refuseDelete {
		return refusal(http.StatusForbidden, sessionstream.ReasonForbidden, sessionstream.ActionStop, nil)
	}
	if c.deleted {
		return refusal(http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe, nil)
	}
	s.deleteLocked(cnv, c)
	return reply{status: http.StatusAccepted, body: struct{}{}}
}

func (s *Server) deleteLocked(cnv string, c *conversation) {
	c.deleted = true
	if prefix, err := sessionstream.ConversationPrefix(TeamID, cnv); err == nil {
		s.Store.DeletePrefix(prefix)
	}
}
