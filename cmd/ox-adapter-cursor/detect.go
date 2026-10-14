package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

var (
	cursorUserHomeDir = os.UserHomeDir
	cursorLookPath    = exec.LookPath
	cursorAppPaths    = func(home string) []string {
		return []string{filepath.Join(home, "Applications", "Cursor.app"), "/Applications/Cursor.app"}
	}
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == adapterName {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=cursor"}, nil
	}

	home, err := cursorUserHomeDir()
	if err == nil {
		if info, statErr := os.Stat(filepath.Join(home, ".cursor", "projects")); statErr == nil && info.IsDir() {
			return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.cursor/projects/"}, nil
		}
		for _, app := range cursorAppPaths(home) {
			if info, statErr := os.Stat(app); statErr == nil && info.IsDir() {
				return &adapterprotocol.DetectResponse{Detected: true, Reason: "found Cursor desktop application"}, nil
			}
		}
	}

	if _, err := cursorLookPath("cursor"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "cursor binary found in PATH"}, nil
	}
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}
	return &adapterprotocol.DetectResponse{Detected: false, Reason: "Cursor desktop, CLI, and native project root not found"}, nil
}
