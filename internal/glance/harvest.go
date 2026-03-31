package glance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/ledger"
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

// GroupByAuthor groups murmurs by author, sorted by most recent activity.
// WIPStatus is set to the latest WIP murmur's content for each author.
// FilesTouched counts unique files from file-change murmurs only.
func GroupByAuthor(murmurs []MurmurRecord) []AuthorSummary {
	byAuthor := make(map[string]*AuthorSummary)
	authorFiles := make(map[string]map[string]bool) // dedup files per author
	latestWIP := make(map[string]time.Time)
	for _, m := range murmurs {
		a, ok := byAuthor[m.User]
		if !ok {
			a = &AuthorSummary{Name: m.User}
			byAuthor[m.User] = a
			authorFiles[m.User] = make(map[string]bool)
		}
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
	for user, a := range byAuthor {
		a.FilesTouched = len(authorFiles[user])
	}

	authors := make([]AuthorSummary, 0, len(byAuthor))
	for _, a := range byAuthor {
		authors = append(authors, *a)
	}
	slices.SortFunc(authors, func(a, b AuthorSummary) int {
		if len(a.Murmurs) == 0 || len(b.Murmurs) == 0 {
			return 0
		}
		return b.Murmurs[0].Time.Compare(a.Murmurs[0].Time)
	})
	return authors
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
