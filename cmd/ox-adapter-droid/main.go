// ox-adapter-droid is the external adapter binary for Factory Droid sessions.
//
// Factory Droid stores sessions as JSONL in ~/.factory/projects/<project-slug>/.
// Each line has a top-level "type" field: "session_start" (first line, metadata)
// or "message" (all subsequent lines). Message entries wrap a nested "message"
// object with "role" (user/assistant) and "content" (array of content blocks).
// Content block types: text, thinking, tool_use, tool_result, image.
//
// Hooks use a structured JSON format in ~/.factory/settings.json:
//   {"hooks": {"EventName": [{"hooks": [{"type": "command", "command": "..."}]}]}}
//
// Format reference: https://docs.factory.ai
package main

import (
	"fmt"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "droid"
	adapterDisplay = "Factory Droid"
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
		FindSession:    handleFindSession,
		ImportSession:  handleImportSession,
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
		HookEnvValues: []string{"droid"},
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
