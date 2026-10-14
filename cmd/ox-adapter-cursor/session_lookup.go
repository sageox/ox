package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// validateCursorSource binds Cursor's native source to one workspace and one
// conversation. It intentionally has no project-wide scan or newest-file
// fallback: a missing export is pending, never another chat.
func validateCursorSource(repoRoot, conversationID, hint string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return cursorpaths.ValidateSource(home, repoRoot, conversationID, hint)
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	if p.RepoRoot == "" {
		return nil, fmt.Errorf("repo root is required")
	}
	if p.AgentSessionID == "" {
		return nil, fmt.Errorf("agent session ID is required")
	}

	source, err := validateCursorSource(p.RepoRoot, p.AgentSessionID, "")
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session not found: Cursor transcript is pending")
	}
	if err != nil {
		return nil, fmt.Errorf("source unreadable: %w", cursorSourceError(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid source path: Cursor transcript is not a regular file")
	}
	return &adapterprotocol.FindSessionResult{SessionFile: source, Offset: 0}, nil
}

// cursorSourceError strips a native filesystem path from RPC-facing errors
// while preserving the underlying category for errors.Is.
func cursorSourceError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return &os.PathError{Op: pathErr.Op, Path: "", Err: pathErr.Err}
	}
	return err
}
