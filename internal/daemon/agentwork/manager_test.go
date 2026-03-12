package agentwork

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

func enabledConfigWith(maxConcurrent, maxPerHour int) *config.AgentWorkerConfig {
	t := true
	return &config.AgentWorkerConfig{
		Enabled:               &t,
		AgentType:             "claude",
		MaxConcurrent:         &maxConcurrent,
		MaxInvocationsPerHour: &maxPerHour,
	}
}

func disabledConfig() *config.AgentWorkerConfig {
	f := false
	return &config.AgentWorkerConfig{Enabled: &f}
}

// mockHandler implements WorkHandler for testing.
type mockHandler struct {
	typ           string
	detectItems   []*WorkItem
	detectErr     error
	buildPromptFn func(item *WorkItem) (RunRequest, error)
	processErr    error
	processCalls  atomic.Int32
}

func (h *mockHandler) Type() string { return h.typ }

func (h *mockHandler) Detect(_ string) ([]*WorkItem, error) {
	return h.detectItems, h.detectErr
}

func (h *mockHandler) BuildPrompt(item *WorkItem) (RunRequest, error) {
	if h.buildPromptFn != nil {
		return h.buildPromptFn(item)
	}
	return RunRequest{Prompt: "test prompt", WorkDir: "/tmp"}, nil
}

func (h *mockHandler) ProcessResult(_ *WorkItem, _ *RunResult) error {
	h.processCalls.Add(1)
	return h.processErr
}

// newTestManager builds a Manager with defaults suitable for unit tests.
func newTestManager(runner Runner, cfgFn func() *config.AgentWorkerConfig) (*Manager, chan struct{}) {
	sig := make(chan struct{}, 10)
	if cfgFn == nil {
		cfgFn = func() *config.AgentWorkerConfig {
			return enabledConfigWith(1, 100)
		}
	}
	m := NewManager(runner, nil, cfgFn, sig, "/tmp/test-ledger")
	return m, sig
}

// --- tests ---

func TestManager_StartStop(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		m.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestManager_RegisterHandler(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, nil)

	h := &mockHandler{typ: "test-type"}
	m.RegisterHandler(h)

	m.mu.Lock()
	_, ok := m.handlers["test-type"]
	m.mu.Unlock()
	assert.True(t, ok, "handler should be registered")
}

func TestManager_DetectAndEnqueue(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, nil)

	h := &mockHandler{
		typ: "review",
		detectItems: []*WorkItem{
			{Type: "review", Priority: 1, DedupKey: "review-abc"},
			{Type: "review", Priority: 2, DedupKey: "review-def"},
		},
	}
	m.RegisterHandler(h)

	m.detectAndEnqueue()

	assert.Equal(t, 2, m.queue.Len())
}

func TestManager_DetectError(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, nil)

	h := &mockHandler{
		typ:       "broken",
		detectErr: errors.New("scan failed"),
	}
	m.RegisterHandler(h)

	m.detectAndEnqueue()
	assert.Equal(t, 0, m.queue.Len())
}

func TestManager_ProcessQueue_Success(t *testing.T) {
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		return &RunResult{Output: "done", ExitCode: 0, Duration: 10 * time.Millisecond}, nil
	}

	var completed atomic.Int32
	m, _ := newTestManager(runner, nil)
	m.SetOnComplete(func(result WorkResult) {
		assert.True(t, result.Success)
		completed.Add(1)
	})

	h := &mockHandler{typ: "finalize"}
	m.RegisterHandler(h)
	m.Enqueue(&WorkItem{Type: "finalize", Priority: 1, DedupKey: "fin-1"})

	m.processQueue(context.Background())

	require.Eventually(t, func() bool {
		return completed.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	status := m.Status()
	assert.Equal(t, 1, status.Stats.TotalInvocations)
	assert.Equal(t, 1, status.Stats.Successes)
	assert.Equal(t, 0, status.Stats.Failures)
	assert.Len(t, status.Recent, 1)
	assert.Equal(t, "completed", status.Recent[0].Status)
}

func TestManager_ProcessQueue_DisabledConfig(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, func() *config.AgentWorkerConfig {
		return disabledConfig()
	})

	h := &mockHandler{typ: "noop"}
	m.RegisterHandler(h)
	m.Enqueue(&WorkItem{Type: "noop", Priority: 1, DedupKey: "n1"})

	m.processQueue(context.Background())

	assert.Equal(t, 1, m.queue.Len())
}

func TestManager_ProcessQueue_UnavailableRunner(t *testing.T) {
	runner := NewMockRunner(false)
	m, _ := newTestManager(runner, nil)

	h := &mockHandler{typ: "noop"}
	m.RegisterHandler(h)
	m.Enqueue(&WorkItem{Type: "noop", Priority: 1, DedupKey: "n1"})

	m.processQueue(context.Background())

	assert.Equal(t, 1, m.queue.Len())
}

func TestManager_RetryOnFailure(t *testing.T) {
	var callCount atomic.Int32
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		n := callCount.Add(1)
		if n < maxRetries {
			return nil, errors.New("transient failure")
		}
		return &RunResult{Output: "ok"}, nil
	}

	var completed atomic.Int32
	m, _ := newTestManager(runner, nil)
	m.SetOnComplete(func(_ WorkResult) {
		completed.Add(1)
	})

	h := &mockHandler{typ: "retry-test"}
	m.RegisterHandler(h)
	m.Enqueue(&WorkItem{Type: "retry-test", Priority: 1, DedupKey: "rt-1"})

	ctx := context.Background()
	for i := 0; i < maxRetries+1; i++ {
		m.processQueue(ctx)
		time.Sleep(50 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return completed.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	assert.Equal(t, int32(maxRetries), callCount.Load())
}

func TestManager_MaxRetriesExceeded(t *testing.T) {
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		return nil, errors.New("permanent failure")
	}

	var failResult atomic.Value
	m, _ := newTestManager(runner, nil)
	m.SetOnComplete(func(result WorkResult) {
		failResult.Store(result)
	})

	h := &mockHandler{typ: "fail-test"}
	m.RegisterHandler(h)
	m.Enqueue(&WorkItem{Type: "fail-test", Priority: 1, DedupKey: "ft-1"})

	ctx := context.Background()
	for i := 0; i < maxRetries+2; i++ {
		m.processQueue(ctx)
		time.Sleep(50 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return failResult.Load() != nil
	}, 2*time.Second, 10*time.Millisecond)

	result := failResult.Load().(WorkResult)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "permanent failure")

	status := m.Status()
	assert.Equal(t, 1, status.Stats.Failures)
}

func TestManager_RateLimiting(t *testing.T) {
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		return &RunResult{Output: "ok"}, nil
	}

	m, _ := newTestManager(runner, func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 2)
	})
	m.rateLimiter = NewRateLimiter(2, time.Hour)

	h := &mockHandler{typ: "rate-test"}
	m.RegisterHandler(h)

	for i := 0; i < 5; i++ {
		m.Enqueue(&WorkItem{Type: "rate-test", Priority: 1, DedupKey: fmt.Sprintf("rl-%d", i)})
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		m.processQueue(ctx)
		time.Sleep(30 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		s := m.Status()
		return s.Stats.TotalInvocations == 2
	}, 2*time.Second, 10*time.Millisecond)

	status := m.Status()
	assert.Equal(t, 2, status.Stats.TotalInvocations)
	assert.True(t, status.QueueDepth > 0, "remaining items should still be in queue")
}

func TestManager_ConcurrencyControl(t *testing.T) {
	var concurrent atomic.Int32
	var maxConcurrentSeen atomic.Int32

	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		n := concurrent.Add(1)
		for {
			cur := maxConcurrentSeen.Load()
			if n <= cur || maxConcurrentSeen.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		return &RunResult{Output: "ok"}, nil
	}

	m, _ := newTestManager(runner, func() *config.AgentWorkerConfig {
		return enabledConfigWith(2, 100)
	})
	m.sem = make(chan struct{}, 2)

	h := &mockHandler{typ: "conc-test"}
	m.RegisterHandler(h)

	for i := 0; i < 4; i++ {
		m.Enqueue(&WorkItem{Type: "conc-test", Priority: 1, DedupKey: fmt.Sprintf("c-%d", i)})
	}

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		m.processQueue(ctx)
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)

	assert.LessOrEqual(t, maxConcurrentSeen.Load(), int32(2), "should never exceed configured concurrency")
}

func TestManager_Status(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})

	status := m.Status()

	assert.True(t, status.Enabled)
	assert.Equal(t, "claude", status.AgentType)
	assert.True(t, status.AgentAvail)
	assert.Equal(t, 0, status.QueueDepth)
	assert.Empty(t, status.Active)
	assert.Empty(t, status.Recent)
	assert.Equal(t, 0, status.Stats.TotalInvocations)
}

func TestManager_Enqueue(t *testing.T) {
	runner := NewMockRunner(true)
	m, _ := newTestManager(runner, nil)

	ok := m.Enqueue(&WorkItem{Type: "ext", Priority: 1, DedupKey: "e1"})
	assert.True(t, ok)
	assert.Equal(t, 1, m.queue.Len())

	ok = m.Enqueue(&WorkItem{Type: "ext", Priority: 1, DedupKey: "e1"})
	assert.False(t, ok)
	assert.Equal(t, 1, m.queue.Len())
}

func TestManager_SyncSignalDrainsQueue(t *testing.T) {
	// sync signal should process items already in the queue
	// but NOT trigger new detection (detection runs on the doctor timer)
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		return &RunResult{Output: "ok"}, nil
	}

	var completions atomic.Int32
	m, sig := newTestManager(runner, nil)
	m.SetOnComplete(func(_ WorkResult) {
		completions.Add(1)
	})

	h := &mockHandler{
		typ: "sync-test",
		detectItems: []*WorkItem{
			{Type: "sync-test", Priority: 1, DedupKey: "s1"},
		},
	}
	m.RegisterHandler(h)

	// pre-enqueue an item (simulating prior doctor detection)
	m.Enqueue(&WorkItem{Type: "sync-test", Priority: 1, DedupKey: "pre-queued"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// sync signal should drain the pre-queued item
	sig <- struct{}{}

	require.Eventually(t, func() bool {
		return completions.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	wg.Wait()

	status := m.Status()
	assert.Equal(t, 1, status.Stats.Successes)
}

func TestManager_RecentCappedAtMax(t *testing.T) {
	runner := NewMockRunner(true)
	runner.RunFunc = func(_ context.Context, _ RunRequest) (*RunResult, error) {
		return &RunResult{Output: "ok"}, nil
	}

	m, _ := newTestManager(runner, nil)
	h := &mockHandler{typ: "cap-test"}
	m.RegisterHandler(h)

	ctx := context.Background()
	for i := 0; i < maxRecentEntries+5; i++ {
		key := fmt.Sprintf("cap-%d", i)
		m.Enqueue(&WorkItem{Type: "cap-test", Priority: 1, DedupKey: key})
		m.processQueue(ctx)
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	status := m.Status()
	assert.LessOrEqual(t, len(status.Recent), maxRecentEntries)
}
