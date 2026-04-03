// ox-adapter-opencode is the external adapter binary for OpenCode sessions.
//
// OpenCode stores sessions in a SQLite database at ~/.local/share/opencode/opencode.db.
// Hooks are installed as TypeScript plugins at .opencode/plugin/ox-prime.ts.
//
// Session reading from SQLite is not yet implemented (tracked as a future enhancement).
// This adapter currently provides detection, hook installation, and diagnostics.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "opencode"
	adapterDisplay = "OpenCode"
	adapterVersion = "0.1.0"

	openCodePluginFileName = "ox-prime.ts"
	openCodeProjectPath    = ".opencode/plugin"
	openCodeUserPath       = ".config/opencode/plugin"
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		Diagnose:       handleDiagnose,
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
			adapterprotocol.CapHookInstaller,
		},
		HookEnvValues: []string{"opencode"},
	}, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "opencode" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=opencode"}, nil
	}

	// check for .opencode directory in cwd
	if info, err := os.Stat(".opencode"); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found .opencode/ in current directory"}, nil
	}

	// check for opencode data directory
	dataDir := openCodeDataDir()
	if dataDir != "" {
		if _, err := os.Stat(filepath.Join(dataDir, "opencode.db")); err == nil {
			return &adapterprotocol.DetectResponse{Detected: true, Reason: "found opencode.db"}, nil
		}
	}

	if _, err := exec.LookPath("opencode"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "opencode binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: ".opencode/ not found and opencode not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	// check if opencode is installed
	if _, err := exec.LookPath("opencode"); err != nil {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "opencode:not-installed",
			Severity: "warning",
			Title:    "OpenCode CLI not detected",
			Detail:   "opencode binary not found in PATH.",
		})
	}

	// check hooks
	if p.RepoRoot != "" {
		pluginPath := filepath.Join(p.RepoRoot, openCodeProjectPath, openCodePluginFileName)
		if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "opencode:hooks-missing",
				Severity: "warning",
				Title:    "OpenCode hooks not installed",
				Detail:   fmt.Sprintf("%s not found.", pluginPath),
				Fix:      "Run: ox-adapter-opencode install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

// openCodeDataDir returns the default OpenCode data directory.
func openCodeDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, ".local", "share", "opencode")
	case "linux":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "opencode")
		}
		return filepath.Join(home, ".local", "share", "opencode")
	default:
		return filepath.Join(home, ".local", "share", "opencode")
	}
}
