// ox-adapter-goose is the external adapter binary for Goose sessions.
//
// Goose (https://github.com/block/goose) stores sessions in a SQLite database at
// ~/.local/share/goose/sessions/sessions.db. Session reading queries SQLite
// directly using modernc.org/sqlite (pure Go, no CGo).
//
// Hooks are installed as an Open Plugins plugin directory under
// .agents/plugins/sageox/ — see hooks.go. That is unlike every other ox adapter,
// which edits a single settings file.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "goose"
	adapterDisplay = "Goose"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		FindSession:    handleFindSession,
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		ReadFromOffset: handleReadFromOffset,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		Diagnose:       handleDiagnose,
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
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapSessionImporter,
			adapterprotocol.CapCapturePrior,
			adapterprotocol.CapServeMode,
		},
		// No CapFileWatcher: the session handle is virtual ("goose:<id>"), so
		// there is no path for fsnotify to watch. Recording is hook-driven.
		HookEnvValues: []string{"goose"},
		ServeMode:     true,
	}, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "goose" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=goose"}, nil
	}
	if os.Getenv("GOOSE") == "1" || os.Getenv("GOOSE_AGENT") == "1" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "GOOSE env var set"}, nil
	}

	if _, err := os.Stat(gooseDBPath()); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found sessions.db"}, nil
	}

	if dir := gooseConfigDir(); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return &adapterprotocol.DetectResponse{Detected: true, Reason: "found " + dir}, nil
		}
	}

	if _, err := exec.LookPath("goose"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "goose binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "goose config not found and goose not in PATH"}, nil
}

func handleImportSession(p adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	var id string
	err = db.QueryRow("SELECT id FROM sessions WHERE id = ? LIMIT 1", p.SessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %q not found", p.SessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("querying session: %w", err)
	}

	entries, _, err := readMessages(db, p.SessionID, 0)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	meta, metaErr := readMetadata(db, p.SessionID)
	if metaErr != nil {
		slog.Warn("reading goose session metadata", "session_id", p.SessionID, "err", metaErr)
	}

	return &adapterprotocol.ImportSessionResult{
		Metadata: meta,
		Entries:  entries,
	}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	if _, err := exec.LookPath("goose"); err != nil {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "not-installed",
			Severity: "warning",
			Title:    "Goose CLI not detected",
			Detail:   "goose binary not found in PATH. The Goose desktop app installs its own copy, so this can be a false alarm.",
		})
	}

	dbPath := gooseDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "no-database",
			Severity: "info",
			Title:    "Goose session database not found",
			Detail:   fmt.Sprintf("%s not found — session reading unavailable until Goose is used.", dbPath),
		})
	}

	if p.RepoRoot != "" {
		hooksPath, pathErr := hooksFilePath(p.RepoRoot, "project")
		if pathErr != nil {
			return nil, pathErr
		}
		manifestPath, pathErr := manifestFilePath(p.RepoRoot, "project")
		if pathErr != nil {
			return nil, pathErr
		}

		if _, err := os.Stat(hooksPath); os.IsNotExist(err) {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "hooks-missing",
				Severity: "warning",
				Title:    "Goose hooks not installed",
				Detail:   fmt.Sprintf("%s not found.", hooksPath),
				Fix:      "ox-adapter-goose install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
			})
		} else if err == nil {
			// Goose plugin discovery keys on the plugin directory; a hooks.json
			// with no sibling plugin.json is silently ignored, which looks
			// identical to "hooks installed" from the filesystem alone.
			if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "manifest-missing",
					Severity: "warning",
					Title:    "Goose plugin manifest missing",
					Detail:   "hooks.json exists but plugin.json does not. Goose will not load the plugin, so the hooks never fire.",
					Fix:      "ox-adapter-goose install-hooks --repo-root " + p.RepoRoot + " --scope project",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

// gooseConfigDir returns Goose's XDG config directory (~/.config/goose).
func gooseConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "goose")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "goose")
}

// gooseDataDir returns Goose's XDG data directory (~/.local/share/goose).
// Goose is XDG-compliant on every platform, including macOS — it does not use
// ~/Library/Application Support.
func gooseDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "goose")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "goose")
}

func gooseDBPath() string {
	dataDir := gooseDataDir()
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "sessions", "sessions.db")
}
