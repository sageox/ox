package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
)

// DaemonInfo represents information about a running daemon.
type DaemonInfo struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspacePath string    `json:"workspace_path"`
	RepoID        string    `json:"repo_id,omitempty"` // identifies repo-scoped daemons across clones
	SocketPath    string    `json:"socket_path"`
	PID           int       `json:"pid"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`

	// Endpoint is the project endpoint slug (NormalizeEndpoint'd) this
	// daemon talks to. Recorded so doctor checks can group daemons by
	// endpoint and verify exactly one global-sync owner per endpoint
	// (ox-6zme).
	Endpoint string `json:"endpoint,omitempty"`

	// GlobalSyncOwner is true if this daemon holds the per-endpoint
	// global-sync flock lease — i.e. it is the daemon responsible for
	// team-context pulls and KB ListBubbles for this user+endpoint.
	// Doctor cross-references this against the running-daemons list to
	// flag "0 owners" or ">1 owner" anomalies. See bead ox-6zme.
	GlobalSyncOwner bool `json:"global_sync_owner,omitempty"`
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
//
// Known race: there is a TOCTOU window between the in-memory map update
// (protected by r.mu) and the disk write (protected by the package-level
// registryMu). Two daemon processes calling Register simultaneously could
// each read a stale on-disk snapshot, causing one entry to be lost.
// This is acceptable because the registry is best-effort: a lost entry
// re-registers on the next heartbeat, and adding flock-based file locking
// would introduce disproportionate complexity for this failure mode.
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

	workspaceID := CurrentWorkspaceID()
	repoID := config.GetRepoID(cwd)
	info := DaemonInfo{
		WorkspaceID:   workspaceID,
		WorkspacePath: cwd,
		RepoID:        repoID,
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

// UpdateGlobalSyncOwnership refreshes the current daemon's endpoint and
// global-sync ownership flag in the registry. Called after the lease is
// acquired (or refused) during initComponents, after the initial
// RegisterDaemon. Best-effort: a registry write failure logs but does
// not abort daemon startup, because the in-memory lease state is the
// real source of truth — the registry entry is for doctor visibility.
func UpdateGlobalSyncOwnership(endpoint string, isOwner bool) error {
	workspaceID := CurrentWorkspaceID()
	reg, err := LoadRegistry()
	if err != nil {
		return err
	}
	reg.mu.Lock()
	info, ok := reg.Daemons[workspaceID]
	if !ok {
		reg.mu.Unlock()
		// Daemon never registered or got pruned — nothing to update.
		// Caller treats this as a soft failure.
		return nil
	}
	info.Endpoint = endpoint
	info.GlobalSyncOwner = isOwner
	reg.Daemons[workspaceID] = info
	reg.mu.Unlock()
	return reg.Save()
}

// UnregisterDaemon removes the current daemon from the registry.
// Uses cached workspace ID since CWD may have been stabilized to $HOME.
func UnregisterDaemon() error {
	workspaceID := CurrentWorkspaceID()
	reg, err := LoadRegistry()
	if err != nil {
		return err
	}
	return reg.Unregister(workspaceID)
}

// FindDaemonForRepo scans the registry for a daemon with a matching repo_id.
// Returns the DaemonInfo if found, nil otherwise.
func FindDaemonForRepo(repoID string) *DaemonInfo {
	if repoID == "" {
		return nil
	}

	reg, err := LoadRegistry()
	if err != nil {
		return nil
	}

	return reg.FindByRepoID(repoID)
}

// FindByWorkspaceID returns the daemon entry for the given workspace ID,
// or nil if no match is found. Empty workspaceID always returns nil.
func (r *Registry) FindByWorkspaceID(workspaceID string) *DaemonInfo {
	if workspaceID == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if info, ok := r.Daemons[workspaceID]; ok {
		return &info
	}
	return nil
}

// FindByRepoID returns the first daemon entry matching the given repo_id,
// or nil if no match is found. Empty repoID always returns nil.
func (r *Registry) FindByRepoID(repoID string) *DaemonInfo {
	if repoID == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, info := range r.Daemons {
		if info.RepoID == repoID {
			return &info
		}
	}
	return nil
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
