package observability

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBase is a RoundTripper that answers with a fixed status per bearer
// and counts how many requests actually reached it.
type countingBase struct {
	mu     sync.Mutex
	status map[string]int // bearer -> status
	hits   []string       // bearer per request that got through
}

func (b *countingBase) RoundTrip(req *http.Request) (*http.Response, error) {
	tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.hits = append(b.hits, tok)
	st := b.status[tok]
	b.mu.Unlock()
	return &http.Response{StatusCode: st, Status: http.StatusText(st), Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func (b *countingBase) hitCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.hits)
}

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://otlp.test/v1/traces", strings.NewReader("{}"))
	require.NoError(t, err)
	return req
}

// A bearer the server rejected must not be re-sent on every batch: a daemon or
// buzz agent holding a revoked SAGEOX_TOKEN produced ~15k 401s/hour in prod
// (sageox-9naj9). Failure prevented: one 401 per export for the life of the
// process.
func TestBearerRoundTripper_SuppressesAfterReject(t *testing.T) {
	base := &countingBase{status: map[string]int{"tok-bad": http.StatusUnauthorized, "tok-good": http.StatusOK}}
	current := "tok-bad"
	var tokMu sync.Mutex
	rt := &bearerRoundTripper{base: base, tokenFunc: func() string { tokMu.Lock(); defer tokMu.Unlock(); return current }}

	for i := 0; i < 5; i++ {
		resp, err := rt.RoundTrip(newReq(t))
		require.NoError(t, err)
		if i == 0 {
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the first rejection is surfaced as-is")
		} else {
			assert.Equal(t, http.StatusOK, resp.StatusCode, "later batches are dropped client-side with a synthetic 2xx")
		}
	}
	assert.Equal(t, 1, base.hitCount(), "only the first export with a rejected bearer may reach the server")

	// A rotated token resumes immediately — the memo is keyed on the value.
	tokMu.Lock()
	current = "tok-good"
	tokMu.Unlock()
	resp, err := rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, base.hitCount())
	assert.Equal(t, "tok-good", base.hits[1])

	// A 2xx clears the memo, so going back to the old value probes again.
	tokMu.Lock()
	current = "tok-bad"
	tokMu.Unlock()
	_, err = rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	assert.Equal(t, 3, base.hitCount())
}

// After the cooldown one probe is let through so a server-side fix (re-issued
// team token, auth outage over) is picked up without a restart.
func TestBearerRoundTripper_ReprobesAfterCooldown(t *testing.T) {
	base := &countingBase{status: map[string]int{"tok-bad": http.StatusUnauthorized}}
	rt := &bearerRoundTripper{base: base, tokenFunc: func() string { return "tok-bad" }}

	_, err := rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	_, err = rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	require.Equal(t, 1, base.hitCount())

	rt.rejectMu.Lock()
	rt.rejectedAt = time.Now().Add(-rejectedTokenCooldown - time.Second)
	rt.rejectMu.Unlock()

	_, err = rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	assert.Equal(t, 2, base.hitCount(), "cooldown elapsed: exactly one probe reaches the server")
	_, err = rt.RoundTrip(newReq(t))
	require.NoError(t, err)
	assert.Equal(t, 2, base.hitCount(), "the probe's 401 re-arms suppression")
}

// 403 is treated like 401 (scope-limited token), 5xx is not: a collector
// outage must keep retrying through the exporter's own backoff, not be
// silenced for ten minutes.
func TestBearerRoundTripper_OnlyAuthFailuresSuppress(t *testing.T) {
	base := &countingBase{status: map[string]int{"tok": http.StatusBadGateway}}
	rt := &bearerRoundTripper{base: base, tokenFunc: func() string { return "tok" }}
	for i := 0; i < 3; i++ {
		_, err := rt.RoundTrip(newReq(t))
		require.NoError(t, err)
	}
	assert.Equal(t, 3, base.hitCount(), "5xx must not arm the memo")

	base.status["tok"] = http.StatusForbidden
	_, _ = rt.RoundTrip(newReq(t))
	_, _ = rt.RoundTrip(newReq(t))
	assert.Equal(t, 4, base.hitCount(), "403 arms the memo like 401")
}
