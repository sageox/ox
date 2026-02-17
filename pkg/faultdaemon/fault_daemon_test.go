package faultdaemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simpleRequest is a minimal protocol for testing.
type simpleRequest struct {
	Type string `json:"type"`
}

type simpleResponse struct {
	Success bool   `json:"success"`
	Data    string `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// createTestDaemon creates a fault daemon in a temp directory with a simple echo handler.
func createTestDaemon(t *testing.T, config Config) (*FaultDaemon, string) {
	t.Helper()

	// use short temp dir for Unix socket path limit (104 chars on macOS)
	tmpDir, err := os.MkdirTemp("/tmp", "fd-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	socketPath := filepath.Join(tmpDir, "test.sock")

	// set up response handler if not provided
	if config.ResponseHandler == nil {
		config.ResponseHandler = func(request []byte) []byte {
			var req simpleRequest
			if err := json.Unmarshal(request, &req); err != nil {
				resp := simpleResponse{Success: false, Error: "invalid request"}
				data, _ := json.Marshal(resp)
				return append(data, '\n')
			}

			var resp simpleResponse
			switch req.Type {
			case "ping":
				resp = simpleResponse{Success: true, Data: "pong"}
			case "status":
				resp = simpleResponse{Success: true, Data: "ok"}
			default:
				resp = simpleResponse{Success: true, Data: "echo"}
			}
			data, _ := json.Marshal(resp)
			return append(data, '\n')
		}
	}

	d := New(socketPath, config)
	return d, socketPath
}

// sendRequest sends a request to the daemon and reads the response.
func sendRequest(socketPath string, reqType string, timeout time.Duration) (*simpleResponse, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))

	req := simpleRequest{Type: reqType}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp simpleResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// =============================================================================
// FAST TESTS
// =============================================================================

func TestFaultDaemon_Healthy(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultNone})
	require.NoError(t, d.Start())
	defer d.Stop()

	// wait for socket
	time.Sleep(50 * time.Millisecond)

	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "pong", resp.Data)
}

func TestFaultDaemon_CloseImmediately(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultCloseImmediately})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_CloseAfterRead(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultCloseAfterRead})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_CorruptResponse(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultCorruptResponse})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err, "should fail to parse corrupt response")
}

func TestFaultDaemon_PanicInHandler(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultPanicInHandler})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_MultipleResponses(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultMultipleResponses})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	// first response should be valid
	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// new connection should also work
	resp, err = sendRequest(socketPath, "status", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestFaultDaemon_InvalidJSON(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultInvalidJSON})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_EmbeddedNewlines(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultEmbeddedNewlines})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err, "JSON with escaped newlines should work")
	assert.True(t, resp.Success)
}

func TestFaultDaemon_RefuseAfterAccept(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultRefuseAfterAccept})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_ConnectionCount(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultNone})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int64(0), d.ConnectionCount())
	_, _ = sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Equal(t, int64(1), d.ConnectionCount())
	_, _ = sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Equal(t, int64(2), d.ConnectionCount())
}

func TestFaultDaemon_CallTracking(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultNone})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	assert.Len(t, d.GetCalls(), 0)

	_, _ = sendRequest(socketPath, "ping", 100*time.Millisecond)
	calls := d.GetCalls()
	assert.Len(t, calls, 1)
	assert.Contains(t, string(calls[0].Request), "ping")

	_, _ = sendRequest(socketPath, "status", 100*time.Millisecond)
	calls = d.GetCalls()
	assert.Len(t, calls, 2)

	d.ResetCalls()
	assert.Len(t, d.GetCalls(), 0)
}

func TestFaultDaemon_FaultMatcher(t *testing.T) {
	matcher := func(request []byte) bool {
		var req simpleRequest
		if json.Unmarshal(request, &req) != nil {
			return false
		}
		return req.Type == "sync" // only apply fault to sync requests
	}

	d, socketPath := createTestDaemon(t, Config{
		Fault:        FaultCloseAfterRead,
		FaultMatcher: matcher,
	})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	// ping should work (fault doesn't apply)
	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// sync should fail (fault applies)
	_, err = sendRequest(socketPath, "sync", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_SetFault(t *testing.T) {
	d, socketPath := createTestDaemon(t, Config{Fault: FaultNone})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	// healthy first
	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// switch to fault mode
	d.SetFault(FaultCloseImmediately)
	_, err = sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)

	// back to healthy
	d.SetFault(FaultNone)
	resp, err = sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestFaultDaemon_SocketPathAccessor(t *testing.T) {
	// use /tmp directly to avoid Unix socket path length limit (~104 chars)
	tmpDir, err := os.MkdirTemp("/tmp", "ox-fd-acc-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	socketPath := filepath.Join(tmpDir, "test.sock")

	d := New(socketPath, Config{
		Fault: FaultNone,
		ResponseHandler: func(request []byte) []byte {
			return []byte(`{"success":true}` + "\n")
		},
	})
	require.NoError(t, d.Start())
	defer d.Stop()

	// verify socket path accessor
	assert.Equal(t, socketPath, d.SocketPath())
}

// =============================================================================
// SLOW TESTS - Involve timeouts
// =============================================================================

func TestFaultDaemon_HangOnAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultHangOnAccept})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_HangBeforeResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultHangBeforeResponse})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_SlowResponse_UnderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{
		Fault:             FaultSlowResponse,
		SlowResponseDelay: 50 * time.Millisecond,
	})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := sendRequest(socketPath, "ping", 200*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestFaultDaemon_SlowResponse_OverTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{
		Fault:             FaultSlowResponse,
		SlowResponseDelay: 200 * time.Millisecond,
	})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_PartialResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultPartialResponse})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_ResponseWithoutNewline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultResponseWithoutNewline})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

func TestFaultDaemon_ChunkedResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultChunkedResponse})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := sendRequest(socketPath, "ping", 500*time.Millisecond)
	require.NoError(t, err, "chunked response should be handled by bufio")
	assert.True(t, resp.Success)
}

func TestFaultDaemon_FlakyConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{
		Fault:      FaultDropConnection,
		DropEveryN: 2,
	})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err) // conn #1
	assert.True(t, resp.Success)

	_, err = sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err) // conn #2 dropped

	resp, err = sendRequest(socketPath, "ping", 100*time.Millisecond)
	require.NoError(t, err) // conn #3
	assert.True(t, resp.Success)
}

func TestFaultDaemon_WriteHalfThenHang(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	d, socketPath := createTestDaemon(t, Config{Fault: FaultWriteHalfThenHang})
	require.NoError(t, d.Start())
	defer d.Stop()

	time.Sleep(50 * time.Millisecond)

	_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)
	assert.Error(t, err)
}

// =============================================================================
// TABLE-DRIVEN TEST
// =============================================================================

func TestAllFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive fault table test")
	}

	tests := []struct {
		name        string
		fault       Fault
		expectError bool
	}{
		// fast faults
		{"healthy", FaultNone, false},
		{"close_immediately", FaultCloseImmediately, true},
		{"close_after_read", FaultCloseAfterRead, true},
		{"corrupt_response", FaultCorruptResponse, true},
		{"panic_in_handler", FaultPanicInHandler, true},
		{"multiple_responses", FaultMultipleResponses, false},
		{"invalid_json", FaultInvalidJSON, true},
		{"embedded_newlines", FaultEmbeddedNewlines, false},
		{"refuse_after_accept", FaultRefuseAfterAccept, true},

		// slow faults
		{"hang_on_accept", FaultHangOnAccept, true},
		{"hang_before_response", FaultHangBeforeResponse, true},
		{"partial_response", FaultPartialResponse, true},
		{"response_without_newline", FaultResponseWithoutNewline, true},
		{"write_half_then_hang", FaultWriteHalfThenHang, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, socketPath := createTestDaemon(t, Config{Fault: tt.fault})
			require.NoError(t, d.Start())
			defer d.Stop()

			time.Sleep(50 * time.Millisecond)

			_, err := sendRequest(socketPath, "ping", 100*time.Millisecond)

			if tt.expectError {
				assert.Error(t, err, "expected error for fault: %s", tt.fault)
			} else {
				assert.NoError(t, err, "expected no error for fault: %s", tt.fault)
			}
		})
	}
}
