package agentwork

// Tests for the INTENT of the anti-entropy wedge fixes, as opposed to the
// mechanics of the individual functions.
//
// The mechanics were already well covered when the daemon wedged for months. What
// was missing was any test of what the mechanics are FOR:
//
//   - a failed finalize must be recorded by the manager as a failure (not just
//     returned as an error by the handler)
//   - a second PROCESS must not run the same ledger's detection scan
//   - a full queue must cost one log line per cycle, not one per rejected item
//
// Each test below asserts an observable outcome a user or operator would care
// about, and each fails against the pre-fix code.

import (
	"context"

	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// --- log capture ---

// countingHandler records slog messages so tests can assert on log volume and
// level, which is the actual deliverable for the queue-full change.
type countingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }

// countMessages returns how many captured records have the given message at or
// above the given level.
func (h *countingHandler) countMessages(msg string, minLevel slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Message == msg && r.Level >= minLevel {
			n++
		}
	}
	return n
}

// --- intent 1: a failed finalize is recorded as a failure ---

// TestManager_UnpushableSession_SurfacesAsFailure asserts the outcome an
// operator sees, end to end through the REAL session-finalize handler rather
// than a mock. A mock handler would only exercise manager plumbing that was
// always correct; the defect was the handler reporting nil, so the wedge has to
// be driven from a genuinely unpushable session for this assertion to mean
// anything.
//
// Before the fix this recorded 897 failures a day as successes, and no counter
// ever moved — which is why nothing surfaced the problem.
func TestManager_UnpushableSession_SurfacesAsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	sessionName := "2026-01-15T18-00-testuser-OxSTATUS"
	cacheDir := writeProductionShapedSession(t, clonePath, sessionName)

	handler := newGitBackedHandler()

	m, _ := newTestManager(NewMockRunner(true), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})
	m.RegisterHandler(handler)

	m.executeItem(context.Background(), &WorkItem{
		ID:       "intent-failure",
		Type:     sessionFinalizeType,
		DedupKey: sessionFinalizeType + ":" + sessionName,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	})

	status := m.Status()
	if status.Stats.Failures != 1 {
		t.Errorf("Stats.Failures = %d, want 1 — a failed finalize was not counted as a failure",
			status.Stats.Failures)
	}
	if status.Stats.Successes != 0 {
		t.Errorf("Stats.Successes = %d, want 0 — a failed finalize was counted as a success",
			status.Stats.Successes)
	}

	var sawFailed bool
	for _, rec := range status.Recent {
		if rec.Status == statusFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("no recent entry recorded with status=failed — `ox daemon status` would show this as healthy")
	}
}

// --- intent 2: a second process does not scan the same ledger ---

// leaseHelperEnv carries the ledger path to the helper subprocess. Its presence
// is what tells the re-executed test binary to act as the lease holder.
const leaseHelperEnv = "OX_TEST_HOLD_LEDGER_LEASE"

// TestHelperHoldsLedgerLease is not a test. It is the subprocess half of
// startLeaseHolder re-executes this test binary as a subprocess that acquires
// the ledger lease and then blocks. It returns once the subprocess is confirmed
// holding the lease; callers own killing it.
func startLeaseHolder(t *testing.T, ledger string) *exec.Cmd {
	t.Helper()

	helper := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLedgerLease", "-test.timeout=90s")
	helper.Env = append(os.Environ(), leaseHelperEnv+"="+ledger)
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	readyFile := filepath.Join(ledger, "helper-ready")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			return helper
		}
		if time.Now().After(deadline) {
			_ = helper.Process.Kill()
			_ = helper.Wait()
			t.Fatal("helper never acquired the lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOwnsLedgerAntiEntropy_DeniedWhileAnotherProcessHolds: it takes the lease,
// signals readiness, and blocks until killed.
func TestHelperHoldsLedgerLease(t *testing.T) {
	ledger := os.Getenv(leaseHelperEnv)
	if ledger == "" {
		t.Skip("subprocess helper; not run directly")
	}

	lease, err := acquireLedgerLease(ledger)
	if err != nil || lease == nil {
		t.Fatalf("helper could not acquire lease: lease=%v err=%v", lease, err)
	}
	defer func() { _ = lease.Release() }()

	if err := os.WriteFile(filepath.Join(ledger, "helper-ready"), []byte("1"), 0644); err != nil {
		t.Fatalf("helper ready signal: %v", err)
	}
	// hold well past the parent's assertions; the parent kills us
	time.Sleep(60 * time.Second)
}

// TestOwnsLedgerAntiEntropy_DeniedWhileAnotherProcessHolds is the test that
// actually covers ox-4qvn. flock is per-open-file-description, so two acquires
// inside ONE process can both succeed — an in-process test would pass while the
// real duplicate-daemon case stayed broken. Only a second OS process proves the
// exclusion, so this re-executes the test binary as a lease holder.
func TestOwnsLedgerAntiEntropy_DeniedWhileAnotherProcessHolds(t *testing.T) {
	ledger := t.TempDir()

	helper := startLeaseHolder(t, ledger)
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	})

	m, _ := newTestManager(NewMockRunner(true), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})
	m.ledgerPath = ledger

	if m.ownsLedgerAntiEntropy() {
		t.Error("a second process claimed anti-entropy while another held the lease — " +
			"both daemons would scan the ledger and each enforce its own invocation ceiling")
	}
}

// TestOwnsLedgerAntiEntropy_TakesOverAfterHolderExits asserts the recovery half:
// a lease abandoned by a dead process must be claimable, or a daemon restart
// would permanently lose anti-entropy for that ledger.
func TestOwnsLedgerAntiEntropy_TakesOverAfterHolderExits(t *testing.T) {
	ledger := t.TempDir()

	helper := startLeaseHolder(t, ledger)

	// kill -9: the kernel, not the process, must release the flock
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = helper.Wait()

	m, _ := newTestManager(NewMockRunner(true), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})
	m.ledgerPath = ledger
	t.Cleanup(m.releaseLedgerAntiEntropy)

	if !m.ownsLedgerAntiEntropy() {
		t.Error("lease not reclaimable after the holder died — anti-entropy would stop for this ledger forever")
	}
}

// --- intent 3: a full queue costs one log line per cycle ---

// TestDetectAndEnqueue_FullQueueLogsOncePerCycle asserts the operator-visible
// deliverable: a backlog larger than the queue must not write a WARN per item.
// The pre-fix behavior produced 43,996 WARN lines in a single day and was the
// largest single contributor to a 511MB log file.
func TestDetectAndEnqueue_FullQueueLogsOncePerCycle(t *testing.T) {
	const backlog = maxQueueDepth * 3

	items := make([]*WorkItem, 0, backlog)
	for i := 0; i < backlog; i++ {
		items = append(items, &WorkItem{
			Type:     sessionFinalizeType,
			DedupKey: sessionFinalizeType + ":" + strings.Repeat("s", i%7) + itoa(i),
		})
	}

	capture := &countingHandler{}
	m, _ := newTestManager(NewMockRunner(true), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})
	m.logger = slog.New(capture)
	m.queue = NewWorkQueue(slog.New(capture))
	m.ledgerPath = "" // fail open: no lease needed for this assertion
	m.RegisterHandler(&mockHandler{typ: sessionFinalizeType, detectItems: items})

	m.detectAndEnqueue(enabledConfigWith(1, 10))

	perItem := capture.countMessages("enqueue skipped: queue full", slog.LevelInfo)
	if perItem != 0 {
		t.Errorf("%d per-item queue-full lines logged at INFO or above, want 0 — "+
			"a large backlog floods the daemon log every 5 minutes", perItem)
	}

	summary := capture.countMessages("detect backlog exceeds queue capacity", slog.LevelInfo)
	if summary != 1 {
		t.Errorf("got %d backlog summary lines, want exactly 1 — the operator must still be told "+
			"the queue is saturated", summary)
	}
}

// TestWorkQueue_TakeRejectedResets guards the counter that makes the summary
// per-cycle rather than cumulative: a second cycle with no rejections must not
// re-report the first cycle's count.
func TestWorkQueue_TakeRejectedResets(t *testing.T) {
	q := NewWorkQueue(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	for i := 0; i < maxQueueDepth+5; i++ {
		q.Enqueue(&WorkItem{Type: sessionFinalizeType, DedupKey: sessionFinalizeType + ":" + itoa(i)})
	}

	if got := q.TakeRejected(); got != 5 {
		t.Errorf("TakeRejected = %d, want 5", got)
	}
	if got := q.TakeRejected(); got != 0 {
		t.Errorf("TakeRejected = %d on a second call, want 0 — the summary would repeat a stale count", got)
	}
}

// itoa avoids pulling strconv in for two call sites in this file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
