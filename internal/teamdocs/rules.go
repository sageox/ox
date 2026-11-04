package teamdocs

import (
	"bytes"
	"fmt"
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
// Subdirectories under agents/rules/ are walked recursively so a local session
// still finds a nested rule, but the supported layout is flat, and RuleWarnings
// reports anything nested.
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

	// withheldReason is non-empty when the frontmatter could not be read safely.
	// PublishedRules drops such a rule, so nothing that delivers a rule can see one.
	withheldReason string
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
// are walked recursively, mirroring how Claude Code walks .claude/rules/; see
// RuleWarnings for why a nested rule is still worth flagging.
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
	rules, err := scanRules(teamPath)
	if err != nil {
		return nil, err
	}
	published := rules[:0]
	for _, r := range rules {
		if r.withheldReason == "" {
			published = append(published, r)
		}
	}
	return published, nil
}

// scanRules is PublishedRules plus the rules withheld over unreadable frontmatter,
// which only the warnings need.
func scanRules(teamPath string) ([]TeamRule, error) {
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

// Advisory rule-file budgets, in bytes. ox prime counts tokens as bytes/4, so these
// are roughly 500 and 2,000 tokens.
const (
	// An always-on rule is paid in every session and shares prime's roughly 10 KB
	// hook budget with everything else ox delivers, so one rule stays near a fifth
	// of it.
	alwaysRuleBudgetBytes = 2 * 1024
	// An on-demand rule costs nothing until it is needed, then competes with the
	// task it was loaded for. Past this it usually holds more than one concern.
	onDemandRuleBudgetBytes = 8 * 1024
)

// RuleWarnings lists problems with the rules a team publishes, one line per problem.
// A rule withheld over unreadable frontmatter comes first, because that rule reaches
// nobody and the bound on these lines must not cut it. The rest are advisory, in
// RelPath order: they ask the author to fix the source, and delivery carries on.
//
// It starts from the scan, not the list prime delivers, because a rule with a native
// projection is dropped from that list while its cost remains. Sizes come from the
// file on disk, frontmatter included, which is cheap and close enough for an
// advisory.
func RuleWarnings(teamPath string) ([]string, error) {
	withheld, advisory, err := ruleWarnings(teamPath)
	return append(withheld, advisory...), err
}

// WithheldRuleWarnings lists only the rules that reach nobody. A compact re-prime
// carries these and not the advisories: a rule that broke since the full prime has
// no other way to be reported, while size advice repeats what was already said.
func WithheldRuleWarnings(teamPath string) ([]string, error) {
	withheld, _, err := ruleWarnings(teamPath)
	return withheld, err
}

func ruleWarnings(teamPath string) (withheld, advisory []string, err error) {
	rules, err := scanRules(teamPath)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rules {
		rel := filepath.ToSlash(r.RelPath)
		if r.withheldReason != "" {
			withheld = append(withheld, rel+": withheld from every session until its frontmatter is fixed: "+r.withheldReason)
			continue
		}
		if strings.Contains(rel, "/") {
			advisory = append(advisory, rel+": nested in a subdirectory, so it may not reach cloud sessions; move it to the top of the rules directory")
		}
		info, statErr := os.Stat(r.AbsPath)
		if statErr != nil {
			continue // a rule that vanished mid-walk has no size worth reporting
		}
		budget, tier := ruleBudget(r)
		if info.Size() > budget {
			advisory = append(advisory, fmt.Sprintf("%s: %d B, over the %d B budget for %s rules", rel, info.Size(), budget, tier))
		}
	}
	return withheld, advisory, nil
}

// ruleBudget picks a rule's tier by when it is paid for. globs: outranks
// visibility: always because the matcher owns activation (see ModeForAgent in
// internal/teamrules), so a scoped rule is never paid in every session.
func ruleBudget(r TeamRule) (budget int64, tier string) {
	if r.Visibility == VisibilityAlways && len(r.Globs) == 0 {
		return alwaysRuleBudgetBytes, "always-on"
	}
	return onDemandRuleBudgetBytes, "on-demand"
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
		scope := ruleScope(fm)
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
			Repos:          scope.Repos,
			Globs:          scope.Globs,
			Audience:       fm.Audience,
			Visibility:     fm.Visibility,
			Status:         fm.Status,
			FromDiscussion: fm.FromDiscussion,
			withheldReason: scope.Reason,
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
	Scope          Scope
	Audience       string
	Visibility     string
	Status         string
	FromDiscussion string
	ValidThrough   string
	// Requires and RequiresDeclared carry a skill's `requires:` key (rules
	// ignore it). See SkillRequirements for the semantics.
	Requires         []string
	RequiresDeclared bool
	// foreignScopeKey is a scope key another tool's format uses; see ruleScope.
	foreignScopeKey string
	// unreadable separates a file that could not be read from frontmatter that could
	// not be understood. A skill's approval flow reports the first in its own terms.
	unreadable bool
}

// parseRuleFrontmatter reads a manifest from disk. An unreadable file comes back
// Invalid rather than empty, because empty would mean "everywhere".
func parseRuleFrontmatter(path string) ruleFrontmatter {
	f, err := os.Open(path)
	if err != nil {
		return ruleFrontmatter{Scope: invalidScope("file cannot be read"), unreadable: true}
	}
	defer f.Close()
	return parseRuleFrontmatterFrom(f)
}

// parseRuleFrontmatterFrom is parseRuleFrontmatter over any reader, so a
// manifest held in memory (`ox skills publish` reads the repository copy
// before it exists in any Team Context) parses with exactly the same rules.
func parseRuleFrontmatterFrom(r io.Reader) ruleFrontmatter {
	// Discovery reads every published rule, drafts and other repos' rules included, so
	// a Team Context writer must not be able to make it allocate a whole large file.
	// Frontmatter is all this needs; a block that does not close within the bound is
	// withheld, never read as unscoped.
	data, err := io.ReadAll(io.LimitReader(r, maxFrontmatterReadBytes+1))
	if err != nil {
		return ruleFrontmatter{Scope: invalidScope("file cannot be read"), unreadable: true}
	}
	if len(data) > maxFrontmatterReadBytes {
		// The bound can cut a line in half, and a cut "----" reads as a closing
		// fence. Drop the partial line so only whole lines decide.
		data = data[:maxFrontmatterReadBytes]
		if cut := bytes.LastIndexByte(data, '\n'); cut >= 0 {
			data = data[:cut+1]
		}
		if _, state := splitFrontmatter(string(data)); state == frontmatterUnclosed {
			return ruleFrontmatter{Scope: invalidScope("frontmatter does not close within %d bytes", maxFrontmatterReadBytes)}
		}
	}
	return parseManifest(string(data))
}

// maxFrontmatterReadBytes bounds what discovery reads from one rule. Real frontmatter
// is under 2 KiB; 64 KiB leaves room for a long description and stays cheap.
const maxFrontmatterReadBytes = 64 << 10

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

// parseGlobs parses a globs: value that YAML read as one string, which is the bare
// comma-separated form some agents' rule formats use (Cursor's globs:, Copilot's
// applyTo:). A value that YAML read as a list never reaches this.
//
// Accepting it is deliberate. An author copying a rule out of .cursor/rules
// writes `globs: **/*.go,**/*.mod`, and rejecting that would mean the rule
// silently applies everywhere — the failure mode is an unscoped rule loading in
// every session, which is exactly what globs: exists to prevent.
//
// The outer quotes come off first because a quoted scalar here is still the comma
// form: ox writes `globs: "a,b"` into Cursor's projections, and a rule copied back
// from one must keep both patterns.
func parseGlobs(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		return parseInlineList(v)
	}
	var out []string
	for _, part := range splitListItems(strings.Trim(v, `"'`)) {
		if trimmed := strings.Trim(strings.TrimSpace(part), `"'`); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// splitListItems splits on the commas that separate list items. A comma inside
// quotes or a brace group belongs to the item: cutting src/*.{ts,tsx} in two leaves
// a pair of patterns that match nothing, so the rule silently never loads.
func splitListItems(v string) []string {
	var items []string
	start, braces := 0, 0
	var quote byte
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		// Only a quote that opens an item counts, so an apostrophe inside a bare
		// path cannot swallow the commas after it.
		case (c == '"' || c == '\'') && strings.TrimSpace(v[start:i]) == "":
			quote = c
		case c == '{':
			braces++
		case c == '}' && braces > 0:
			braces--
		case c == ',' && braces == 0:
			items = append(items, v[start:i])
			start = i + 1
		}
	}
	return append(items, v[start:])
}

// parseInlineList parses a YAML inline list like ["a", "b", "c"] or [a, b], or a
// single bare value, for the requires: lines YAML could not read.
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
	parts := splitListItems(inner)
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
