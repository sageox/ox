package recap

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/teamdocs"
)

// teamBuilt summarizes what the team has authored into shared context. It feeds
// both the "value to your team" half of the report and the cold-start story:
// even a brand-new user's future sessions will inherit all of this.
type teamBuilt struct {
	artifacts   []TeamArtifact
	docCount    int
	discCount   int
	memoryLines int
}

// gatherTeamContext reads the team-context repo — docs/ (with titles and
// quotable snippets) and recorded discussions — into a bounded list of
// TeamArtifacts. Missing team path or sub-dirs is not an error.
func gatherTeamContext(teamPath string) teamBuilt {
	var tb teamBuilt
	if teamPath == "" {
		return tb
	}

	docs, _ := teamdocs.DiscoverDocs(teamPath)
	tb.docCount = len(docs)
	for _, d := range docs {
		if len(tb.artifacts) >= maxTeamArtifacts {
			break
		}
		title, snippet := readArtifactContent(d.Path)
		if d.Title != "" {
			title = d.Title
		}
		tb.artifacts = append(tb.artifacts, TeamArtifact{
			Doc:     d.Name,
			Title:   title,
			Kind:    "doc",
			Snippet: snippet,
			Receipt: d.Path,
		})
	}

	discussions := recentDiscussions(teamPath, 4)
	tb.discCount = countDiscussions(teamPath)
	for _, da := range discussions {
		if len(tb.artifacts) >= maxTeamArtifacts {
			break
		}
		tb.artifacts = append(tb.artifacts, da)
	}

	tb.memoryLines = memorySubstance(filepath.Join(teamPath, "MEMORY.md"))
	return tb
}

// populated reports whether the team has meaningfully filled its context — the
// signal that flips the report between "here's your value" and cold-start
// prescriptions.
func (tb teamBuilt) populated() bool {
	return tb.docCount > 0 || tb.discCount > 0 || tb.memoryLines > 0
}

// countDiscussions counts recorded discussion directories.
func countDiscussions(teamPath string) int {
	entries, err := os.ReadDir(filepath.Join(teamPath, "discussions"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// recentDiscussions returns the most recent recorded discussions as
// TeamArtifacts, reading each one's summary for a title and snippet. Discussion
// dirs are date-prefixed, so reverse-lexical order is reverse-chronological.
func recentDiscussions(teamPath string, limit int) []TeamArtifact {
	dir := filepath.Join(teamPath, "discussions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > limit {
		names = names[:limit]
	}

	var out []TeamArtifact
	for _, name := range names {
		summaryPath := filepath.Join(dir, name, "summary.md")
		title, snippet := readArtifactContent(summaryPath)
		out = append(out, TeamArtifact{
			Doc:     name,
			Title:   discussionTitle(name, title),
			Kind:    "discussion",
			Snippet: snippet,
			Receipt: filepath.Join(dir, name),
		})
	}
	return out
}

// discussionTitle prefers the summary's own title, falling back to a readable
// form of the dir name (which encodes date + author).
func discussionTitle(dirName, summaryTitle string) string {
	if summaryTitle != "" {
		return summaryTitle
	}
	return dirName
}

// memorySubstance returns the count of substantive (non-blank, non-comment,
// non-heading) lines in MEMORY.md — a cheap proxy for "has the distillation
// pipeline actually filled team memory, or is it still the scaffold?".
func memorySubstance(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<!--") {
			continue
		}
		n++
	}
	return n
}
