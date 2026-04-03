package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const agentsMDFileName = "AGENTS.md"

// resolveAgentsMDPath returns the path to AGENTS.md at the git root.
// Falls back to cwd if not in a git repo.
func resolveAgentsMDPath() (string, error) {
	root := findGitRoot()
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		root = cwd
	}
	return filepath.Join(root, agentsMDFileName), nil
}
