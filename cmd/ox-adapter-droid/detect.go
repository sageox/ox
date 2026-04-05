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

	projectsDir := filepath.Join(factoryDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.factory/projects/ directory not found",
		}, nil
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{
			Detected: false,
			Reason:   "~/.factory/projects/ is empty",
		}, nil
	}

	return &adapterprotocol.DetectResponse{
		Detected: true,
		Reason:   "found ~/.factory/projects/ with session data",
	}, nil
}
