package doctor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/markers"
)

// Marker file names (in .sageox/ directory)
const (
	NeedsDoctorMarker = ".needs-doctor"
	// NeedsDoctorAgentMarker aliases the single source of truth in
	// internal/markers so the daemon's agentwork producer (which cannot import
	// doctor) shares the exact filename — divergence becomes impossible.
	NeedsDoctorAgentMarker = markers.NeedsDoctorAgent
	sageoxDir              = ".sageox"
)

// NeedsDoctorHuman checks if .sageox/.needs-doctor exists.
// Returns false if the file doesn't exist or .sageox/ directory doesn't exist.
func NeedsDoctorHuman(gitRoot string) bool {
	path := filepath.Join(gitRoot, sageoxDir, NeedsDoctorMarker)
	_, err := os.Stat(path)
	return err == nil
}

// NeedsDoctorAgent checks if .sageox/.needs-doctor-agent exists.
// Returns false if the file doesn't exist or .sageox/ directory doesn't exist.
func NeedsDoctorAgent(gitRoot string) bool {
	path := filepath.Join(gitRoot, sageoxDir, NeedsDoctorAgentMarker)
	_, err := os.Stat(path)
	return err == nil
}

// SetNeedsDoctorHuman creates .sageox/.needs-doctor marker file.
// Creates an empty file if it doesn't exist, updates mtime if it does.
// Returns error if .sageox/ directory doesn't exist.
func SetNeedsDoctorHuman(gitRoot string) error {
	return touchMarker(gitRoot, NeedsDoctorMarker)
}

// SetNeedsDoctorAgent creates .sageox/.needs-doctor-agent marker file.
// Creates an empty file if it doesn't exist, updates mtime if it does.
// Returns error if .sageox/ directory doesn't exist.
func SetNeedsDoctorAgent(gitRoot string) error {
	return touchMarker(gitRoot, NeedsDoctorAgentMarker)
}

// ClearNeedsDoctorHuman removes .sageox/.needs-doctor marker file.
// Idempotent: returns nil if file doesn't exist.
func ClearNeedsDoctorHuman(gitRoot string) error {
	return clearMarker(gitRoot, NeedsDoctorMarker)
}

// ClearNeedsDoctorAgent removes .sageox/.needs-doctor-agent marker file.
// Idempotent: returns nil if file doesn't exist.
func ClearNeedsDoctorAgent(gitRoot string) error {
	return clearMarker(gitRoot, NeedsDoctorAgentMarker)
}

// GetDoctorNeeds returns which doctor types are needed.
// Returns (human, agent) booleans indicating which markers exist.
func GetDoctorNeeds(gitRoot string) (human, agent bool) {
	return NeedsDoctorHuman(gitRoot), NeedsDoctorAgent(gitRoot)
}

// touchMarker creates or updates a marker file in .sageox/ directory.
// Creates an empty file if it doesn't exist, updates mtime if it does.
func touchMarker(gitRoot, markerName string) error {
	sageoxPath := filepath.Join(gitRoot, sageoxDir)
	if _, err := os.Stat(sageoxPath); os.IsNotExist(err) {
		return errors.New(".sageox directory does not exist")
	}

	markerPath := filepath.Join(sageoxPath, markerName)

	// check if file exists
	if _, err := os.Stat(markerPath); err == nil {
		// file exists, update mtime
		now := time.Now()
		return os.Chtimes(markerPath, now, now)
	}

	// create empty file
	f, err := os.Create(markerPath)
	if err != nil {
		return err
	}
	return f.Close()
}

// clearMarker removes a marker file from .sageox/ directory.
// Idempotent: returns nil if file doesn't exist.
func clearMarker(gitRoot, markerName string) error {
	markerPath := filepath.Join(gitRoot, sageoxDir, markerName)
	err := os.Remove(markerPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SessionRecoveryInfo contains metadata needed for automated session recovery.
// Written to .sageox/.session-recovery.json when session file discovery fails at stop time.
type SessionRecoveryInfo struct {
	AgentID       string    `json:"agent_id"`
	AdapterName   string    `json:"adapter_name"`
	StartedAt     time.Time `json:"started_at"`
	WorkspacePath string    `json:"workspace_path"`
	FailedAt      time.Time `json:"failed_at"`
	Error         string    `json:"error,omitempty"`
}

const sessionRecoveryFile = ".session-recovery.json"

// SetSessionRecoveryInfo writes recovery metadata so ox doctor can drive automated recovery.
func SetSessionRecoveryInfo(gitRoot string, info SessionRecoveryInfo) error {
	sageoxPath := filepath.Join(gitRoot, sageoxDir)
	if _, err := os.Stat(sageoxPath); os.IsNotExist(err) {
		return errors.New(".sageox directory does not exist")
	}

	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sageoxPath, sessionRecoveryFile), data, 0o644)
}

// GetSessionRecoveryInfo reads recovery metadata if it exists.
// Returns nil if no recovery is pending.
func GetSessionRecoveryInfo(gitRoot string) *SessionRecoveryInfo {
	data, err := os.ReadFile(filepath.Join(gitRoot, sageoxDir, sessionRecoveryFile))
	if err != nil {
		return nil
	}
	var info SessionRecoveryInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil
	}
	return &info
}

// ClearSessionRecoveryInfo removes the recovery metadata file.
func ClearSessionRecoveryInfo(gitRoot string) error {
	path := filepath.Join(gitRoot, sageoxDir, sessionRecoveryFile)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
