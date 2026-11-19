package sessionstream

// Adversarial tests for the reference client (Streamer), the executable
// specification a production client must match. ADR-247 twin conformance rule
// 2: "the client frees local bytes only after a 200 that covers them". Each
// test scripts a server answer a real network or a buggy proxy can produce and
// asserts what the client may free and whether it stops.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const (
	advCnv   = "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122"
	advLayer = "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
)

// advScripted serves appends with answer(offset, body, call#).
func advScripted(t *testing.T, answer func(off int64, body []byte, n int64) (int, any)) (*Streamer, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		status, out := answer(off, body, n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if raw, ok := out.(string); ok {
			_, _ = io.WriteString(w, raw)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, Token: "t", Key: advKey(9), HTTP: &http.Client{Timeout: 5 * time.Second}}
	return NewStreamer(c, advCnv, advLayer), &calls
}

func advResync(stored int64) ErrorResponse {
	return ErrorResponse{Error: "gap", Reason: ReasonOffsetGap, Action: ActionResync, StoredThrough: &stored}
}

// A 409 resync that claims the server holds every pending byte is not a 200:
// nothing proved those bytes arrived (no hash was compared). Freeing them
// means a later close mismatch can never be repaired by a resend.
func TestAdversarialStreamerNeverFreesUnprovenBytesOnResync(t *testing.T) {
	st, _ := advScripted(t, func(off int64, body []byte, _ int64) (int, any) {
		return http.StatusConflict, advResync(off + int64(len(body)))
	})
	data := []byte(`{"a":1}` + "\n" + `{"b":2}` + "\n")
	st.Write(data)
	err := st.Flush(context.Background())
	if st.Pending() != len(data) || st.Acked() != 0 {
		t.Fatalf("freed %d of %d bytes on a resync that proved nothing (acked=%d, flush err=%v)",
			len(data)-st.Pending(), len(data), st.Acked(), err)
	}
}

// A server (or proxy) that keeps answering resync at the client's own offset
// must not pin the client in a hot loop: the production client caps resyncs
// per drain (ox maxResyncs=16); the reference one must stop too.
func TestAdversarialStreamerResyncLoopIsBounded(t *testing.T) {
	const ceiling = 200
	st, calls := advScripted(t, func(off int64, _ []byte, n int64) (int, any) {
		if n >= ceiling {
			return http.StatusServiceUnavailable, ErrorResponse{Error: "stop", Reason: ReasonUnavailable, Action: ActionRetry}
		}
		return http.StatusConflict, advResync(off)
	})
	st.Write([]byte(`{"a":1}` + "\n"))
	_ = st.Flush(context.Background())
	if got := calls.Load(); got > 32 {
		t.Fatalf("one Flush sent %d requests answered resync-at-the-same-offset; want a small bound", got)
	}
}

// A 200 whose stream hash disagrees, or whose stored_through lies outside the
// bytes the client holds, frees nothing and stops with ErrStreamDiverged.
func TestAdversarialStreamerRefusesA200ItCannotProve(t *testing.T) {
	data := []byte(`{"a":1}` + "\n")
	cases := map[string]func(off int64, body []byte) AppendResponse{
		"wrong hash": func(off int64, body []byte) AppendResponse {
			return AppendResponse{StoredThrough: off + int64(len(body)), PrefixSHA256: BytesSHA256([]byte("other"))}
		},
		"past the held bytes": func(off int64, body []byte) AppendResponse {
			return AppendResponse{StoredThrough: off + int64(len(body)) + 10, PrefixSHA256: BytesSHA256(body)}
		},
		"behind the acked offset": func(int64, []byte) AppendResponse {
			return AppendResponse{StoredThrough: -1, PrefixSHA256: EmptyStreamSHA256}
		},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := advScripted(t, func(off int64, body []byte, _ int64) (int, any) { return http.StatusOK, answer(off, body) })
			st.Write(data)
			if err := st.Flush(context.Background()); !errors.Is(err, ErrStreamDiverged) {
				t.Fatalf("Flush = %v, want ErrStreamDiverged", err)
			}
			if st.Pending() != len(data) || st.Acked() != 0 {
				t.Fatalf("freed bytes on an unprovable 200")
			}
		})
	}
}

// What a proxy or load balancer sends instead of the contract's JSON: an
// HTML 502, an empty 503, a truncated 200. None may free a byte, and the
// non-2xx ones must read as retry, never as a permanent stop.
func TestAdversarialStreamerSurvivesForeignResponses(t *testing.T) {
	data := []byte(`{"a":1}` + "\n")
	cases := []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{"html 502 from a proxy", http.StatusBadGateway, "<html>bad gateway</html>", true},
		{"empty 503", http.StatusServiceUnavailable, "", true},
		{"truncated 200", http.StatusOK, `{"stored_through":`, false},
		{"200 with no body", http.StatusOK, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// After 50 identical answers the script says 503 so a client that
			// loops on this answer fails the bound below instead of hanging.
			st, calls := advScripted(t, func(_ int64, _ []byte, n int64) (int, any) {
				if n > 50 {
					return http.StatusServiceUnavailable, ""
				}
				return tc.status, tc.body
			})
			st.Write(data)
			err := st.Flush(context.Background())
			if n := calls.Load(); n > 10 {
				t.Fatalf("Flush sent %d requests on %q: it loops on an answer that acknowledges nothing", n, tc.name)
			}
			if st.Pending() != len(data) || st.Acked() != 0 {
				t.Fatalf("freed bytes on %q (err=%v)", tc.name, err)
			}
			var api *APIError
			if tc.retry && (!errors.As(err, &api) || api.Body.Action != ActionRetry) {
				t.Fatalf("Flush = %v, want a retry APIError", err)
			}
			if err == nil {
				t.Fatalf("Flush succeeded on %q", tc.name)
			}
		})
	}
}

// A server that accepts the request and never answers: the drain gives up at
// its deadline with an error and frees nothing.
func TestAdversarialStreamerHungServerFreesNothing(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http notices a client hang-up only once the body is drained
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	st := NewStreamer(&Client{BaseURL: srv.URL, Token: "t", Key: advKey(9)}, advCnv, advLayer)
	data := []byte(`{"a":1}` + "\n")
	st.Write(data)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := st.Flush(ctx); err == nil {
		t.Fatal("Flush against a hung server returned nil")
	}
	if st.Pending() != len(data) {
		t.Fatal("freed bytes the server never acknowledged")
	}
}
