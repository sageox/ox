package recap

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/teamdocs"
)

// artifactFile is a resolved team-context artifact on disk.
type artifactFile struct {
	Title string
	Path  string
}

// knownRootDocs maps the well-known team-context artifacts that live at the
// team root (or a fixed sub-path) rather than under docs/. Keyed by lowercase
// basename to match provided-event doc names case-insensitively.
var knownRootDocs = map[string]string{
	"memory.md":                "MEMORY.md",
	"agents.md":                "AGENTS.md",
	"claude.md":                "CLAUDE.md",
	"soul.md":                  "SOUL.md",
	"team.md":                  "TEAM.md",
	"distilled-discussions.md": filepath.Join("agent-context", "distilled-discussions.md"),
}

// resolveArtifacts builds a lookup from doc basename to its on-disk file,
// merging the docs/ catalog (via teamdocs, which already parses titles) with
// the known root artifacts. Missing team path or docs dir is not an error.
func resolveArtifacts(teamPath string) map[string]artifactFile {
	out := map[string]artifactFile{}
	if teamPath == "" {
		return out
	}

	// docs/ catalog — titles already resolved from frontmatter / first H1
	docs, _ := teamdocs.DiscoverDocs(teamPath)
	for _, d := range docs {
		out[strings.ToLower(d.Name)] = artifactFile{Title: d.Title, Path: d.Path}
	}

	// well-known root artifacts
	for key, rel := range knownRootDocs {
		if _, exists := out[key]; exists {
			continue
		}
		p := filepath.Join(teamPath, rel)
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			out[key] = artifactFile{Path: p}
		}
	}
	return out
}

// buildArtifactReaches turns the trace aggregate into the ordered ArtifactReach
// list, reading each doc's real title and a quotable snippet from disk. Docs
// that reached the user but can no longer be resolved on disk are still
// reported by name (with an empty receipt) — honest, not hidden.
func buildArtifactReaches(scan traceScan, teamPath string) []ArtifactReach {
	files := resolveArtifacts(teamPath)

	var reaches []ArtifactReach
	for _, dr := range scan.sorted() {
		if len(reaches) >= maxArtifacts {
			break
		}
		r := ArtifactReach{
			Doc:         dr.Doc,
			Source:      dr.Source,
			Sessions:    len(dr.SessionSet),
			SampleWork:  dr.Samples,
			LatestReach: dr.Latest,
		}
		if af, ok := files[strings.ToLower(dr.Doc)]; ok {
			r.Title = af.Title
			r.Receipt = af.Path
			title, snippet := readArtifactContent(af.Path)
			if r.Title == "" {
				r.Title = title
			}
			r.Snippet = snippet
		}
		reaches = append(reaches, r)
	}
	return reaches
}

// readArtifactContent reads a markdown artifact and returns its title (from
// frontmatter `title:` or the first H1) and a quotable snippet drawn from the
// first substantive body lines. Returns empty strings on any read error.
func readArtifactContent(path string) (title, snippet string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	body := stripFrontmatter(string(data), &title)

	var quoteLines []string
	total := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(quoteLines) > 0 {
				break // end of the first substantive block
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if title == "" {
				title = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
			}
			continue // headings frame, they don't quote well
		}
		if strings.HasPrefix(trimmed, "<!--") {
			continue // HTML comments (pipeline markers) are noise
		}
		if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "---") {
			continue // markdown table rows / separators don't quote as prose
		}
		quoteLines = append(quoteLines, trimmed)
		total += len(trimmed)
		if total >= snippetMax {
			break
		}
	}
	snippet = truncate(strings.Join(quoteLines, " "), snippetMax)
	return title, snippet
}

// stripFrontmatter removes a leading YAML frontmatter block, capturing its
// `title:` into titleOut when present, and returns the remaining body.
func stripFrontmatter(content string, titleOut *string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	lines := strings.Split(content, "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		if titleOut != nil && *titleOut == "" {
			if v, ok := frontmatterValue(lines[i], "title"); ok {
				*titleOut = v
			}
		}
	}
	if end < 0 {
		return content // unterminated frontmatter — treat whole thing as body
	}
	return strings.Join(lines[end+1:], "\n")
}

// frontmatterValue extracts a simple `key: value` from a frontmatter line.
func frontmatterValue(line, key string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	prefix := key + ":"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	v := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	v = strings.Trim(v, `"'`)
	if v == "" || v == ">-" || v == "|" {
		return "", false // multi-line scalar — skip, not worth the complexity
	}
	return v, true
}

// truncate caps s at n runes, appending an ellipsis when it cuts.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
