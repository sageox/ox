package ledger

import (
	"errors"
	"sync"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// readRefusal reports whether err is the server declining to serve a request
// now — a 429 or a 503 — and the wait its Retry-After asked for, zero when it
// named none. A refusal is about the server's load, not about the object.
func readRefusal(err error) (retryAfter time.Duration, refused bool) {
	var httpErr *lfs.HTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == 429 || httpErr.StatusCode == 503) {
		return httpErr.RetryAfter, true
	}
	return 0, false
}

// readLimiter bounds how many object download requests are at the server at
// once, and adapts the bound to what the server will serve. A read route that
// caps concurrent transfers refuses each one over its cap on arrival, with a
// 429 or 503, so each refusal lowers the bound by one, never below one, and
// each run of successes as long as the bound raises it by one, never above
// readHydrationConcurrency. The client settles at the server's cap without
// knowing its number, and follows the cap when it changes (ox #982).
//
// A refused download gives up its slot while it waits to retry, so the bound
// counts only requests the server can see. The bound is not raised while any
// refused download is still waiting: its retry is the probe of whether the
// server has room again, and probing ahead of it parks one download after
// another for a Retry-After each.
//
// So a retry whose Retry-After is over takes the next free slot ahead of any
// fresh download. Without that, on a busy scheduler a fresh download reaches
// each freed slot before the woken retry runs, the retry waits out the rest of
// the batch, and the bound stays at the server's old cap after the server has
// stopped refusing.
type readLimiter struct {
	mu      sync.Mutex
	freed   *sync.Cond // broadcast when a slot frees, the bound rises, or the last due retry takes a slot
	limit   int        // requests that may be at the server at once
	held    int        // requests at the server
	waiting int        // refused downloads that have not retried yet
	due     int        // of those, the ones past their Retry-After, waiting only for a slot
	streak  int        // successes since the bound last changed
}

func newReadLimiter() *readLimiter {
	l := &readLimiter{limit: readHydrationConcurrency}
	l.freed = sync.NewCond(&l.mu)
	return l
}

// acquire waits for a slot at the server. retry says the request repeats a
// refused one, which goes ahead of fresh downloads.
func (l *readLimiter) acquire(retry bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if retry {
		l.due++
		for l.held >= l.limit {
			l.freed.Wait()
		}
		if l.due--; l.due == 0 {
			l.freed.Broadcast()
		}
	} else {
		for l.held >= l.limit || l.due > 0 {
			l.freed.Wait()
		}
	}
	l.held++
}

func (l *readLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held--
	l.freed.Broadcast()
}

// observe adapts the bound to one download attempt; retried says the attempt
// repeated a refused one. A failure other than a refusal says nothing about
// the server's capacity and leaves the bound alone.
func (l *readLimiter) observe(err error, retried bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if retried {
		l.waiting--
	}
	if _, refused := readRefusal(err); refused {
		l.limit, l.streak, l.waiting = max(1, l.limit-1), 0, l.waiting+1
	} else if err == nil {
		if l.streak++; l.waiting == 0 && l.streak >= l.limit && l.limit < readHydrationConcurrency {
			l.limit, l.streak = l.limit+1, 0
			l.freed.Broadcast()
		}
	}
}

// forgo records that a refused download is not going to retry after all, so
// the bound is not held down waiting for it.
func (l *readLimiter) forgo() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.waiting--
}
