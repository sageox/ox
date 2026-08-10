// ox-adapter-pi is the external adapter binary for Pi coding agent sessions.
//
// Pi stores sessions as JSONL in ~/.pi/agent/sessions/--<path>--/<timestamp>_<uuid>.jsonl.
// Hooks are installed via AGENTS.md markers in the project root.
package main

import (
	"fmt"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "pi"
	adapterDisplay = "Pi"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(adapterConfig)
}

// adapterConfig is the one-shot/serve dispatch table for this binary. It is a
// package-level var (rather than inlined in main) so tests can drive it
// through adapterruntime.RunWithArgs the same way the real CLI dispatch
// does — exercising the actual wiring, not just the handler functions in
// isolation. See TestReadFromOffset_WiredInOneShotMode.
var adapterConfig = adapterruntime.Config{
	Info:           handleInfo,
	Detect:         handleDetect,
	InstallHooks:   handleInstallHooks,
	CheckHooks:     handleCheckHooks,
	UninstallHooks: handleUninstallHooks,
	FindSession:    handleFindSession,
	Read:           handleRead,
	ReadMetadata:   handleReadMetadata,
	ReadFromOffset: handleReadFromOffset,
	ImportSession:  handleImportSession,
	Diagnose:       handleDiagnose,
	Serve:          handleServe,
}

// handleReadFromOffset is the one-shot mode handler for read-from-offset. The
// serve-mode handler lives in serve.go (srv.OnReadFromOffset) — one-shot and
// serve mode are separate registrations. This one was missing, so every
// one-shot invocation returned "read-from-offset not implemented" and
// silently dropped every turn written since the last persisted offset (e.g.
// the daemon's catch-up read on restart,
// internal/daemon/agentwork/session_watcher.go).
func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	if p.SessionFile == "" {
		return nil, fmt.Errorf("--session-file is required")
	}
	entries, newOffset, err := readPiFromOffset(p.SessionFile, p.Offset)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: newOffset}, nil
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
	return &adapterprotocol.InfoResponse{
		ProtocolVersion: adapterprotocol.ProtocolVersion,
		Name:            adapterName,
		DisplayName:     adapterDisplay,
		Version:         adapterVersion,
		Type:            adapterprotocol.TypeSession,
		Capabilities: []string{
			adapterprotocol.CapSessionReader,
			adapterprotocol.CapHookInstaller,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapFileWatcher,
			adapterprotocol.CapSessionImporter,
			adapterprotocol.CapServeMode,
		},
		HookEnvValues: []string{"pi"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findPiSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	var offset int64
	if info, err := os.Stat(sessionFile); err == nil {
		offset = info.Size()
	}
	return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
}

func handleImportSession(p adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	path, err := findPiSession(p.RepoRoot, "", "", p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	entries, err := readPiFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	meta := extractPiMetadata(path)

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}
