package ledger

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

// These tests cover a read route shedding load (ox #982). A server that caps
// concurrent transfers refuses each one over its cap on arrival with a 429 or
// 503, usually naming a Retry-After. Nothing is wrong with the object, so a
// refusal must never be what leaves it a stub.

// attemptLog records when each object download arrived.
type attemptLog struct {
	mu    sync.Mutex
	times []time.Time
}

// record notes an arrival and reports how many came before it.
func (a *attemptLog) record() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.times = append(a.times, time.Now())
	return len(a.times) - 1
}

func (a *attemptLog) gaps() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	gaps := make([]time.Duration, 0, len(a.times))
	for i := 1; i < len(a.times); i++ {
		gaps = append(gaps, a.times[i].Sub(a.times[i-1]))
	}
	return gaps
}

// newRefusingReadFixture serves content, refusing the first refusals downloads
// with status and, when retryAfter is set, that Retry-After.
func newRefusingReadFixture(t *testing.T, content []byte, refusals int, status int, retryAfter string) (*readFixture, *attemptLog) {
	t.Helper()
	attempts := &attemptLog{}
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		if attempts.record() < refusals {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(content)
	})
	return f, attempts
}

// Failure prevented: a refusal is retried on the local 200ms backoff against a
// server that asked for a second, so the object loses the race for the
// server's bound again and is skipped although nothing is wrong with it.
func TestReadSyncLFSRefusedDownloadWaitsForRetryAfter(t *testing.T) {
	const path = "sessions/refused/session.md"
	content := []byte("content the server refused once for load\n")
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, attempts := newRefusingReadFixture(t, content, 1, status, "1")
			commitReadLFSPointer(t, f, path, content)

			result := ReadSync(context.Background(), f.opts)
			require.True(t, result.Ready, "%+v", result)
			gaps := attempts.gaps()
			require.Len(t, gaps, 1, "one refusal, then the object")
			require.GreaterOrEqual(t, gaps[0], time.Second, "the retry waits as long as Retry-After asked")
		})
	}
}

// Failure prevented: refusals count toward the attempts that bound a failing
// request, so an object refused three times for load alone — about 600ms of
// refusals — is abandoned and left a stub.
func TestReadSyncLFSRefusalsDoNotSpendTheObjectsAttempts(t *testing.T) {
	const path = "sessions/refused-often/session.md"
	content := []byte("content served after more refusals than attempts\n")
	const refusals = readRequestAttempts + 1
	f, attempts := newRefusingReadFixture(t, content, refusals, http.StatusServiceUnavailable, "")
	commitReadLFSPointer(t, f, path, content)

	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Nil(t, result.ErrorDetail)
	require.Len(t, attempts.gaps(), refusals, "every refusal was retried")
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, content, actual)
}

// Failure prevented: a hostile or broken Retry-After parks a request, and the
// batch waiting on it, for as long as the header says — or a malformed one is
// read as "retry now" and spends the refusals at wire speed.
func TestReadSyncLFSRetryAfterIsBounded(t *testing.T) {
	const path = "sessions/retry-after/session.md"
	content := []byte("content behind an unusual Retry-After\n")
	for _, tc := range []struct {
		name, retryAfter string
		budget           time.Duration // zero runs without a deadline
		atLeast          time.Duration
	}{
		// A day is clamped to the retry window, a thirtieth of what remains of
		// a 60s budget once the checkout is ready: up to two seconds. Waiting
		// longer than one backoff shows the value was clamped, not discarded.
		{"absurd delay is clamped to the retry window", "86400", time.Minute, 2 * readRequestBackoff},
		// Without a deadline the window is a minute. Finishing well inside it
		// shows neither value was read as a long wait.
		{"malformed delay falls back to the backoff", "soon", 0, readRequestBackoff},
		{"zero delay falls back to the backoff", "0", 0, readRequestBackoff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, attempts := newRefusingReadFixture(t, content, 1, http.StatusServiceUnavailable, tc.retryAfter)
			commitReadLFSPointer(t, f, path, content)
			ctx := context.Background()
			if tc.budget != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.budget)
				defer cancel()
			}

			result := ReadSync(ctx, f.opts)
			require.True(t, result.Ready, "%+v", result)
			gaps := attempts.gaps()
			require.Len(t, gaps, 1)
			require.GreaterOrEqual(t, gaps[0], tc.atLeast)
			require.Less(t, gaps[0], 10*time.Second)
		})
	}
}

// Failure prevented: a refusal that never clears is retried until the budget
// expires, so one object the server never has room for turns the whole sync
// into "interrupted" instead of being skipped and named like any other object
// that could not be served.
func TestReadSyncLFSPersistentRefusalEndsWhenItsWindowCloses(t *testing.T) {
	const path = "sessions/always-refused/session.md"
	content := []byte("content the server never has room for\n")
	f, attempts := newRefusingReadFixture(t, content, math.MaxInt, http.StatusServiceUnavailable, "1")
	// Publish a checkout first: a cold clone that fails hydration keeps its
	// stage unpublished, and the stub under test would not exist.
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	pointer := commitReadLFSPointer(t, f, path, content)

	// A 25s budget gives the request a window under a second, however long the
	// fetch and checkout before hydration take: shorter than the Retry-After,
	// so its one retry is made as the window closes.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result := ReadSync(ctx, f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "missing_hydration", result.ErrorClass, "the object is skipped; the operation was not interrupted")
	require.Equal(t, &ReadFailureDetail{Reason: "download_refused", Path: path, OID: lfs.ComputeOID(content),
		ServerCode: http.StatusServiceUnavailable}, result.ErrorDetail)
	gaps := attempts.gaps()
	require.Len(t, gaps, 1, "one refusal, and one retry as the window closed")
	require.Less(t, gaps[0], time.Second, "the Retry-After was clamped to the window")
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual), "a refused object keeps its stub")
}

// Failure prevented: hydration keeps 8 transfers in flight against a server
// that serves fewer, so a steady share of every batch is refused on arrival for
// as long as the sync runs; and once the server has room again, a client that
// narrowed never widens.
func TestReadSyncLFSConcurrencyFollowsTheServersBound(t *testing.T) {
	const bound = 2
	const objects = 80
	contents := make(map[string][]byte, objects)
	type arrival struct {
		at         time.Time
		concurrent int32
		capped     bool
	}
	var inFlight, served atomic.Int32
	var mu sync.Mutex
	var arrivals []arrival
	firstWaveReady := make(chan struct{})
	recoveredOverlap := make(chan struct{})
	pendingRetries := make(map[string]bool)
	recoveryArrivals, recoveryHeld := 0, 0
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		// The cap lifts once the client has been held at it for half the objects.
		capped := served.Load() < objects/2
		concurrent := inFlight.Add(1)
		defer inFlight.Add(-1)
		mu.Lock()
		arrivals = append(arrivals, arrival{time.Now(), concurrent, capped})
		arrivalNumber := len(arrivals)
		if arrivalNumber == readHydrationConcurrency {
			close(firstWaveReady)
		}
		oid := filepath.Base(r.URL.Path)
		if capped && concurrent > bound {
			pendingRetries[oid] = true
		} else {
			delete(pendingRetries, oid)
		}
		// Let all refused objects return and successes raise the bound before
		// holding a recovery wave. Otherwise the barrier could itself prevent
		// the successes that are required to widen the limit.
		if !capped && len(pendingRetries) == 0 {
			recoveryArrivals++
		}
		holdRecovery := recoveryArrivals > 2*readHydrationConcurrency
		if holdRecovery {
			recoveryHeld++
			if recoveryHeld == bound+2 {
				close(recoveredOverlap)
			}
		}
		mu.Unlock()
		// Require actual overlap instead of assuming a 100ms sleep makes
		// enough handlers run together on a busy race/coverage runner.
		var ready <-chan struct{}
		if arrivalNumber <= readHydrationConcurrency {
			ready = firstWaveReady
		} else if holdRecovery {
			ready = recoveredOverlap
		}
		if ready != nil {
			select {
			case <-ready:
			case <-r.Context().Done():
				return
			}
		}
		// A refusal is answered at once, as the read route answers it; a
		// transfer takes long enough that concurrent requests overlap observably.
		if capped && concurrent > bound {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(contents[filepath.Base(r.URL.Path)])
		served.Add(1)
	})
	for i := range objects {
		content := []byte(fmt.Sprintf("object %03d behind a bounded server\n", i))
		contents[lfs.ComputeOID(content)] = content
		commitReadLFSPointer(t, f, fmt.Sprintf("sessions/bounded/object-%03d.md", i), content)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result := ReadSync(ctx, f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Equal(t, ReadHydration{State: "complete", Required: objects, Completed: objects}, result.Hydration)

	peak := func(arrivals []arrival) (peak int32) {
		for _, a := range arrivals {
			peak = max(peak, a.concurrent)
		}
		return peak
	}
	var whileCapped, afterwards []arrival
	for _, a := range arrivals {
		if a.capped {
			whileCapped = append(whileCapped, a)
		} else {
			afterwards = append(afterwards, a)
		}
	}
	// The first wave goes out at full concurrency and costs one refusal per
	// request over the bound. Every refusal after it must be a probe: the
	// bound stepping up once the last refused download has retried, which is
	// at least a Retry-After after the previous refusal.
	firstWave := whileCapped[:min(readHydrationConcurrency, len(whileCapped))]
	var probes []time.Time
	for _, a := range whileCapped[len(firstWave):] {
		if a.concurrent > bound {
			probes = append(probes, a.at)
		}
	}
	t.Logf("capped: %d arrivals, first wave peak %d, %d probes refused after it; afterwards: %d arrivals, peak %d",
		len(whileCapped), peak(firstWave), len(probes), len(afterwards), peak(afterwards))
	require.Greater(t, peak(firstWave), int32(bound), "the first requests go out above the bound; only refusals narrow it")
	require.LessOrEqual(t, peak(whileCapped[len(firstWave):]), int32(bound+1), "sustained refusals must bring transfers down to what the server serves")
	for i := 1; i < len(probes); i++ {
		require.GreaterOrEqual(t, probes[i].Sub(probes[i-1]), time.Second, "a refusal after the first wave must wait for the Retry-After of the one before it")
	}
	require.GreaterOrEqual(t, peak(afterwards), int32(bound+2), "transfers must widen again once the server stops refusing")
}
