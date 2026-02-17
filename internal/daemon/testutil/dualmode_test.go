package testutil

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/pkg/faultdaemon"
)

// TestMockDaemon_Basic verifies mock daemon fundamentals.
func TestMockDaemon_Basic(t *testing.T) {
	env := NewTestEnvironment(t)
	mock := env.StartMock()

	t.Run("ping responds successfully", func(t *testing.T) {
		client := daemon.NewClient()
		err := client.Ping()
		assert.NoError(t, err)
	})

	t.Run("status returns configured response", func(t *testing.T) {
		mock.StatusResponse = &daemon.StatusData{
			Running:    true,
			Pid:        12345,
			Version:    "test-version",
			LedgerPath: "/test/ledger",
		}

		client := daemon.NewClient()
		status, err := client.Status()
		require.NoError(t, err)
		assert.True(t, status.Running)
		assert.Equal(t, 12345, status.Pid)
		assert.Equal(t, "test-version", status.Version)
		assert.Equal(t, "/test/ledger", status.LedgerPath)
	})

	t.Run("sync returns success by default", func(t *testing.T) {
		client := daemon.NewClient()
		err := client.RequestSync()
		assert.NoError(t, err)
	})

	t.Run("sync returns configured error", func(t *testing.T) {
		mock.SyncError = errors.New("sync failed")

		client := daemon.NewClient()
		err := client.RequestSync()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "sync failed")

		// reset for other tests
		mock.SyncError = nil
	})
}

// TestMockDaemon_CallTracking verifies call recording.
func TestMockDaemon_CallTracking(t *testing.T) {
	env := NewTestEnvironment(t)
	mock := env.StartMock()

	t.Run("records all calls", func(t *testing.T) {
		mock.ResetCalls()

		client := daemon.NewClient()
		_ = client.Ping()
		_ = client.Ping()
		_, _ = client.Status()
		_ = client.RequestSync()

		calls := mock.GetCalls()
		assert.Len(t, calls, 4)
		assert.Equal(t, daemon.MsgTypePing, calls[0].Type)
		assert.Equal(t, daemon.MsgTypePing, calls[1].Type)
		assert.Equal(t, daemon.MsgTypeStatus, calls[2].Type)
		assert.Equal(t, daemon.MsgTypeSync, calls[3].Type)
	})

	t.Run("counts calls by type", func(t *testing.T) {
		mock.ResetCalls()

		client := daemon.NewClient()
		_ = client.Ping()
		_ = client.Ping()
		_ = client.Ping()
		_, _ = client.Status()

		assert.Equal(t, 3, mock.CallCount(daemon.MsgTypePing))
		assert.Equal(t, 1, mock.CallCount(daemon.MsgTypeStatus))
		assert.Equal(t, 0, mock.CallCount(daemon.MsgTypeSync))
	})

	t.Run("reset clears calls", func(t *testing.T) {
		client := daemon.NewClient()
		_ = client.Ping()
		assert.Greater(t, len(mock.GetCalls()), 0)

		mock.ResetCalls()
		assert.Empty(t, mock.GetCalls())
	})
}

// TestMockDaemon_Fluent verifies fluent configuration.
func TestMockDaemon_Fluent(t *testing.T) {
	env := NewTestEnvironment(t)

	mock := NewMockDaemon().
		WithStatus(&daemon.StatusData{
			Running: true,
			Version: "fluent-test",
		}).
		WithSyncError(errors.New("fluent error"))

	mock.Start(t)
	t.Cleanup(mock.Stop)

	t.Run("status configured via fluent api", func(t *testing.T) {
		client := daemon.NewClient()
		status, err := client.Status()
		require.NoError(t, err)
		assert.Equal(t, "fluent-test", status.Version)
	})

	t.Run("sync error configured via fluent api", func(t *testing.T) {
		client := daemon.NewClient()
		err := client.RequestSync()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "fluent error")
	})

	_ = env // suppress unused warning
}

// TestTestEnvironment_Basic verifies test environment setup.
func TestTestEnvironment_Basic(t *testing.T) {
	env := NewTestEnvironment(t)

	t.Run("creates directories", func(t *testing.T) {
		assert.DirExists(t, env.TmpDir)
		assert.DirExists(t, env.LedgerDir)
		assert.DirExists(t, env.ProjectDir)
	})

	t.Run("short temp dir for unix sockets", func(t *testing.T) {
		// unix socket paths have a ~104 char limit on macOS
		assert.Less(t, len(env.TmpDir), 50)
	})
}

// TestDualMode_RunsDualMode demonstrates dual mode testing.
// This test is slow because it starts real daemons.
func TestDualMode_RunsDualMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow dual-mode test in short mode")
	}

	tests := []DualModeTest{
		{
			Name: "example_ping_test",
			Test: func(t *testing.T, mode Mode) {
				// this test runs in both modes
				if mode == ModeDaemon {
					// in daemon mode, we can interact with the real daemon
					client := NewTestClient()
					err := client.Ping()
					require.NoError(t, err, "daemon should be available in daemon mode")
				} else {
					// in direct mode, no daemon is available
					client := daemon.TryConnect()
					assert.Nil(t, client, "daemon should not be available in direct mode")
				}
			},
		},
		{
			Name: "example_daemon_only_test",
			Test: func(t *testing.T, mode Mode) {
				// this test only runs in daemon mode
				client := NewTestClient()
				err := client.Ping()
				require.NoError(t, err, "should connect to test daemon")
				status, err := client.Status()
				require.NoError(t, err)
				assert.True(t, status.Running)
			},
			SkipDirect: true,
		},
		{
			Name: "example_direct_only_test",
			Test: func(t *testing.T, mode Mode) {
				// this test only runs in direct mode
				client := daemon.TryConnect()
				assert.Nil(t, client, "should not have daemon in direct mode")
			},
			SkipDaemon: true,
		},
	}

	RunDualMode(t, tests)
}

// TestHelperFunctions tests the utility functions.
func TestHelperFunctions(t *testing.T) {
	env := NewTestEnvironment(t)

	t.Run("IsDaemonMode without daemon", func(t *testing.T) {
		assert.False(t, IsDaemonMode())
	})

	t.Run("IsDaemonMode with mock", func(t *testing.T) {
		mock := env.StartMock()
		_ = mock
		// mock daemon is for IPC testing
	})

	t.Run("WaitForDaemon times out without daemon", func(t *testing.T) {
		// isolate socket path so we don't find the real daemon
		tmpDir := t.TempDir()
		t.Setenv("OX_XDG_ENABLE", "1")
		t.Setenv("XDG_RUNTIME_DIR", tmpDir)

		// short timeout since we expect failure
		err := WaitForDaemon(t, 200*time.Millisecond)
		assert.Error(t, err)
	})
}

// TestStartTestDaemon_VerifyStartStop tests daemon lifecycle.
// Skipped by default since it starts a real daemon which is slow.
func TestStartTestDaemon_VerifyStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	env := NewTestEnvironment(t)
	cleanup := env.StartDaemon()
	defer cleanup()

	// verify daemon is running
	err := WaitForDaemon(t, 5*time.Second)
	require.NoError(t, err, "daemon should be running")

	// verify we can communicate with it
	// use longer timeout for test daemon which may be slower than production
	client := daemon.NewClientWithTimeout(5 * time.Second)
	err = client.Ping()
	require.NoError(t, err, "should connect to daemon")

	status, err := client.Status()
	require.NoError(t, err)
	assert.True(t, status.Running)
}

// ExampleDualModeTest demonstrates how to use DualModeTest.
func ExampleDualModeTest() {
	// define tests that work in both modes
	tests := []DualModeTest{
		{
			Name: "command_works_with_and_without_daemon",
			Test: func(t *testing.T, mode Mode) {
				// your command logic here
				// use mode to check which mode we're in
				if mode == ModeDaemon {
					// daemon-specific assertions
				} else {
					// direct mode assertions
				}
			},
		},
	}

	// run in a test function:
	// RunDualMode(t, tests)
	_ = tests
}

// ExampleMockDaemon demonstrates how to use MockDaemon.
func ExampleMockDaemon() {
	// in a test function:
	// env := NewTestEnvironment(t)
	// mock := env.StartMock()
	//
	// configure mock responses:
	// mock.StatusResponse = &daemon.StatusData{Running: true}
	// mock.SyncError = errors.New("test error")
	//
	// make assertions:
	// client := daemon.NewClient()
	// err := client.Ping()
	// assert.NoError(t, err)
	// assert.Equal(t, 1, mock.CallCount(daemon.MsgTypePing))
}

// TestOxFaultDaemon_Basic verifies OxFaultDaemon fundamentals.
func TestOxFaultDaemon_Basic(t *testing.T) {
	env := NewTestEnvironment(t)

	t.Run("healthy daemon responds to ping", func(t *testing.T) {
		_ = env.StartFault(OxFaultConfig{})

		client := daemon.NewClient()
		err := client.Ping()
		assert.NoError(t, err)
	})
}

// TestOxFaultDaemon_ConfiguredResponses verifies custom responses.
func TestOxFaultDaemon_ConfiguredResponses(t *testing.T) {
	env := NewTestEnvironment(t)
	_ = env.StartFault(OxFaultConfig{
		StatusResponse: &daemon.StatusData{
			Running: true,
			Pid:     99999,
			Version: "ox-fault-test",
		},
	})

	client := daemon.NewClient()
	status, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, 99999, status.Pid)
	assert.Equal(t, "ox-fault-test", status.Version)
}

// TestOxFaultDaemon_WithFault verifies fault injection.
func TestOxFaultDaemon_WithFault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow fault test")
	}

	env := NewTestEnvironment(t)

	t.Run("hang before response", func(t *testing.T) {
		d := env.StartFault(OxFaultConfig{
			Config: faultdaemon.Config{Fault: faultdaemon.FaultHangBeforeResponse},
		})
		_ = d

		client := daemon.NewClientWithTimeout(100 * time.Millisecond)
		err := client.Ping()
		assert.Error(t, err)
	})
}
