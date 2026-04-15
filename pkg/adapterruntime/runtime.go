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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/ndjson"
)

// statTimeout is the maximum time to wait for os.Stat on .sageox/ directories.
// NFS-mounted repos can hang indefinitely on stat calls; 500ms is generous for
// local and most network filesystems while preventing hook-path hangs.
const statTimeout = 500 * time.Millisecond

// ValidateRepoRoot checks that a repo root path is non-empty, absolute, and
// contains a .sageox/ directory. All adapters using this SDK get this
// validation for free when called via the find-session dispatch path.
// The .sageox stat uses a 500ms timeout to prevent NFS hangs in hook paths.
func ValidateRepoRoot(root string) error {
	if root == "" || root == "." {
		return fmt.Errorf("repo-root must be absolute, got %q", root)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("repo-root %q is not absolute", root)
	}
	info, err := statWithTimeout(filepath.Join(root, ".sageox"), statTimeout)
	if err != nil {
		return fmt.Errorf("repo-root %q has no .sageox/ directory: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo-root %q has no .sageox/ directory", root)
	}
	return nil
}

// statWithTimeout runs os.Stat in a goroutine with a deadline.
// Returns the FileInfo on success, or an error on timeout / stat failure.
func statWithTimeout(path string, timeout time.Duration) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := os.Stat(path)
		ch <- result{info, err}
	}()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("stat %q timed out after %v (possible NFS hang)", path, timeout)
	}
}

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
	FindSession    func(adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error)
	ReadFromOffset func(adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error)
	ImportSession  func(adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error)
	CapturePrior   func(adapterprotocol.CapturePriorParams) (*adapterprotocol.CapturePriorResult, error)
	InstallRules      func(adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error)
	CheckRules        func(adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error)
	UninstallRules    func(adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error)
	InstallCommands   func(adapterprotocol.CommandsParams) (*adapterprotocol.InstallCommandsResponse, error)
	CheckCommands     func(adapterprotocol.CommandsParams) (*adapterprotocol.CheckCommandsResponse, error)
	UninstallCommands func(adapterprotocol.CommandsParams) (*adapterprotocol.UninstallCommandsResponse, error)
	Serve             func(*Server)
}

// Run dispatches to the appropriate handler based on os.Args[1].
// It reads the subcommand from the CLI arguments, calls the handler,
// serializes the result as compact JSON to stdout, and exits.
// For --serve, it enters serve mode (blocking).
func Run(cfg Config) {
	if err := RunWithArgs(cfg, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// RunWithArgs is like Run but accepts explicit args and IO for testing.
// Returns an error instead of calling os.Exit, making it safe for tests and embedding.
func RunWithArgs(cfg Config, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		printUsage(cfg, os.Stderr)
		return nil
	}

	cmd := args[0]
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)

	switch cmd {
	case "info":
		return runOneShot(enc, func() (any, error) {
			if cfg.Info == nil {
				return nil, fmt.Errorf("info not implemented")
			}
			return cfg.Info()
		})

	case "detect":
		return runOneShot(enc, func() (any, error) {
			if cfg.Detect == nil {
				return nil, fmt.Errorf("detect not implemented")
			}
			return cfg.Detect()
		})

	case "install-hooks":
		p := parseHookParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.InstallHooks == nil {
				return nil, fmt.Errorf("install-hooks not implemented")
			}
			return cfg.InstallHooks(p)
		})

	case "check-hooks":
		p := parseHookParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.CheckHooks == nil {
				return nil, fmt.Errorf("check-hooks not implemented")
			}
			return cfg.CheckHooks(p)
		})

	case "uninstall-hooks":
		p := parseHookParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.UninstallHooks == nil {
				return nil, fmt.Errorf("uninstall-hooks not implemented")
			}
			return cfg.UninstallHooks(p)
		})

	case "read":
		p := parseReadParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.Read == nil {
				return nil, fmt.Errorf("read not implemented")
			}
			return cfg.Read(p)
		})

	case "read-metadata":
		p := parseReadParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.ReadMetadata == nil {
				return nil, fmt.Errorf("read-metadata not implemented")
			}
			return cfg.ReadMetadata(p)
		})

	case "diagnose":
		p := parseDiagnoseParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.Diagnose == nil {
				return nil, fmt.Errorf("diagnose not implemented")
			}
			return cfg.Diagnose(p)
		})

	case "find-session":
		p := parseFindSessionParams(args[1:])
		if err := ValidateRepoRoot(p.RepoRoot); err != nil {
			return runOneShot(enc, func() (any, error) {
				return nil, fmt.Errorf("invalid repo-root: %w", err)
			})
		}
		return runOneShot(enc, func() (any, error) {
			if cfg.FindSession == nil {
				return nil, fmt.Errorf("find-session not implemented")
			}
			return cfg.FindSession(p)
		})

	case "read-from-offset":
		p := parseReadFromOffsetParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.ReadFromOffset == nil {
				return nil, fmt.Errorf("read-from-offset not implemented")
			}
			return cfg.ReadFromOffset(p)
		})

	case "import-session":
		p := parseImportSessionParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.ImportSession == nil {
				return nil, fmt.Errorf("import-session not implemented")
			}
			return cfg.ImportSession(p)
		})

	case "capture-prior":
		p := parseCapturePriorParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.CapturePrior == nil {
				return nil, fmt.Errorf("capture-prior not implemented")
			}
			return cfg.CapturePrior(p)
		})

	case "install-rules":
		p := parseRulesParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.InstallRules == nil {
				return nil, fmt.Errorf("install-rules not implemented")
			}
			return cfg.InstallRules(p)
		})

	case "check-rules":
		p := parseRulesParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.CheckRules == nil {
				return nil, fmt.Errorf("check-rules not implemented")
			}
			return cfg.CheckRules(p)
		})

	case "uninstall-rules":
		p := parseRulesParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.UninstallRules == nil {
				return nil, fmt.Errorf("uninstall-rules not implemented")
			}
			return cfg.UninstallRules(p)
		})

	case "install-commands":
		p := parseCommandsParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.InstallCommands == nil {
				return nil, fmt.Errorf("install-commands not implemented")
			}
			return cfg.InstallCommands(p)
		})

	case "check-commands":
		p := parseCommandsParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.CheckCommands == nil {
				return nil, fmt.Errorf("check-commands not implemented")
			}
			return cfg.CheckCommands(p)
		})

	case "uninstall-commands":
		p := parseCommandsParams(args[1:])
		return runOneShot(enc, func() (any, error) {
			if cfg.UninstallCommands == nil {
				return nil, fmt.Errorf("uninstall-commands not implemented")
			}
			return cfg.UninstallCommands(p)
		})

	case "--serve":
		if cfg.Serve == nil {
			return fmt.Errorf("serve mode not implemented")
		}
		srv := NewServer(stdin, stdout)
		cfg.Serve(srv)
		return nil

	default:
		return fmt.Errorf("unknown subcommand: %s", cmd)
	}
}

// printUsage prints a human-friendly message when someone runs the adapter
// binary directly without arguments.
func printUsage(cfg Config, w io.Writer) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	name := "ox-adapter"
	displayName := ""
	version := ""
	protoVersion := 0
	if cfg.Info != nil {
		if info, err := cfg.Info(); err == nil {
			name = "ox-adapter-" + info.Name
			displayName = info.DisplayName
			version = info.Version
			protoVersion = info.ProtocolVersion
		}
	}

	header := name
	if displayName != "" {
		header = fmt.Sprintf("%s — %s adapter for ox", name, displayName)
	}

	p("%s\n", header)
	if version != "" || protoVersion > 0 {
		p("  adapter %s · protocol v%d\n", version, protoVersion)
	}
	p("\nThis is an adapter plugin for ox. It is not meant to be run directly —\n")
	p("ox discovers and invokes it automatically.\n")
	p("\nSubcommands:\n")
	p("  info, detect, install-hooks, check-hooks, uninstall-hooks,\n")
	p("  install-rules, check-rules, uninstall-rules,\n")
	p("  install-commands, check-commands, uninstall-commands,\n")
	p("  read, read-metadata, diagnose, find-session, read-from-offset,\n")
	p("  import-session, capture-prior, --serve\n")
	p("\nTo get started with ox:\n")
	p("  brew install sageox/tap/ox\n")
	p("  ox init\n")
	p("  ox doctor\n")
	p("\nLearn more: https://sageox.ai/docs\n")
}

func runOneShot(enc *json.Encoder, fn func() (any, error)) error {
	result, err := fn()
	if err != nil {
		errResp := map[string]string{"error": err.Error()}
		if encErr := enc.Encode(errResp); encErr != nil {
			return fmt.Errorf("failed to encode error response: %w", encErr)
		}
		return err
	}
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("failed to encode response: %w", err)
	}
	return nil
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
		case "--version":
			p.Version = args[i+1]
			i++
		}
	}
	return p
}

func parseFindSessionParams(args []string) adapterprotocol.FindSessionParams {
	p := adapterprotocol.FindSessionParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--agent-id":
			p.AgentID = args[i+1]
			i++
		case "--since":
			p.Since = args[i+1]
			i++
		case "--agent-session-id":
			p.AgentSessionID = args[i+1]
			i++
		}
	}
	return p
}

func parseReadFromOffsetParams(args []string) adapterprotocol.ReadFromOffsetParams {
	p := adapterprotocol.ReadFromOffsetParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--session-file":
			p.SessionFile = args[i+1]
			i++
		case "--offset":
			if v, err := strconv.ParseInt(args[i+1], 10, 64); err == nil {
				p.Offset = v
			}
			i++
		}
	}
	return p
}

func parseRulesParams(args []string) adapterprotocol.RulesParams {
	p := adapterprotocol.RulesParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--version":
			p.Version = args[i+1]
			i++
		}
	}
	return p
}

func parseCommandsParams(args []string) adapterprotocol.CommandsParams {
	p := adapterprotocol.CommandsParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--version":
			p.Version = args[i+1]
			i++
		}
	}
	return p
}

func parseImportSessionParams(args []string) adapterprotocol.ImportSessionParams {
	p := adapterprotocol.ImportSessionParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--session-id":
			p.SessionID = args[i+1]
			i++
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		}
	}
	return p
}

func parseCapturePriorParams(args []string) adapterprotocol.CapturePriorParams {
	p := adapterprotocol.CapturePriorParams{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--session-id":
			p.SessionID = args[i+1]
			i++
		case "--repo-root":
			p.RepoRoot = args[i+1]
			i++
		case "--agent-id":
			p.AgentID = args[i+1]
			i++
		case "--title":
			p.Title = args[i+1]
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
		// pre-validate repoRoot before dispatching to handler
		var fp adapterprotocol.FindSessionParams
		if err := json.Unmarshal(req.Params, &fp); err == nil {
			if err := ValidateRepoRoot(fp.RepoRoot); err != nil {
				s.writer.WriteResponse(adapterprotocol.Response{
					ID:    req.ID,
					Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: fmt.Sprintf("invalid repo-root: %v", err)},
				})
				return
			}
		}
		dispatchMethod(s, req, s.findSession)
	case adapterprotocol.MethodReadFromOffset:
		dispatchMethod(s, req, s.readFromOffset)
	case adapterprotocol.MethodEndSession:
		dispatchVoid(s, req, s.endSession)
	case adapterprotocol.MethodSpawnSubagent:
		dispatchMethod(s, req, s.spawnSubagent)
	case adapterprotocol.MethodSubagentStatus:
		dispatchMethod(s, req, s.subagentStatus)
	case adapterprotocol.MethodCancelSubagent:
		dispatchMethod(s, req, s.cancelSubagent)
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

// dispatchMethod handles the common pattern: unmarshal params, check handler,
// call handler, write result or error response.
func dispatchMethod[P any, R any](s *Server, req adapterprotocol.Request, handler func(context.Context, P) (*R, error)) {
	var p P
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
		})
		return
	}
	if handler == nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: req.Method + " not registered"},
		})
		return
	}
	result, err := handler(s.ctx, p)
	if err != nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
		})
		return
	}
	s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: result})
}

// dispatchVoid is like dispatchMethod but for handlers that return only an error (no result).
// needed because Go generics can't express "R = void" — EndSession returns no payload.
func dispatchVoid[P any](s *Server, req adapterprotocol.Request, handler func(context.Context, P) error) {
	var p P
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInvalidParams, Message: err.Error()},
		})
		return
	}
	if handler == nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeMethodNotFound, Message: req.Method + " not registered"},
		})
		return
	}
	if err := handler(s.ctx, p); err != nil {
		s.writer.WriteResponse(adapterprotocol.Response{
			ID:    req.ID,
			Error: &adapterprotocol.RPCError{Code: adapterprotocol.ErrCodeInternalError, Message: err.Error()},
		})
		return
	}
	s.writer.WriteResponse(adapterprotocol.Response{ID: req.ID, Result: nil})
}

// --- Writer (thread-safe stdout) ---

// Writer provides thread-safe JSON writing to stdout.
// Both serve-mode responses and push events share the same pipe.
// Writes are buffered to a bytes.Buffer and flushed as a single Write call
// to ensure atomicity on pipes (writes < PIPE_BUF are atomic on POSIX).
type Writer struct {
	w  io.Writer
	mu sync.Mutex
}

// NewWriter creates a thread-safe Writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteResponse writes a serve-mode response.
func (w *Writer) WriteResponse(resp adapterprotocol.Response) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.writeJSON(resp); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}

// PushEvent writes an unsolicited event (e.g., file_watcher entries push).
func (w *Writer) PushEvent(evt adapterprotocol.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.writeJSON(evt); err != nil {
		log.Printf("failed to write event: %v", err)
	}
}

// writeJSON serializes v as a single JSON line and writes it atomically.
// buffering to bytes.Buffer ensures the pipe sees one Write() call per message,
// which is atomic for writes < PIPE_BUF (4KB on POSIX). json.Encoder.Encode
// directly to the pipe could split a message across multiple Write() calls,
// allowing concurrent goroutines to interleave bytes mid-line.
func (w *Writer) writeJSON(v any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	_, err := w.w.Write(buf.Bytes())
	return err
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
