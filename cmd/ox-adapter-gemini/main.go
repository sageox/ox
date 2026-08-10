// ox-adapter-gemini is the external adapter binary for Gemini CLI sessions.
//
// Gemini writes monolithic JSON session files (not JSONL). The entire file
// is rewritten on each turn. This adapter re-reads the JSON and uses entry
// count as the offset (not byte position).
package main

import (
	"fmt"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "gemini"
	adapterDisplay = "Gemini CLI"
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
	Diagnose:       handleDiagnose,
	ReadFromOffset: handleReadFromOffset,
	ImportSession:  handleImportSession,
	Serve:          handleServe,
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
		HookEnvValues: []string{"gemini"},
		ServeMode:     true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findGeminiSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	return &adapterprotocol.FindSessionResult{SessionFile: sessionFile}, nil
}

func handleImportSession(p adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	tmpDir := home + "/.gemini/tmp"
	path, err := findGeminiBySessionID(tmpDir, p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session file: %w", err)
	}

	entries, meta, err := parseGeminiSession(data)
	if err != nil {
		return nil, fmt.Errorf("parsing session: %w", err)
	}

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}
