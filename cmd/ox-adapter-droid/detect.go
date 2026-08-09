package main

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "cannot determine home directory",
		}, nil
	}

	factoryDir := filepath.Join(home, ".factory")
	if _, err := os.Stat(factoryDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.factory directory not found",
		}, nil
	}

	// shares droidSessionsDir with session discovery (session.go) so the two
	// code paths cannot drift onto different directory names again.
	sessionsDir, err := droidSessionsDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "cannot determine home directory",
		}, nil
	}
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.factory/sessions/ directory not found",
		}, nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.factory/sessions/ is empty",
		}, nil
	}

	return &adapterprotocol.DetectResponse{
		Detected: true,
		Reason:   "found ~/.factory/sessions/ with session data",
	}, nil
}
