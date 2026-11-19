package sessionstream

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// refServer is the smallest correct server: signature check, the shared Append
// algorithm, and a fault hook that drops the response after storing. It proves
// the reference client and the algorithm agree end to end over real HTTP.
type refServer struct {
	mu         sync.Mutex
	pub        ed25519.PublicKey
	store      *MemoryStore
	cur        Cursor
	dropReply  int // drop this many successful replies after storing
	signatures int
}

func (s *refServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	sig, err := ParseSignature(r.Header.Get(HeaderSignature))
	if err == nil {
		err = Verify(s.pub, sig, r.Method, r.URL.RequestURI(), body, time.Now())
	}
	if err != nil {
		writeJSON(w, 401, ErrorResponse{Error: "bad signature", Reason: ReasonBadSignature, Action: ActionReauth})
		return
	}
	s.signatures++
	off, err := ParseOffset(r.URL.Query().Get("offset"))
	if err != nil {
		writeJSON(w, 400, ErrorResponse{Error: err.Error(), Reason: ReasonInvalidRequest, Action: ActionFix})
		return
	}
	next, ref, err := Append(r.Context(), s.store, tKey, s.cur, AppendRequest{Offset: off, PrefixSHA256: r.Header.Get(HeaderPrefixHash), Body: body})
	if err != nil {
		writeJSON(w, 503, ErrorResponse{Error: "storage", Reason: ReasonUnavailable, Action: ActionRetry})
		return
	}
	if ref != nil {
		writeJSON(w, ref.Status, ref.Body)
		return
	}
	s.cur = next
	if s.dropReply > 0 {
		s.dropReply--
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // stored, but the client never hears the 200
		return
	}
	writeJSON(w, 200, AppendResponse{StoredThrough: next.StoredThrough, PrefixSHA256: next.PrefixSHA256})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newRef(t *testing.T) (*refServer, *Streamer) {
	t.Helper()
	pub, priv := testKey(t)
	srv := &refServer{pub: pub, store: NewMemoryStore(), cur: NewCursor()}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c := &Client{BaseURL: ts.URL, Token: "t", Key: priv, HTTP: ts.Client()}
	return srv, NewStreamer(c, tConv, tLayer)
}

func TestStreamerDeliversExactlyOnceThroughLostReplies(t *testing.T) {
	srv, st := newRef(t)
	want := lines(0, 40)
	srv.dropReply = 2
	half := nextLineEnd(want, 0, 20)
	st.Write(want[:half])
	for attempt := 0; st.Pending() > 0; attempt++ {
		if attempt > 5 {
			t.Fatal("streamer never drained")
		}
		if err := st.Flush(context.Background()); err != nil {
			t.Logf("attempt %d: %v", attempt, err)
		} // a dropped reply surfaces as an error; retry
	}
	st.Write(want[half:])
	if err := st.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, _, _ := srv.store.Reassemble(context.Background(), mustPrefix(t))
	if !bytes.Equal(got, want) || st.StreamSHA256() != BytesSHA256(want) || st.Acked() != int64(len(want)) {
		t.Fatalf("stream differs after lost replies: stored %d bytes, want %d", len(got), len(want))
	}
}

func TestStreamerRefusesToContinueAForeignStream(t *testing.T) {
	srv, st := newRef(t)
	srv.cur = mustAppend(t, srv.store, srv.cur, 0, EmptyStreamSHA256, lines(100, 103))
	st.Write(lines(0, 3))
	err := st.Flush(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Body.Reason != ReasonPrefixMismatch && apiErr.Body.Reason != ReasonBytesConflict {
		t.Fatalf("foreign stream not refused: %v", err)
	}
	if st.Acked() != 0 || st.Pending() == 0 {
		t.Fatal("client freed bytes the server never stored")
	}
}

func TestClientSignsEveryRequest(t *testing.T) {
	srv, st := newRef(t)
	st.Write(lines(0, 2))
	if err := st.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Client.Key = nil
	st.Write(lines(2, 3))
	err := st.Flush(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || srv.signatures != 1 {
		t.Fatalf("unsigned request not refused: %v", err)
	}
}

func TestStreamerHoldsBackAPartialLine(t *testing.T) {
	srv, st := newRef(t)
	full := lines(0, 3)
	st.Write(full[:len(full)-5])
	if err := st.Flush(context.Background()); err != nil {
		t.Fatalf("flush with a partial tail: %v", err)
	}
	st.Write(full[len(full)-5:])
	if err := st.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := srv.store.Reassemble(context.Background(), mustPrefix(t))
	if !bytes.Equal(got, full) || st.Pending() != 0 {
		t.Fatalf("partial line was sent early or lost")
	}
}

func TestBatchLenSplitsOnLineBoundaries(t *testing.T) {
	big := []byte(strings.Repeat("{\"a\":\""+strings.Repeat("x", 1000)+"\"}\n", 400))
	n := batchLen(big)
	if n > MaxBatchBytes || big[n-1] != '\n' {
		t.Fatalf("batch of %d bytes is not line-aligned under the cap", n)
	}
}
