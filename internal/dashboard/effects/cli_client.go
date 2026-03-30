package effects

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
)

// cliClient is the production Client backed by the live daemon IPC and the
// session store on disk.
type cliClient struct {
	mu           sync.Mutex
	cachedStatus *daemon.StatusData // last successful daemon status, used by ListSessions
}

// NewCLIClient returns a production Client wired to the real daemon and ledger.
func NewCLIClient() Client {
	return &cliClient{}
}

// GetDaemonStatus connects to the daemon with a short timeout and returns the
// current status. If the daemon is unreachable or not running, it returns a
// StatusData with Running=false rather than an error, so the TUI can display
// a degraded-state indicator rather than an error banner.
func (c *cliClient) GetDaemonStatus() (*daemon.StatusData, error) {
	cl := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	if err := cl.Ping(); err != nil {
		return &daemon.StatusData{Running: false}, nil
	}
	data, err := cl.Status()
	if err != nil {
		return &daemon.StatusData{Running: false}, nil
	}
	// cache for downstream callers that derive ledger path from daemon status.
	c.mu.Lock()
	c.cachedStatus = data
	c.mu.Unlock()
	return data, nil
}

// ListSessions reads recent sessions from the ledger path reported by the
// daemon. Returns nil, nil when no ledger path is available so the TUI
// renders an empty state rather than an error.
func (c *cliClient) ListSessions() ([]session.SessionInfo, error) {
	c.mu.Lock()
	status := c.cachedStatus
	c.mu.Unlock()

	if status == nil || status.LedgerPath == "" {
		return nil, nil
	}

	store, err := session.NewStore(status.LedgerPath)
	if err != nil {
		return nil, nil
	}
	sessions, err := store.ListSessions()
	if err != nil {
		return nil, nil
	}
	return sessions, nil
}

// ListMurmurs scans team context workspace paths for murmur files within the
// last 24 hours. Returns nil, nil on any filesystem error.
func (c *cliClient) ListMurmurs() ([]domain.MurmurEntry, error) {
	paths := c.teamContextPaths()
	if len(paths) == 0 {
		return nil, nil
	}

	var entries []domain.MurmurEntry
	for _, base := range paths {
		murmurs, err := ledger.ReadMurmursInWindow(base, ledger.MaxMurmurWindowHours)
		if err != nil {
			continue
		}
		for _, m := range murmurs {
			entries = append(entries, domain.MurmurEntry{
				AgentID:   m.AgentID,
				Author:    m.PrincipalID,
				Topic:     m.Topic,
				Content:   m.Content,
				Timestamp: m.Timestamp,
			})
		}
	}
	return entries, nil
}

// ListTeamDiscussions scans memory/ directories in team context paths for
// Markdown files, reads the first 200 bytes as a preview, and returns the
// 20 most recently modified entries.
func (c *cliClient) ListTeamDiscussions() ([]domain.TeamDiscussion, error) {
	paths := c.teamContextPaths()
	if len(paths) == 0 {
		return nil, nil
	}

	var discussions []domain.TeamDiscussion
	for _, base := range paths {
		memDir := filepath.Join(base, "memory")
		entries, err := os.ReadDir(memDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			fullPath := filepath.Join(memDir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			preview := readPreview(fullPath, 200)
			title := strings.TrimSuffix(entry.Name(), ".md")
			discussions = append(discussions, domain.TeamDiscussion{
				Title:   title,
				Path:    fullPath,
				Preview: preview,
				ModTime: info.ModTime(),
			})
		}
	}

	// newest first, capped at 20.
	sort.Slice(discussions, func(i, j int) bool {
		return discussions[i].ModTime.After(discussions[j].ModTime)
	})
	if len(discussions) > 20 {
		discussions = discussions[:20]
	}
	return discussions, nil
}

// teamContextPaths returns the filesystem paths for all team-context workspaces
// that exist on disk, derived from the cached daemon status.
func (c *cliClient) teamContextPaths() []string {
	c.mu.Lock()
	status := c.cachedStatus
	c.mu.Unlock()

	if status == nil {
		return nil
	}
	var paths []string
	for wsType, wsList := range status.Workspaces {
		if wsType != "team-context" {
			continue
		}
		for _, ws := range wsList {
			if ws.Exists && ws.Path != "" {
				paths = append(paths, ws.Path)
			}
		}
	}
	return paths
}

// readPreview reads up to n bytes from path and returns them as a trimmed string.
func readPreview(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, n)
	read, _ := f.Read(buf)
	return strings.TrimSpace(string(buf[:read]))
}

