package sessionstream

// The Streamer frees bytes only on a 200 whose stream hash proves them
// (ADR-247 conformance rule 2). These cases pin the paths around that rule
// that the adversarial suite only checks from the refusal side: a resync that
// claims every held line must still end in a proving 200, and a 200 that
// proves nothing must stop the drain without freeing or looping.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// When the server already holds every line the client holds (lost replies
// left more objects past acked than one resend may re-read, so the full
// resend is refused too_large/resync), there is nothing past the resync offset
// to send. The client must still get a 200 that proves those bytes, or it can
// neither free them nor close with a matching stored_through.
func TestStreamerProvesAResyncThatClaimsEveryHeldLine(t *testing.T) {
	srv, st := newRef(t)
	const objects = MaxResendObjects + 1
	srv.dropReply = objects
	for i := range objects {
		st.Write(lines(i, i+1))
		if err := st.Flush(context.Background()); err == nil {
			t.Fatalf("flush %d: the reply was meant to be lost", i)
		}
	}
	if st.Acked() != 0 {
		t.Fatalf("acked %d with every reply lost", st.Acked())
	}
	if err := st.Flush(context.Background()); err != nil {
		t.Fatalf("flush after the server took every line: %v", err)
	}
	want := lines(0, objects)
	got, _, err := srv.store.Reassemble(context.Background(), mustPrefix(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || st.Acked() != int64(len(want)) || st.Pending() != 0 || st.StreamSHA256() != BytesSHA256(want) {
		t.Fatalf("acked %d of %d (pending %d), stored %d bytes", st.Acked(), len(want), st.Pending(), len(got))
	}
}

// scriptedStreamer serves every append with answer(offset, body, call number).
func scriptedStreamer(t *testing.T, answer func(off int64, body []byte, n int64) (int, any)) (*Streamer, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		status, out := answer(off, body, n)
		writeJSON(w, status, out)
	}))
	t.Cleanup(srv.Close)
	return NewStreamer(&Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}, tConv, tLayer), &calls
}

// A 200 that does not move past acked, or carries no stream hash, proves
// nothing. Before, the first was resent forever and the second freed bytes
// with no hash compared.
func TestStreamerStopsOnA200ThatProvesNothing(t *testing.T) {
	data := lines(0, 2)
	cases := []struct {
		name   string
		answer func(off int64, body []byte) AppendResponse
		want   error
	}{
		{"no progress past acked", func(off int64, _ []byte) AppendResponse {
			return AppendResponse{StoredThrough: off, PrefixSHA256: EmptyStreamSHA256}
		}, ErrStreamDiverged},
		{"no stream hash", func(off int64, body []byte) AppendResponse {
			return AppendResponse{StoredThrough: off + int64(len(body))}
		}, ErrMalformedResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, calls := scriptedStreamer(t, func(off int64, body []byte, _ int64) (int, any) {
				return http.StatusOK, tc.answer(off, body)
			})
			st.Write(data)
			if err := st.Flush(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("Flush = %v, want %v", err, tc.want)
			}
			if calls.Load() != 1 || st.Pending() != len(data) || st.Acked() != 0 {
				t.Fatalf("sent %d requests, freed %d bytes", calls.Load(), len(data)-st.Pending())
			}
		})
	}
}

// A resync may only point at bytes the client holds and has not freed. One
// behind acked means the server lost acknowledged bytes; one past the held end
// claims bytes the client never sent. Neither may move anything.
func TestStreamerRefusesAResyncOutsideHeldBytes(t *testing.T) {
	first, second := lines(0, 1), lines(1, 2)
	cases := map[string]func(acked, held int64) int64{
		"behind acked":      func(acked, _ int64) int64 { return acked - 1 },
		"past the held end": func(_, held int64) int64 { return held + 1 },
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := scriptedStreamer(t, func(off int64, body []byte, n int64) (int, any) {
				if n == 1 {
					return http.StatusOK, AppendResponse{StoredThrough: off + int64(len(body)), PrefixSHA256: BytesSHA256(body)}
				}
				at := stored(off, off+int64(len(body)))
				return http.StatusConflict, ErrorResponse{Error: "gap", Reason: ReasonOffsetGap, Action: ActionResync, StoredThrough: &at}
			})
			st.Write(first)
			if err := st.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			st.Write(second)
			if err := st.Flush(context.Background()); !errors.Is(err, ErrStreamDiverged) {
				t.Fatalf("Flush = %v, want ErrStreamDiverged", err)
			}
			if st.Acked() != int64(len(first)) || st.Pending() != len(second) {
				t.Fatalf("acked %d, pending %d after a resync outside the held bytes", st.Acked(), st.Pending())
			}
		})
	}
}

// Do decodes a 2xx into out only when the body is JSON; an empty or truncated
// body is ErrMalformedResponse, never a zero-valued success.
func TestClientDoRefusesAnUndecodable2xx(t *testing.T) {
	for _, body := range []string{"", `{"stored_through":`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
		var out AppendResponse
		err := c.Do(context.Background(), http.MethodGet, "/", nil, "", nil, &out)
		srv.Close()
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("body %q: Do = %v, want ErrMalformedResponse", body, err)
		}
	}
}
