package testutil

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/sageox/ox/internal/daemon"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/stretchr/testify/require"
)

func TestMockServiceNew(t *testing.T) {
	m := NewMockService()
	if m == nil {
		t.Fatal("NewMockService returned nil")
	}
}

func TestMockServiceDefaultStatus(t *testing.T) {
	m := NewMockService()
	status := m.Status()
	if status == nil {
		t.Fatal("default Status() returned nil")
	}
	if !status.Running {
		t.Error("default status should be Running=true")
	}
	if status.Pid != os.Getpid() {
		t.Errorf("default status Pid=%d, want %d", status.Pid, os.Getpid())
	}
	if status.Version != "mock-service-test" {
		t.Errorf("default status Version=%q, want %q", status.Version, "mock-service-test")
	}
}

func TestMockServiceNilFuncsReturnDefaults(t *testing.T) {
	m := NewMockService()

	// sync operations return nil error
	if err := m.Sync(); err != nil {
		t.Errorf("Sync() = %v, want nil", err)
	}
	if err := m.SyncWithProgress(nil); err != nil {
		t.Errorf("SyncWithProgress() = %v, want nil", err)
	}
	if err := m.TeamSync(nil); err != nil {
		t.Errorf("TeamSync() = %v, want nil", err)
	}

	// query operations return nil/empty
	if got := m.SyncHistory(); got != nil {
		t.Errorf("SyncHistory() = %v, want nil", got)
	}
	if got := m.GetErrors(); got != nil {
		t.Errorf("GetErrors() = %v, want nil", got)
	}
	if got := m.Sessions(); got != nil {
		t.Errorf("Sessions() = %v, want nil", got)
	}
	if got := m.Instances(); got != nil {
		t.Errorf("Instances() = %v, want nil", got)
	}
	if got := m.CodeStatus(); got != nil {
		t.Errorf("CodeStatus() = %v, want nil", got)
	}

	entries, err := m.Whispers("agent-1", whisperstore.AttentionNormal, nil)
	if entries != nil || err != nil {
		t.Errorf("Whispers() = (%v, %v), want (nil, nil)", entries, err)
	}

	// mutation operations return nil
	result, err := m.Checkout(daemon.CheckoutPayload{}, nil)
	if result != nil || err != nil {
		t.Errorf("Checkout() = (%v, %v), want (nil, nil)", result, err)
	}
	if got := m.TriggerGC(); got != nil {
		t.Errorf("TriggerGC() = %v, want nil", got)
	}
	codeResult, err := m.CodeIndex(daemon.CodeIndexPayload{}, nil)
	if codeResult != nil || err != nil {
		t.Errorf("CodeIndex() = (%v, %v), want (nil, nil)", codeResult, err)
	}
	if got := m.Doctor(); got != nil {
		t.Errorf("Doctor() = %v, want nil", got)
	}

	// fire-and-forget operations should not panic
	m.Stop()
	m.MarkErrors([]string{"err-1"})
	m.SessionFinalize(daemon.SessionFinalizeIPCPayload{})
	m.Activity()
	m.Heartbeat("caller-1", json.RawMessage(`{}`))
	m.Telemetry(json.RawMessage(`{}`))
	m.Friction(daemon.FrictionPayload{})
}

func TestMockServiceOverrides(t *testing.T) {
	m := NewMockService()

	t.Run("sync error", func(t *testing.T) {
		want := errors.New("sync failed")
		m.SyncFunc = func() error { return want }
		if got := m.Sync(); !errors.Is(got, want) {
			t.Errorf("Sync() = %v, want %v", got, want)
		}
	})

	t.Run("custom status", func(t *testing.T) {
		m.StatusFunc = func() *daemon.StatusData {
			return &daemon.StatusData{Running: false, Version: "custom"}
		}
		status := m.Status()
		if status.Running {
			t.Error("expected Running=false from override")
		}
		if status.Version != "custom" {
			t.Errorf("Version=%q, want %q", status.Version, "custom")
		}
	})

	t.Run("custom errors", func(t *testing.T) {
		want := []daemon.StoredError{{ID: "err-1", Message: "boom"}}
		m.GetErrorsFunc = func() []daemon.StoredError { return want }
		got := m.GetErrors()
		if len(got) != 1 || got[0].ID != "err-1" {
			t.Errorf("GetErrors() = %v, want %v", got, want)
		}
	})

	t.Run("stop called", func(t *testing.T) {
		called := false
		m.StopFunc = func() { called = true }
		m.Stop()
		if !called {
			t.Error("StopFunc was not called")
		}
	})

	t.Run("checkout override", func(t *testing.T) {
		want := &daemon.CheckoutResult{Path: "/test", Cloned: true}
		m.CheckoutFunc = func(_ daemon.CheckoutPayload, _ *daemon.ProgressWriter) (*daemon.CheckoutResult, error) {
			return want, nil
		}
		got, err := m.Checkout(daemon.CheckoutPayload{}, nil)
		if err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		if got.Path != want.Path {
			t.Errorf("Checkout().Path = %q, want %q", got.Path, want.Path)
		}
	})

	t.Run("doctor override", func(t *testing.T) {
		want := &daemon.DoctorResponse{AntiEntropyTriggered: true}
		m.DoctorFunc = func() *daemon.DoctorResponse { return want }
		got := m.Doctor()
		if !got.AntiEntropyTriggered {
			t.Error("expected AntiEntropyTriggered=true")
		}
	})

	t.Run("heartbeat called", func(t *testing.T) {
		var gotID string
		m.HeartbeatFunc = func(callerID string, _ json.RawMessage) { gotID = callerID }
		m.Heartbeat("agent-42", nil)
		if gotID != "agent-42" {
			t.Errorf("Heartbeat callerID = %q, want %q", gotID, "agent-42")
		}
	})

	t.Run("friction called", func(t *testing.T) {
		var called bool
		m.FrictionFunc = func(_ daemon.FrictionPayload) { called = true }
		m.Friction(daemon.FrictionPayload{})
		if !called {
			t.Error("FrictionFunc was not called")
		}
	})

	t.Run("telemetry called", func(t *testing.T) {
		var gotPayload json.RawMessage
		m.TelemetryFunc = func(p json.RawMessage) { gotPayload = p }
		m.Telemetry(json.RawMessage(`{"event":"test"}`))
		if string(gotPayload) != `{"event":"test"}` {
			t.Errorf("Telemetry payload = %s, want %s", gotPayload, `{"event":"test"}`)
		}
	})

	t.Run("activity called", func(t *testing.T) {
		var called bool
		m.ActivityFunc = func() { called = true }
		m.Activity()
		if !called {
			t.Error("ActivityFunc was not called")
		}
	})

	t.Run("session finalize called", func(t *testing.T) {
		var called bool
		m.SessionFinalizeFunc = func(_ daemon.SessionFinalizeIPCPayload) { called = true }
		m.SessionFinalize(daemon.SessionFinalizeIPCPayload{})
		if !called {
			t.Error("SessionFinalizeFunc was not called")
		}
	})

	t.Run("mark errors called", func(t *testing.T) {
		var gotIDs []string
		m.MarkErrorsFunc = func(ids []string) { gotIDs = ids }
		m.MarkErrors([]string{"a", "b"})
		if len(gotIDs) != 2 || gotIDs[0] != "a" || gotIDs[1] != "b" {
			t.Errorf("MarkErrors ids = %v, want [a b]", gotIDs)
		}
	})

	t.Run("trigger gc override", func(t *testing.T) {
		want := &daemon.TriggerGCResponse{Triggered: 3, LedgerTriggered: true}
		m.TriggerGCFunc = func() *daemon.TriggerGCResponse { return want }
		got := m.TriggerGC()
		if got.Triggered != 3 {
			t.Errorf("expected Triggered=3, got %d", got.Triggered)
		}
	})

	t.Run("whispers override", func(t *testing.T) {
		want := []whisperstore.WhisperEntry{{Content: "hello"}}
		m.WhispersFunc = func(_ string, _ whisperstore.Attention, _ []string) ([]whisperstore.WhisperEntry, error) {
			return want, nil
		}
		got, err := m.Whispers("agent-1", whisperstore.AttentionNormal, nil)
		if err != nil {
			t.Fatalf("Whispers() error = %v", err)
		}
		if len(got) != 1 || got[0].Content != "hello" {
			t.Errorf("Whispers() = %v, want %v", got, want)
		}
	})

	t.Run("code index override", func(t *testing.T) {
		want := &daemon.CodeIndexResult{BlobsParsed: 42}
		m.CodeIndexFunc = func(_ daemon.CodeIndexPayload, _ *daemon.ProgressWriter) (*daemon.CodeIndexResult, error) {
			return want, nil
		}
		got, err := m.CodeIndex(daemon.CodeIndexPayload{}, nil)
		if err != nil {
			t.Fatalf("CodeIndex() error = %v", err)
		}
		if got.BlobsParsed != 42 {
			t.Errorf("expected BlobsParsed=42, got %d", got.BlobsParsed)
		}
	})

	t.Run("code status override", func(t *testing.T) {
		want := &daemon.CodeDBStats{IndexingNow: true, Commits: 10}
		m.CodeStatusFunc = func() *daemon.CodeDBStats { return want }
		got := m.CodeStatus()
		if !got.IndexingNow {
			t.Error("expected IndexingNow=true")
		}
	})

	t.Run("sync with progress override", func(t *testing.T) {
		want := errors.New("progress sync failed")
		m.SyncWithProgressFunc = func(_ *daemon.ProgressWriter) error { return want }
		if got := m.SyncWithProgress(nil); !errors.Is(got, want) {
			t.Errorf("SyncWithProgress() = %v, want %v", got, want)
		}
	})

	t.Run("team sync override", func(t *testing.T) {
		want := errors.New("team sync failed")
		m.TeamSyncFunc = func(_ *daemon.ProgressWriter) error { return want }
		if got := m.TeamSync(nil); !errors.Is(got, want) {
			t.Errorf("TeamSync() = %v, want %v", got, want)
		}
	})

	t.Run("sync history override", func(t *testing.T) {
		want := []daemon.SyncEvent{{Type: "pull"}}
		m.SyncHistoryFunc = func() []daemon.SyncEvent { return want }
		got := m.SyncHistory()
		if len(got) != 1 || got[0].Type != "pull" {
			t.Errorf("SyncHistory() = %v, want %v", got, want)
		}
	})

	t.Run("sessions override", func(t *testing.T) {
		want := []daemon.AgentSession{{AgentID: "a1"}}
		m.SessionsFunc = func() []daemon.AgentSession { return want }
		got := m.Sessions()
		if len(got) != 1 || got[0].AgentID != "a1" {
			t.Errorf("Sessions() = %v, want %v", got, want)
		}
	})

	t.Run("instances override", func(t *testing.T) {
		want := []daemon.InstanceInfo{{AgentID: "i1"}}
		m.InstancesFunc = func() []daemon.InstanceInfo { return want }
		got := m.Instances()
		if len(got) != 1 || got[0].AgentID != "i1" {
			t.Errorf("Instances() = %v, want %v", got, want)
		}
	})
}

// TestMockServiceInterfaceSatisfaction verifies the compile-time assertion works at runtime too.
func TestMockServiceInterfaceSatisfaction(t *testing.T) {
	svc := NewMockService()
	require.NotNil(t, svc)
	// compile-time assertion is in mock_service.go: var _ daemon.DaemonService = (*MockService)(nil)
}

// TestMockService_AsStatusProvider demonstrates the consumer pattern: create a
// MockService, override specific methods, and interact through DaemonService.
func TestMockService_AsStatusProvider(t *testing.T) {
	mock := NewMockService()
	mock.StatusFunc = func() *daemon.StatusData {
		return &daemon.StatusData{
			Running:    true,
			Pid:        42,
			Version:    "v1.2.3",
			LedgerPath: "/tmp/test-ledger",
		}
	}

	// use through the interface, not the concrete type
	var svc daemon.DaemonService = mock
	status := svc.Status()
	require.NotNil(t, status)
	require.True(t, status.Running)
	require.Equal(t, 42, status.Pid)
	require.Equal(t, "v1.2.3", status.Version)
	require.Equal(t, "/tmp/test-ledger", status.LedgerPath)
}

// TestMockService_AsSyncOrchestrator demonstrates using MockService through the
// interface for sync operations, with error injection.
func TestMockService_AsSyncOrchestrator(t *testing.T) {
	syncCalls := 0
	mock := NewMockService()
	mock.SyncFunc = func() error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("transient network error")
		}
		return nil
	}

	var svc daemon.DaemonService = mock

	// first call fails
	err := svc.Sync()
	require.Error(t, err)
	require.Contains(t, err.Error(), "transient network error")

	// retry succeeds
	err = svc.Sync()
	require.NoError(t, err)
	require.Equal(t, 2, syncCalls)
}

// TestMockService_FireAndForgetSafety verifies that fire-and-forget methods
// (Activity, Heartbeat, Telemetry, Friction) are safe to call through the
// interface with nil function fields -- they must not panic.
func TestMockService_FireAndForgetSafety(t *testing.T) {
	var svc daemon.DaemonService = NewMockService()

	// none of these should panic on a zero-value mock
	require.NotPanics(t, func() { svc.Activity() })
	require.NotPanics(t, func() { svc.Heartbeat("caller-1", json.RawMessage(`{"ping":true}`)) })
	require.NotPanics(t, func() { svc.Telemetry(json.RawMessage(`{"event":"test"}`)) })
	require.NotPanics(t, func() {
		svc.Friction(daemon.FrictionPayload{Kind: "unknown-command", Command: "ox foo"})
	})
}

// TestMockService_MultiMethodConsumer demonstrates a consumer that uses several
// DaemonService methods together, as real callers would.
func TestMockService_MultiMethodConsumer(t *testing.T) {
	mock := NewMockService()

	mock.StatusFunc = func() *daemon.StatusData {
		return &daemon.StatusData{Running: true, Pid: 100, Version: "test"}
	}
	mock.GetErrorsFunc = func() []daemon.StoredError {
		return []daemon.StoredError{
			{ID: "e-1", Message: "clone failed"},
			{ID: "e-2", Message: "push timeout"},
		}
	}
	mock.MarkErrorsFunc = func(ids []string) {
		require.Equal(t, []string{"e-1", "e-2"}, ids)
	}

	var svc daemon.DaemonService = mock

	// simulate: check status, retrieve errors, acknowledge them
	status := svc.Status()
	require.True(t, status.Running)

	errs := svc.GetErrors()
	require.Len(t, errs, 2)

	ids := make([]string, len(errs))
	for i, e := range errs {
		ids[i] = e.ID
	}
	svc.MarkErrors(ids) // assertion inside the func
}
