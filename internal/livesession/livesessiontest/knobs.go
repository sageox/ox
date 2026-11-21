package livesessiontest

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

func (s *Server) knob(f func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f()
}

func (s *Server) latencyValue() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latency
}

func (s *Server) openDelayValue() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openDelay
}

func (s *Server) enter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight++
	s.maxInflight = max(s.maxInflight, s.inflight)
}

func (s *Server) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
}

func (s *Server) record(c Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}

// DropAfterWrite stores the next n successful appends, then closes the
// connection without a reply: bytes landed, the client believes they did not.
func (s *Server) DropAfterWrite(n int) { s.set(func() { s.dropAfterWrite = n }) }

// ForceNewLayer refuses the next n appends with new_layer (stream diverged).
func (s *Server) ForceNewLayer(n int) { s.set(func() { s.forceNewLayer = n }) }

// ForceResync refuses the next append with resync at storedThrough.
func (s *Server) ForceResync(storedThrough int64) { s.set(func() { s.forceResync = &storedThrough }) }

// RefuseOpen answers the next n open (PUT) requests 503, after the key registered.
func (s *Server) RefuseOpen(n int) { s.set(func() { s.refuseOpen = n }) }

// DropOpenReply opens the next n sessions, then closes the connection without
// a reply: the conversation exists, the client believes the open failed.
func (s *Server) DropOpenReply(n int) { s.set(func() { s.dropOpenReply = n }) }

// RefuseDelete answers every delete 403 forbidden (action stop) until cleared.
func (s *Server) RefuseDelete(on bool) { s.set(func() { s.refuseDelete = on }) }

// RefuseNextAppend answers the next append with this refusal, whatever it is;
// an empty action models a body this client cannot classify.
func (s *Server) RefuseNextAppend(status int, reason string, action sessionstream.Action) {
	s.set(func() { x := refusal(status, reason, action, nil); s.appendRefusal = &x })
}

// RefuseNextClose answers the next close with this refusal, whatever it is.
// stored is the refusal's stored_through; nil leaves it out.
func (s *Server) RefuseNextClose(status int, reason string, action sessionstream.Action, stored *int64) {
	s.set(func() { x := refusal(status, reason, action, stored); s.closeRefusal = &x })
}

// RefuseNextOpens answers the next n valid opens (PUT), reopen or not, with
// this refusal, where api-go's admission refuses one: after the request
// validates, before any layer is created. RefuseNextOpens(n, 403, feature_off,
// stop) is the rollout flag turned off after the client's settings cache said on.
func (s *Server) RefuseNextOpens(n int, status int, reason string, action sessionstream.Action) {
	s.set(func() { x := refusal(status, reason, action, nil); s.openRefusal, s.openRefusals = &x, n })
}

// RefuseNextReopen answers the next open that carries reopen with this refusal;
// an open without reopen is never affected.
func (s *Server) RefuseNextReopen(status int, reason string, action sessionstream.Action) {
	s.set(func() { x := refusal(status, reason, action, nil); s.reopenRefusal = &x })
}

// LoseStoredBytes erases every stored object of cnv and rewinds its layers to
// empty, as a storage restore from before the session would: the server now
// stands behind every ack it gave. Later appends and closes answer what api-go
// answers for an empty layer (close: resync from 0).
func (s *Server) LoseStoredBytes(cnv string) {
	s.set(func() {
		c := s.convs[cnv]
		if c == nil {
			return
		}
		for id, l := range c.layers {
			if prefix, err := sessionstream.BytesPrefix(TeamID, cnv, id); err == nil {
				s.Store.DeletePrefix(prefix)
			}
			l.cur = sessionstream.NewCursor()
		}
	})
}

// LoseProgressRow rewinds cnv's layer rows to empty while every stored object
// survives (a Postgres restore or reset; ADR-247 keeps the row rebuildable).
// The next append past the row or close claiming more rebuilds the cursor
// from the objects, as api-go does, so the client sees nothing but latency.
func (s *Server) LoseProgressRow(cnv string) {
	s.set(func() {
		if c := s.convs[cnv]; c != nil {
			for _, l := range c.layers {
				l.cur = sessionstream.NewCursor()
			}
		}
	})
}

// DeferRecovery makes every cursor rebuild unable to prove anything (the
// per-layer budget is spent, or the listing failed) until cleared. Requests
// that need one answer 503 unavailable, action retry, with retry_after_ms:
// the client must back off and keep its ack, never resend from behind it.
func (s *Server) DeferRecovery(on bool) { s.set(func() { s.deferRecovery = on }) }

// RateLimit answers the next n appends 429 with retry_after_ms.
func (s *Server) RateLimit(n int, retryAfterMS int64) {
	s.set(func() { s.rateLimit, s.rateLimitMS = n, retryAfterMS })
}

// RejectAuth makes every request fail 401 reauth until cleared.
func (s *Server) RejectAuth(on bool) { s.set(func() { s.rejectAuth = on }) }

// ForceKeyConflict makes key registration answer 409 forbidden until cleared
// (the login already has a different key).
func (s *Server) ForceKeyConflict(on bool) { s.set(func() { s.forceKeyConflict = on }) }

// SetDown makes every request fail 503 until cleared.
func (s *Server) SetDown(on bool) { s.set(func() { s.down = on }) }

// Hang makes requests block until the client gives up (a stalled network).
func (s *Server) Hang(on bool) { s.set(func() { s.hang = on }) }

// BlockAtEdge answers the next n requests with this method ("" for any) as
// the network edge in front of api-go would: 403 with this content type and
// body, which is no live-capture refusal (CloudFront's HTML page, the WAF's
// custom JSON).
func (s *Server) BlockAtEdge(method string, n int, contentType, body string) {
	s.set(func() { s.edgeMethod, s.edgeBlocks, s.edgeType, s.edgeBody = method, n, contentType, body })
}

// takeEdgeBlock spends one BlockAtEdge answer for r, if any are left.
func (s *Server) takeEdgeBlock(r *http.Request) (contentType, body string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.edgeBlocks <= 0 || (s.edgeMethod != "" && s.edgeMethod != r.Method) {
		return "", "", false
	}
	s.edgeBlocks--
	return s.edgeType, s.edgeBody, true
}

// HangAppendsOver makes appends whose body is over n bytes block until the
// client gives up, as a link too slow to carry a large batch inside one
// request timeout; smaller appends go through. 0 clears it.
func (s *Server) HangAppendsOver(n int) { s.set(func() { s.hangAppendsOver = n }) }

// hangsAppend reports whether the HangAppendsOver knob holds this request. mu held.
func (s *Server) hangsAppend(r *http.Request, body []byte) bool {
	return s.hangAppendsOver > 0 && r.Method == http.MethodPost &&
		strings.HasSuffix(r.URL.Path, "/lines") && len(body) > s.hangAppendsOver
}

// DelayOpen delays only open (PUT) requests, so a request sent after an open
// can reach the server before it.
func (s *Server) DelayOpen(d time.Duration) { s.set(func() { s.openDelay = d }) }

// Latency delays every request, so concurrent callers overlap in time.
func (s *Server) Latency(d time.Duration) { s.set(func() { s.latency = d }) }

// DeleteSession deletes a conversation as if the owner removed it in the web
// app: its objects are erased and later requests answer 410.
func (s *Server) DeleteSession(cnv string) {
	s.set(func() {
		if c := s.convs[cnv]; c != nil {
			s.deleteLocked(cnv, c)
		}
	})
}

func (s *Server) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

// Calls returns every request seen so far, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallsTo counts requests with this method whose path ends in suffix.
func (s *Server) CallsTo(method, suffix string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Method == method && len(c.Path) >= len(suffix) && c.Path[len(c.Path)-len(suffix):] == suffix {
			n++
		}
	}
	return n
}

// InFlight is how many requests the fake is handling right now, so a test can
// act while a client is mid-request (and holding whatever it locked for it).
func (s *Server) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight
}

// MaxConcurrent is the most requests that were in flight at once.
func (s *Server) MaxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInflight
}

// ClosedWith returns what the server saw when cnv was closed.
func (s *Server) ClosedWith(cnv string) (Closed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[cnv]; c != nil && c.closed != nil {
		return *c.closed, true
	}
	return Closed{}, false
}

// OpenedWith returns the newest open request the server accepted for cnv, as
// it decoded it from the wire.
func (s *Server) OpenedWith(cnv string) (sessionstream.OpenSessionRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[cnv]; c != nil && c.opened.LayerID != "" {
		return c.opened, true
	}
	return sessionstream.OpenSessionRequest{}, false
}

// IsDeleted reports whether cnv was deleted on the server.
func (s *Server) IsDeleted(cnv string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[cnv]
	return c != nil && c.deleted
}

// Layers lists the layer ids the server holds for cnv.
func (s *Server) Layers(cnv string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if c := s.convs[cnv]; c != nil {
		for id := range c.layers {
			out = append(out, id)
		}
	}
	return out
}

// Stored reassembles the bytes the server holds for one layer, in offset order.
func (s *Server) Stored(t testing.TB, cnv, layer string) []byte {
	t.Helper()
	prefix, err := sessionstream.BytesPrefix(TeamID, cnv, layer)
	if err != nil {
		t.Fatalf("bytes prefix: %v", err)
	}
	got, _, err := s.Store.Reassemble(t.Context(), prefix)
	if err != nil {
		t.Fatalf("reassemble layer: %v", err)
	}
	return got
}

// Preload stores batches on a layer directly, one object each, as if an earlier
// client run had sent them (its acknowledgements lost). It creates the layer.
func (s *Server) Preload(t testing.TB, cnv, layer string, batches ...[]byte) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[cnv]
	if c == nil {
		c = &conversation{layers: map[string]*layerState{}}
		s.convs[cnv] = c
	}
	l := c.layers[layer]
	if l == nil {
		l = &layerState{cur: sessionstream.NewCursor()}
		c.layers[layer] = l
	}
	key := func(o int64) (string, error) { return sessionstream.BytesKey(TeamID, cnv, layer, o) }
	for _, b := range batches {
		next, ref, err := sessionstream.Append(t.Context(), s.Store, key, l.cur, sessionstream.AppendRequest{
			Offset: l.cur.StoredThrough, PrefixSHA256: l.cur.PrefixSHA256, Body: b,
		})
		if err != nil || ref != nil {
			t.Fatalf("preload batch: err=%v refusal=%v", err, ref)
		}
		l.cur = next
	}
}

// QueueTeammateComment adds a comment every append answer carries until an ack
// for its id is recorded, as api-go re-sends undelivered ones.
func (s *Server) QueueTeammateComment(c sessionstream.TeammateComment) {
	s.set(func() { s.comments = append(s.comments, c) })
}

// DeliveredAtSeq is the delivered_at_seq recorded for a comment id.
func (s *Server) DeliveredAtSeq(id string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok := s.delivered[id]
	return seq, ok
}

// AckRequests are the ack bodies the server accepted, in order.
func (s *Server) AckRequests() []sessionstream.AckTeammateCommentsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sessionstream.AckTeammateCommentsRequest(nil), s.ackBodies...)
}

// RefuseNextAck answers the next ack with this refusal.
func (s *Server) RefuseNextAck(status int, reason string, action sessionstream.Action) {
	s.RefuseAcks(1, status, reason, action)
}

// IgnoreNextAcks answers the next n acks 200 without recording them, as a lost
// write behind a healthy reply would: the note stays undelivered server-side.
func (s *Server) IgnoreNextAcks(n int) { s.set(func() { s.ackIgnored = n }) }

// RemoveTeammateComment stops listing a comment, as the server does once the
// owner opts out or the author deletes it before delivery.
func (s *Server) RemoveTeammateComment(id string) {
	s.set(func() {
		s.comments = slices.DeleteFunc(s.comments, func(c sessionstream.TeammateComment) bool { return c.ID == id })
	})
}

// FailTeammateLookup makes appends answer without a pending list
// (teammate_comments_listed false and no notes), as a server whose lookup erred.
func (s *Server) FailTeammateLookup(on bool) { s.set(func() { s.listFails = on }) }

// RefuseOpensAsNameTaken answers every open 409 session_name_taken (action stop).
func (s *Server) RefuseOpensAsNameTaken(on bool) { s.set(func() { s.nameTaken = on }) }

// RefuseAcks answers the next n acks with this refusal and records nothing: a
// 503 (action retry) is a transient outage, a 400 (action fix) a permanent one.
func (s *Server) RefuseAcks(n int, status int, reason string, action sessionstream.Action) {
	s.set(func() { x := refusal(status, reason, action, nil); s.ackRefusal, s.ackRefusals = &x, n })
}

// DropAcksBeforeRecording closes the connection on the next n acks before the
// server records them: the caller sees a network error, the server nothing.
func (s *Server) DropAcksBeforeRecording(n int) { s.set(func() { s.ackDropBefore = n }) }

// DropAcksAfterRecording records the next n acks, then closes the connection
// without an answer: the caller must treat the ack as unsent and send it again.
func (s *Server) DropAcksAfterRecording(n int) { s.set(func() { s.ackDropAfter = n }) }

// AckAttempts are the ack requests received, in order, as their callers saw them.
func (s *Server) AckAttempts() []AckAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AckAttempt(nil), s.ackAttempts...)
}
