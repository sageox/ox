// ox-adapter-claude-code is the external adapter binary for Claude Code sessions.
//
// It implements the ox adapter protocol, handling session file discovery,
// transcript parsing, hook installation, and diagnostics for Claude Code.
// The daemon spawns this binary in serve mode for active sessions, or
// one-shot for info/detect/hooks/diagnose.
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "claude-code"
	adapterDisplay = "Claude Code"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		Diagnose:       handleDiagnose,
		InstallRules:      handleInstallRules,
		CheckRules:        handleCheckRules,
		UninstallRules:    handleUninstallRules,
		InstallCommands:   handleInstallCommands,
		CheckCommands:     handleCheckCommands,
		UninstallCommands: handleUninstallCommands,
		FindSession:    handleFindSession,
		ImportSession:  handleImportSession,
		CapturePrior:   handleCapturePrior,
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
			adapterprotocol.CapRulesInstaller,
			adapterprotocol.CapCommandsInstaller,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapFileWatcher,
			adapterprotocol.CapServeMode,
			adapterprotocol.CapSessionImporter,
			adapterprotocol.CapCapturePrior,
		},
		HookEnvValues: []string{"claude-code"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, offset, err := findSessionFile(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	return &adapterprotocol.FindSessionResult{
		SessionFile: sessionFile,
		Offset:      offset,
	}, nil
}

func handleImportSession(p adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	// use the existing find logic with the session ID as the native identifier
	path, _, err := findSessionFile(p.RepoRoot, "", "", p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	entries, meta, err := readSessionFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}

func handleCapturePrior(p adapterprotocol.CapturePriorParams) (*adapterprotocol.CapturePriorResult, error) {
	// find the session file (direct lookup if session ID provided, else most recent)
	path, _, err := findSessionFile(p.RepoRoot, "", "", p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	entries, meta, err := readSessionFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries found in session")
	}

	// extract session ID from the file path if not provided
	resolvedID := p.SessionID
	if resolvedID == "" {
		base := filepath.Base(path)
		resolvedID = strings.TrimSuffix(base, ".jsonl")
	}

	return &adapterprotocol.CapturePriorResult{
		Entries:   entries,
		Metadata:  meta,
		AgentType: adapterName,
		SessionID: resolvedID,
	}, nil
}
