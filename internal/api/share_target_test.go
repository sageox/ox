package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestGetShareTarget_RejectsPathShapedTokens: the client refuses a token that
// could rewrite the request route before any request is made. Failure
// prevented: a caller that skipped validation turning a share token into
// /api/v1/shares/../<other route> with the user's credential attached.
func TestGetShareTarget_RejectsPathShapedTokens(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	client := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")

	for _, token := range []string{"", ".", "..", "a/b", `a\b`, "a?b", "a#b", "a%2Fb"} {
		if _, err := client.GetShareTarget(context.Background(), token); err == nil {
			t.Errorf("token %q accepted", token)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server received %d requests for rejected tokens", n)
	}
}

// TestGetShareTarget_NotFoundBodyDistinguishesDenialFromMissingRoute: only
// the documented {"error":"share not found"} body means the share is gone; a
// 404 from a server without the route must not read as a revoked share.
func TestGetShareTarget_NotFoundBodyDistinguishesDenialFromMissingRoute(t *testing.T) {
	tests := []struct {
		body string
		want error
	}{
		{`{"error":"share not found"}`, ErrShareNotFound},
		{`{"error":"Share Not Found"}`, ErrShareNotFound},
		{``, ErrShareLookupUnsupported},
		{`404 page not found`, ErrShareLookupUnsupported},
		{`{"error":"not found"}`, ErrShareLookupUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			_, err := NewRepoClientWithEndpoint(srv.URL).GetShareTarget(context.Background(), "rs-abc123")
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
