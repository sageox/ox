// Package adapterruntime is a Go SDK for ox adapter authors.
//
// It handles protocol framing, serve-mode dispatch, graceful shutdown, and
// unknown-method responses so adapter authors can focus on agent-specific logic
// (session file discovery, transcript parsing, hook installation).
//
// Non-Go adapters implement the same protocol directly against the spec.
// This package is a convenience layer, not a requirement.
package adapterruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/ndjson"
)

// Config holds the handler functions for each adapter subcommand.
// Nil handlers cause the subcommand to return an error.
type Config struct {
	Info           func() (*adapterprotocol.InfoResponse, error)
	Detect         func() (*adapterprotocol.DetectResponse, error)
	InstallHooks   func(adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error)
	CheckHooks     func(adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error)
	UninstallHooks func(adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error)
	Read           func(adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error)
	ReadMetadata   func(adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error)
	Diagnose       func(adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error)
	Serve          func(*Server)
}

// Run dispatches to the appropriate handler based on os.Args[1].
// It reads the subcommand from the CLI arguments, calls the handler,
// serializes the result as compact JSON to stdout, and exits.
// For --serve, it enters serve mode (blocking).
func Run(cfg Config) {
	RunWithArgs(cfg, os.Args[1:], os.Stdin, os.Stdout)
}

// RunWithArgs is like Run but accepts explicit args and IO for testing.
func RunWithArgs(cfg Config, args []string, stdin io.Reader, stdout io.Writer) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: <adapter> <subcommand> [flags]")
		os.Exit(1)
	}

	cmd := args[0]
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)

	switch cmd {
	case "info":
		runOneShot(enc, func() (any, error) {
			if cfg.Info == nil {
				return nil, fmt.Errorf("info not implemented")
			}
			return cfg.Info()
		})

	case "detect":
		runOneShot(enc, func() (any, error) {
			if cfg.Detect == nil {
				return nil, fmt.Errorf("detect not implemented")
			}
			return cfg.Detect()
		})

	case "install-hooks":
		p := parseHookParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.InstallHooks == nil {
				return nil, fmt.Errorf("install-hooks not implemented")
			}
			return cfg.InstallHooks(p)
		})

	case "check-hooks":
		p := parseHookParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.CheckHooks == nil {
				return nil, fmt.Errorf("check-hooks not implemented")
			}
			return cfg.CheckHooks(p)
		})

	case "uninstall-hooks":
		p := parseHookParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.UninstallHooks == nil {
				return nil, fmt.Errorf("uninstall-hooks not implemented")
			}
			return cfg.UninstallHooks(p)
		})

	case "read":
		p := parseReadParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.Read == nil {
				return nil, fmt.Errorf("read not implemented")
			}
			return cfg.Read(p)
		})

	case "read-metadata":
		p := parseReadParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.ReadMetadata == nil {
				return nil, fmt.Errorf("read-metadata not implemented")
			}
			return cfg.ReadMetadata(p)
		})

	case "diagnose":
		p := parseDiagnoseParams(args[1:])
		runOneShot(enc, func() (any, error) {
			if cfg.Diagnose == nil {
				return nil, fmt.Errorf("diagnose not implemented")
			}
			return cfg.Diagnose(p)
		})

	case "--serve":
		if cfg.Serve == nil {
			fmt.Fprintln(os.Stderr, "serve mode not implemented")
			os.Exit(1)
		}
		srv := NewServer(stdin, stdout)
		cfg.Serve(srv)

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(1)
	}
}

func runOneShot(enc *json.Encoder, fn func() (any, error)) {
	result, err := fn()
	if err != nil {
		errResp := map[string]string{"error": err.Error()}
		if encErr := enc.Encode(errResp); encErr != nil {
			log.Fatalf("failed to encode error response: %v", encErr)
		}
		os.Exit(1)
	}
	if err := enc.Encode(result); err != nil {
		log.Fatalf("failed to encode response: %v", err)
	}
}

func parseHookParams(args []string) adapterprotocol.HookParams {
	p := adapterprotocol.HookParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--scope":
			p.Scope = args[i+1]
			i++
		}
	}
	return p
}

func parseReadParams(args []string) adapterprotocol.ReadParams {
	p := adapterprotocol.ReadParams{}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--session-file" {
			p.SessionFile = args[i+1]
			i++
		}
	}
	return p
}

func parseDiagnoseParams(args []string) adapterprotocol.DiagnoseParams {
	p := adapterprotocol.DiagnoseParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--scope":
			p.Scope = args[i+1]
			i++
		}
	}
	return p
}

// --- Server (serve mode) ---

// FindSessionHandler handles find-session requests.
type FindSessionHandler func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error)

// ReadFromOffsetHandler handles read-from-offset requests.
type ReadFromOffsetHandler func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error)

// EndSessionHandler handles end-session requests.
type EndSessionHandler func(ctx context.Context, p adapterprotocol.EndSessionParams) error

// SpawnSubagentHandler handles spawn-subagent requests.
type SpawnSubagentHandler func(ctx context.Context, p adapterprotocol.SpawnSubagentParams) (*adapterprotocol.SpawnSubagentResult, error)

// SubagentStatusHandler handles subagent-status requests.
type SubagentStatusHandler func(ctx context.Context, p adapterprotocol.SubagentStatusParams) (*adapterprotocol.SubagentStatusResult, error)

// CancelSubagentHandler handles cancel-subagent requests.
type CancelSubagentHandler func(ctx context.Context, p adapterprotocol.CancelSubagentParams) (*adapterprotocol.CancelSubagentResult, error)

// Server manages the serve-mode request/response loop.
type Server struct {
	scanner *ndjson.Scanner
	writer  *Writer

	findSession    FindSessionHandler
	readFromOffset ReadFromOffsetHandler
	endSession     EndSessionHandler

	spawnSubagent  SpawnSubagentHandler
	subagentStatus SubagentStatusHandler
	cancelSubagent CancelSubagentHandler

	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer creates a serve-mode server reading from r, writing to w.
func NewServer(r io.Reader, w io.Writer) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		scanner: ndjson.NewScanner(r),
		writer:  NewWriter(w),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// OnFindSession registers the handler for find-session requests.
func (s *Server) OnFindSession(h FindSessionHandler) {
	s.findSession = h
}

// OnReadFromOffset registers the handler for read-from-offset requests.
func (s *Server) OnReadFromOffset(h ReadFromOffsetHandler) {
	s.readFromOffset = h
}

// OnEndSession registers the handler for end-session requests.
func (s *Server) OnEndSession(h EndSessionHandler) {
	s.endSession = h
}

// OnSpawnSubagent registers the handler for spawn-subagent requests.
func (s *Server) OnSpawnSubagent(h SpawnSubagentHandler) {
	s.spawnSubagent = h
}

// OnSubagentStatus registers the handler for subagent-status requests.
func (s *Server) OnSubagentStatus(h SubagentStatusHandler) {
	s.subagentStatus = h
}

// OnCancelSubagent registers the handler for cancel-subagent requests.
func (s *Server) OnCancelSubagent(h CancelSubagentHandler) {
	s.cancelSubagent = h
}

// Writer returns the thread-safe writer for push events.
func (s *Server) Writer() *Writer {
	return s.writer
}

// Context returns the server's context, canceled on shutdown.
func (s *Server) Context() context.Context {
	return s.ctx
}

// Serve runs the serve-mode loop. It blocks until shutdown or EOF.
func (s *Server) Serve() {
	defer s.cancel()

	for s.scanner.Scan() {
		var req adapterprotocol.Request
		if err := ndjson.Decode(s.scanner.Bytes(), &req); err != nil {
			log.Printf("malformed request: %v", err)
			continue
		}

		s.dispatch(req)

		// exit after processing shutdown
		if s.ctx.Err() != nil {
			return
		}
	}

	if err := s.scanner.Err(); err != nil {
		log.Printf("scanner error: %v", err)
	}
}

func (s *Server) dispatch(req adapterprotocol.Request) {
	switch req.Method {
	case adapterprotocol.MethodFindSession:
		var p adapterprotocol.FindSessionParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.findSession == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "find-session not registered"},
			})
			return
		}
		result, err := s.findSession(s.ctx, p)
		if err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})

	case adapterprotocol.MethodReadFromOffset:
		var p adapterprotocol.ReadFromOffsetParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.readFromOffset == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "read-from-offset not registered"},
			})
			return
		}
		result, err := s.readFromOffset(s.ctx, p)
		if err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})

	case adapterprotocol.MethodEndSession:
		var p adapterprotocol.EndSessionParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.endSession == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "end-session not registered"},
			})
			return
		}
		if err := s.endSession(s.ctx, p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: nil})

	case adapterprotocol.MethodSpawnSubagent:
		var p adapterprotocol.SpawnSubagentParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.spawnSubagent == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "spawn-subagent not registered"},
			})
			return
		}
		result, err := s.spawnSubagent(s.ctx, p)
		if err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})

	case adapterprotocol.MethodSubagentStatus:
		var p adapterprotocol.SubagentStatusParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.subagentStatus == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "subagent-status not registered"},
			})
			return
		}
		result, err := s.subagentStatus(s.ctx, p)
		if err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})

	case adapterprotocol.MethodCancelSubagent:
		var p adapterprotocol.CancelSubagentParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
			})
			return
		}
		if s.cancelSubagent == nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: "cancel-subagent not registered"},
			})
			return
		}
		result, err := s.cancelSubagent(s.ctx, p)
		if err != nil {
			s.writer.WriteResponse(adapterprotocol.Response{
				ID:    req.ID,
				Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
			})
			return
		}
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})

	case adapterprotocol.MethodShutdown:
		s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: nil})
		s.cancel()

	default:
		s.writer.WriteResponse(adapterprotocol.Response{
			ID: req.ID,
			Error: &adapterprotocol.RPCError{
				Code:    adapterprotocol.ErrCodeMethodNotFound,
				Message: fmt.Sprintf("unknown method: %s", req.Method),
			},
		})
	}
}

// --- Writer (thread-safe stdout) ---

// Writer provides thread-safe JSON writing to stdout.
// Both serve-mode responses and push events share the same pipe.
type Writer struct {
	enc *ndjson.Encoder
	mu  sync.Mutex
}

// NewWriter creates a thread-safe Writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{enc: ndjson.NewEncoder(w)}
}

// WriteResponse writes a serve-mode response.
func (w *Writer) WriteResponse(resp adapterprotocol.Response) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(resp); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}

// PushEvent writes an unsolicited event (e.g., file_watcher entries push).
func (w *Writer) PushEvent(evt adapterprotocol.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(evt); err != nil {
		log.Printf("failed to write event: %v", err)
	}
}

// --- SessionStore (typed per-session state) ---

// SessionStore is a typed, concurrent-safe store for per-session state.
// Adapters use it to cache file handles, byte offsets, and other state
// keyed by agent_id.
type SessionStore[T any] struct {
	m sync.Map
}

// NewSessionStore creates a new SessionStore.
func NewSessionStore[T any]() *SessionStore[T] {
	return &SessionStore[T]{}
}

// Set stores state for an agent.
func (s *SessionStore[T]) Set(agentID string, state T) {
	s.m.Store(agentID, state)
}

// Get retrieves state for an agent. Returns false if not found.
func (s *SessionStore[T]) Get(agentID string) (T, bool) {
	v, ok := s.m.Load(agentID)
	if !ok {
		var zero T
		return zero, false
	}
	return v.(T), true
}

// Delete removes and returns state for an agent.
func (s *SessionStore[T]) Delete(agentID string) (T, bool) {
	v, ok := s.m.LoadAndDelete(agentID)
	if !ok {
		var zero T
		return zero, false
	}
	return v.(T), true
}

// ErrSessionNotFound is returned when an agent_id has no stored session state.
var ErrSessionNotFound = fmt.Errorf("session not found")
