package main

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	// env var is a stronger signal — works in containers
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return &adapterprotocol.DetectResponse{
			Detected: true,
			Reason:   "CLAUDE_CODE_ENTRYPOINT environment variable set",
		}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "cannot determine home directory",
		}, nil
	}

	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.claude directory not found",
		}, nil
	}

	projectsDir := filepath.Join(claudeDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.claude/projects/ directory not found",
		}, nil
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.claude/projects/ is empty",
		}, nil
	}

	return &adapterprotocol.DetectResponse{
		Detected: true,
		Reason:   "found ~/.claude/projects/ with session data",
	}, nil
}
