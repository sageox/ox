package effects

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
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
	cl := daemon.NewClientWithTimeout(500 * time.Millisecond)
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
// daemon. Falls back to resolving the ledger path directly from project config
// when the daemon is offline or hasn't reported a ledger path yet. Returns
// nil, nil when no ledger path is available so the TUI renders an empty state.
func (c *cliClient) ListSessions() ([]session.SessionInfo, error) {
	c.mu.Lock()
	status := c.cachedStatus
	c.mu.Unlock()

	ledgerPath := ""
	if status != nil {
		ledgerPath = status.LedgerPath
	}

	// daemon didn't supply a ledger path — resolve it directly from project config
	if ledgerPath == "" {
		ledgerPath = projectLedgerPath()
	}

	if ledgerPath == "" {
		return nil, nil
	}

	store, err := session.NewStore(ledgerPath)
	if err != nil {
		return nil, nil
	}
	sessions, err := store.ListSessions()
	if err != nil {
		return nil, nil
	}
	return sessions, nil
}

// projectLedgerPath resolves the ledger path from the current project's
// config without requiring a running daemon. Returns empty string if the git
// root cannot be found or the project is not initialized.
func projectLedgerPath() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	ctx, err := config.LoadProjectContext(root)
	if err != nil {
		return ""
	}
	return ctx.DefaultLedgerPath()
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
// that exist on disk. Prefers paths reported by the daemon; falls back to
// scanning the team context directories from project config when the daemon is
// offline or hasn't populated its workspace list yet.
func (c *cliClient) teamContextPaths() []string {
	c.mu.Lock()
	status := c.cachedStatus
	c.mu.Unlock()

	// prefer daemon-reported paths when available
	if status != nil {
		var tcPaths []string
		for wsType, wsList := range status.Workspaces {
			if wsType != "team-context" {
				continue
			}
			for _, ws := range wsList {
				if ws.Exists && ws.Path != "" {
					tcPaths = append(tcPaths, ws.Path)
				}
			}
		}
		if len(tcPaths) > 0 {
			return tcPaths
		}
	}

	// daemon offline or hasn't populated workspace list — discover via project config
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return nil
	}

	teamContexts := config.FindAllTeamContexts(root)
	if len(teamContexts) == 0 {
		return nil
	}

	var tcPaths []string
	for _, tc := range teamContexts {
		if tc.Path != "" {
			tcPaths = append(tcPaths, tc.Path)
		}
	}
	return tcPaths
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

