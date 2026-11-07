// detect.go handles OMP detection and diagnostics.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "omp" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=omp"}, nil
	}
	if _, err := exec.LookPath("omp"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "omp binary found in PATH"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}
	configDir := os.Getenv("PI_CONFIG_DIR")
	if configDir == "" {
		configDir = ".omp"
	}
	root := filepath.Join(home, configDir)
	if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found " + root}, nil
	}
	return &adapterprotocol.DetectResponse{Detected: false, Reason: "OMP config directory not found and omp not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	if detected, _ := handleDetect(); !detected.Detected {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "omp:not-installed",
			Severity: "warning",
			Title:    "OMP coding agent not detected",
			Detail:   detected.Reason,
		})
	}

	if p.RepoRoot != "" {
		agentsPath := resolveOMPAgentsMDPath(p.RepoRoot)
		data, err := os.ReadFile(agentsPath)
		switch {
		case err == nil && strings.Contains(string(data), ompPrimeMarkerStart):
		case err == nil, errors.Is(err, os.ErrNotExist):
			detail := ".omp/AGENTS.md does not contain the OMP-specific ox agent prime marker."
			if err != nil {
				detail = ".omp/AGENTS.md not found at " + agentsPath + "."
			}
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "omp:hooks-missing",
				Severity: "warning",
				Title:    "OMP prime instructions not installed",
				Detail:   detail,
				Fix:      "ox integrate install --omp",
				FixArgv:  []string{"ox", "integrate", "install", "--omp"},
				FixSafe:  true,
			})
		default:
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "omp:agents-md-unreadable",
				Severity: "error",
				Title:    ".omp/AGENTS.md could not be read",
				Detail:   fmt.Sprintf("%s: %v - check file permissions and ownership.", agentsPath, err),
				FixSafe:  false,
			})
		}
	}

	if detail := checkTranscriptFormat(p.RepoRoot); detail != "" {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "omp:format-unsupported",
			Severity: "error",
			Title:    "OMP transcript format is not supported",
			Detail:   detail,
		})
	}
	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

func checkTranscriptFormat(repoRoot string) string {
	path, err := findOMPSession(repoRoot, "", "", "")
	if err != nil {
		return ""
	}
	meta := extractOMPMetadata(path)
	if meta == nil || meta.AgentVersion == "" {
		return ""
	}

	var version int
	if _, err := fmt.Sscanf(meta.AgentVersion, "omp-session-v%d", &version); err != nil || ompSupportedVersions[version] {
		return ""
	}
	return fmt.Sprintf(
		"%s is session format version %d; this adapter reads version 3. Sessions will record as empty until the reader is updated.",
		path, version)
}
