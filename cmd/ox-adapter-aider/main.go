// ox-adapter-aider is the external adapter binary for Aider coding agent sessions.
//
// Aider stores sessions as markdown in .aider.chat.history.md in the project root.
// Hooks are installed via .aider.conf.yml pointing to CONVENTIONS.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "aider"
	adapterDisplay = "Aider"
	adapterVersion = "0.1.0"
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
			adapterprotocol.CapSessionImporter,
			adapterprotocol.CapServeMode,
		},
		HookEnvValues: []string{"aider"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findAiderSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
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
		return nil, fmt.Errorf("--session-id is required (use the session start timestamp from '# aider chat started at <timestamp>')")
	}

	repoRoot := p.RepoRoot
	if repoRoot == "" {
		repoRoot, _ = os.Getwd()
	}

	historyFile := filepath.Join(repoRoot, aiderHistoryFile)
	entries, err := parseAiderSessionByTimestamp(historyFile, p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("importing aider session: %w", err)
	}

	return &adapterprotocol.ImportSessionResult{
		Entries: entries,
	}, nil
}
