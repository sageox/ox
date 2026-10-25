package teamdocs

import (
	"bufio"
	"io"
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
// delivered through one adapter-selected path: an ignored native projection
// where the agent can preserve the rule's semantics, otherwise ox agent prime.
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
	Globs           []string `json:"globs,omitempty"`            // path scope; empty = applies to any file
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

// RuleRoots are the canonical and legacy Team Context rule locations. Every
// discovery and blindness check shares this list so one can never walk a root
// the other silently ignores.
var RuleRoots = []string{"agents/rules", "coworkers/rules"}

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
// Native projection is a derived cache, never the source of truth. The
// teamrules package owns its reserved *-team namespace and retires
// projections when a rule is removed or filtered out. Prime suppresses only
// the active agent's existing projection, preserving exactly-one delivery.
func DiscoverRules(teamPath, repoSlug string) ([]TeamRule, error) {
	published, err := PublishedRules(teamPath)
	if err != nil {
		return nil, err
	}

	rules := published[:0]
	for _, r := range published {
		if RuleAppliesToRepo(r, repoSlug) {
			rules = append(rules, r)
		}
	}

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

	return rules, nil
}

// PublishedRules returns every rule the team publishes to AI coworkers, before
// the per-repo `repos:` filter is applied and without loading bodies.
//
// Split out from DiscoverRules for the same reason PublishedSkills was: a
// human asking "which repos does this rule reach?" needs the list BEFORE the
// filter has removed the answer. The audience/visibility/status filters are not
// repo-specific and stay here — a draft or human-only rule is not published
// anywhere, so listing it as "available elsewhere" would be wrong too.
func PublishedRules(teamPath string) ([]TeamRule, error) {
	if teamPath == "" {
		return nil, nil
	}

	var rules []TeamRule

	for _, rulesRoot := range RuleRoots {
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

	// filter by audience, status, and visibility — never by repo
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
		filtered = append(filtered, r)
	}
	rules = filtered

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].RelPath < rules[j].RelPath
	})

	return rules, nil
}

// AnyRuleRootOnDisk reports whether sparse checkout materialized a parent that
// rule discovery can authoritatively treat as empty. The parent (agents/ or
// coworkers/) is the sparse-set unit, matching Team Skill retention behavior.
func AnyRuleRootOnDisk(teamPath string) bool {
	for _, root := range RuleRoots {
		parent := filepath.Dir(filepath.FromSlash(root))
		if info, err := os.Stat(filepath.Join(teamPath, parent)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
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
		// Team Context is shared input. Never follow a rule-file symlink: its
		// target can escape the clone and turn arbitrary local files into agent
		// instructions or native projections.
		if d.Type()&os.ModeSymlink != 0 {
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
			Globs:          fm.Globs,
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

// RuleAppliesToRepo reports whether a rule's repos: filter matches the
// current repo slug. Empty repos: means "all team repos." Empty repoSlug
// means we don't know what repo we're in — be permissive and include
// non-filtered rules so the agent still gets team-wide guidance.
func RuleAppliesToRepo(r TeamRule, repoSlug string) bool {
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
	return ManifestBody(string(data)), nil
}

// ManifestBody returns a rule or skill manifest's markdown with any leading
// YAML frontmatter stripped. An unterminated frontmatter returns everything
// after the opening fence, so no part of the document is ever silently dropped
// from what a reader (or a body scan) sees.
func ManifestBody(s string) string {
	s = trimBOM(s)
	first, rest, found := strings.Cut(s, "\n")
	if !found || !IsFrontmatterFence(first) {
		return s
	}
	// The closing fence is the next line IsFrontmatterFence accepts; a line
	// such as "---something" is content, not a fence.
	for offset := 0; offset <= len(rest); {
		line, _, _ := strings.Cut(rest[offset:], "\n")
		if IsFrontmatterFence(line) {
			return strings.TrimLeft(rest[offset+len(line):], "\r\n")
		}
		offset += len(line) + 1
	}
	return strings.TrimLeft(rest, "\r\n")
}

// ReadRuleBody returns a Team Rule's markdown body without YAML frontmatter.
// Native projection uses the same parser as prime so the two delivery modes can
// never disagree about which bytes constitute the rule.
func ReadRuleBody(path string) (string, error) {
	return readRuleBody(path)
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
	Globs          []string
	Audience       string
	Visibility     string
	Status         string
	FromDiscussion string
	ValidThrough   string
	// Requires and RequiresDeclared carry a skill's `requires:` key (rules
	// ignore it). See SkillRequirements for the semantics.
	Requires         []string
	RequiresDeclared bool
	requiresBlock    bool // inside a `requires:` block list (`  - shell` lines)
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
	return parseRuleFrontmatterFrom(f)
}

// parseRuleFrontmatterFrom is parseRuleFrontmatter over any reader, so a
// manifest held in memory (`ox skills publish` reads the repository copy
// before it exists in any Team Context) parses with exactly the same rules.
func parseRuleFrontmatterFrom(r io.Reader) ruleFrontmatter {
	scanner := bufio.NewScanner(r)
	lineCount := 0
	var fm ruleFrontmatter

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		if lineCount == 1 {
			if !IsFrontmatterFence(trimBOM(line)) {
				return ruleFrontmatter{}
			}
			continue
		}
		if IsFrontmatterFence(line) {
			break
		}

		// A `requires:` block list is the one multi-line form read here.
		if fm.requiresBlock {
			if item, isItem := blockListItem(line); isItem {
				if item != "" {
					fm.Requires = append(fm.Requires, item)
					fm.RequiresDeclared = true
				}
				continue
			}
			fm.requiresBlock = false
		}

		// requires: is read on EVERY frontmatter line, not only inside the
		// window below (trust contract v2, R4). It is the one key where a miss
		// fails open: a declaration pushed past line 30 by a long folded
		// description: read as "undeclared", so a skill whose author said it
		// needs a shell could be offered where there is none. Every other key
		// keeps the window, and with it the behavior teams already rely on.
		if strings.HasPrefix(line, "requires:") {
			fm.applyRequires(stripYAMLComment(strings.TrimSpace(strings.TrimPrefix(line, "requires:"))))
			continue
		}
		// The window: the 30 lines after the opening fence.
		if lineCount > 31 {
			continue
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
		case strings.HasPrefix(line, "valid-through:"):
			fm.ValidThrough = extractValue(line, "valid-through:")
		case strings.HasPrefix(line, "from-discussion:"):
			fm.FromDiscussion = extractValue(line, "from-discussion:")
		case strings.HasPrefix(line, "repos:"):
			fm.Repos = parseInlineList(stripYAMLComment(strings.TrimSpace(strings.TrimPrefix(line, "repos:"))))
		case strings.HasPrefix(line, "globs:"):
			fm.Globs = parseGlobs(stripYAMLComment(strings.TrimSpace(strings.TrimPrefix(line, "globs:"))))
		}
	}

	return fm
}

// applyRequires records a `requires:` value. An empty value opens a block list.
//
// The block form is read here, unlike for repos:, because the safe default
// points the other way. A block-form repos: read as absent means "every repo",
// which only widens where prose lands. A block-form requires: read as absent
// would let a skill whose author SAID it needs a shell be offered to chat hosts
// that have none. Mirrors packages/teamskills applyRequires in sageox-mono, so
// the server and the CLI classify the same manifest the same way.
func (fm *ruleFrontmatter) applyRequires(value string) {
	if value == "" {
		fm.requiresBlock = true
		return
	}
	fm.RequiresDeclared = true
	fm.Requires = parseInlineList(value)
}

// blockListItem parses one line of a YAML block sequence. The second return is
// false for any line that is not an item, which ends the list.
//
// Both indentations count (trust contract v2, R3). `requires:\n- shell`, with
// the dash at column 0, is what PyYAML and most formatters emit; reading it as
// "no declaration" let a skill whose author declared a shell requirement pass
// for portable. At column 0 only `-` or `- …` is an item, so a line such as
// "-foo" still ends the list; indented items keep the looser historical rule.
// An empty item (`-` alone) yields "" and keeps the list open.
func blockListItem(line string) (string, bool) {
	if line == "" {
		return "", false
	}
	trimmed := strings.TrimSpace(line)
	if line[0] == ' ' || line[0] == '\t' {
		if !strings.HasPrefix(trimmed, "-") {
			return "", false
		}
	} else if trimmed != "-" && !strings.HasPrefix(line, "- ") {
		return "", false
	}
	item := stripYAMLComment(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
	return strings.Trim(item, `"'`), true
}

// parseGlobs parses the globs: field, accepting both the inline-list form that
// repos: uses and the bare comma-separated form every other agent's rule format
// uses (Cursor's globs:, Copilot's applyTo:, Cline's paths:).
//
// Accepting both is deliberate. An author copying a rule out of .cursor/rules
// writes `globs: **/*.go,**/*.mod`, and rejecting that would mean the rule
// silently applies everywhere — the failure mode is an unscoped rule loading in
// every session, which is exactly what globs: exists to prevent.
func parseGlobs(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		return parseInlineList(v)
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if trimmed := strings.Trim(strings.TrimSpace(part), `"'`); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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
