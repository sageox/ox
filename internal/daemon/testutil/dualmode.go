// Package testutil provides testing utilities for daemon functionality.
// It enables dual-mode testing where commands can be tested both with
// a running daemon and in direct mode (no daemon).
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
)

// Mode represents the execution mode for tests.
type Mode string

const (
	// ModeDaemon runs tests with a real daemon.
	ModeDaemon Mode = "daemon"

	// ModeDirect runs tests without a daemon (direct execution).
	ModeDirect Mode = "direct"
)

// DualModeTest defines a test that should work in both daemon and direct modes.
// This ensures commands function correctly regardless of daemon availability.
type DualModeTest struct {
	// Name is the test name (will have mode suffix appended).
	Name string

	// Test is the test function. Mode indicates whether the daemon is running.
	// Test should use mode to determine how to execute the command.
	Test func(t *testing.T, mode Mode)

	// SkipDaemon skips daemon mode test if true.
	SkipDaemon bool

	// SkipDirect skips direct mode test if true.
	SkipDirect bool
}

// RunDualMode executes tests in both daemon and direct modes.
// Each test runs twice: once with a daemon and once without.
// Note: Tests do not run in parallel because daemon mode requires
// modifying environment variables, which conflicts with t.Parallel().
func RunDualMode(t *testing.T, tests []DualModeTest) {
	t.Helper()

	for _, test := range tests {
		test := test // capture range variable

		// run in direct mode (no daemon)
		if !test.SkipDirect {
			t.Run(test.Name+"/direct", func(t *testing.T) {
				// use isolated environment but don't start daemon
				tmpDir := t.TempDir()

				// fully isolate all XDG directories
				t.Setenv("OX_XDG_ENABLE", "1")
				t.Setenv("XDG_RUNTIME_DIR", tmpDir)
				t.Setenv("XDG_CONFIG_HOME", tmpDir)
				t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
				t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))
				t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))

				test.Test(t, ModeDirect)
			})
		}

		// run in daemon mode
		if !test.SkipDaemon {
			t.Run(test.Name+"/daemon", func(t *testing.T) {
				// use short temp dir for Unix socket path limit
				shortDir, err := os.MkdirTemp("/tmp", "ox-dual-")
				if err != nil {
					t.Fatalf("failed to create temp dir: %v", err)
				}
				tmpDir := t.TempDir()
				t.Cleanup(func() { os.RemoveAll(shortDir) })

				// fully isolate all XDG directories
				t.Setenv("OX_XDG_ENABLE", "1")
				t.Setenv("XDG_RUNTIME_DIR", shortDir)
				t.Setenv("XDG_CONFIG_HOME", shortDir)
				t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
				t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))
				t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))

				cleanup := StartTestDaemon(t, shortDir)
				defer cleanup()

				test.Test(t, ModeDaemon)
			})
		}
	}
}

// StartTestDaemon starts a real daemon in a temp directory for testing.
// Returns a cleanup function that must be called to stop the daemon.
//
// Usage:
//
//	cleanup := StartTestDaemon(t, tmpDir)
//	defer cleanup()
//	// ... run tests that interact with daemon ...
func StartTestDaemon(t *testing.T, tmpDir string) (cleanup func()) {
	t.Helper()

	// configure daemon with test-specific settings
	cfg := daemon.DefaultConfig()
	cfg.LedgerPath = filepath.Join(tmpDir, "ledger")
	cfg.ProjectRoot = tmpDir
	cfg.SyncIntervalRead = 1 * time.Hour // don't auto-sync during tests
	cfg.InactivityTimeout = 0            // disable inactivity timeout for tests

	// create ledger directory
	if err := os.MkdirAll(cfg.LedgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}

	// create daemon - use debug logging to diagnose startup issues
	logLevel := slog.LevelError
	if os.Getenv("OX_TEST_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	d := daemon.New(cfg, logger)

	// start daemon in background
	_, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- d.Start()
	}()

	// wait for daemon to be fully ready (both lock acquired AND IPC server listening)
	var ready bool
	var lastErr error
startupLoop:
	for i := 0; i < 50; i++ { // 5 seconds max
		time.Sleep(100 * time.Millisecond)

		// check for early failure
		select {
		case err := <-errChan:
			lastErr = err
			break startupLoop
		default:
		}

		// check if we can actually connect (not just lock acquired)
		client := daemon.TryConnect()
		if client != nil {
			ready = true
			break
		}
	}

	if lastErr != nil {
		cancel()
		t.Fatalf("daemon failed to start: %v", lastErr)
	}

	if !ready {
		cancel()
		t.Fatalf("daemon failed to start within timeout (socket: %s)", daemon.SocketPath())
	}

	return func() {
		cancel()
		_ = d.Stop()

		// wait for daemon to stop
		select {
		case <-errChan:
			// daemon stopped
		case <-time.After(5 * time.Second):
			t.Log("warning: daemon did not stop cleanly within timeout")
		}
	}
}

// TestDaemonConfig returns a daemon config suitable for testing.
func TestDaemonConfig(tmpDir string) *daemon.Config {
	cfg := daemon.DefaultConfig()
	cfg.LedgerPath = filepath.Join(tmpDir, "ledger")
	cfg.ProjectRoot = tmpDir
	cfg.SyncIntervalRead = 1 * time.Hour
	cfg.InactivityTimeout = 0
	return cfg
}

// RPCCall records an RPC call made to the mock daemon.
type RPCCall struct {
	Type      string
	Payload   json.RawMessage
	Timestamp time.Time
}

// MockDaemon provides a mock daemon for unit tests.
// It records all RPC calls and returns configurable responses.
//
// Deprecated: Use OxFaultDaemon for new tests. It provides the same
// functionality plus fault injection capabilities.
type MockDaemon struct {
	// Responses maps message types to their responses.
	// Key is the message type (e.g., "status", "sync").
	Responses map[string]any

	// StatusResponse is the status to return for status requests.
	StatusResponse *daemon.StatusData

	// SyncError is the error to return for sync requests (nil = success).
	SyncError error

	// Calls records all RPC calls made to the mock.
	Calls []RPCCall

	mu       sync.Mutex
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewMockDaemon creates a new mock daemon.
func NewMockDaemon() *MockDaemon {
	return &MockDaemon{
		Responses: make(map[string]any),
		StatusResponse: &daemon.StatusData{
			Running: true,
			Pid:     os.Getpid(),
			Version: "test",
		},
	}
}

// Start starts the mock daemon on the default socket path.
// Call this after setting environment variables for XDG_RUNTIME_DIR.
func (m *MockDaemon) Start(t *testing.T) {
	t.Helper()

	m.ctx, m.cancel = context.WithCancel(context.Background())

	socketPath := daemon.SocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		t.Fatalf("failed to create socket dir: %v", err)
	}

	// remove existing socket
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	m.listener = listener

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.serve()
	}()

	// wait for listener to be ready
	time.Sleep(50 * time.Millisecond)
}

// Stop stops the mock daemon.
func (m *MockDaemon) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.listener != nil {
		m.listener.Close()
	}
	m.wg.Wait()
}

// GetCalls returns a copy of all recorded calls.
func (m *MockDaemon) GetCalls() []RPCCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]RPCCall, len(m.Calls))
	copy(calls, m.Calls)
	return calls
}

// ResetCalls clears all recorded calls.
func (m *MockDaemon) ResetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = nil
}

// CallCount returns the number of calls of a specific type.
func (m *MockDaemon) CallCount(msgType string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, call := range m.Calls {
		if call.Type == msgType {
			count++
		}
	}
	return count
}

// serve handles incoming connections.
func (m *MockDaemon) serve() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.ctx.Done():
				return
			default:
				continue
			}
		}

		go m.handleConn(conn)
	}
}

// handleConn handles a single connection.
func (m *MockDaemon) handleConn(conn net.Conn) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// read message
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	var msg daemon.Message
	if err := json.Unmarshal(buf[:n], &msg); err != nil {
		m.sendError(conn, "invalid message")
		return
	}

	// record call
	m.mu.Lock()
	m.Calls = append(m.Calls, RPCCall{
		Type:      msg.Type,
		Payload:   msg.Payload,
		Timestamp: time.Now(),
	})
	m.mu.Unlock()

	// generate response based on message type
	var resp daemon.Response
	switch msg.Type {
	case daemon.MsgTypePing:
		resp = daemon.Response{Success: true, Data: json.RawMessage(`"pong"`)}

	case daemon.MsgTypeStatus:
		data, _ := json.Marshal(m.StatusResponse)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeSync:
		if m.SyncError != nil {
			resp = daemon.Response{Success: false, Error: m.SyncError.Error()}
		} else {
			resp = daemon.Response{Success: true}
		}

	case daemon.MsgTypeStop:
		resp = daemon.Response{Success: true}

	case daemon.MsgTypeHeartbeat, daemon.MsgTypeTelemetry, daemon.MsgTypeFriction, daemon.MsgTypeSessionFinalize:
		// fire-and-forget messages - no response
		return

	default:
		// check for custom response
		if customResp, ok := m.Responses[msg.Type]; ok {
			data, _ := json.Marshal(customResp)
			resp = daemon.Response{Success: true, Data: data}
		} else {
			resp = daemon.Response{Success: false, Error: "unknown message type"}
		}
	}

	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	conn.Write(data)
}

// sendError sends an error response.
func (m *MockDaemon) sendError(conn net.Conn, errMsg string) {
	resp := daemon.Response{Success: false, Error: errMsg}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	conn.Write(data)
}

// WithStatus configures the status response.
func (m *MockDaemon) WithStatus(status *daemon.StatusData) *MockDaemon {
	m.StatusResponse = status
	return m
}

// WithSyncError configures sync to return an error.
func (m *MockDaemon) WithSyncError(err error) *MockDaemon {
	m.SyncError = err
	return m
}

// WithResponse adds a custom response for a message type.
func (m *MockDaemon) WithResponse(msgType string, response any) *MockDaemon {
	m.Responses[msgType] = response
	return m
}

// TestEnvironment sets up an isolated test environment for daemon tests.
// Returns a cleanup function that must be called to restore the environment.
type TestEnvironment struct {
	TmpDir     string
	LedgerDir  string
	ProjectDir string
	t          *testing.T
}

// NewTestEnvironment creates a new isolated test environment.
// Sets up XDG directories to isolate tests from user's real config.
func NewTestEnvironment(t *testing.T) *TestEnvironment {
	t.Helper()

	tmpDir := t.TempDir()

	// use short path for Unix socket (104 char limit on macOS)
	shortDir, err := os.MkdirTemp("/tmp", "ox-test-")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}

	ledgerDir := filepath.Join(tmpDir, "ledger")
	projectDir := filepath.Join(tmpDir, "project")

	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// set environment for FULLY isolated daemon
	// all XDG dirs must be set to prevent tests from polluting user config
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", shortDir)                      // sockets, locks
	t.Setenv("XDG_CONFIG_HOME", shortDir)                      // config, restart history
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))   // persistent data
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache")) // cache
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state")) // state

	env := &TestEnvironment{
		TmpDir:     shortDir,
		LedgerDir:  ledgerDir,
		ProjectDir: projectDir,
		t:          t,
	}

	// cleanup short dir on test completion
	t.Cleanup(func() {
		os.RemoveAll(shortDir)
	})

	return env
}

// StartDaemon starts a test daemon using this environment.
func (e *TestEnvironment) StartDaemon() func() {
	return StartTestDaemon(e.t, e.TmpDir)
}

// StartMock starts a mock daemon using this environment.
// Deprecated: Use StartFault for new tests.
func (e *TestEnvironment) StartMock() *MockDaemon {
	mock := NewMockDaemon()
	mock.Start(e.t)
	e.t.Cleanup(mock.Stop)
	return mock
}

// StartFault starts an ox fault daemon using this environment.
// This provides access to all fault injection modes while understanding ox protocol.
func (e *TestEnvironment) StartFault(config OxFaultConfig) *OxFaultDaemon {
	d := NewOxFaultDaemon(e.t, config)
	d.Start()
	e.t.Cleanup(d.Stop)
	return d
}

// IsDaemonMode returns true if we're running in daemon mode.
// Useful for tests that need to behave differently based on daemon availability.
func IsDaemonMode() bool {
	return daemon.IsRunning()
}

// RequireDaemon skips the test if no daemon is running.
func RequireDaemon(t *testing.T) {
	t.Helper()
	if !daemon.IsRunning() {
		t.Skip("skipping test: daemon not running")
	}
}

// RequireNoDaemon skips the test if a daemon is running.
func RequireNoDaemon(t *testing.T) {
	t.Helper()
	if daemon.IsRunning() {
		t.Skip("skipping test: daemon is running")
	}
}

// WaitForDaemon waits for the daemon to become available.
func WaitForDaemon(t *testing.T, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemon.IsRunning() {
			client := daemon.TryConnect()
			if client != nil {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon not available within %v", timeout)
}

// NewTestClient creates a daemon client with test-appropriate timeout.
// Uses 500ms (vs 50ms production default) for test stability under load.
func NewTestClient() *daemon.Client {
	return daemon.NewClientWithTimeout(500 * time.Millisecond)
}
