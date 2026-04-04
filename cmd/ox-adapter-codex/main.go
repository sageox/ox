// ox-adapter-codex is the external adapter binary for OpenAI Codex CLI sessions.
//
// Codex stores sessions as JSONL in ~/.codex/sessions/YYYY/MM/DD/.
// Entry types: session_meta, turn_context, response_item, event_msg.
// Only response_item carries conversation data.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "codex"
	adapterDisplay = "OpenAI Codex CLI"
	adapterVersion = "0.1.0"
	searchDays     = 14
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		FindSession:    handleFindSession,
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		ImportSession:  handleImportSession,
		Diagnose:       handleDiagnose,
		Serve:          handleServe,
	})
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
			adapterprotocol.CapServeMode,
			adapterprotocol.CapSessionImporter,
		},
		HookEnvValues: []string{"codex"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findCodexSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
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

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")

	path, err := findCodexBySessionID(sessionsDir, p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	entries, err := readCodexFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	meta := extractCodexMetadata(path)

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}
