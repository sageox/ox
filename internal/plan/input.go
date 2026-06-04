package plan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// h2Pattern matches a markdown H2 heading at the start of a line ("## Heading").
// Content before the first H2 forms a preamble section with an empty Heading.
var h2Pattern = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)

// fileRefPattern extracts file references from a plan section. Two shapes:
//   - backtick-quoted paths: `internal/plan/input.go`, `cmd/ox/plan.go`
//   - path:line refs: internal/plan/input.go:42
//
// Kept deliberately conservative — a reference must look path-like (contain a
// slash or a known source extension) to avoid matching prose in backticks.
var fileRefPattern = regexp.MustCompile(
	"`([^`\\n]+)`" + // group 1: backtick-quoted span
		"|" +
		`\b([\w./-]+\.[A-Za-z0-9]+(?::\d+)?)\b`, // group 2: path.ext or path.ext:line
)

// pathLike reports whether a candidate extracted reference looks like a file
// path rather than incidental prose. Requires either a directory separator or a
// recognizable source-file extension.
var pathLikePattern = regexp.MustCompile(`(/|\.[A-Za-z0-9]{1,6}(?::\d+)?$)`)

// Resolve reads a plan from --file if set, otherwise from a piped stdin, and
// parses it into an Input. When neither is provided it best-effort auto-discovers
// the active plan-mode file: the newest *.md under ~/.claude/plans/ (the dir
// Claude Code plan-mode writes to). Precedence is --file > piped stdin >
// auto-discovery. An empty/unfound source yields an Input with empty Raw and no
// sections (the enrich path must stay fail-open on empty input); the caller is
// expected to surface a clear "no plan found" message rather than enrich nothing.
func Resolve(file string, stdin io.Reader) (Input, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return Input{}, fmt.Errorf("read plan file %q: %w", file, err)
		}
		in := Parse(string(data))
		in.Path = file
		return in, nil
	}

	// A piped stdin takes precedence over auto-discovery: agents pipe the plan
	// explicitly. A nil reader (no pipe) means the human porcelain path, where
	// we fall through to auto-discovery.
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return Input{}, fmt.Errorf("read plan from stdin: %w", err)
		}
		// Only treat stdin as the source when it actually carried content; an
		// empty pipe (e.g. a TTY reported as a non-nil but empty reader) should
		// still fall through to auto-discovery.
		if strings.TrimSpace(string(data)) != "" {
			return Parse(string(data)), nil
		}
	}

	// Auto-discovery: newest plan-mode markdown file, if any. Best-effort —
	// a missing dir / no files yields an empty Input (unchanged behavior).
	if path := discoverActivePlanFile(planModeDir()); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			in := Parse(string(data))
			in.Path = path
			return in, nil
		}
	}

	return Parse(""), nil
}

// planModeDirOverride lets tests point auto-discovery at a temp directory
// without touching the real ~/.claude/plans. Empty means "use the default".
var planModeDirOverride string

// planModeDir returns the Claude Code plan-mode directory (~/.claude/plans),
// or the test override when set. Returns "" when the home dir is unresolvable
// so discovery degrades to a no-op.
func planModeDir() string {
	if planModeDirOverride != "" {
		return planModeDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "plans")
}

// discoverActivePlanFile returns the absolute path of the newest *.md file in
// dir (by modification time), or "" when dir is empty, missing, or holds no
// markdown. Best-effort: any read error degrades to "" rather than aborting.
func discoverActivePlanFile(dir string) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var newest string
	var newestMod int64 = -1
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); mod > newestMod {
			newestMod = mod
			newest = filepath.Join(dir, entry.Name())
		}
	}
	return newest
}

// Parse splits markdown into Sections on "## " H2 headings and extracts the file
// references cited in each section. Content before the first H2 becomes a
// preamble Section with an empty Heading (only emitted when it has content).
func Parse(raw string) Input {
	in := Input{Raw: raw}
	if strings.TrimSpace(raw) == "" {
		return in
	}

	locs := h2Pattern.FindAllStringSubmatchIndex(raw, -1)

	// preamble: everything before the first H2 heading.
	firstHeadingStart := len(raw)
	if len(locs) > 0 {
		firstHeadingStart = locs[0][0]
	}
	if preamble := strings.TrimSpace(raw[:firstHeadingStart]); preamble != "" {
		in.Sections = append(in.Sections, newSection("", preamble))
	}

	for i, loc := range locs {
		heading := strings.TrimSpace(raw[loc[2]:loc[3]])
		bodyStart := loc[1]
		bodyEnd := len(raw)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		body := strings.TrimSpace(raw[bodyStart:bodyEnd])
		in.Sections = append(in.Sections, newSection(heading, body))
	}

	return in
}

// newSection builds a Section, extracting file references from heading+body.
func newSection(heading, body string) Section {
	return Section{
		Heading: heading,
		Body:    body,
		Files:   extractFileRefs(heading + "\n" + body),
	}
}

// extractFileRefs pulls path-like file references out of text, deduped and
// sorted for deterministic output.
func extractFileRefs(text string) []string {
	matches := fileRefPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	var refs []string
	for _, m := range matches {
		candidate := m[1]
		if candidate == "" {
			candidate = m[2]
		}
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if !pathLikePattern.MatchString(candidate) {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		refs = append(refs, candidate)
	}

	sort.Strings(refs)
	return refs
}
