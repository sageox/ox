package testutil

import (
	"encoding/json"
	"os"
	"time"

	"github.com/sageox/ox/internal/daemon"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// compile-time assertion that MockService implements daemon.DaemonService
var _ daemon.DaemonService = (*MockService)(nil)

// MockService implements daemon.DaemonService with sensible defaults for testing.
// Override specific methods by setting the corresponding function fields.
// Unlike the socket-level MockDaemon, this operates at the Go interface level
// and can be passed directly to NewServerWithService or any code that accepts
// DaemonService.
type MockService struct {
	// sync operations
	SyncFunc             func() error
	SyncWithProgressFunc func(progress *daemon.ProgressWriter) error
	TeamSyncFunc         func(progress *daemon.ProgressWriter) error
	SyncHistoryFunc      func() []daemon.SyncEvent

	// status / query operations
	StatusFunc     func() *daemon.StatusData
	GetErrorsFunc  func() []daemon.StoredError
	SessionsFunc   func() []daemon.AgentSession
	InstancesFunc  func() []daemon.InstanceInfo
	WhispersFunc   func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error)
	CodeStatusFunc func() *daemon.CodeDBStats

	// mutation operations
	StopFunc            func()
	CheckoutFunc        func(payload daemon.CheckoutPayload, progress *daemon.ProgressWriter) (*daemon.CheckoutResult, error)
	MarkErrorsFunc      func(ids []string)
	TriggerGCFunc       func() *daemon.TriggerGCResponse
	CodeIndexFunc       func(payload daemon.CodeIndexPayload, progress *daemon.ProgressWriter) (*daemon.CodeIndexResult, error)
	DoctorFunc          func() *daemon.DoctorResponse
	SessionFinalizeFunc    func(payload daemon.SessionFinalizeIPCPayload)
	SessionWatchStartFunc  func(payload daemon.SessionWatchStartPayload)
	SessionWatchStopFunc   func(payload daemon.SessionWatchStopPayload)

	// fire-and-forget operations
	ActivityFunc  func()
	HeartbeatFunc func(callerID string, payload json.RawMessage)
	TelemetryFunc func(payload json.RawMessage)
	FrictionFunc       func(payload daemon.FrictionPayload)
	PublishMurmurFunc     func(payload daemon.MurmurPayload)
	PauseMurmuringFunc    func(agentID string)
	ResumeMurmuringFunc   func(agentID string)
}

// NewMockService creates a MockService with sensible defaults: healthy status, no errors,
// empty slices. All function fields are nil, so the default implementations are used.
func NewMockService() *MockService {
	return &MockService{}
}

// --- DaemonService implementation ---

func (m *MockService) Sync() error {
	if m.SyncFunc != nil {
		return m.SyncFunc()
	}
	return nil
}

func (m *MockService) SyncWithProgress(progress *daemon.ProgressWriter) error {
	if m.SyncWithProgressFunc != nil {
		return m.SyncWithProgressFunc(progress)
	}
	return nil
}

func (m *MockService) TeamSync(progress *daemon.ProgressWriter) error {
	if m.TeamSyncFunc != nil {
		return m.TeamSyncFunc(progress)
	}
	return nil
}

func (m *MockService) SyncHistory() []daemon.SyncEvent {
	if m.SyncHistoryFunc != nil {
		return m.SyncHistoryFunc()
	}
	return nil
}

func (m *MockService) Status() *daemon.StatusData {
	if m.StatusFunc != nil {
		return m.StatusFunc()
	}
	return &daemon.StatusData{
		Running: true,
		Pid:     os.Getpid(),
		Version: "mock-service-test",
	}
}

func (m *MockService) GetErrors() []daemon.StoredError {
	if m.GetErrorsFunc != nil {
		return m.GetErrorsFunc()
	}
	return nil
}

func (m *MockService) Sessions() []daemon.AgentSession {
	if m.SessionsFunc != nil {
		return m.SessionsFunc()
	}
	return nil
}

func (m *MockService) Instances() []daemon.InstanceInfo {
	if m.InstancesFunc != nil {
		return m.InstancesFunc()
	}
	return nil
}

func (m *MockService) Whispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
	if m.WhispersFunc != nil {
		return m.WhispersFunc(agentID, attention, topics)
	}
	return nil, nil
}

func (m *MockService) WhisperHistory(agentID string, before time.Time, limit int) (*daemon.WhisperHistoryResponse, error) {
	return &daemon.WhisperHistoryResponse{Entries: []whisperstore.WhisperEntry{}}, nil
}

func (m *MockService) CodeStatus() *daemon.CodeDBStats {
	if m.CodeStatusFunc != nil {
		return m.CodeStatusFunc()
	}
	return nil
}

func (m *MockService) Stop() {
	if m.StopFunc != nil {
		m.StopFunc()
	}
}

func (m *MockService) Checkout(payload daemon.CheckoutPayload, progress *daemon.ProgressWriter) (*daemon.CheckoutResult, error) {
	if m.CheckoutFunc != nil {
		return m.CheckoutFunc(payload, progress)
	}
	return nil, nil
}

func (m *MockService) MarkErrors(ids []string) {
	if m.MarkErrorsFunc != nil {
		m.MarkErrorsFunc(ids)
	}
}

func (m *MockService) TriggerGC() *daemon.TriggerGCResponse {
	if m.TriggerGCFunc != nil {
		return m.TriggerGCFunc()
	}
	return nil
}

func (m *MockService) CodeIndex(payload daemon.CodeIndexPayload, progress *daemon.ProgressWriter) (*daemon.CodeIndexResult, error) {
	if m.CodeIndexFunc != nil {
		return m.CodeIndexFunc(payload, progress)
	}
	return nil, nil
}

func (m *MockService) Doctor() *daemon.DoctorResponse {
	if m.DoctorFunc != nil {
		return m.DoctorFunc()
	}
	return nil
}

func (m *MockService) SessionFinalize(payload daemon.SessionFinalizeIPCPayload) {
	if m.SessionFinalizeFunc != nil {
		m.SessionFinalizeFunc(payload)
	}
}

func (m *MockService) SessionWatchStart(payload daemon.SessionWatchStartPayload) {
	if m.SessionWatchStartFunc != nil {
		m.SessionWatchStartFunc(payload)
	}
}

func (m *MockService) SessionWatchStop(payload daemon.SessionWatchStopPayload) {
	if m.SessionWatchStopFunc != nil {
		m.SessionWatchStopFunc(payload)
	}
}

func (m *MockService) Activity() {
	if m.ActivityFunc != nil {
		m.ActivityFunc()
	}
}

func (m *MockService) Heartbeat(callerID string, payload json.RawMessage) {
	if m.HeartbeatFunc != nil {
		m.HeartbeatFunc(callerID, payload)
	}
}

func (m *MockService) Telemetry(payload json.RawMessage) {
	if m.TelemetryFunc != nil {
		m.TelemetryFunc(payload)
	}
}

func (m *MockService) Friction(payload daemon.FrictionPayload) {
	if m.FrictionFunc != nil {
		m.FrictionFunc(payload)
	}
}

func (m *MockService) PublishMurmur(payload daemon.MurmurPayload) {
	if m.PublishMurmurFunc != nil {
		m.PublishMurmurFunc(payload)
	}
}

func (m *MockService) PauseMurmuring(agentID string) {
	if m.PauseMurmuringFunc != nil {
		m.PauseMurmuringFunc(agentID)
	}
}

func (m *MockService) ResumeMurmuring(agentID string) {
	if m.ResumeMurmuringFunc != nil {
		m.ResumeMurmuringFunc(agentID)
	}
}
