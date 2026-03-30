package testutil

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// IPC METHOD FAULT TESTS
// Comprehensive fault injection testing for all IPC client methods.
// Ensures each method handles daemon failures gracefully.
// =============================================================================

// testTimeout is the standard timeout for fault tests
const testTimeout = 200 * time.Millisecond

// criticalFaults are faults that every IPC method must handle correctly
var criticalFaults = []struct {
	name  string
	fault Fault
}{
	{"hung_daemon", FaultHangBeforeResponse},
	{"crash_after_read", FaultCloseAfterRead},
	{"corrupt_response", FaultCorruptResponse},
	{"partial_response", FaultPartialResponse},
	{"connection_closed", FaultCloseImmediately},
}

// =============================================================================
// Status() Tests
// =============================================================================

func TestStatus_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	status, err := client.Status()

	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.True(t, status.Running)
}

func TestStatus_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			status, err := client.Status()

			assert.Error(t, err, "Status() should fail with fault: %s", tt.name)
			assert.Nil(t, status)
		})
	}
}

// =============================================================================
// Sessions() Tests (deprecated but still used)
// =============================================================================

func TestSessions_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	sessions, err := client.Sessions()

	assert.NoError(t, err)
	assert.NotNil(t, sessions) // may be empty slice
}

func TestSessions_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.Sessions()

			assert.Error(t, err, "Sessions() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// Instances() Tests
// =============================================================================

func TestInstances_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	instances, err := client.Instances()

	assert.NoError(t, err)
	assert.NotNil(t, instances) // may be empty slice
}

func TestInstances_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.Instances()

			assert.Error(t, err, "Instances() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// SyncHistory() Tests
// =============================================================================

func TestSyncHistory_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	history, err := client.SyncHistory()

	assert.NoError(t, err)
	assert.NotNil(t, history) // may be empty slice
}

func TestSyncHistory_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.SyncHistory()

			assert.Error(t, err, "SyncHistory() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// Doctor() Tests
// =============================================================================

func TestDoctor_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	resp, err := client.Doctor()

	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestDoctor_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.Doctor()

			assert.Error(t, err, "Doctor() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// RequestSync() Tests
// =============================================================================

func TestRequestSync_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	err := client.RequestSync()

	assert.NoError(t, err)
}

func TestRequestSync_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			err := client.RequestSync()

			assert.Error(t, err, "RequestSync() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// SyncWithProgress() Tests
// =============================================================================

func TestSyncWithProgress_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(1 * time.Second)
	err := client.SyncWithProgress(nil)

	assert.NoError(t, err)
}

func TestSyncWithProgress_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			err := client.SyncWithProgress(nil)

			assert.Error(t, err, "SyncWithProgress() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// TeamSyncWithProgress() Tests
// =============================================================================

func TestTeamSyncWithProgress_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(1 * time.Second)
	err := client.TeamSyncWithProgress(nil)

	assert.NoError(t, err)
}

func TestTeamSyncWithProgress_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			err := client.TeamSyncWithProgress(nil)

			assert.Error(t, err, "TeamSyncWithProgress() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// Stop() Tests
// =============================================================================

func TestStop_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	err := client.Stop()

	assert.NoError(t, err)
}

func TestStop_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			err := client.Stop()

			assert.Error(t, err, "Stop() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// GetUnviewedErrors() Tests
// =============================================================================

func TestGetUnviewedErrors_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	errors, err := client.GetUnviewedErrors()

	assert.NoError(t, err)
	assert.NotNil(t, errors) // may be empty slice
}

func TestGetUnviewedErrors_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.GetUnviewedErrors()

			assert.Error(t, err, "GetUnviewedErrors() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// MarkErrorsViewed() Tests
// =============================================================================

func TestMarkErrorsViewed_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	err := client.MarkErrorsViewed([]string{"error-1", "error-2"})

	assert.NoError(t, err)
}

func TestMarkErrorsViewed_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			err := client.MarkErrorsViewed([]string{"error-1"})

			assert.Error(t, err, "MarkErrorsViewed() should fail with fault: %s", tt.name)
		})
	}
}

// =============================================================================
// Checkout() Tests - CRITICAL PATH
// This is used for ox clone and must be extremely reliable
// =============================================================================

func TestCheckout_Healthy(t *testing.T) {
	setupFaultTest(t)

	d := NewHealthyFaultDaemon(t)
	d.Start()
	defer d.Stop()

	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second)
	result, err := client.Checkout(daemon.CheckoutPayload{
		CloneURL: "https://example.com/repo.git",
		RepoPath: "/tmp/test-checkout",
		RepoType: "ledger",
	}, nil)

	// fault daemon returns success but no real checkout
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestCheckout_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault test in short mode")
	}

	for _, tt := range criticalFaults {
		t.Run(tt.name, func(t *testing.T) {
			setupFaultTest(t)

			d := NewFaultDaemon(t, FaultConfig{Fault: tt.fault})
			d.Start()
			defer d.Stop()

			client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
			_, err := client.Checkout(daemon.CheckoutPayload{
				CloneURL: "https://example.com/repo.git",
				RepoPath: "/tmp/test-checkout",
				RepoType: "ledger",
			}, nil)

			assert.Error(t, err, "Checkout() should fail with fault: %s", tt.name)
		})
	}
}

// TestCheckout_ProgressCallbackWithFault ensures progress callbacks
// don't cause issues when the daemon fails mid-operation
func TestCheckout_ProgressCallbackWithFault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}
	setupFaultTest(t)

	d := NewFaultDaemon(t, FaultConfig{Fault: FaultHangBeforeResponse})
	d.Start()
	defer d.Stop()

	progressCalled := false
	client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
	_, err := client.Checkout(daemon.CheckoutPayload{
		CloneURL: "https://example.com/repo.git",
		RepoPath: "/tmp/test-checkout",
		RepoType: "ledger",
	}, func(stage string, percent *int, message string) {
		progressCalled = true
	})

	assert.Error(t, err)
	// progress callback may or may not be called before timeout - that's ok
	_ = progressCalled
}

// =============================================================================
// COMPREHENSIVE TABLE-DRIVEN TEST
// Tests ALL methods against ALL critical faults in one place
// =============================================================================

func TestAllIPCMethods_AllCriticalFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive IPC fault matrix in short mode")
	}

	methods := []struct {
		name string
		call func(client *daemon.Client) error
	}{
		{"Ping", func(c *daemon.Client) error { return c.Ping() }},
		{"Status", func(c *daemon.Client) error { _, err := c.Status(); return err }},
		{"Sessions", func(c *daemon.Client) error { _, err := c.Sessions(); return err }},
		{"Instances", func(c *daemon.Client) error { _, err := c.Instances(); return err }},
		{"SyncHistory", func(c *daemon.Client) error { _, err := c.SyncHistory(); return err }},
		{"Doctor", func(c *daemon.Client) error { _, err := c.Doctor(); return err }},
		{"RequestSync", func(c *daemon.Client) error { return c.RequestSync() }},
		{"SyncWithProgress", func(c *daemon.Client) error { return c.SyncWithProgress(nil) }},
		{"TeamSyncWithProgress", func(c *daemon.Client) error { return c.TeamSyncWithProgress(nil) }},
		{"Stop", func(c *daemon.Client) error { return c.Stop() }},
		{"GetUnviewedErrors", func(c *daemon.Client) error { _, err := c.GetUnviewedErrors(); return err }},
		{"MarkErrorsViewed", func(c *daemon.Client) error { return c.MarkErrorsViewed([]string{"test"}) }},
		{"Checkout", func(c *daemon.Client) error {
			_, err := c.Checkout(daemon.CheckoutPayload{
				CloneURL: "https://example.com/repo.git",
				RepoPath: "/tmp/test",
				RepoType: "ledger",
			}, nil)
			return err
		}},
	}

	for _, method := range methods {
		for _, fault := range criticalFaults {
			testName := method.name + "/" + fault.name
			t.Run(testName, func(t *testing.T) {
				setupFaultTest(t)

				d := NewFaultDaemon(t, FaultConfig{Fault: fault.fault})
				d.Start()
				defer d.Stop()

				client := daemon.NewClientForCurrentRepoWithTimeout(testTimeout)
				err := method.call(client)

				assert.Error(t, err, "%s should fail with fault %s", method.name, fault.name)
			})
		}
	}
}
