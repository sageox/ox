package testutil

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupFaultTest creates an isolated test environment.
func setupFaultTest(t *testing.T) {
	t.Helper()
	env := NewTestEnvironment(t)
	_ = env // sets up XDG vars with unique paths
}

// =============================================================================
// FAST TESTS - Run in normal test suite (even with -short)
// These tests complete in <100ms each, no timeouts involved
// =============================================================================

func TestFaultDaemon_Fast_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.NoError(t, err)
}

func TestFaultDaemon_Fast_CloseImmediately(t *testing.T) {
	setupFaultTest(t)

	d := NewCrashingDaemon(t)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_CloseAfterRead(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultCloseAfterRead})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_CorruptResponse(t *testing.T) {
	setupFaultTest(t)

	d := NewCorruptDaemon(t)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_PanicInHandler(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultPanicInHandler})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_MultipleResponses(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultMultipleResponses})
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
	err := client.Ping()
	assert.NoError(t, err, "first response should be valid")

	err = client.Ping()
	assert.NoError(t, err, "new connection should work")
}

func TestFaultDaemon_Fast_InvalidJSON(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultInvalidJSON})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_EmbeddedNewlines(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultEmbeddedNewlines})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.NoError(t, err, "JSON with escaped newlines should work")
}

func TestFaultDaemon_Fast_RefuseAfterAccept(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultRefuseAfterAccept})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Fast_FaultOnSpecificMessageType(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{
		Fault:              FaultHangBeforeResponse,
		FaultOnMessageType: daemon.MsgTypeSync,
	})
	d.Start()
	defer d.Stop()

	// Ping should work (fault only applies to sync)
	err := daemon.IsHealthy()
	assert.NoError(t, err)
}

func TestFaultDaemon_Fast_ConnectionCount(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	// baseline accounts for the probe connection from AwaitUnixSocket in Start()
	time.Sleep(10 * time.Millisecond)
	baseline := d.ConnectionCount()
	_ = daemon.IsHealthy()
	assert.Equal(t, baseline+1, d.ConnectionCount())
	_ = daemon.IsHealthy()
	assert.Equal(t, baseline+2, d.ConnectionCount())
}

func TestFaultDaemon_Fast_ResponseTooLarge(t *testing.T) {
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultResponseTooLarge})
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second)
	err := client.Ping()
	assert.Error(t, err, "should reject response larger than 1MB limit")
}

// =============================================================================
// SLOW TESTS - Tests that involve timeouts (100ms+)
// Skipped in short mode to keep developer iteration fast
// Run for releases with: go test -count=1 ./internal/daemon/testutil
// =============================================================================

func TestFaultDaemon_Slow_HangOnAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultHangOnAccept})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_HangBeforeResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewHungDaemon(t)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_SlowResponse_UnderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewSlowDaemon(t, 50*time.Millisecond)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.NoError(t, err, "should succeed when response is under timeout")
}

func TestFaultDaemon_Slow_SlowResponse_OverTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewSlowDaemon(t, 200*time.Millisecond)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_PartialResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultPartialResponse})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_Deadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultDeadlock})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_FlakyConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	// DropEveryN=3: every 3rd connection is dropped.
	// AwaitUnixSocket probe in Start() consumes conn #1, so the first
	// daemon.IsHealthy() call is conn #2, and conn #3 is the drop.
	d := NewFlakyDaemon(t, 3)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.NoError(t, err) // conn #2

	err = daemon.IsHealthy()
	assert.Error(t, err) // conn #3 dropped

	err = daemon.IsHealthy()
	assert.NoError(t, err) // conn #4
}

func TestFaultDaemon_Slow_SwitchFaultMidTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	require.NoError(t, err)

	d.SetFault(FaultHangBeforeResponse)
	err = daemon.IsHealthy()
	assert.Error(t, err)

	d.SetFault(FaultNone)
	err = daemon.IsHealthy()
	assert.NoError(t, err)
}

func TestFaultDaemon_Slow_ResponseWithoutNewline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultResponseWithoutNewline})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_ChunkedResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultChunkedResponse})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.NoError(t, err, "chunked response should be handled by bufio.Reader")
}

func TestFaultDaemon_Slow_SlowAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{
		Fault:             FaultSlowAccept,
		SlowResponseDelay: 500 * time.Millisecond,
	})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err, "100ms timeout should fail when accept is delayed 500ms")

	client := daemon.NewClientForCurrentRepoWithTimeout(2 * time.Second)
	err = client.Ping()
	assert.NoError(t, err, "2s timeout should succeed")
}

func TestFaultDaemon_Slow_WriteHalfThenHang(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultWriteHalfThenHang})
	d.Start()
	defer d.Stop()

	err := daemon.IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not responsive")
}

func TestFaultDaemon_Slow_VerySlowResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping very slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultVerySlowResponse})
	d.Start()
	defer d.Stop()

	start := time.Now()
	err := daemon.IsHealthy()
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second, "should timeout quickly, not wait 15s")
}

// =============================================================================
// TABLE-DRIVEN TESTS - Comprehensive fault coverage for release testing
// =============================================================================

func TestClientPing_AllFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault table test in short mode")
	}

	tests := []struct {
		name        string
		fault       Fault
		expectError bool
	}{
		// Fast faults (no timeout wait)
		{"healthy", FaultNone, false},
		{"close_immediately", FaultCloseImmediately, true},
		{"close_after_read", FaultCloseAfterRead, true},
		{"corrupt_response", FaultCorruptResponse, true},
		{"panic_in_handler", FaultPanicInHandler, true},
		{"multiple_responses", FaultMultipleResponses, false},
		{"invalid_json", FaultInvalidJSON, true},
		{"embedded_newlines", FaultEmbeddedNewlines, false},
		{"refuse_after_accept", FaultRefuseAfterAccept, true},

		// Slow faults (require timeout to trigger)
		{"hang_on_accept", FaultHangOnAccept, true},
		{"hang_before_response", FaultHangBeforeResponse, true},
		{"partial_response", FaultPartialResponse, true},
		{"response_without_newline", FaultResponseWithoutNewline, true},
		{"write_half_then_hang", FaultWriteHalfThenHang, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
			err := client.Ping()

			if tt.expectError {
				assert.Error(t, err, "expected error for fault: %s", tt.fault)
			} else {
				assert.NoError(t, err, "expected no error for fault: %s", tt.fault)
			}
		})
	}
}
