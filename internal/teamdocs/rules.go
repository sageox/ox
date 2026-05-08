package teamdocs

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TeamRule represents a single team-context behavioral rule discovered under
// agents/rules/ (preferred) or coworkers/rules/ (legacy fallback).
//
// Team rules apply to every supported AI coding agent (Claude, Codex, Amp,
// Cursor, etc.) used by teammates running ox. They are the team-scope cousin
// of Claude's .claude/rules/<topic>.md — modular, one concern per file, and
// loaded into agent context via ox agent prime.
//
// Subdirectories under agents/rules/ are walked recursively. The relative
// path is preserved so organization (e.g., backend/postgres.md) stays
// meaningful for navigation and dedupe.
type TeamRule struct {
	Name            string   `json:"name"`                       // kebab-case identifier; from frontmatter or filename
	Description     string   `json:"description,omitempty"`      // one-line summary
	RelPath         string   `json:"rel_path"`                   // path relative to rules root (e.g., "backend/postgres.md")
	AbsPath         string   `json:"abs_path"`                   // absolute path on disk
	Repos           []string `json:"repos,omitempty"`            // repo filter; empty = all team repos
	Audience        string   `json:"audience,omitempty"`         // "ai" | "human" | "both" (default: "ai")
	Visibility      string   `json:"visibility"`                 // "always" | "indexed" | "hidden"
	Status          string   `json:"status,omitempty"`           // "active" | "draft" | "superseded-by:<other>"
	FromDiscussion  string   `json:"from_discussion,omitempty"`  // optional provenance link
	Body            string   `json:"body,omitempty"`             // markdown body (excluding frontmatter); only populated for visibility=always
	EstimatedTokens int      `json:"estimated_tokens,omitempty"` // rough estimate (len(body)/4); only set when Body is loaded
}

// Rule status values.
const (
	RuleStatusActive = "active"
	RuleStatusDraft  = "draft"
	// RuleStatusSupersededPrefix is matched as a prefix on status values like
	// "superseded-by:<other-rule-name>".
	RuleStatusSupersededPrefix = "superseded-by:"
)

// Rule audience values.
const (
	RuleAudienceAI    = "ai"
	RuleAudienceHuman = "human"
	RuleAudienceBoth  = "both"
)

// DefaultRuleAudience is what's assumed when frontmatter omits the field.
const DefaultRuleAudience = RuleAudienceAI

// DefaultRuleVisibility is what's assumed when frontmatter omits the field.
// "indexed" — much closer to Claude's paths-deferred lazy loading than
// "always" is. Loading every team rule into prime would silently bloat
// context as the rule library grows; opting in to "always" forces teams
// to think about cost.
const DefaultRuleVisibility = VisibilityIndexed

// DiscoverRules walks the team context for behavioral rule files under
// agents/rules/**/*.md (preferred) and coworkers/rules/**/*.md (legacy
// fallback for any teams that adopted that location early). Subdirectories
// are walked recursively, mirroring how Claude Code walks .claude/rules/.
//
// Rules with audience: human are excluded (this scanner is for agent
// consumption). Rules with status: draft and visibility: hidden are
// excluded. Rules with status starting with superseded-by: are excluded.
//
// If repoSlug is non-empty, rules with a non-empty repos: list that does
// NOT contain repoSlug are excluded. Empty/absent repos: means the rule
// applies to all team repos.
//
// For visibility: always rules, the markdown body (excluding frontmatter)
// is loaded into TeamRule.Body so the prime emitter can inline it. For
// indexed rules, only metadata is returned; the agent reads the file on
// demand from AbsPath.
//
// Results are sorted by RelPath for stable output.
//
// TODO(sync-out): A future daemon-driven optimization could sync filtered,
// repo-scoped team rules OUT of team context and into each cloned repo's
// agent-native rules directory (e.g., .claude/sageox-team-<slug>/rules/
// for Claude, .cursor/rules/sageox-team-<slug>/ for Cursor). That would
// let team rules participate in the agent's NATIVE rule-loading machinery
// (Claude's `paths:`-scoped lazy loading, Cursor's `globs:` matching,
// etc.) instead of being delivered up front via prime XML.
//
// Implementation considerations when picking this up:
//
//  1. Adapter dispatch — each agent has its own rules dir convention.
//     The pkg/adapterprotocol RulesParams + handleInstallRules pattern
//     already abstracts this (cmd/ox-adapter-claude-code/rules.go is the
//     reference implementation). Sync-out would extend the rule file
//     list passed to handleInstallRules with discovered team rules.
//
//  2. Namespace — write to a sageox-team-<slug>/ subdirectory under the
//     agent's rules dir, never directly into .claude/rules/ root, to
//     avoid colliding with project-local rules of the same name and to
//     make cleanup unambiguous.
//
//  3. Lifecycle — daemon writes on team-context sync (after pull), and
//     on repo-open. Daemon removes files for rules that disappear from
//     team context (true mirror, not append-only). At prime time the
//     sync state is read-only.
//
//  4. Filtering — apply the same repos:/audience:/status: filters here
//     that DiscoverRules already applies, so a teammate working in
//     repo A doesn't get repo B's rules synced into their working tree.
//
//  5. Multi-adapter coverage — today only ox-adapter-claude-code and
//     ox-adapter-droid implement handleInstallRules. Other adapters
//     (codex, amp, aider, gemini, opencode, pi) need rules.go before
//     they can participate. Tracking the gap is the prerequisite for
//     a uniform sync-out story.
//
//  6. Per-user knowledge bubble — when the user-context bubble lands,
//     sync-out for user rules has the same shape but reads from the
//     user-context location and tags entries with audience: ai,
//     source: user (so the budget accounting in agent_prime_xml.go
//     attributes them correctly to BudgetSourceUser).
//
// Not implemented yet; this comment IS the design memo until someone
// picks up the work.
func DiscoverRules(teamPath, repoSlug string) ([]TeamRule, error) {
	if teamPath == "" {
		return nil, nil
	}

	var rules []TeamRule

	for _, rulesRoot := range []string{"agents/rules", "coworkers/rules"} {
		root := filepath.Join(teamPath, rulesRoot)
		discovered, err := walkRulesDir(root)
		if err != nil {
			return nil, err
		}
		rules = append(rules, discovered...)
	}

	// dedupe by Name (preferred location wins, since agents/rules is walked first)
	seen := make(map[string]bool, len(rules))
	deduped := rules[:0]
	for _, r := range rules {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		deduped = append(deduped, r)
	}
	rules = deduped

	// filter by audience, status, visibility, and repos
	filtered := rules[:0]
	for _, r := range rules {
		if r.Audience == RuleAudienceHuman {
			continue
		}
		if r.Visibility == VisibilityHidden {
			continue
		}
		if r.Status == RuleStatusDraft {
			continue
		}
		if strings.HasPrefix(r.Status, RuleStatusSupersededPrefix) {
			continue
		}
		if !ruleAppliesToRepo(r, repoSlug) {
			continue
		}
		filtered = append(filtered, r)
	}
	rules = filtered

	// load body + estimate tokens for visibility: always
	for i := range rules {
		if rules[i].Visibility != VisibilityAlways {
			continue
		}
		body, err := readRuleBody(rules[i].AbsPath)
		if err != nil {
			// missing or unreadable bodies are skipped silently — prime
			// must never fail because a single rule file is malformed
			continue
		}
		rules[i].Body = body
		rules[i].EstimatedTokens = estimateTokens(body)
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].RelPath < rules[j].RelPath
	})

	return rules, nil
}

// walkRulesDir recursively walks a rules root directory. Each rule's
// RelPath is computed relative to absRoot. Missing directories are not an
// error.
func walkRulesDir(absRoot string) ([]TeamRule, error) {
	var rules []TeamRule

	info, err := os.Stat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// skip unreadable subtrees rather than failing the whole walk
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		// README.md is always treated as directory-instructions, not a rule
		if strings.EqualFold(name, "readme.md") {
			return nil
		}

		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			rel = name
		}

		fm := parseRuleFrontmatter(path)
		if fm.Name == "" {
			fm.Name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if fm.Visibility == "" {
			fm.Visibility = DefaultRuleVisibility
		}
		if fm.Audience == "" {
			fm.Audience = DefaultRuleAudience
		}
		if fm.Status == "" {
			fm.Status = RuleStatusActive
		}

		rules = append(rules, TeamRule{
			Name:           fm.Name,
			Description:    fm.Description,
			RelPath:        rel,
			AbsPath:        path,
			Repos:          fm.Repos,
			Audience:       fm.Audience,
			Visibility:     fm.Visibility,
			Status:         fm.Status,
			FromDiscussion: fm.FromDiscussion,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	return rules, nil
}

// ruleAppliesToRepo reports whether a rule's repos: filter matches the
// current repo slug. Empty repos: means "all team repos." Empty repoSlug
// means we don't know what repo we're in — be permissive and include
// non-filtered rules so the agent still gets team-wide guidance.
func ruleAppliesToRepo(r TeamRule, repoSlug string) bool {
	if len(r.Repos) == 0 {
		return true
	}
	if repoSlug == "" {
		// can't filter; include only rules without a repos: list
		return false
	}
	for _, want := range r.Repos {
		if strings.EqualFold(strings.TrimSpace(want), repoSlug) {
			return true
		}
	}
	return false
}

// readRuleBody returns the markdown content of a rule file with any leading
// YAML frontmatter stripped.
func readRuleBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := string(data)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return s, nil
	}
	// find the closing --- on its own line
	rest := s
	// drop opening fence
	if strings.HasPrefix(rest, "---\r\n") {
		rest = rest[5:]
	} else {
		rest = rest[4:]
	}
	// look for closing fence at start of a line
	for {
		i := strings.Index(rest, "\n---")
		if i < 0 {
			return strings.TrimLeft(rest, "\r\n"), nil
		}
		// must be followed by end-of-line (or EOF)
		after := i + 4
		if after >= len(rest) || rest[after] == '\n' || rest[after] == '\r' {
			rest = rest[after:]
			rest = strings.TrimLeft(rest, "\r\n")
			return rest, nil
		}
		// not a real fence (e.g. "---something"), keep looking
		rest = rest[i+1:]
	}
}

// estimateTokens is a cheap rule-of-thumb estimator (len/4). Avoids pulling
// in a tokenizer dependency for what is just a hygiene signal.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s) / 4
}

// ruleFrontmatter holds parsed YAML frontmatter fields specific to team rules.
type ruleFrontmatter struct {
	Name           string
	Description    string
	Repos          []string
	Audience       string
	Visibility     string
	Status         string
	FromDiscussion string
}

// parseRuleFrontmatter does minimal YAML extraction sufficient for the rule
// frontmatter spec. Mirrors the line-based approach in frontmatter.go to
// avoid a YAML dep. Multi-line scalars and complex YAML are not supported;
// rule frontmatter is intentionally simple.
func parseRuleFrontmatter(path string) ruleFrontmatter {
	f, err := os.Open(path)
	if err != nil {
		return ruleFrontmatter{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	lineCount := 0
	var fm ruleFrontmatter

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		if lineCount == 1 {
			if line != "---" {
				return ruleFrontmatter{}
			}
			inFrontmatter = true
			continue
		}
		if inFrontmatter && line == "---" {
			break
		}
		if !inFrontmatter {
			break
		}

		switch {
		case strings.HasPrefix(line, "name:"):
			fm.Name = extractValue(line, "name:")
		case strings.HasPrefix(line, "description:"):
			fm.Description = extractValue(line, "description:")
		case strings.HasPrefix(line, "audience:"):
			fm.Audience = extractValue(line, "audience:")
		case strings.HasPrefix(line, "visibility:"):
			fm.Visibility = extractValue(line, "visibility:")
		case strings.HasPrefix(line, "status:"):
			fm.Status = extractValue(line, "status:")
		case strings.HasPrefix(line, "from-discussion:"):
			fm.FromDiscussion = extractValue(line, "from-discussion:")
		case strings.HasPrefix(line, "repos:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "repos:"))
			fm.Repos = parseInlineList(val)
		}

		if lineCount > 30 {
			break
		}
	}

	return fm
}

// parseInlineList parses a YAML inline list like ["a", "b", "c"] or [a, b].
// Handles only the inline (flow) form — the block form (- item) is not
// supported here for simplicity. Rule frontmatter authors using the block
// form will have repos: = nil, which means "all repos" — a safer default
// than silently dropping the filter.
func parseInlineList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "[]" {
		return nil
	}
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		// could be a single bare value: repos: foo/bar
		return []string{strings.Trim(v, `"'`)}
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
