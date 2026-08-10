// ox-adapter-amp is the external adapter binary for Sourcegraph Amp sessions.
//
// Current Amp does not persist conversation transcripts to disk on its
// own. install-hooks therefore drops two artifacts into a project:
//
//  1. an AGENTS.md "ox prime" marker block so the model knows to run
//     `ox agent prime` at session start, and
//  2. a Bun plugin at ~/.config/amp/plugins/ox-bridge.ts (embedded via
//     Go's embed package, installed user-globally so one plugin serves
//     every project) that subscribes to Amp's plugin events and writes a
//     per-thread JSONL sidecar to ~/.cache/amp/ox-sessions/<thread-id>.jsonl.
//
// The adapter discovers and tails those sidecar files via FindSession +
// Serve. For pre-2026 Amp installs that wrote ~/.amp/sessions/*.jsonl
// directly, discovery falls back to the legacy directory so existing
// users keep working without a reinstall.
package main

import (
	"fmt"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "amp"
	adapterDisplay = "Amp"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(newConfig())
}

// newConfig builds the adapter's dispatch table. Factored out of main() so
// tests can drive adapterruntime.RunWithArgs against the exact same wiring
// the daemon invokes, rather than a hand-copied stand-in that could pass
// even if main() itself were broken.
func newConfig() adapterruntime.Config {
	return adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		FindSession:    handleFindSession,
		ImportSession:  handleImportSession,
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		ReadFromOffset: handleReadFromOffset,
		Diagnose:       handleDiagnose,
		Serve:          handleServe,
	}
}

// handleReadFromOffset is the one-shot mode handler for read-from-offset.
// The serve-mode handler lives in serve.go (srv.OnReadFromOffset) and was
// already correctly wired — but the daemon's catch-up read after a restart
// (internal/daemon/agentwork/session_watcher.go: runWatcher) always goes
// through ExternalAdapter.ReadFromOffset, which calls the one-shot
// subprocess path, never the serve-mode pipe. Leaving this field nil made
// every catch-up read answer "not implemented" and silently drop turns
// written since the last persisted offset.
func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	if p.SessionFile == "" {
		return nil, fmt.Errorf("--session-file is required")
	}
	entries, newOffset, err := readAmpFromOffset(p.SessionFile, p.Offset)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadFromOffsetResult{
		Entries:   entries,
		NewOffset: newOffset,
	}, nil
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
			adapterprotocol.CapSessionImporter,
			adapterprotocol.CapHookInstaller,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapFileWatcher,
			adapterprotocol.CapServeMode,
		},
		HookEnvValues: []string{"amp"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findAmpSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
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

	path, err := findAmpSession("", "", "", p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	entries, err := readAmpFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	meta := extractAmpMetadata(path)

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}
