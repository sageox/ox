package glance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// HarvestResult contains the harvested murmurs.
type HarvestResult struct {
	Murmurs []MurmurRecord
}

// HarvestMurmurs reads murmurs from the ledger between since and until,
// extracts file paths from Metadata["files"], and converts to MurmurRecords.
func HarvestMurmurs(ledgerPath string, since, until time.Time) (*HarvestResult, error) {
	var murmurs []MurmurRecord
	current := since.Truncate(time.Hour)
	end := until
	if end.IsZero() {
		end = time.Now().UTC()
	}

	for !current.After(end) {
		dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(current))
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				current = current.Add(time.Hour)
				continue
			}
			return nil, fmt.Errorf("read murmur dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read murmur file %s: %w", path, err)
			}
			var m ledger.MurmurFile
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, fmt.Errorf("unmarshal murmur %s: %w", path, err)
			}
			if m.Timestamp.Before(since) {
				continue
			}
			if !until.IsZero() && m.Timestamp.After(until) {
				continue
			}
			murmurs = append(murmurs, MurmurRecord{
				ID:         m.ID,
				User:       m.PrincipalID,
				AgentID:    m.AgentID,
				Topic:      m.Topic,
				Time:       m.Timestamp,
				Content:    m.Content,
				Files:      extractFilesFromMetadata(m.Metadata),
				Branch:     m.Metadata["branch"],
				Importance: m.Importance,
			})
		}
		current = current.Add(time.Hour)
	}

	slices.SortFunc(murmurs, func(a, b MurmurRecord) int {
		return b.Time.Compare(a.Time)
	})
	return &HarvestResult{Murmurs: murmurs}, nil
}

// GroupByAuthor groups murmurs and sessions by author, sorted by most recent activity.
// WIPStatus is set to the latest WIP murmur's content for each author.
// FilesTouched counts unique files from file-change murmurs and session tool calls.
func GroupByAuthor(murmurs []MurmurRecord, sessions ...[]SessionRecord) []AuthorSummary {
	byAuthor := make(map[string]*AuthorSummary)
	authorFiles := make(map[string]map[string]bool) // dedup files per author
	latestWIP := make(map[string]time.Time)

	ensureAuthor := func(user string) *AuthorSummary {
		a, ok := byAuthor[user]
		if !ok {
			a = &AuthorSummary{Name: user}
			byAuthor[user] = a
			authorFiles[user] = make(map[string]bool)
		}
		return a
	}

	for _, m := range murmurs {
		a := ensureAuthor(m.User)
		a.Murmurs = append(a.Murmurs, m)
		a.MurmurCount++
		for _, f := range m.Files {
			authorFiles[m.User][f] = true
		}
		if m.Topic == "wip" && m.Time.After(latestWIP[m.User]) {
			a.WIPStatus = m.Content
			latestWIP[m.User] = m.Time
		}
	}

	// merge sessions if provided
	for _, ss := range sessions {
		for _, s := range ss {
			a := ensureAuthor(s.User)
			a.Sessions = append(a.Sessions, s)
			a.SessionCount++
			for _, f := range s.Files {
				authorFiles[s.User][f] = true
			}
		}
	}

	for user, a := range byAuthor {
		a.FilesTouched = len(authorFiles[user])
	}

	authors := make([]AuthorSummary, 0, len(byAuthor))
	for _, a := range byAuthor {
		authors = append(authors, *a)
	}
	slices.SortFunc(authors, func(a, b AuthorSummary) int {
		aTime := latestActivity(a)
		bTime := latestActivity(b)
		return bTime.Compare(aTime)
	})
	return authors
}

// latestActivity returns the most recent timestamp across murmurs and sessions.
func latestActivity(a AuthorSummary) time.Time {
	var latest time.Time
	if len(a.Murmurs) > 0 {
		latest = a.Murmurs[0].Time
	}
	if len(a.Sessions) > 0 && a.Sessions[0].Time.After(latest) {
		latest = a.Sessions[0].Time
	}
	return latest
}

// extractFilesFromMetadata parses Metadata["files"] (comma-separated) into sorted, deduplicated paths.
func extractFilesFromMetadata(metadata map[string]string) []string {
	raw := metadata["files"]
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	var files []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files
}

// --- Session harvesting ---

// HarvestSessionResult contains the harvested sessions plus metadata.
type HarvestSessionResult struct {
	Sessions          []SessionRecord
	SkippedDehydrated int
}

// HarvestSessions reads sessions from the ledger and extracts files touched
// from summary.json and raw.jsonl tool calls.
func HarvestSessions(ledgerPath string, since, until time.Time) (*HarvestSessionResult, error) {
	store, err := session.NewStore(ledgerPath)
	if err != nil {
		return nil, err
	}
	infos, err := store.ListSessionsSince(since)
	if err != nil {
		return nil, err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	result := &HarvestSessionResult{}

	for _, info := range infos {
		if !until.IsZero() && info.CreatedAt.After(until) {
			continue
		}
		if info.IsSubagent {
			continue
		}

		dir := filepath.Join(sessionsDir, info.SessionName)
		rec := SessionRecord{
			Name:      info.SessionName,
			User:      resolveUsername(info),
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
			for _, f := range summary.FilesChanged {
				rec.Files = append(rec.Files, f.Path)
			}
		}

		// Primary file source: raw.jsonl tool calls (also check cache)
		if len(rec.Files) == 0 {
			rawPath := store.ResolveContentFile(info.SessionName, "raw.jsonl")
			if lfs.IsPointerFile(rawPath) {
				result.SkippedDehydrated++
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

// SessionFilesToMurmurRecords converts session file touches into synthetic
// MurmurRecords so they can be fed into DetectConflicts alongside real murmurs.
func SessionFilesToMurmurRecords(sessions []SessionRecord) []MurmurRecord {
	var records []MurmurRecord
	for _, s := range sessions {
		if len(s.Files) == 0 {
			continue
		}
		records = append(records, MurmurRecord{
			ID:    "session:" + s.Name,
			User:  s.User,
			Topic: "session",
			Time:  s.Time,
			Files: s.Files,
		})
	}
	return records
}

// --- file extraction from raw.jsonl ---

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
func normalizePath(p string) string {
	markers := []string{"/cmd/", "/internal/", "/pkg/", "/docs/", "/tests/", "/.github/", "/.sageox/"}
	for _, m := range markers {
		if idx := strings.Index(p, m); idx >= 0 {
			return p[idx+1:]
		}
	}
	base := filepath.Base(p)
	if slices.Contains([]string{"go.mod", "go.sum", "CLAUDE.md", "Makefile", "CHANGELOG.md", ".goreleaser.yml"}, base) {
		return base
	}
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

func resolveUsername(info session.SessionInfo) string {
	name := info.Username
	if name == "" {
		return usernameFromSessionName(info.SessionName)
	}
	if at := strings.Index(name, "@"); at > 0 {
		return name[:at]
	}
	return name
}

func usernameFromSessionName(sessionName string) string {
	parts := strings.Split(sessionName, "-")
	if len(parts) >= 6 {
		return strings.Join(parts[4:len(parts)-1], "-")
	}
	return "unknown"
}
