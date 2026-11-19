package livesession_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionstream"
)

// The server lost bytes it had acknowledged (a storage restore). Close answers
// resync from 0, as api-go does for an empty layer; ox still holds its whole
// spool, so it resends from there and closes the same layer. A client that
// only retried the close stayed closing for good, and the fake once answered
// new_layer here, which hid that.
func TestCloseAfterTheServerLostAckedBytesResendsAndClosesTheSameLayer(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.LoseStoredBytes(e.st.ConversationID)
	before := len(e.srv.Calls())
	var logs lockedBuffer
	e.cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err != nil || !res.Done || res.Status != livesession.StatusClosed {
		t.Fatalf("close after the server lost its bytes: %+v %v", res, err)
	}
	spool := e.spoolBytes()
	// the resend repairs it, but a server that loses acknowledged bytes must show
	line := logs.lineWith("live_session_server_behind_ack")
	for _, want := range []string{"level=ERROR", "stored_through=0", fmt.Sprintf("acked=%d", len(spool)), "session_id=", "conversation_id=", "layer_id="} {
		if !strings.Contains(line, want) {
			t.Fatalf("behind-ack report %q lacks %q", line, want)
		}
	}
	closed, ok := e.srv.ClosedWith(e.st.ConversationID)
	if !ok || closed.LayerID != e.st.LayerID || closed.StreamSHA256 != sessionstream.BytesSHA256(spool) {
		t.Fatalf("closed=%v with %+v, want the opened layer closed over the whole %d-byte spool", ok, closed.CloseRequest, len(spool))
	}
	if n := len(e.srv.Layers(e.st.ConversationID)); n != 1 {
		t.Fatalf("%d layers, want the stream kept on its one layer", n)
	}
	if !bytes.Equal(e.stored(e.st), spool) {
		t.Fatal("the server does not hold the spool again after the resend")
	}
	resentFromZero := false
	for _, c := range e.srv.Calls()[before:] {
		resentFromZero = resentFromZero || (c.Method == http.MethodPost && c.Offset == 0 && c.Status == http.StatusOK)
	}
	if !resentFromZero {
		t.Fatal("close never resent the spool from the server's stored_through 0")
	}
}

// Each answer close's resync can carry, and what ox must do with it.
func TestCloseResyncAnswers(t *testing.T) {
	firstLine := int64(len(lines(0, 1)))
	tests := []struct {
		name string
		// stored maps the spool size to the refusal's stored_through; nil omits it
		stored    func(size int64) *int64
		newLayer  bool
		deferred  bool // the close fails and a later close finishes it
		resendsAt func(size int64) int64
	}{
		{name: "behind the ack at a line boundary resends from there", stored: at(func(int64) int64 { return firstLine }),
			resendsAt: func(int64) int64 { return firstLine }},
		{name: "at the closed length is a diverged stream", stored: at(func(size int64) int64 { return size }), newLayer: true},
		{name: "inside a line is a diverged stream", stored: at(func(int64) int64 { return 5 }), newLayer: true},
		{name: "past the spool is a diverged stream", stored: at(func(size int64) int64 { return size + 100 }), newLayer: true},
		{name: "without stored_through is retried later", stored: func(int64) *int64 { return nil }, deferred: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			// two drains, so firstLine is a stored object boundary: api-go only
			// ever answers with an offset it stored up to
			e.spool(lines(0, 1))
			e.mustDrain()
			e.spool(lines(1, 3))
			e.mustDrain()
			size := int64(len(e.spoolBytes()))
			e.srv.RefuseNextClose(http.StatusConflict, sessionstream.ReasonStreamMismatch, sessionstream.ActionResync, tt.stored(size))
			before := len(e.srv.Calls())

			res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

			if tt.deferred {
				if err == nil || res.Done || e.state().Status != livesession.StatusClosing {
					t.Fatalf("close answered resync without stored_through: %+v %v, want closing and an error", res, err)
				}
				res, err = livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget) // the daemon's next tick
			}
			if err != nil || !res.Done || res.Status != livesession.StatusClosed {
				t.Fatalf("close: %+v %v", res, err)
			}
			st := e.state()
			layers := e.srv.Layers(e.st.ConversationID)
			wantLayers := 1
			if tt.newLayer {
				wantLayers = 2
			}
			if tt.newLayer != (st.LayerID != e.st.LayerID) || len(layers) != wantLayers {
				t.Fatalf("layer %s of %d layers (opened %s), want a new layer: %v", st.LayerID, len(layers), e.st.LayerID, tt.newLayer)
			}
			spool := e.spoolBytes()
			closed, ok := e.srv.ClosedWith(e.st.ConversationID)
			if !ok || closed.LayerID != st.LayerID || closed.StreamSHA256 != sessionstream.BytesSHA256(spool) {
				t.Fatalf("closed=%v with %+v, want layer %s over the whole spool", ok, closed.CloseRequest, st.LayerID)
			}
			if tt.resendsAt != nil {
				want, resent := tt.resendsAt(size), false
				for _, c := range e.srv.Calls()[before:] {
					resent = resent || (c.Method == http.MethodPost && c.Offset == want && c.Status == http.StatusOK)
				}
				if !resent {
					t.Fatalf("no append resent from stored_through %d", want)
				}
			}
		})
	}
}

func at(f func(size int64) int64) func(int64) *int64 {
	return func(size int64) *int64 { v := f(size); return &v }
}

// A server that answers every close with resync (a bug, or a row that never
// recovers) must not hold a close in a loop: it gives up after the resync cap
// and stays closing for a later attempt.
func TestCloseResyncLoopIsCapped(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	rt := &alwaysResyncClose{}
	e.cfg.HTTP = &http.Client{Transport: rt, Timeout: testBudget}

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err == nil || res.Done || e.state().Status != livesession.StatusClosing {
		t.Fatalf("close against a server that always resyncs: %+v %v, want closing and an error", res, err)
	}
	// one close per resync, plus the one that hits the cap (drain.go maxResyncs)
	if n := rt.closes.Load(); n > 17 {
		t.Fatalf("one close sent %d close requests, want at most 17", n)
	}
}

// alwaysResyncClose answers every close 409 resync from 0 and passes every
// other request to the fake.
type alwaysResyncClose struct{ closes atomic.Int32 }

func (rt *alwaysResyncClose) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/close") {
		return http.DefaultTransport.RoundTrip(r)
	}
	rt.closes.Add(1)
	if r.Body != nil {
		_ = r.Body.Close() // nothing to read: the answer does not depend on it
	}
	body := `{"error":"stored stream does not match the close request","reason":"stream_mismatch","action":"resync","stored_through":0}`
	return &http.Response{
		StatusCode: http.StatusConflict, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}, nil
}

// The progress row lost its progress but the stored objects survive. api-go
// rebuilds the cursor from them when the close claims more than the row, so
// the close succeeds on the same layer without resending anything.
func TestCloseAfterTheProgressRowWasLostRecoversWithoutAResend(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.LoseProgressRow(e.st.ConversationID)
	before := len(e.srv.Calls())

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err != nil || !res.Done || res.Status != livesession.StatusClosed {
		t.Fatalf("close after the row was lost: %+v %v", res, err)
	}
	closed, ok := e.srv.ClosedWith(e.st.ConversationID)
	if !ok || closed.LayerID != e.st.LayerID || closed.StreamSHA256 != sessionstream.BytesSHA256(e.spoolBytes()) {
		t.Fatalf("closed=%v with %+v, want the opened layer closed over the whole spool", ok, closed.CloseRequest)
	}
	if n := countAppends(e.srv.Calls()[before:]); n != 0 {
		t.Fatalf("%d appends after the row was lost, want the close to recover the cursor itself", n)
	}
}

// A drain past a lost row: the append past the row's end makes api-go rebuild
// the cursor, and the new bytes land on the same layer.
func TestDrainPastALostProgressRowRecovers(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.LoseProgressRow(e.st.ConversationID)
	e.spool(lines(3, 5))

	if res := e.mustDrain(); !res.Done {
		t.Fatalf("drain past a lost row: %+v", res)
	}
	if !bytes.Equal(e.stored(e.st), e.spoolBytes()) || len(e.srv.Layers(e.st.ConversationID)) != 1 {
		t.Fatal("the stream did not continue on its layer after the row was rebuilt")
	}
}

// Recovery cannot prove the stored end (budget spent, listing failed): api-go
// answers 503 retry with retry_after_ms. The close stays closing with its ack
// intact, hands the hint to the daemon, sends nothing from behind its ack,
// and closes once recovery can run.
func TestCloseWhileRecoveryIsDeferredBacksOffAndKeepsItsAck(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	acked := e.state().Acked
	e.srv.LoseProgressRow(e.st.ConversationID)
	e.srv.DeferRecovery(true)
	before := len(e.srv.Calls())

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err == nil || res.Done || res.RetryAfter <= 0 || e.state().Status != livesession.StatusClosing {
		t.Fatalf("close with recovery deferred: %+v %v, want closing, an error and the server's retry hint", res, err)
	}
	if st := e.state(); st.Acked != acked {
		t.Fatalf("acked moved %d -> %d on a retry answer that proved nothing", acked, st.Acked)
	}
	if n := countAppends(e.srv.Calls()[before:]); n != 0 {
		t.Fatalf("%d appends sent on a deferred recovery, want none", n)
	}
	e.srv.DeferRecovery(false)
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || !res.Done {
		t.Fatalf("close once recovery could run: %+v %v", res, err)
	}
}

func countAppends(calls []livesessiontest.Call) int {
	n := 0
	for _, c := range calls {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/lines") {
			n++
		}
	}
	return n
}
