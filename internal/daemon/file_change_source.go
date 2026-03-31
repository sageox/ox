package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
)

// MurmurPublisher writes a murmur to the ledger via the daemon service.
type MurmurPublisher interface {
	PublishMurmur(payload MurmurPayload)
}

// ActiveAgentResolver returns IDs of active AI coworker instances.
type ActiveAgentResolver interface {
	ActiveAgentIDs() []string
}

const (
	// drainInterval is how often we pull settled changes from the accumulator.
	drainInterval = 5 * time.Second

	// defaultMurmurInterval is how often file-change murmurs are published.
	defaultMurmurInterval = 10 * time.Minute

	// startupMaxAge caps how far back we report on first murmur after startup.
	startupMaxAge = 30 * time.Minute
)

// FileChangeMurmurPublisher drains the ChangeAccumulator frequently and
// batches changes into a murmur every 10-15 minutes. Only reports changes
// since the last murmur. On startup, caps at the last 30 minutes.
type FileChangeMurmurPublisher struct {
	accumulator    *ChangeAccumulator
	publisher      MurmurPublisher
	ledgerPath     string
	projectRoot    string
	logger         *slog.Logger
	remoteURL      string // cached origin remote URL
	murmurInterval time.Duration
	agentResolver  ActiveAgentResolver // optional; nil falls back to "daemon"

	mu        sync.Mutex
	pending   map[string]*FileChange // path → latest change, collapsed
	startTime time.Time
	lastEmit  time.Time
}

// NewFileChangeMurmurPublisher creates a publisher that batches file changes
// and emits murmurs at a regular interval (default 10 minutes).
func NewFileChangeMurmurPublisher(
	accumulator *ChangeAccumulator,
	publisher MurmurPublisher,
	ledgerPath string,
	projectRoot string,
	logger *slog.Logger,
) *FileChangeMurmurPublisher {
	return &FileChangeMurmurPublisher{
		accumulator:    accumulator,
		publisher:      publisher,
		ledgerPath:     ledgerPath,
		projectRoot:    projectRoot,
		logger:         logger,
		remoteURL:      resolveOriginURL(projectRoot),
		murmurInterval: defaultMurmurInterval,
		pending:        make(map[string]*FileChange),
		startTime:      time.Now(),
	}
}

// SetAgentResolver sets the resolver used to look up active AI coworker IDs.
func (p *FileChangeMurmurPublisher) SetAgentResolver(r ActiveAgentResolver) {
	p.agentResolver = r
}

// resolveAgentID returns a compact agent ID string for murmur attribution.
// Falls back to "daemon" when no resolver is set or no agents are active.
func (p *FileChangeMurmurPublisher) resolveAgentID() string {
	if p.agentResolver == nil {
		return "daemon"
	}
	ids := p.agentResolver.ActiveAgentIDs()
	switch len(ids) {
	case 0:
		return "daemon"
	case 1:
		return ids[0]
	case 2:
		return ids[0] + "," + ids[1]
	default:
		return ids[0] + "," + ids[1] + ",..."
	}
}

// Start drains the accumulator and publishes murmurs. Blocks until ctx is canceled.
func (p *FileChangeMurmurPublisher) Start(ctx context.Context) {
	drainTicker := time.NewTicker(drainInterval)
	murmurTicker := time.NewTicker(p.murmurInterval)
	defer drainTicker.Stop()
	defer murmurTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// flush any remaining changes on shutdown
			p.drain()
			p.publish()
			return
		case <-drainTicker.C:
			p.drain()
		case <-murmurTicker.C:
			p.drain()
			p.publish()
		}
	}
}

// drain pulls settled changes from the accumulator into the pending buffer,
// collapsing duplicate paths (latest change type wins).
func (p *FileChangeMurmurPublisher) drain() {
	changes := p.accumulator.DrainSettled()
	if len(changes) == 0 {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range changes {
		c := &changes[i]
		existing, ok := p.pending[c.Path]
		if ok {
			// collapse: same rules as accumulator
			existing.ChangeType = collapseType(existing.ChangeType, c.ChangeType)
			existing.Timestamp = c.Timestamp
			// create+delete = suppressed
			if existing.ChangeType == "" {
				delete(p.pending, c.Path)
			}
		} else {
			p.pending[c.Path] = c
		}
	}
}

// collapseType applies the same rules as ChangeAccumulator.collapseChange.
func collapseType(existing, incoming ChangeType) ChangeType {
	switch {
	case existing == ChangeCreated && incoming == ChangeDeleted:
		return ""
	case existing == ChangeCreated && incoming == ChangeModified:
		return ChangeCreated
	case existing == ChangeDeleted && incoming == ChangeCreated:
		return ChangeModified
	default:
		return incoming
	}
}

// publish emits a murmur if there are pending changes. Applies startup cap.
func (p *FileChangeMurmurPublisher) publish() {
	p.mu.Lock()
	if len(p.pending) == 0 {
		p.mu.Unlock()
		return
	}

	// on first publish, drop changes older than startupMaxAge
	if p.lastEmit.IsZero() {
		cutoff := time.Now().Add(-startupMaxAge)
		for path, c := range p.pending {
			if c.Timestamp.Before(cutoff) {
				delete(p.pending, path)
			}
		}
		if len(p.pending) == 0 {
			p.mu.Unlock()
			return
		}
	}

	// snapshot and clear
	changes := make([]FileChange, 0, len(p.pending))
	for _, c := range p.pending {
		changes = append(changes, *c)
	}
	p.pending = make(map[string]*FileChange)
	p.mu.Unlock()

	now := time.Now()
	branch := repotools.GetCurrentBranch(p.projectRoot)
	content := formatFileChangeMurmur(changes, branch, p.projectRoot)
	importance := "ambient"
	if len(changes) > 10 {
		importance = "normal"
	}

	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}

	murmur := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            id.String(),
		Timestamp:     now.UTC(),
		AgentID:       p.resolveAgentID(),
		PrincipalID:   resolvePrincipal(p.projectRoot),
		PrincipalType: "human",
		Topic:         "file-changes",
		Importance:    importance,
		Content:       content,
		Scope:         "ledger",
		Metadata:      buildFileChangeMetadata(changes, branch, p.projectRoot),
	}

	relPath := ledger.MurmurFilePath(now.UTC(), id.String())
	murmurJSON, err := json.Marshal(murmur)
	if err != nil {
		p.logger.Warn("file change murmur: failed to marshal", "error", err)
		return
	}

	p.publisher.PublishMurmur(MurmurPayload{
		TargetDir:  p.ledgerPath,
		RelPath:    relPath,
		Content:    content,
		MurmurJSON: murmurJSON,
	})

	p.lastEmit = now
	p.logger.Debug("file change murmur published",
		"repo", p.remoteURL,
		"branch", branch,
		"file_count", len(changes),
		"files", fileList(changes),
	)
}

// PendingCount returns the number of buffered changes (for testing).
func (p *FileChangeMurmurPublisher) PendingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

// fileList returns a compact comma-separated list of changed file paths.
func fileList(changes []FileChange) string {
	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = string(c.ChangeType) + ":" + c.Path
	}
	sort.Strings(paths)
	result := strings.Join(paths, ", ")
	if len(result) > 200 {
		return result[:200] + "..."
	}
	return result
}

// resolveOriginURL returns a short repo identifier from the git origin remote.
func resolveOriginURL(projectRoot string) string {
	urls, err := repotools.GetRemoteURLsForDir(projectRoot)
	if err != nil || len(urls) == 0 {
		return "N/A"
	}
	return shortenRemoteURL(urls[0])
}

// resolvePrincipal returns the short username from ox login credentials.
func resolvePrincipal(projectRoot string) string {
	ep := endpoint.GetForProject(projectRoot)
	if username := auth.GetUsername(ep); username != "" {
		if idx := strings.IndexByte(username, '@'); idx > 0 {
			return username[:idx]
		}
		return username
	}
	return ""
}

// shortenRemoteURL strips common hosting prefixes and .git suffix to produce
// a compact "owner/repo" identifier.
func shortenRemoteURL(url string) string {
	s := url
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.Replace(s, ":", "/", 1)
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		s = strings.TrimPrefix(s, host)
	}
	s = strings.TrimSuffix(s, ".git")
	return s
}

// buildFileChangeMetadata creates structured metadata for programmatic consumers.
func buildFileChangeMetadata(changes []FileChange, branch, projectRoot string) map[string]string {
	meta := map[string]string{
		"worktree": projectRoot,
	}
	if branch != "" {
		meta["branch"] = branch
	}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	if len(paths) > 50 {
		paths = paths[:50]
	}
	meta["files"] = strings.Join(paths, ",")
	meta["file_count"] = fmt.Sprintf("%d", len(changes))
	return meta
}

// formatFileChangeMurmur produces compact, human-scannable change summaries.
// Files come first (visible in ox murmur list), branch/worktree as compact tag.
// Agents use the JSON metadata for structured data.
func formatFileChangeMurmur(changes []FileChange, branch, projectRoot string) string {
	// context tag: [main@myproject]
	var ctx string
	worktree := filepath.Base(projectRoot)
	switch {
	case branch != "" && worktree != "" && worktree != ".":
		ctx = fmt.Sprintf("[%s@%s] ", branch, worktree)
	case branch != "":
		ctx = fmt.Sprintf("[%s] ", branch)
	case worktree != "" && worktree != ".":
		ctx = fmt.Sprintf("[@%s] ", worktree)
	}

	count := len(changes)
	switch {
	case count > 100:
		return ctx + formatHugeChanges(changes)
	case count > 20:
		return ctx + formatLargeChanges(changes)
	case count > 5:
		return ctx + formatMediumChanges(changes)
	default:
		return ctx + formatSmallChanges(changes)
	}
}

// formatSmallChanges lists each file inline (1-5 files).
func formatSmallChanges(changes []FileChange) string {
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Path < changes[j].Path
	})
	parts := make([]string, len(changes))
	for i, c := range changes {
		parts[i] = fmt.Sprintf("%s %s", c.ChangeType, c.Path)
	}
	return strings.Join(parts, ", ")
}

// formatMediumChanges groups by directory (6-20 files).
func formatMediumChanges(changes []FileChange) string {
	type dirSummary struct{ created, modified, deleted int }
	dirs := make(map[string]*dirSummary)

	for _, c := range changes {
		dir := filepath.Dir(c.Path)
		if dir == "." {
			dir = "./"
		} else {
			dir += "/"
		}
		s, ok := dirs[dir]
		if !ok {
			s = &dirSummary{}
			dirs[dir] = s
		}
		switch c.ChangeType {
		case ChangeCreated:
			s.created++
		case ChangeDeleted:
			s.deleted++
		default:
			s.modified++
		}
	}

	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)

	var parts []string
	for _, dir := range dirNames {
		s := dirs[dir]
		var counts []string
		if s.modified > 0 {
			counts = append(counts, fmt.Sprintf("%dM", s.modified))
		}
		if s.created > 0 {
			counts = append(counts, fmt.Sprintf("%dA", s.created))
		}
		if s.deleted > 0 {
			counts = append(counts, fmt.Sprintf("%dD", s.deleted))
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", dir, strings.Join(counts, ",")))
	}
	return fmt.Sprintf("%d files: %s", len(changes), strings.Join(parts, " "))
}

// formatLargeChanges shows top directories only (21-100 files).
func formatLargeChanges(changes []FileChange) string {
	dirs := make(map[string]int)
	for _, c := range changes {
		dir := filepath.Dir(c.Path)
		if dir == "." {
			dir = "./"
		} else {
			dir += "/"
		}
		dirs[dir]++
	}

	type dirCount struct {
		dir   string
		count int
	}
	sorted := make([]dirCount, 0, len(dirs))
	for d, n := range dirs {
		sorted = append(sorted, dirCount{d, n})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	// show top 5 dirs
	var parts []string
	shown := 0
	for _, dc := range sorted {
		if shown >= 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s(%d)", dc.dir, dc.count))
		shown++
	}
	rest := len(dirs) - shown
	summary := fmt.Sprintf("%d files: %s", len(changes), strings.Join(parts, " "))
	if rest > 0 {
		summary += fmt.Sprintf(" +%d dirs", rest)
	}
	return summary
}

// formatHugeChanges is ultra-terse for 100+ files (branch switch, codegen).
func formatHugeChanges(changes []FileChange) string {
	dirs := make(map[string]struct{})
	for _, c := range changes {
		dirs[filepath.Dir(c.Path)] = struct{}{}
	}
	return fmt.Sprintf("%d files across %d dirs", len(changes), len(dirs))
}
