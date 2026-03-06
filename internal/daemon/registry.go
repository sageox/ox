package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DaemonInfo represents information about a running daemon.
type DaemonInfo struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspacePath string    `json:"workspace_path"`
	SocketPath    string    `json:"socket_path"`
	PID           int       `json:"pid"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
}

// Registry tracks all running ox daemons on the host.
type Registry struct {
	Daemons map[string]DaemonInfo `json:"daemons"`
	mu      sync.Mutex
}

// registryMu protects file operations on the registry
var registryMu sync.Mutex

// LoadRegistry loads the daemon registry from disk.
func LoadRegistry() (*Registry, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	path := RegistryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{Daemons: make(map[string]DaemonInfo)}, nil
		}
		return nil, err
	}

	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		// corrupted registry, start fresh
		return &Registry{Daemons: make(map[string]DaemonInfo)}, nil
	}
	if reg.Daemons == nil {
		reg.Daemons = make(map[string]DaemonInfo)
	}
	return &reg, nil
}

// Save writes the registry to disk.
func (r *Registry) Save() error {
	registryMu.Lock()
	defer registryMu.Unlock()

	path := RegistryPath()
	// 0700 = owner-only directory access (security: hide workspace paths)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	// 0600 = owner read/write only (security: registry contains workspace paths)
	return os.WriteFile(path, data, 0600)
}

// Register adds or updates a daemon entry.
func (r *Registry) Register(info DaemonInfo) error {
	r.mu.Lock()
	r.Daemons[info.WorkspaceID] = info
	r.mu.Unlock()
	return r.Save()
}

// Unregister removes a daemon entry.
func (r *Registry) Unregister(workspaceID string) error {
	r.mu.Lock()
	delete(r.Daemons, workspaceID)
	r.mu.Unlock()
	return r.Save()
}

// List returns all registered daemons.
func (r *Registry) List() []DaemonInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]DaemonInfo, 0, len(r.Daemons))
	for _, info := range r.Daemons {
		result = append(result, info)
	}
	return result
}

// ListRunningDaemons returns all daemons that are actually running (socket responsive).
// Prunes stale entries from the registry.
func ListRunningDaemons() ([]DaemonInfo, error) {
	reg, err := LoadRegistry()
	if err != nil {
		return nil, err
	}

	var running []DaemonInfo
	var stale []string

	for id, info := range reg.Daemons {
		client := NewClientWithSocket(info.SocketPath)
		if err := client.Ping(); err == nil {
			running = append(running, info)
		} else {
			stale = append(stale, id)
		}
	}

	// prune stale entries
	if len(stale) > 0 {
		for _, id := range stale {
			delete(reg.Daemons, id)
		}
		if err := reg.Save(); err != nil {
			slog.Debug("failed to save registry after pruning stale daemons", "error", err)
		}
	}

	return running, nil
}

// RegisterDaemon registers the current daemon in the registry.
func RegisterDaemon(workspacePath, version string) error {
	cwd := workspacePath
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	workspaceID := WorkspaceID(cwd)
	info := DaemonInfo{
		WorkspaceID:   workspaceID,
		WorkspacePath: cwd,
		SocketPath:    SocketPathForWorkspace(workspaceID),
		PID:           os.Getpid(),
		Version:       version,
		StartedAt:     time.Now(),
	}

	reg, err := LoadRegistry()
	if err != nil {
		return err
	}
	return reg.Register(info)
}

// UnregisterDaemon removes the current daemon from the registry.
func UnregisterDaemon() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	workspaceID := WorkspaceID(cwd)
	reg, err := LoadRegistry()
	if err != nil {
		return err
	}
	return reg.Unregister(workspaceID)
}

// KillAllDaemons stops all running daemons gracefully.
func KillAllDaemons() ([]DaemonInfo, error) {
	daemons, err := ListRunningDaemons()
	if err != nil {
		return nil, err
	}

	var killed []DaemonInfo
	for _, info := range daemons {
		client := &Client{
			socketPath: info.SocketPath,
			timeout:    2 * time.Second,
		}
		if err := client.Stop(); err == nil {
			killed = append(killed, info)
		}
	}

	return killed, nil
}
