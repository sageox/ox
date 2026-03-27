package glance

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// HarvestOptions controls session filtering and enrichment.
type HarvestOptions struct {
	IncludeSubagents bool
	Mailmap          map[string]string                      // variant name → canonical name
	HydrateFunc      func(sessionsDir, sessionName string) error // called for dehydrated sessions; nil = skip
}

// HarvestResult contains the harvested sessions plus metadata.
type HarvestResult struct {
	Sessions          []SessionRecord
	SkippedDehydrated int
}

// HarvestSessions reads sessions from the ledger, hydrating dehydrated ones,
// and extracts files touched from raw.jsonl tool calls.
func HarvestSessions(ledgerPath string, since, until time.Time, opts HarvestOptions) (*HarvestResult, error) {
	store, err := session.NewStore(ledgerPath)
	if err != nil {
		return nil, err
	}
	infos, err := store.ListSessionsSince(since)
	if err != nil {
		return nil, err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	result := &HarvestResult{}

	for _, info := range infos {
		if !until.IsZero() && info.CreatedAt.After(until) {
			continue
		}
		if !opts.IncludeSubagents && info.IsSubagent {
			continue
		}

		dir := filepath.Join(sessionsDir, info.SessionName)
		rec := SessionRecord{
			Name:      info.SessionName,
			User:      resolveUsername(info, opts.Mailmap),
			Time:      info.CreatedAt,
			Title:     info.Summary,
			Recording: info.Recording,
		}

		// Enrich title/summary from summary.json when available
		if summary, serr := readSummaryJSON(filepath.Join(dir, "summary.json")); serr == nil {
			if summary.Title != "" {
				rec.Title = summary.Title
			}
			if summary.Summary != "" {
				rec.Summary = summary.Summary
			}
			// Use files_changed if populated (future-proof)
			for _, f := range summary.FilesChanged {
				rec.Files = append(rec.Files, f.Path)
			}
		}

		// Primary file source: raw.jsonl tool calls
		if len(rec.Files) == 0 {
			rawPath := filepath.Join(dir, "raw.jsonl")
			if lfs.IsPointerFile(rawPath) {
				if opts.HydrateFunc != nil {
					if opts.HydrateFunc(sessionsDir, info.SessionName) == nil {
						rec.Files = extractFilesFromRawJSONL(rawPath)
					} else {
						result.SkippedDehydrated++
					}
				} else {
					result.SkippedDehydrated++
				}
			} else {
				rec.Files = extractFilesFromRawJSONL(rawPath)
			}
		}

		result.Sessions = append(result.Sessions, rec)
	}

	slices.SortFunc(result.Sessions, func(a, b SessionRecord) int {
		return b.Time.Compare(a.Time)
	})
	return result, nil
}

// GroupByAuthor groups sessions by author, sorted by most recent activity.
func GroupByAuthor(sessions []SessionRecord) []AuthorSummary {
	byAuthor := make(map[string]*AuthorSummary)
	for _, s := range sessions {
		a, ok := byAuthor[s.User]
		if !ok {
			a = &AuthorSummary{Name: s.User}
			byAuthor[s.User] = a
		}
		a.Sessions = append(a.Sessions, s)
		a.SessionCount++
		a.FilesTouched += len(s.Files)
	}

	authors := make([]AuthorSummary, 0, len(byAuthor))
	for _, a := range byAuthor {
		authors = append(authors, *a)
	}
	slices.SortFunc(authors, func(a, b AuthorSummary) int {
		if len(a.Sessions) == 0 || len(b.Sessions) == 0 {
			return 0
		}
		return b.Sessions[0].Time.Compare(a.Sessions[0].Time)
	})
	return authors
}

// --- file extraction ---

// extractFilesFromRawJSONL scans raw.jsonl for Edit/Write/MultiEdit tool calls
// and returns deduplicated, normalized file paths.
func extractFilesFromRawJSONL(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !strings.Contains(string(line), `"tool"`) {
			continue
		}
		var entry struct {
			Type      string `json:"type"`
			ToolName  string `json:"tool_name"`
			ToolInput string `json:"tool_input"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "tool" {
			continue
		}
		switch strings.ToLower(entry.ToolName) {
		case "edit", "write", "multiedit":
			if fp := filePathFromToolInput(entry.ToolInput); fp != "" {
				seen[fp] = true
			}
		}
	}
	// Note: scanner.Err() intentionally not checked — truncated lines
	// are acceptable; we extract what we can from partial sessions.

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	slices.Sort(files)
	return files
}

// filePathFromToolInput parses tool input JSON for file_path or path.
func filePathFromToolInput(input string) string {
	if input == "" {
		return ""
	}
	var data map[string]any
	if json.Unmarshal([]byte(input), &data) != nil {
		return ""
	}
	if fp, ok := data["file_path"].(string); ok && fp != "" {
		return normalizePath(fp)
	}
	if fp, ok := data["path"].(string); ok && fp != "" {
		return normalizePath(fp)
	}
	return ""
}

// normalizePath converts an absolute file path to a repo-relative path.
// Finds known top-level directory markers (cmd/, internal/, etc.) to locate
// the repo root. This handles worktree paths from different machines.
func normalizePath(p string) string {
	markers := []string{"/cmd/", "/internal/", "/pkg/", "/docs/", "/tests/", "/.github/", "/.sageox/"}
	for _, m := range markers {
		if idx := strings.Index(p, m); idx >= 0 {
			return p[idx+1:]
		}
	}
	// For root-level known files, return just the basename
	base := filepath.Base(p)
	if slices.Contains([]string{"go.mod", "go.sum", "CLAUDE.md", "Makefile", "CHANGELOG.md", ".goreleaser.yml"}, base) {
		return base
	}
	// Preserve parent/file to avoid aliasing (e.g., examples/config.go vs scripts/config.go)
	dir := filepath.Base(filepath.Dir(p))
	if dir != "" && dir != "." && dir != "/" {
		return dir + "/" + base
	}
	return base
}

// --- summary.json ---

func readSummaryJSON(path string) (*sessionsummary.SummarizeResponse, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(string(data), "version https://git-lfs") {
		return nil, os.ErrNotExist
	}
	var summary sessionsummary.SummarizeResponse
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

// --- username resolution ---

// resolveUsername extracts a display name from session info, deduplicating via mailmap.
// Checks: meta.json username, session name username, and email local-part against mailmap.
func resolveUsername(info session.SessionInfo, mailmap map[string]string) string {
	// Extract raw name from meta.json
	name := usernameFromMeta(info.Username)
	if name == "" {
		name = usernameFromSessionName(info.SessionName)
	}

	if len(mailmap) == 0 {
		return name
	}

	// Try all known identifiers against the mailmap
	candidates := []string{name, info.Username, usernameFromSessionName(info.SessionName)}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if canonical, ok := mailmap[strings.ToLower(c)]; ok {
			return canonical
		}
	}
	return name
}

func usernameFromMeta(username string) string {
	if username == "" {
		return ""
	}
	if at := strings.Index(username, "@"); at > 0 {
		return username[:at]
	}
	return username
}

func usernameFromSessionName(sessionName string) string {
	// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
	// Split by "-": [YYYY, MM, DDTHH, MM, <username parts...>, sessionID]
	// The datetime prefix consumes 4 parts (indices 0-3).
	// Username may contain hyphens (e.g., "alice-smith"), so join everything
	// between the datetime prefix and the final session ID.
	parts := strings.Split(sessionName, "-")
	if len(parts) >= 6 {
		return strings.Join(parts[4:len(parts)-1], "-")
	}
	return "unknown"
}

// LoadMailmap parses a .mailmap file into a lookup table.
// Maps email addresses, email local-parts, and first names to canonical display names.
func LoadMailmap(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Format: "Canonical Name <canonical@email> [Variant Name <variant@email>]"
		parts := strings.SplitN(line, "<", 3)
		if len(parts) < 2 {
			continue
		}

		canonicalName := strings.TrimSpace(parts[0])
		canonicalEmail := strings.TrimRight(strings.TrimSpace(parts[1]), "> ")

		addMapping := func(key string) {
			if key != "" {
				result[strings.ToLower(key)] = canonicalName
			}
		}
		addEmailMapping := func(email string) {
			addMapping(email)
			if at := strings.Index(email, "@"); at > 0 {
				addMapping(email[:at])
			}
		}

		if fields := strings.Fields(canonicalName); len(fields) > 0 {
			addMapping(fields[0])
		}
		addEmailMapping(canonicalEmail)

		if len(parts) >= 3 {
			variantEmail := strings.TrimRight(strings.TrimSpace(parts[2]), "> ")
			addEmailMapping(variantEmail)
			// Variant name between > and <
			if after := strings.SplitN(parts[1], ">", 2); len(after) >= 2 {
				if fields := strings.Fields(strings.TrimSpace(after[1])); len(fields) > 0 {
					addMapping(fields[0])
				}
			}
		}
	}
	return result
}
