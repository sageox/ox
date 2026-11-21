package livesession_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

// Failure prevented: the rollout flag turned off after ox's settings cache said
// on, so the first open was refused 403 feature_off (action stop) and ox
// retried it every 30 s for good. Only feature_off ends the stream: any other
// refusal of the open, a stop included, keeps today's retry.
func TestOpenRefusalEndsTheStreamOnlyForFeatureOff(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		reason       string
		wantDisabled bool
	}{
		{"feature off", http.StatusForbidden, sessionstream.ReasonFeatureOff, true},
		{"forbidden is retried", http.StatusForbidden, sessionstream.ReasonForbidden, false},
		{"repo not found is retried", http.StatusNotFound, sessionstream.ReasonNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cfg, dir, p := prepared(t)
			if _, err := livesession.Spool(t.Context(), dir, prompt(), livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			cnv := mustState(t, dir).ConversationID
			srv.RefuseNextOpen(tc.status, tc.reason, sessionstream.ActionStop)

			st, err := livesession.Open(t.Context(), cfg, dir, p)

			if !tc.wantDisabled {
				var api *sessionstream.APIError
				if !errors.As(err, &api) || api.Body.Reason != tc.reason || mustState(t, dir).Status != livesession.StatusPending {
					t.Fatalf("open = %+v, %v; want the refusal returned and the stream left pending", st, err)
				}
				if st, err := livesession.Open(t.Context(), cfg, dir, p); err != nil || st.Status != livesession.StatusStreaming {
					t.Fatalf("retry = %+v, %v; want it to open once the server admits it", st, err)
				}
				return
			}
			if err != nil || st.Status != livesession.StatusDisabled || st.DisabledReason != sessionstream.ReasonFeatureOff {
				t.Fatalf("open = %+v, %v; want a disabled stream", st, err)
			}
			if on := mustState(t, dir); on.Status != livesession.StatusDisabled {
				t.Fatalf("on-disk status = %s, want disabled", on.Status)
			}
			if _, err := livesession.Open(t.Context(), cfg, dir, p); err != nil {
				t.Fatalf("open of a disabled stream: %v", err)
			}
			if n := srv.CallsTo(http.MethodPut, cnv); n != 1 {
				t.Fatalf("%d opens sent, want 1: a disabled stream is never opened again", n)
			}
		})
	}
}

// Failure prevented: a stop whose open was refused feature_off left the stream
// pending with its close requested, so the daemon re-ran the summary model and
// the open on every tick. The stop settles it at once.
func TestCloseRefusedFeatureOffAtOpenSettlesTheStream(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(0, 2)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	cnv := mustState(t, dir).ConversationID
	srv.RefuseNextOpen(http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

	res, err := livesession.Close(t.Context(), cfg, dir, "a summary", testBudget)
	if err != nil || !res.Done || res.Status != livesession.StatusDisabled {
		t.Fatalf("close = %+v, %v; want it done with the stream disabled", res, err)
	}
	if res, err := livesession.Close(t.Context(), cfg, dir, "a summary", testBudget); err != nil || !res.Done {
		t.Fatalf("second close = %+v, %v; want done", res, err)
	}
	if n := srv.CallsTo(http.MethodPut, cnv); n != 1 {
		t.Fatalf("%d opens sent, want 1", n)
	}
	if _, ok := srv.ClosedWith(cnv); ok {
		t.Fatal("a stream the server never admitted was closed")
	}
}

func mustState(t *testing.T, dir string) livesession.State {
	t.Helper()
	st, err := livesession.ReadState(dir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	return st
}
