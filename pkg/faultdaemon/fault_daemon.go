package faultdaemon

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// maxRequestSize limits the maximum size of an incoming request.
const maxRequestSize = 1 * 1024 * 1024 // 1MB

// FaultDaemon is a generic fault injection daemon for testing IPC.
// It can simulate various failure conditions on Unix sockets.
type FaultDaemon struct {
	socketPath string

	configMu sync.RWMutex
	config   Config

	listener net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	connCount  atomic.Int64
	deadlockMu sync.Mutex // used for FaultDeadlock

	callsMu sync.Mutex
	calls   []RPCCall
}

// New creates a new fault injection daemon.
// socketPath: Unix socket path to listen on.
// config: Fault injection configuration.
func New(socketPath string, config Config) *FaultDaemon {
	return &FaultDaemon{
		socketPath: socketPath,
		config:     config,
	}
}

// Start starts the fault daemon, listening on the socket.
// Returns an error if the socket cannot be created.
func (d *FaultDaemon) Start() error {
	d.ctx, d.cancel = context.WithCancel(context.Background())

	// create socket
	if err := os.MkdirAll(filepath.Dir(d.socketPath), 0755); err != nil {
		return err
	}
	os.Remove(d.socketPath) // remove stale socket

	listener, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return err
	}
	d.listener = listener

	// start accepting connections
	d.wg.Add(1)
	go d.serve()

	return nil
}

// Stop stops the fault daemon and cleans up.
func (d *FaultDaemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.listener != nil {
		d.listener.Close()
	}
	d.wg.Wait()

	os.Remove(d.socketPath)
}

// SetFault changes the fault mode while running.
func (d *FaultDaemon) SetFault(fault Fault) {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	d.config.Fault = fault
}

// SetConfig updates the full fault configuration.
func (d *FaultDaemon) SetConfig(config Config) {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	d.config = config
}

// getConfig returns a copy of the current config (thread-safe).
func (d *FaultDaemon) getConfig() Config {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.config
}

// ConnectionCount returns the number of connections handled.
func (d *FaultDaemon) ConnectionCount() int64 {
	return d.connCount.Load()
}

// GetCalls returns a copy of all recorded calls.
func (d *FaultDaemon) GetCalls() []RPCCall {
	d.callsMu.Lock()
	defer d.callsMu.Unlock()
	calls := make([]RPCCall, len(d.calls))
	copy(calls, d.calls)
	return calls
}

// ResetCalls clears all recorded calls.
func (d *FaultDaemon) ResetCalls() {
	d.callsMu.Lock()
	defer d.callsMu.Unlock()
	d.calls = nil
}

// SocketPath returns the socket path this daemon is listening on.
func (d *FaultDaemon) SocketPath() string {
	return d.socketPath
}

func (d *FaultDaemon) recordCall(request []byte) {
	d.callsMu.Lock()
	defer d.callsMu.Unlock()
	d.calls = append(d.calls, RPCCall{
		Request:   append([]byte(nil), request...), // copy
		Timestamp: time.Now(),
	})
}

func (d *FaultDaemon) serve() {
	defer d.wg.Done()

	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return
			default:
				continue
			}
		}

		d.connCount.Add(1)
		go d.handleConn(conn)
	}
}

func (d *FaultDaemon) handleConn(conn net.Conn) {
	defer func() {
		// swallow panics from FaultPanicInHandler
		_ = recover()
		conn.Close()
	}()

	cfg := d.getConfig()

	// check for connection-level faults (before reading request)
	switch cfg.Fault {
	case FaultHangOnAccept:
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(10 * time.Minute):
			return
		}

	case FaultCloseImmediately, FaultRefuseAfterAccept:
		return

	case FaultDropConnection:
		if cfg.DropEveryN > 0 && d.connCount.Load()%int64(cfg.DropEveryN) == 0 {
			return
		}

	case FaultSlowAccept:
		delay := cfg.SlowResponseDelay
		if delay == 0 {
			delay = 3 * time.Second
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(delay):
		}
	}

	// read the request
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(io.LimitReader(conn, maxRequestSize))
	request, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	// record the call
	d.recordCall(request)

	// check if fault applies to this request
	if cfg.FaultMatcher != nil && !cfg.FaultMatcher(request) {
		// fault doesn't apply - respond normally
		d.sendNormalResponse(conn, request, cfg)
		return
	}

	// apply post-read faults
	switch cfg.Fault {
	case FaultCloseAfterRead:
		return

	case FaultHangBeforeResponse:
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(10 * time.Minute):
			return
		}

	case FaultSlowResponse:
		delay := cfg.SlowResponseDelay
		if delay == 0 {
			delay = 200 * time.Millisecond
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(delay):
		}
		d.sendNormalResponse(conn, request, cfg)
		return

	case FaultCorruptResponse:
		conn.Write([]byte("not json garbage \x00\xff\xfe\n"))
		return

	case FaultPartialResponse:
		conn.Write([]byte(`{"success":true,"data":`))
		time.Sleep(100 * time.Millisecond)
		return

	case FaultPanicInHandler:
		panic("intentional panic for testing")

	case FaultDeadlock:
		d.deadlockMu.Lock()
		select {
		case <-d.ctx.Done():
			d.deadlockMu.Unlock()
			return
		case <-time.After(10 * time.Minute):
			d.deadlockMu.Unlock()
			return
		}

	case FaultMultipleResponses:
		// send two responses
		resp := d.generateResponse(request, cfg)
		if resp != nil {
			conn.Write(resp)
			conn.Write(resp) // second response
		}
		return

	case FaultResponseWithoutNewline:
		resp := d.generateResponse(request, cfg)
		if len(resp) > 0 {
			// remove trailing newline
			if resp[len(resp)-1] == '\n' {
				resp = resp[:len(resp)-1]
			}
			conn.Write(resp)
		}
		time.Sleep(200 * time.Millisecond)
		return

	case FaultChunkedResponse:
		resp := d.generateResponse(request, cfg)
		for _, b := range resp {
			conn.Write([]byte{b})
			time.Sleep(2 * time.Millisecond)
		}
		return

	case FaultVerySlowResponse:
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		d.sendNormalResponse(conn, request, cfg)
		return

	case FaultResponseTooLarge:
		size := cfg.LargeSizeBytes
		if size == 0 {
			size = 2 * 1024 * 1024 // 2MB default
		}
		hugePayload := make([]byte, size)
		for i := range hugePayload {
			hugePayload[i] = 'x'
		}
		conn.Write(append(hugePayload, '\n'))
		return

	case FaultInvalidJSON:
		conn.Write([]byte("{\"success\":true,\"data\":\"bad\x00char\"}\n"))
		return

	case FaultEmbeddedNewlines:
		// send response with escaped newlines (valid JSON)
		conn.Write([]byte("{\"success\":true,\"data\":\"line1\\nline2\\nline3\"}\n"))
		return

	case FaultWriteHalfThenHang:
		resp := d.generateResponse(request, cfg)
		if resp != nil {
			half := len(resp) / 2
			conn.Write(resp[:half])
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(10 * time.Minute):
			return
		}
	}

	// no fault or unrecognized fault - respond normally
	d.sendNormalResponse(conn, request, cfg)
}

func (d *FaultDaemon) generateResponse(request []byte, cfg Config) []byte {
	if cfg.ResponseHandler != nil {
		return cfg.ResponseHandler(request)
	}
	// default: echo request back
	return request
}

func (d *FaultDaemon) sendNormalResponse(conn net.Conn, request []byte, cfg Config) {
	resp := d.generateResponse(request, cfg)
	if resp != nil {
		conn.Write(resp)
	}
}
