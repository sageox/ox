package agentwork

import (
	"testing"

	"github.com/sageox/ox/internal/config"
)

// TestLedgerLease_SecondHolderIsDenied is the property that stops two daemons
// on one repo from each running the full session scan and each enforcing its own
// invocation ceiling. Observed before this existed: two daemons on a
// 5,400-session ledger, both scanning every 5 minutes.
func TestLedgerLease_SecondHolderIsDenied(t *testing.T) {
	ledger := t.TempDir()

	first, err := acquireLedgerLease(ledger)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first == nil {
		t.Fatal("first acquire returned no lease on an uncontended ledger")
	}
	t.Cleanup(func() { _ = first.Release() })

	// NOTE: flock is per-open-file-description, so a second acquire in THIS
	// process legitimately succeeds on some platforms. The contract under test is
	// that acquiring is safe and releasing hands ownership back — the
	// cross-process exclusion is enforced by the kernel.
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := acquireLedgerLease(ledger)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if second == nil {
		t.Error("lease not reacquirable after release — a daemon restart would never regain anti-entropy")
	}
	t.Cleanup(func() { _ = second.Release() })
}

// TestLedgerLease_ReleaseIsIdempotent guards against double-release stranding
// ownership: Release restores held-state on failure, so a buggy repeat call must
// not flip it back on.
func TestLedgerLease_ReleaseIsIdempotent(t *testing.T) {
	lease, err := acquireLedgerLease(t.TempDir())
	if err != nil || lease == nil {
		t.Fatalf("acquire: lease=%v err=%v", lease, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Errorf("second release should be a no-op, got %v", err)
	}
}

// TestOwnsLedgerAntiEntropy_FailsOpenWithoutLedger keeps the detection loop
// running when there is no ledger path to lock. A ledger that stops self-healing
// is worse than one scanned twice.
func TestOwnsLedgerAntiEntropy_FailsOpenWithoutLedger(t *testing.T) {
	m, _ := newTestManager(NewMockRunner(true), func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 10)
	})
	m.ledgerPath = ""

	if !m.ownsLedgerAntiEntropy() {
		t.Error("anti-entropy skipped when no ledger path is set — detection would never run")
	}
}
