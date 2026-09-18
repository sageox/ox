package ledger

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

// Failure prevented: the bound on concurrent downloads ignores the server's
// refusals, staying at 8 against a server that serves fewer; or it never
// recovers once they stop, holding hydration at one transfer for the rest of
// the sync; or it probes upward while a refused download is still waiting to
// retry, parking one download after another for a Retry-After each.
func TestReadLimiterFollowsRefusals(t *testing.T) {
	refusal := &lfs.HTTPError{StatusCode: http.StatusServiceUnavailable}
	l := newReadLimiter()
	require.Equal(t, readHydrationConcurrency, l.limit)
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		l.observe(&lfs.HTTPError{StatusCode: status}, false)
	}
	require.Equal(t, readHydrationConcurrency-3, l.limit, "each refusal takes one slot away")
	for range 2 * readHydrationConcurrency {
		l.observe(refusal, false)
	}
	require.Equal(t, 1, l.limit, "never below one transfer")
	for _, err := range []error{&lfs.HTTPError{StatusCode: http.StatusNotFound}, &lfs.HTTPError{StatusCode: http.StatusBadGateway},
		errors.New("connection reset by peer"), lfs.ErrOIDMismatch} {
		l.observe(err, false)
	}
	require.Equal(t, 1, l.limit, "a failure that is not a refusal says nothing about capacity")

	for range 100 {
		l.observe(nil, false)
	}
	require.Equal(t, 1, l.limit, "no probe while a refused download has yet to retry")
	for l.waiting > 1 {
		l.observe(nil, true) // the refused downloads retry, one after another
	}
	l.forgo() // and the last one is abandoned instead
	require.Equal(t, 0, l.waiting)
	successes := 0
	for ; l.limit < readHydrationConcurrency && successes < 1000; successes++ {
		l.observe(nil, false)
	}
	// The pending successes step the bound to 2 at once; each step after that
	// takes a run of successes as long as the bound: 2 + 3 + … + 7.
	require.Equal(t, readHydrationConcurrency*(readHydrationConcurrency-1)/2, successes)
	l.observe(nil, false)
	require.Equal(t, readHydrationConcurrency, l.limit, "never above readHydrationConcurrency")

	l.observe(refusal, false)
	l.observe(refusal, true) // refused again on its retry: still waiting
	require.Equal(t, 1, l.waiting)
	l.observe(errors.New("connection reset by peer"), true)
	require.Equal(t, 0, l.waiting, "a retry that fails some other way is no longer waiting")
}

// Failure prevented: a lowered bound holds back no new download, so the
// transfers the server just refused are joined by more; or a raised one wakes
// nothing, and hydration waits on a slot that is already free.
func TestReadLimiterHoldsBackDownloadsOverTheBound(t *testing.T) {
	l := newReadLimiter()
	for range readHydrationConcurrency {
		l.acquire()
	}
	l.observe(&lfs.HTTPError{StatusCode: http.StatusServiceUnavailable}, false)
	l.release() // the refused download gives its slot up while it waits
	acquired := make(chan struct{})
	go func() {
		l.acquire()
		acquired <- struct{}{}
	}()
	select {
	case <-acquired:
		t.Fatal("a download started over the lowered bound")
	case <-time.After(50 * time.Millisecond):
	}
	l.release()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("a slot freed under the bound was never handed out")
	}

	go func() {
		l.acquire()
		acquired <- struct{}{}
	}()
	l.observe(nil, true) // the refused download's retry succeeds
	for range readHydrationConcurrency - 2 {
		l.observe(nil, false) // a run as long as the bound raises it to readHydrationConcurrency
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("a raised bound woke no waiting download")
	}
}
