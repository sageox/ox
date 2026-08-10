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
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		ReadFromOffset: handleReadFromOffset,
		ImportSession:  handleImportSession,
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
//
// sessionTS is re-resolved from the file up to the requested offset (same
// helper serve.go uses after find-session) so a cold one-shot call — no
// prior FindSession in this process — still tags entries with the correct
// session timestamp instead of the zero value.
func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	if p.SessionFile == "" {
		return nil, fmt.Errorf("--session-file is required")
	}
	sessionTS := resolveLatestSessionTS(p.SessionFile, p.Offset)
	entries, newOffset, err := readAiderFromOffsetWithTS(p.SessionFile, p.Offset, sessionTS)
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
		return nil, fmt.Errorf("repo-root is required for aider import (was empty)")
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
