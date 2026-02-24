// Package ledger provides functionality for managing project ledgers.
// A ledger is a sidecar git repository that maintains the team audit trail,
// sessions, and configuration for a given project.
//
// The ledger serves as the single source of truth for:
//   - Agent sessions
//   - Change history
//   - Configuration snapshots
//   - Audit trail for compliance
//
// IMPORTANT: Ledger repositories are provisioned by the cloud server, not created locally.
// The client (ox) only clones and uses ledgers provisioned by the cloud.
//
// SageOx-guided: use 'ox' CLI when planning changes
package ledger

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
)

// ErrNotProvisioned indicates the ledger has not been provisioned by cloud and cloned.
var ErrNotProvisioned = errors.New("ledger not provisioned")

// ErrNoRemoteURL indicates Init was called without a remote URL.
// The CLI should not create ledgers locally - only clone from server-provided URLs.
var ErrNoRemoteURL = errors.New("remote URL required: ledgers must be cloned from cloud")

// Ledger represents a project ledger repository.
type Ledger struct {
	// Path is the absolute path to the ledger repository
	Path string

	// RepoID is the unique identifier for the associated project repository
	RepoID string

	// LastSync is the timestamp of the last successful sync with remote
	LastSync time.Time
}

// DefaultPath returns the default ledger path for the current project.
// Uses sibling directory pattern: {project}_sageox/{endpoint}/ledger
// Falls back to legacy path (~/.cache/sageox/context) if not in a git repo.
func DefaultPath() (string, error) {
	return DefaultPathForEndpoint("")
}

// DefaultPathForEndpoint returns the default ledger path for a specific endpoint.
// Uses the user directory with repo ID:
//
//	~/.local/share/sageox/<endpoint_slug>/ledgers/<repo_id>/
//
// If endpointURL is empty, uses the current endpoint from environment or project config.
// Falls back to legacy path (~/.cache/sageox/context) if not in a git repo.
func DefaultPathForEndpoint(endpointURL string) (string, error) {
	// find main project root (resolves through worktrees to ensure shared ledger)
	projectRoot, err := repotools.FindMainRepoRoot(repotools.VCSGit)
	if err == nil && projectRoot != "" {
		// load project config to get repo ID
		projectCfg, cfgErr := config.LoadProjectConfig(projectRoot)
		if cfgErr != nil {
			return "", fmt.Errorf("load project config: %w", cfgErr)
		}

		repoID := projectCfg.RepoID
		if repoID == "" {
			return "", fmt.Errorf("project config missing repo_id (run 'ox init')")
		}

		// determine endpoint: explicit parameter > project config > env > default
		ep := endpointURL
		if ep == "" {
			ep = endpoint.GetForProject(projectRoot)
		}

		return config.DefaultLedgerPath(repoID, ep), nil
	}

	// fallback to legacy path if not in a git repo
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}

	return filepath.Join(home, ".cache", "sageox", "context"), nil
}

// LegacyPath returns the legacy ledger path for the current project.
// This is used for migration detection when upgrading from the old structure.
// Format: <project_parent>/<repo_name>_sageox_ledger
func LegacyPath() (string, error) {
	projectRoot, err := repotools.FindMainRepoRoot(repotools.VCSGit)
	if err == nil && projectRoot != "" {
		repoName := filepath.Base(projectRoot)
		return config.LegacyLedgerPath(repoName, projectRoot), nil
	}

	// no legacy path available if not in a git repo
	return "", nil
}

// Open opens an existing ledger at the given path.
// Returns ErrNotProvisioned if no ledger exists at the path.
func Open(path string) (*Ledger, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	if !Exists(path) {
		return nil, ErrNotProvisioned
	}

	return &Ledger{
		Path: path,
	}, nil
}

// OpenForEndpoint opens an existing ledger for a specific endpoint.
// Returns ErrNotProvisioned if no ledger exists at the resolved path.
func OpenForEndpoint(endpointURL string) (*Ledger, error) {
	path, err := DefaultPathForEndpoint(endpointURL)
	if err != nil {
		return nil, err
	}

	if !Exists(path) {
		return nil, ErrNotProvisioned
	}

	return &Ledger{
		Path: path,
	}, nil
}

// Exists checks if a ledger exists at the given path.
// A ledger exists if the path contains a .git directory.
func Exists(path string) bool {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return false
		}
	}

	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}

	return info.IsDir()
}

// ExistsForEndpoint checks if a ledger exists for a specific endpoint.
func ExistsForEndpoint(endpointURL string) bool {
	path, err := DefaultPathForEndpoint(endpointURL)
	if err != nil {
		return false
	}
	return Exists(path)
}

// ExistsAtLegacyPath checks if a ledger exists at the legacy path.
// This is used for migration detection.
func ExistsAtLegacyPath() bool {
	path, err := LegacyPath()
	if err != nil || path == "" {
		return false
	}
	return Exists(path)
}

// GetPath returns the ledger path.
func (l *Ledger) GetPath() string {
	if l == nil {
		return ""
	}
	return l.Path
}

// Status returns the current status of the ledger.
type Status struct {
	// Exists indicates if the ledger is cloned
	Exists bool

	// Path is the ledger path
	Path string

	// HasRemote indicates if a remote is configured
	HasRemote bool

	// SyncedWithRemote indicates if local is up to date with remote
	SyncedWithRemote bool

	// SyncStatus describes the sync state (e.g., "ahead by 3", "behind by 1")
	SyncStatus string

	// PendingChanges is the count of uncommitted changes
	PendingChanges int

	// Error contains any error encountered during status check
	Error string
}

// GetStatus returns the current status of the ledger.
func GetStatus(path string) *Status {
	status := &Status{
		Path: path,
	}

	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			status.Error = fmt.Sprintf("get default path: %v", err)
			return status
		}
		status.Path = path
	}

	status.Exists = Exists(path)
	if !status.Exists {
		status.Error = "ledger not provisioned"
		return status
	}

	// check for remote
	cmd := exec.Command("git", "-C", path, "remote")
	output, err := cmd.Output()
	if err != nil {
		status.Error = fmt.Sprintf("check remote: %v", err)
		return status
	}
	status.HasRemote = strings.TrimSpace(string(output)) != ""

	// check sync status if remote exists
	if status.HasRemote {
		checkSyncStatus(path, status)
	}

	// count pending changes
	countPendingChanges(path, status)

	return status
}

// GetStatusForEndpoint returns the current status of the ledger for a specific endpoint.
func GetStatusForEndpoint(endpointURL string) *Status {
	path, err := DefaultPathForEndpoint(endpointURL)
	if err != nil {
		return &Status{
			Error: fmt.Sprintf("get ledger path: %v", err),
		}
	}
	return GetStatus(path)
}

// checkSyncStatus checks the ahead/behind status with remote.
func checkSyncStatus(path string, status *Status) {
	// fetch to update tracking info (quiet, ignore errors)
	fetchCmd := exec.Command("git", "-C", path, "fetch", "--quiet")
	_ = fetchCmd.Run()

	// check ahead/behind
	cmd := exec.Command("git", "-C", path, "status", "--porcelain", "-b")
	output, err := cmd.Output()
	if err != nil {
		status.SyncStatus = "unknown"
		return
	}

	outputStr := string(output)

	// parse branch line for ahead/behind: ## branch...origin/branch [ahead N, behind M]
	if strings.Contains(outputStr, "[") {
		start := strings.Index(outputStr, "[")
		end := strings.Index(outputStr, "]")
		if start >= 0 && end > start {
			status.SyncStatus = outputStr[start+1 : end]
			status.SyncedWithRemote = false
			return
		}
	}

	status.SyncedWithRemote = true
	status.SyncStatus = "up to date"
}

// countPendingChanges counts uncommitted changes in the ledger.
func countPendingChanges(path string, status *Status) {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line != "" {
			status.PendingChanges++
		}
	}
}

// Init clones a ledger repository from the given remote URL.
// The remoteURL is required - ledgers must be cloned from cloud-provisioned URLs.
// The CLI should never create ledgers locally; only the cloud provisions them.
func Init(path string, remoteURL string) (*Ledger, error) {
	if remoteURL == "" {
		return nil, ErrNoRemoteURL
	}

	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	// create parent directory
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	// clone with sparse checkout from cloud-provisioned URL
	if err := cloneWithSparseCheckout(path, remoteURL); err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}

	return &Ledger{Path: path}, nil
}

// InitForEndpoint clones a ledger repository for a specific endpoint.
// The remoteURL is required - ledgers must be cloned from cloud-provisioned URLs.
func InitForEndpoint(endpointURL string, remoteURL string) (*Ledger, error) {
	path, err := DefaultPathForEndpoint(endpointURL)
	if err != nil {
		return nil, err
	}
	return Init(path, remoteURL)
}

// cloneWithSparseCheckout clones a remote repo with sparse checkout enabled.
// Only .sync/, sessions/, and audit/ directories are checked out.
// The assets/ directory is excluded to save space.
func cloneWithSparseCheckout(path, remoteURL string) error {
	// remove existing directory if empty
	entries, _ := os.ReadDir(path)
	if len(entries) == 0 {
		os.Remove(path)
	}

	// clone with filter (partial clone) and sparse checkout
	cloneCmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--sparse",
		remoteURL,
		path,
	)
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, output)
	}

	// configure sparse checkout
	if err := configureSparseCheckout(path); err != nil {
		return fmt.Errorf("configure sparse checkout: %w", err)
	}

	return nil
}

// configureSparseCheckout sets up sparse checkout for the ledger.
// Includes: .sync/, sessions/, audit/
// Excludes: assets/ (large files)
func configureSparseCheckout(path string) error {
	// init sparse checkout in cone mode
	initCmd := exec.Command("git", "-C", path, "sparse-checkout", "init", "--cone")
	if output, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sparse-checkout init: %w: %s", err, output)
	}

	// set directories to include (excludes everything else like assets/)
	setCmd := exec.Command("git", "-C", path, "sparse-checkout", "set",
		".sync",
		"sessions",
		"audit",
	)
	if output, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sparse-checkout set: %w: %s", err, output)
	}

	// configure pull strategy to use rebase (avoids merge commits, cleaner history)
	// This prevents git warnings about divergent branches on manual pulls
	configCmd := exec.Command("git", "-C", path, "config", "pull.rebase", "true")
	if output, err := configCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("config pull.rebase: %w: %s", err, output)
	}

	return nil
}

// EnableSparseCheckout enables sparse checkout on an existing ledger.
// This is useful for converting an existing full checkout to sparse.
func (l *Ledger) EnableSparseCheckout() error {
	if l == nil || l.Path == "" {
		return ErrNotProvisioned
	}

	return configureSparseCheckout(l.Path)
}

// DisableSparseCheckout disables sparse checkout, fetching all content.
func (l *Ledger) DisableSparseCheckout() error {
	if l == nil || l.Path == "" {
		return ErrNotProvisioned
	}

	cmd := exec.Command("git", "-C", l.Path, "sparse-checkout", "disable")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("disable sparse checkout: %w: %s", err, output)
	}

	return nil
}

// AddToSparseCheckout adds additional directories to sparse checkout.
func (l *Ledger) AddToSparseCheckout(dirs ...string) error {
	if l == nil || l.Path == "" {
		return ErrNotProvisioned
	}

	args := append([]string{"-C", l.Path, "sparse-checkout", "add"}, dirs...)
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sparse-checkout add: %w: %s", err, output)
	}

	return nil
}
