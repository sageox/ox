package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/ndjson"
)

var (
	// ErrAdapterNotFound is returned when no binary exists for the requested adapter type.
	ErrAdapterNotFound = errors.New("adapter binary not found")

	// ErrAdapterRespawnLimit is returned when an adapter has crashed too many times.
	ErrAdapterRespawnLimit = errors.New("adapter exceeded max respawn attempts")

	// ErrAdapterCrashed is returned when the adapter process has exited unexpectedly.
	ErrAdapterCrashed = errors.New("adapter process crashed")

	// ErrSupervisorShutdown is returned when a request is made after shutdown.
	ErrSupervisorShutdown = errors.New("adapter supervisor is shut down")
)

const (
	maxRespawnAttempts   = 3
	idleShutdownDuration = 30 * time.Second
	processWaitTimeout   = 5 * time.Second
	adapterBinaryPrefix  = "ox-adapter-"
	stderrRingMaxLines   = 50
)

// AdapterStatus represents the observable state of an adapter process.
type AdapterStatus struct {
	Type         string    `json:"type"`
	PID          int       `json:"pid"`
	State        string    `json:"state"` // "running", "crashed", "degraded", "stopped"
	Sessions     []string  `json:"sessions"`
	RespawnCount int       `json:"respawn_count"`
	LastActivity time.Time `json:"last_activity"`
	StderrTail   []string  `json:"stderr_tail,omitempty"`
}

// AdapterProcess tracks a single long-lived adapter serve-mode process.
//
// Lock order: AdapterSupervisor.mu before AdapterProcess.mu when both needed.
// Violating this order risks deadlock between concurrent request dispatch
// (which holds supervisor lock while selecting a process) and process restart
// (which holds process lock while updating supervisor state).
type AdapterProcess struct {
	adapterType  string
	binaryPath   string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *ndjson.Scanner
	encoder      *ndjson.Encoder
	sessions     map[string]bool // active agent_ids
	nextID       atomic.Int64
	mu           sync.Mutex
	respawnCount int
	lastActivity time.Time
	crashed      bool
	degraded     bool // set when max respawns exceeded

	// stderr ring buffer for debugging
	stderrRing *stderrRingBuffer

	// idle shutdown
	idleTimer    *time.Timer
	idleDuration time.Duration
}

// AdapterSupervisor manages long-lived adapter processes for the daemon.
// One serve-mode process per adapter type, shared across all sessions of that type.
type AdapterSupervisor struct {
	processes    map[string]*AdapterProcess
	mu           sync.RWMutex
	logger       *slog.Logger
	adapterDirs  []string
	shutdownFlag atomic.Bool

	// tracks degraded adapter types that have been removed from processes map
	// but need to report status until the supervisor is restarted
	degradedTypes map[string]*degradedRecord

	// subagent worker lifecycle tracking
	workers     *WorkerTracker
	adapterCaps map[string][]string // adapter_type -> declared capabilities

	// terminal_error event dispatch (see adapter_supervisor_terminal.go).
	terminalMu      sync.RWMutex
	terminalHandler TerminalErrorHandler
	terminalQueues  map[string]*terminalAgentQueue

	// overridable for testing
	idleDuration time.Duration
}

// degradedRecord tracks metadata for an adapter type that hit its respawn limit.
type degradedRecord struct {
	respawnCount int
	lastActivity time.Time
	stderrTail   []string
}

// NewAdapterSupervisor creates a supervisor that manages adapter processes.
// adapterDirs specifies directories to search for ox-adapter-* binaries.
func NewAdapterSupervisor(logger *slog.Logger, adapterDirs []string) *AdapterSupervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdapterSupervisor{
		processes:     make(map[string]*AdapterProcess),
		logger:        logger,
		adapterDirs:   adapterDirs,
		idleDuration:  idleShutdownDuration,
		degradedTypes: make(map[string]*degradedRecord),
		adapterCaps:   make(map[string][]string),
	}
}

// SendRequest routes a request to the appropriate adapter process, spawning it
// lazily if needed. Returns the adapter response or an error.
func (s *AdapterSupervisor) SendRequest(ctx context.Context, adapterType, agentID, method string, params any) (*adapterprotocol.Response, error) {
	if s.shutdownFlag.Load() {
		return nil, ErrSupervisorShutdown
	}

	proc, err := s.getOrSpawn(adapterType)
	if err != nil {
		return nil, fmt.Errorf("get adapter %s: %w", adapterType, err)
	}

	proc.mu.Lock()

	// check if process crashed since last request — must respawn under a different lock scope
	if proc.crashed {
		proc.mu.Unlock()
		respawned, err := s.handleCrash(adapterType)
		if err != nil {
			return nil, err
		}
		respawned.mu.Lock()
		proc = respawned
	}

	// from here proc is the (possibly respawned) live process, locked
	defer proc.mu.Unlock()

	// track session and emit lifecycle event for new sessions
	if agentID != "" {
		if !proc.sessions[agentID] {
			proc.sessions[agentID] = true
			s.logger.Info("adapter session started", "type", adapterType, "agent_id", agentID)
		}
	}
	proc.lastActivity = time.Now()

	// cancel any pending idle timer since we have activity
	if proc.idleTimer != nil {
		proc.idleTimer.Stop()
		proc.idleTimer = nil
	}

	return s.sendRequestLocked(ctx, proc, method, params)
}

// sendRequestLocked sends a request on the process's stdin and reads the response.
// Caller must hold proc.mu. The read loop skips push events (which have no ID)
// to correctly handle interleaved event/response messages on the shared stdout pipe.
func (s *AdapterSupervisor) sendRequestLocked(ctx context.Context, proc *AdapterProcess, method string, params any) (*adapterprotocol.Response, error) {
	id := int(proc.nextID.Add(1))

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	req := adapterprotocol.Request{
		ID:     id,
		Method: method,
		Params: rawParams,
	}

	if err := proc.encoder.Encode(req); err != nil {
		proc.crashed = true
		s.logger.Warn("adapter crashed", "type", proc.adapterType, "pid", s.processPID(proc), "respawn_count", proc.respawnCount, "reason", "write failed")
		return nil, fmt.Errorf("%w: write failed: %w", ErrAdapterCrashed, err)
	}

	// Read lines until we get a response (has "id" field). Push events (no "id")
	// are logged and skipped. This prevents interleaved file_watcher events from
	// being misinterpreted as responses.
	for {
		type scanResult struct {
			ok   bool
			data []byte
			err  error
		}
		ch := make(chan scanResult, 1)
		go func() {
			ok := proc.stdout.Scan()
			// copy bytes before sending — scanner reuses the buffer
			var data []byte
			if ok {
				src := proc.stdout.Bytes()
				data = make([]byte, len(src))
				copy(data, src)
			}
			ch <- scanResult{ok: ok, data: data, err: proc.stdout.Err()}
		}()

		var result scanResult
		select {
		case <-ctx.Done():
			// Mark process as crashed so the leaked scanner goroutine doesn't
			// corrupt future requests — the next caller will respawn the process.
			proc.crashed = true
			s.logger.Warn("adapter marked for respawn", "type", proc.adapterType, "reason", "context canceled with pending read")
			return nil, ctx.Err()
		case result = <-ch:
		}

		if !result.ok {
			proc.crashed = true
			scanErr := result.err
			if scanErr != nil {
				s.logger.Warn("adapter crashed", "type", proc.adapterType, "pid", s.processPID(proc), "respawn_count", proc.respawnCount, "reason", "read failed")
				return nil, fmt.Errorf("%w: read failed: %w", ErrAdapterCrashed, scanErr)
			}
			s.logger.Warn("adapter crashed", "type", proc.adapterType, "pid", s.processPID(proc), "respawn_count", proc.respawnCount, "reason", "pipe closed (EOF)")
			return nil, fmt.Errorf("%w: pipe closed (EOF)", ErrAdapterCrashed)
		}

		// check if this is a push event (has "event" field, no "id") or a response
		var probe struct {
			ID    int    `json:"id"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(result.data, &probe); err != nil {
			return nil, fmt.Errorf("decode adapter message: %w", err)
		}

		if probe.Event != "" {
			// push event — decode and dispatch asynchronously so the
			// read loop never blocks on downstream work (finalize,
			// metrics, etc.). Unknown event types are still logged.
			var evt adapterprotocol.Event
			if err := json.Unmarshal(result.data, &evt); err != nil {
				s.logger.Warn("decode push event failed", "type", proc.adapterType, "event", probe.Event, "err", err)
				continue
			}
			s.dispatchPushEvent(proc.adapterType, &evt)
			continue
		}

		// this is a response
		var resp adapterprotocol.Response
		if err := ndjson.Decode(result.data, &resp); err != nil {
			return nil, fmt.Errorf("decode adapter response: %w", err)
		}

		return &resp, nil
	}
}

// EndSession sends an end-session request and removes the agent from tracking.
// If no sessions remain, starts the idle shutdown timer.
func (s *AdapterSupervisor) EndSession(adapterType, agentID string) error {
	s.mu.RLock()
	proc, exists := s.processes[adapterType]
	s.mu.RUnlock()

	if !exists {
		return nil // no process for this type, nothing to do
	}

	// send end-session request (best effort)
	params := adapterprotocol.EndSessionParams{
		AgentID: agentID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := s.sendEndSession(ctx, proc, params)
	if err != nil {
		s.logger.Warn("end-session request failed", "type", adapterType, "agent_id", agentID, "error", err)
	} else if resp != nil && resp.Error != nil {
		s.logger.Warn("end-session returned error", "type", adapterType, "agent_id", agentID, "code", resp.Error.Code, "msg", resp.Error.Message)
	}

	// remove session from tracking
	proc.mu.Lock()
	delete(proc.sessions, agentID)
	remaining := len(proc.sessions)
	proc.mu.Unlock()

	s.logger.Info("adapter session ended", "type", adapterType, "agent_id", agentID)

	// start idle timer if no sessions remain
	if remaining == 0 {
		s.startIdleTimer(adapterType, proc)
	}

	return nil
}

// sendEndSession sends an end-session request directly on the process.
func (s *AdapterSupervisor) sendEndSession(ctx context.Context, proc *AdapterProcess, params adapterprotocol.EndSessionParams) (*adapterprotocol.Response, error) {
	proc.mu.Lock()
	defer proc.mu.Unlock()

	if proc.crashed {
		return nil, ErrAdapterCrashed
	}

	return s.sendRequestLocked(ctx, proc, adapterprotocol.MethodEndSession, params)
}

// startIdleTimer starts a timer that shuts down the adapter process after the
// idle duration if no new sessions arrive.
func (s *AdapterSupervisor) startIdleTimer(adapterType string, proc *AdapterProcess) {
	proc.mu.Lock()
	defer proc.mu.Unlock()

	// cancel any existing timer
	if proc.idleTimer != nil {
		proc.idleTimer.Stop()
	}

	duration := proc.idleDuration
	if duration == 0 {
		duration = s.idleDuration
	}

	proc.idleTimer = time.AfterFunc(duration, func() {
		proc.mu.Lock()
		sessionCount := len(proc.sessions)
		proc.mu.Unlock()

		// re-check: new sessions may have arrived during the timer
		if sessionCount > 0 {
			return
		}

		s.logger.Info("adapter shutdown", "type", adapterType, "reason", "idle")
		s.shutdownProcess(adapterType, proc)
	})
}

// Shutdown gracefully shuts down all managed adapter processes.
// Sends shutdown to each, waits up to processWaitTimeout, then SIGTERMs stragglers.
func (s *AdapterSupervisor) Shutdown(ctx context.Context) error {
	s.shutdownFlag.Store(true)

	s.mu.Lock()
	procs := make(map[string]*AdapterProcess, len(s.processes))
	for k, v := range s.processes {
		procs[k] = v
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for adapterType, proc := range procs {
		wg.Add(1)
		go func(at string, p *AdapterProcess) {
			defer wg.Done()
			s.logger.Info("adapter shutdown", "type", at, "reason", "daemon-stop")
			s.shutdownProcess(at, p)
		}(adapterType, proc)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shutdownProcess sends a shutdown request and waits for the process to exit.
func (s *AdapterSupervisor) shutdownProcess(adapterType string, proc *AdapterProcess) {
	proc.mu.Lock()

	// stop idle timer
	if proc.idleTimer != nil {
		proc.idleTimer.Stop()
		proc.idleTimer = nil
	}

	if proc.crashed || proc.cmd == nil || proc.cmd.Process == nil {
		proc.mu.Unlock()
		s.removeProcess(adapterType)
		return
	}

	// send shutdown request (best effort)
	shutReq := adapterprotocol.Request{
		ID:     int(proc.nextID.Add(1)),
		Method: adapterprotocol.MethodShutdown,
	}
	_ = proc.encoder.Encode(shutReq)

	if proc.stdin != nil {
		proc.stdin.Close()
	}

	cmd := proc.cmd
	proc.mu.Unlock()

	// wait for process to exit
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		s.logger.Info("adapter exited cleanly", "type", adapterType)
	case <-time.After(processWaitTimeout):
		s.logger.Warn("adapter did not exit, sending SIGTERM", "type", adapterType)
		cmd.Process.Kill()
		<-done
	}

	s.removeProcess(adapterType)
}

// removeProcess removes a process from the supervisor's map.
func (s *AdapterSupervisor) removeProcess(adapterType string) {
	s.mu.Lock()
	delete(s.processes, adapterType)
	s.mu.Unlock()
}

// getOrSpawn returns an existing process or spawns a new one for the adapter type.
// If a crashed process exists, it routes through handleCrash for respawn logic.
func (s *AdapterSupervisor) getOrSpawn(adapterType string) (*AdapterProcess, error) {
	s.mu.RLock()
	proc, exists := s.processes[adapterType]
	s.mu.RUnlock()

	if exists {
		proc.mu.Lock()
		crashed := proc.crashed
		proc.mu.Unlock()

		if !crashed {
			return proc, nil
		}
		return s.handleCrash(adapterType)
	}

	// no process exists, spawn fresh
	s.mu.Lock()
	defer s.mu.Unlock()

	// double-check under write lock (another goroutine may have spawned)
	if proc, exists := s.processes[adapterType]; exists {
		proc.mu.Lock()
		c := proc.crashed
		proc.mu.Unlock()
		if !c {
			return proc, nil
		}
	}

	return s.spawnAdapterLocked(adapterType)
}

// spawnAdapterLocked starts a new adapter process in serve mode.
// Caller must hold s.mu write lock.
func (s *AdapterSupervisor) spawnAdapterLocked(adapterType string) (*AdapterProcess, error) {
	binaryPath, err := s.findBinary(adapterType)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(binaryPath, "--serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion),
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	// capture stderr in a line-based ring buffer for diagnostics
	ring := newStderrRingBuffer(stderrRingMaxLines)
	cmd.Stderr = ring

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		return nil, fmt.Errorf("start adapter %s: %w", adapterType, err)
	}

	proc := &AdapterProcess{
		adapterType:  adapterType,
		binaryPath:   binaryPath,
		cmd:          cmd,
		stdin:        stdinPipe,
		stdout:       ndjson.NewScanner(stdoutPipe),
		encoder:      ndjson.NewEncoder(stdinPipe),
		sessions:     make(map[string]bool),
		lastActivity: time.Now(),
		idleDuration: s.idleDuration,
		stderrRing:   ring,
	}

	s.processes[adapterType] = proc
	s.logger.Info("adapter spawned", "type", adapterType, "pid", cmd.Process.Pid, "binary", binaryPath)

	return proc, nil
}

// handleCrash handles an adapter crash by respawning if under the limit.
func (s *AdapterSupervisor) handleCrash(adapterType string) (*AdapterProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, exists := s.processes[adapterType]
	if !exists {
		return s.spawnAdapterLocked(adapterType)
	}

	if old.respawnCount >= maxRespawnAttempts {
		// capture stderr before removing for diagnostics
		var stderrLines []string
		if old.stderrRing != nil {
			stderrLines = old.stderrRing.Lines()
		}

		delete(s.processes, adapterType)

		// record degraded state so Status() can report it
		s.degradedTypes[adapterType] = &degradedRecord{
			respawnCount: old.respawnCount,
			lastActivity: old.lastActivity,
			stderrTail:   stderrLines,
		}

		s.logger.Warn("adapter degraded", "type", adapterType, "reason", "max_respawns_exceeded", "attempts", old.respawnCount)
		return nil, fmt.Errorf("%w: %s crashed %d times", ErrAdapterRespawnLimit, adapterType, old.respawnCount)
	}

	prevCount := old.respawnCount
	prevSessions := make(map[string]bool, len(old.sessions))
	for k, v := range old.sessions {
		prevSessions[k] = v
	}

	// clean up old process
	if old.cmd != nil && old.cmd.Process != nil {
		old.cmd.Process.Kill()
		go old.cmd.Wait() // reap zombie
	}
	delete(s.processes, adapterType)

	s.logger.Info("adapter respawning", "type", adapterType, "attempt", prevCount+1, "max", maxRespawnAttempts)

	proc, err := s.spawnAdapterLocked(adapterType)
	if err != nil {
		return nil, err
	}

	proc.respawnCount = prevCount + 1
	proc.sessions = prevSessions

	return proc, nil
}

// findBinary locates the adapter binary in the configured directories.
func (s *AdapterSupervisor) findBinary(adapterType string) (string, error) {
	binaryName := adapterBinaryPrefix + adapterType
	for _, dir := range s.adapterDirs {
		path := filepath.Join(dir, binaryName)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if fi.Mode()&0111 == 0 {
			continue // not executable
		}
		return path, nil
	}
	return "", fmt.Errorf("%w: %s (searched: %s)", ErrAdapterNotFound, binaryName, strings.Join(s.adapterDirs, ", "))
}

// ActiveSessions returns the number of active sessions across all adapter processes.
func (s *AdapterSupervisor) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, proc := range s.processes {
		proc.mu.Lock()
		total += len(proc.sessions)
		proc.mu.Unlock()
	}
	return total
}

// ProcessCount returns the number of active adapter processes.
func (s *AdapterSupervisor) ProcessCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.processes)
}

// Status returns the current status of all adapter processes, including degraded ones.
func (s *AdapterSupervisor) Status() map[string]AdapterStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]AdapterStatus, len(s.processes)+len(s.degradedTypes))

	for adapterType, proc := range s.processes {
		proc.mu.Lock()
		state := "running"
		if proc.crashed {
			state = "crashed"
		}

		sessions := make([]string, 0, len(proc.sessions))
		for agentID := range proc.sessions {
			sessions = append(sessions, agentID)
		}

		var stderrLines []string
		if proc.crashed && proc.stderrRing != nil {
			stderrLines = proc.stderrRing.Lines()
		}

		pid := 0
		if proc.cmd != nil && proc.cmd.Process != nil {
			pid = proc.cmd.Process.Pid
		}

		result[adapterType] = AdapterStatus{
			Type:         adapterType,
			PID:          pid,
			State:        state,
			Sessions:     sessions,
			RespawnCount: proc.respawnCount,
			LastActivity: proc.lastActivity,
			StderrTail:   stderrLines,
		}
		proc.mu.Unlock()
	}

	// include degraded adapters that hit their respawn limit
	for adapterType, rec := range s.degradedTypes {
		result[adapterType] = AdapterStatus{
			Type:         adapterType,
			State:        "degraded",
			Sessions:     []string{},
			RespawnCount: rec.respawnCount,
			LastActivity: rec.lastActivity,
			StderrTail:   rec.stderrTail,
		}
	}

	return result
}

// StderrTail returns the last N lines of stderr output for the given adapter type.
// Returns nil if the adapter has no captured stderr.
func (s *AdapterSupervisor) StderrTail(adapterType string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if proc, exists := s.processes[adapterType]; exists {
		proc.mu.Lock()
		defer proc.mu.Unlock()
		if proc.stderrRing != nil {
			return proc.stderrRing.Lines()
		}
		return nil
	}

	// check degraded types
	if rec, exists := s.degradedTypes[adapterType]; exists {
		return rec.stderrTail
	}

	return nil
}

// processPID returns the PID of the adapter process, or 0 if unavailable.
func (s *AdapterSupervisor) processPID(proc *AdapterProcess) int {
	if proc.cmd != nil && proc.cmd.Process != nil {
		return proc.cmd.Process.Pid
	}
	return 0
}

// stderrRingBuffer is a thread-safe ring buffer that captures the last N lines
// of stderr output. It implements io.Writer so it can be assigned to exec.Cmd.Stderr.
type stderrRingBuffer struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	partial  []byte // incomplete line (no trailing newline yet)
}

// newStderrRingBuffer creates a ring buffer that retains the last maxLines lines.
func newStderrRingBuffer(maxLines int) *stderrRingBuffer {
	return &stderrRingBuffer{
		lines:    make([]string, 0, maxLines),
		maxLines: maxLines,
	}
}

// Write implements io.Writer. Splits input on newlines and retains the last maxLines.
func (r *stderrRingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.partial = append(r.partial, p...)

	for {
		idx := -1
		for i, b := range r.partial {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}

		line := string(r.partial[:idx])
		r.partial = r.partial[idx+1:]

		// trim trailing \r for Windows-style line endings
		line = strings.TrimRight(line, "\r")

		if len(r.lines) >= r.maxLines {
			// shift out oldest line
			copy(r.lines, r.lines[1:])
			r.lines[len(r.lines)-1] = line
		} else {
			r.lines = append(r.lines, line)
		}
	}

	return len(p), nil // always succeed to avoid breaking the subprocess
}

// Lines returns a copy of the buffered lines.
func (r *stderrRingBuffer) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]string, len(r.lines))
	copy(result, r.lines)

	// include any partial line (no trailing newline yet)
	if len(r.partial) > 0 {
		partial := strings.TrimRight(string(r.partial), "\r")
		result = append(result, partial)
	}

	return result
}
