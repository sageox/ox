// ox-adapter-droid is the external adapter binary for Factory Droid sessions.
//
// Factory Droid stores sessions as JSONL in ~/.factory/sessions/<project-slug>/
// (confirmed against a real droid 0.126.0 install).
// Each line has a top-level "type" field: "session_start" (first line, metadata)
// or "message" (all subsequent lines). Message entries wrap a nested "message"
// object with "role" (user/assistant) and "content" (array of content blocks).
// Content block types: text, thinking, tool_use, tool_result, image.
//
// Hooks use a structured JSON format in ~/.factory/settings.json:
//
//	{"hooks": {"EventName": [{"hooks": [{"type": "command", "command": "..."}]}]}}
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
	Read:           handleRead,
	ReadMetadata:   handleReadMetadata,
	Diagnose:       handleDiagnose,
	InstallRules:   handleInstallRules,
	CheckRules:     handleCheckRules,
	UninstallRules: handleUninstallRules,
	FindSession:    handleFindSession,
	ReadFromOffset: handleReadFromOffset,
	ImportSession:  handleImportSession,
	Serve:          handleServe,
}

// handleReadFromOffset is the one-shot mode handler for read-from-offset. The
// serve-mode handler lives in serve.go (srv.OnReadFromOffset) — one-shot and
// serve mode are separate registrations. This one was missing, so every
// one-shot invocation (e.g. the daemon's catch-up read on restart) returned
// "read-from-offset not implemented" and silently dropped every turn written
// since the last persisted offset.
func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	if p.SessionFile == "" {
		return nil, fmt.Errorf("--session-file is required")
	}
	return adapterruntime.ReadFromOffsetJSONL(p, parseLine)
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
