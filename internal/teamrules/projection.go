// Package teamrules projects Team Context rules into agent-native rule roots
// when the target can preserve their semantics, and otherwise leaves delivery
// to ox agent prime.
package teamrules

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
)

// managedProjectionName reports whether a filename in a rule root is a Team Rule
// projection ox wrote — by NAME only; ownership still needs the stamp.
//
// Two shapes are accepted because two exist on disk. The current one is
// <slug>-<hash>-team<ext>: a suffix, matching Team Skills, so that every artifact
// ox projects out of Team Context is recognizable by one rule instead of one rule
// per artifact kind. The legacy one is the "sageox-team-" prefix, kept only so a
// repository that upgrades mid-reconcile can still have its old files swept.
func managedProjectionName(name, ext string) bool {
	if !strings.HasSuffix(name, ext) {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(name, ext), skillmanager.TeamSuffix) ||
		strings.HasPrefix(name, skillmanager.LegacyTeamPrefix)
}

// ignoreProbeName is the path managedPathIgnored asks git about to learn whether
// this root's projections are covered by an ignore rule. It must match the SAME
// glob a real projection matches, so it carries the suffix rather than a prefix.
func ignoreProbeName(ext string) string {
	return "probe" + skillmanager.TeamSuffix + ext
}

// ErrProjectionConflict marks a native rule path that ox cannot safely claim.
// The caller should surface it as settled human-action work, not retry it as an
// environmental failure.
var ErrProjectionConflict = errors.New("existing repository content blocks Team Rule projection")

type DeliveryMode string

const (
	DeliveryNative       DeliveryMode = "native"
	DeliveryPrimeInline  DeliveryMode = "prime-inline"
	DeliveryPrimeIndexed DeliveryMode = "prime-index"
)

// globStyle is how an agent's rule format holds more than one pattern.
type globStyle int

const (
	globCommaString globStyle = iota // one string, patterns joined with commas
	globBlockList                    // a YAML block sequence
	globFlowList                     // a YAML flow sequence on one line
)

type policy struct {
	Agent            string
	Root             string
	Extension        string
	GlobField        string
	GlobStyle        globStyle
	ScopedFields     []string // frontmatter lines a scoped rule also needs
	AlwaysApplyField string
}

// These mappings are agentx's documented native rule surfaces. A policy is
// activated only when its rule root already exists in the repository; ox never
// creates an unrelated agent's footprint during background convergence.
//
// A wrong key or shape is not an error anywhere: the agent ignores it and the rule
// either loads in every session or never loads. Claude Code's reading is observed
// by the live test under tests/agents. The others are taken from each agent's docs,
// because none of them has a headless mode to run here:
//
//   - Cursor: comma-separated globs and alwaysApply (cursor.com/docs/context/rules).
//     Brace groups are not documented.
//   - Copilot: applyTo as one quoted, comma-separated string
//     (docs.github.com, "Adding repository custom instructions"). Brace groups are
//     not documented.
//   - Cline: paths as a list, with {a,b} documented (docs.cline.bot, "Cline Rules").
//     Nested groups are not.
//   - Kiro: fileMatchPattern as a string or array, and only with
//     inclusion: fileMatch, because a steering file with no inclusion mode is always
//     included (kiro.dev/docs/steering). Brace groups are not documented.
//
// Every agent gets brace groups expanded. A glob such as src/{client,admin}/*.{ts,css}
// holds commas, so a comma field would cut it into fragments like src/{client. And
// Claude Code loads only part of a nested group: given x/{a,b/{c,d}}/** it loaded
// x/b/c and missed x/a and x/b/d. The as-written rows in
// tests/agents/testdata/glob-conformance.json record that. Flat groups read the same
// expanded or not on every row there, so expansion costs Claude nothing.
var policies = []policy{
	{Agent: "claude", Root: ".claude/rules", Extension: ".md", GlobField: "paths", GlobStyle: globBlockList},
	{Agent: "cursor", Root: ".cursor/rules", Extension: ".mdc", GlobField: "globs", AlwaysApplyField: "alwaysApply"},
	{Agent: "copilot", Root: ".github/instructions", Extension: ".md", GlobField: "applyTo"},
	{Agent: "cline", Root: ".clinerules", Extension: ".md", GlobField: "paths", GlobStyle: globBlockList},
	{Agent: "kiro", Root: ".kiro/steering", Extension: ".md", GlobField: "fileMatchPattern", GlobStyle: globFlowList, ScopedFields: []string{"inclusion: fileMatch"}},
	{Agent: "droid", Root: ".factory/rules", Extension: ".md"},
	{Agent: "windsurf", Root: ".windsurf/rules", Extension: ".md"},
}

type Fallback struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

type Result struct {
	NativeAgents map[string][]string   `json:"native_agents"`
	Fallbacks    map[string][]Fallback `json:"fallbacks"`
	Written      []string              `json:"written"`
	Removed      []string              `json:"removed"`
}

func newResult() Result {
	return Result{
		NativeAgents: map[string][]string{},
		Fallbacks:    map[string][]Fallback{},
		Written:      []string{},
		Removed:      []string{},
	}
}

// ModeForAgent chooses exactly one delivery mechanism. A globs-scoped rule is
// never flattened into an always-on native rule: agents without a native glob
// field, and patterns that field cannot hold unambiguously, receive an indexed
// prime entry instead. For capable agents, globs supersedes visibility because the
// native matcher owns activation.
func ModeForAgent(agent string, rule teamdocs.TeamRule) DeliveryMode {
	p, ok := policyFor(agent)
	if len(rule.Globs) > 0 {
		if ok && p.GlobField != "" {
			if _, writable := nativePatterns(rule.Globs, p); writable {
				return DeliveryNative
			}
		}
		return DeliveryPrimeIndexed
	}
	if rule.Visibility == teamdocs.VisibilityAlways {
		if ok {
			return DeliveryNative
		}
		return DeliveryPrimeInline
	}
	return DeliveryPrimeIndexed
}

// Reconcile mirrors the native-compatible subset into every agent rule root
// already present and protected by gitignore. Reserved files absent from desired
// state are removed when their content hash verifies that ox created them, so
// filtering and retirement converge without claiming hand-authored files that
// happen to use the reserved filename prefix.
func Reconcile(ctx context.Context, projectRoot string, rules []teamdocs.TeamRule) (Result, error) {
	result := newResult()
	// A per-file conflict in one policy's root (a hand-authored or tracked file
	// sitting at a projection's path) must not stop every LATER policy from
	// converging, so errors are accumulated across the whole table and reported
	// only once every policy has run. Kept separate from genuine I/O/git-check
	// failures, which stay retryable rather than settled — see the merge below.
	var conflictErrs []error
	var hardErrs []error
	for _, p := range policies {
		rootPath := filepath.Join(projectRoot, filepath.FromSlash(p.Root))
		info, err := os.Stat(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, fmt.Errorf("inspect %s Team Rule root: %w", p.Agent, err)
		}
		if !info.IsDir() {
			continue
		}

		desired := map[string][]byte{}
		protected := managedPathIgnored(ctx, projectRoot, filepath.ToSlash(filepath.Join(p.Root, ignoreProbeName(p.Extension))))
		for _, rule := range rules {
			mode := ModeForAgent(p.Agent, rule)
			if mode != DeliveryNative {
				if len(rule.Globs) > 0 {
					result.Fallbacks[rule.Name] = append(result.Fallbacks[rule.Name], Fallback{
						Agent: p.Agent, Reason: "native rule format cannot preserve globs; using the prime index",
					})
				}
				continue
			}
			if !protected {
				result.Fallbacks[rule.Name] = append(result.Fallbacks[rule.Name], Fallback{
					Agent: p.Agent, Reason: "native rule root exists but Team Rule files are not ignored by git",
				})
				continue
			}
			content, renderErr := render(rule, p)
			if renderErr != nil {
				return result, fmt.Errorf("render Team Rule %s for %s: %w", rule.Name, p.Agent, renderErr)
			}
			desired[nativeFilename(rule, p)] = content
			result.NativeAgents[rule.Name] = append(result.NativeAgents[rule.Name], p.Agent)
		}

		// Automatic convergence must not create unignored working-tree content.
		// When protection is missing, prime remains the delivery path and doctor
		// owns repairing the tracked ignore setup.
		if !protected {
			continue
		}
		written, removed, rErr := reconcileRoot(ctx, projectRoot, rootPath, p, desired)
		for _, name := range written {
			result.Written = append(result.Written, filepath.ToSlash(filepath.Join(p.Root, name)))
		}
		for _, name := range removed {
			result.Removed = append(result.Removed, filepath.ToSlash(filepath.Join(p.Root, name)))
		}
		if rErr != nil {
			wrapped := fmt.Errorf("reconcile %s Team Rules: %w", p.Agent, rErr)
			if errors.Is(rErr, ErrProjectionConflict) {
				conflictErrs = append(conflictErrs, wrapped)
			} else {
				hardErrs = append(hardErrs, wrapped)
			}
		}
	}
	for name := range result.NativeAgents {
		sort.Strings(result.NativeAgents[name])
	}
	for name := range result.Fallbacks {
		sort.Slice(result.Fallbacks[name], func(i, j int) bool {
			return result.Fallbacks[name][i].Agent < result.Fallbacks[name][j].Agent
		})
	}
	sort.Strings(result.Written)
	sort.Strings(result.Removed)

	if len(hardErrs) > 0 {
		joined := errors.Join(hardErrs...)
		if len(conflictErrs) > 0 {
			// A retryable I/O failure alongside a settled conflict must be
			// reported as retryable: errors.Is(err, ErrProjectionConflict)
			// matches if ANY error in the chain matches, so folding conflictErrs
			// in here via %w would make the whole batch look settled and the
			// I/O failure would never be retried. The conflict count is noted in
			// the message only, outside the %w chain; it resurfaces cleanly on
			// the next retry once the I/O failure clears.
			return result, fmt.Errorf("%w (plus %d Team Rule projection conflict(s) pending retry)", joined, len(conflictErrs))
		}
		return result, joined
	}
	if len(conflictErrs) > 0 {
		return result, errors.Join(conflictErrs...)
	}
	return result, nil
}

// HasNativeProjections reports whether any supported native root still carries
// a Team Rule ox owns. It lets convergence distinguish "blind and nothing to
// do" from "blind and retirement must remain pending" without inventing a new
// machine-local state file.
func HasNativeProjections(projectRoot string) bool {
	for _, p := range policies {
		rootPath := filepath.Join(projectRoot, filepath.FromSlash(p.Root))
		entries, err := os.ReadDir(rootPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !managedProjectionName(entry.Name(), p.Extension) {
				continue
			}
			if !entry.Type().IsRegular() {
				continue
			}
			content, readErr := os.ReadFile(filepath.Join(rootPath, entry.Name()))
			if readErr == nil && projectionOwned(content) {
				return true
			}
		}
	}
	return false
}

// primeProbeTimeout bounds the single `git check-ignore` ForPrime runs. Prime is
// the SessionStart critical path, and this is the only subprocess on it — an
// unbounded one turns a wedged git (an index lock, a stalled network mount)
// into a coding session that never starts.
//
// A timeout falls to "not protected", which is the SAME direction the decision
// below already takes when the answer is genuinely no: deliver the rule through
// prime rather than suppress it. Timing out therefore costs a duplicated rule,
// never a silently missing one.
//
// It is a var so a test can shrink it; nothing in production reassigns it.
var primeProbeTimeout = 2 * time.Second

// ForPrime removes rules already owned by a projection in the active agent's
// native root and converts any scoped fallback to indexed delivery. It is the
// second half of the exactly-once contract: projection chooses native-or-prime;
// session start never duplicates an owned native file while convergence is
// repairing stale bytes.
func ForPrime(projectRoot, agent string, rules []teamdocs.TeamRule) []teamdocs.TeamRule {
	// Protection is probed at most once, lazily, and only if some rule actually
	// has a native file worth suppressing. Reconcile uses this same probe, so the
	// two halves of the exactly-once contract cannot answer differently — a
	// cheaper second signal here would be a new source of truth, which is the
	// defect class this whole path keeps producing.
	protected, probed := false, false
	rootProtected := func(p policy) bool {
		if !probed {
			probed = true
			ctx, cancel := context.WithTimeout(context.Background(), primeProbeTimeout)
			defer cancel()
			protected = managedPathIgnored(ctx, projectRoot,
				filepath.ToSlash(filepath.Join(p.Root, ignoreProbeName(p.Extension))))
		}
		return protected
	}

	out := make([]teamdocs.TeamRule, 0, len(rules))
	for _, rule := range rules {
		mode := ModeForAgent(agent, rule)
		if mode == DeliveryNative && nativePresent(projectRoot, agent, rule) {
			// A native file ox owns is only "delivered" while Reconcile can still
			// maintain it. Once the root loses its ignore rule, Reconcile refuses
			// the whole root and the file freezes at whatever text it last held —
			// so suppressing prime here would strand the rule on a stale copy
			// forever, reaching the coworker through neither surface honestly.
			// Falling through delivers it twice in that state, which is the safe
			// direction: duplicated beats silently frozen.
			if p, ok := policyFor(agent); !ok || rootProtected(p) {
				continue
			}
		}
		copy := rule
		if mode == DeliveryPrimeIndexed || len(rule.Globs) > 0 {
			copy.Visibility = teamdocs.VisibilityIndexed
			copy.Body = ""
			copy.EstimatedTokens = 0
		} else if copy.Body == "" {
			if body, err := teamdocs.ReadRuleBody(copy.AbsPath); err == nil {
				copy.Body = body
				copy.EstimatedTokens = len(body) / 4
			}
		}
		out = append(out, copy)
	}
	return out
}

func NativePath(projectRoot, agent string, rule teamdocs.TeamRule) (string, bool) {
	p, ok := policyFor(agent)
	if !ok {
		return "", false
	}
	return filepath.Join(projectRoot, filepath.FromSlash(p.Root), nativeFilename(rule, p)), true
}

// nativePresent reports whether the native root already carries this rule's
// OWN projection, using the same ownership predicate reconcileRoot uses to
// decide whether it may write there. A bare Stat used to treat ANY file at
// the path as "present" — foreign or git-tracked content included — so
// reconcileRoot would refuse to write it as a conflict while ForPrime, seeing
// only that a file existed, suppressed prime delivery for the same rule: it
// reached neither surface. A read error (missing file, permission denied) is
// treated as "not present", the safe direction: delivering a rule twice via
// prime is recoverable, delivering it zero times is not.
func nativePresent(projectRoot, agent string, rule teamdocs.TeamRule) bool {
	path, ok := NativePath(projectRoot, agent, rule)
	if !ok {
		return false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return projectionOwned(content)
}

func policyFor(agent string) (policy, bool) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "claude-code" || agent == "claudecode" {
		agent = "claude"
	}
	for _, p := range policies {
		if p.Agent == agent {
			return p, true
		}
	}
	return policy{}, false
}

func render(rule teamdocs.TeamRule, p policy) ([]byte, error) {
	body, err := teamdocs.ReadRuleBody(rule.AbsPath)
	if err != nil {
		return nil, err
	}
	var lines []string
	if rule.Description != "" || len(rule.Globs) > 0 || p.AlwaysApplyField != "" {
		lines = append(lines, "---")
		if rule.Description != "" {
			lines = append(lines, "description: "+strconv.Quote(rule.Description))
		}
		if len(rule.Globs) > 0 && p.GlobField != "" {
			patterns, writable := nativePatterns(rule.Globs, p)
			if !writable {
				return nil, fmt.Errorf("patterns cannot be written for %s", p.Agent)
			}
			lines = append(lines, p.ScopedFields...)
			lines = append(lines, globFieldLines(patterns, p)...)
		}
		if p.AlwaysApplyField != "" {
			lines = append(lines, fmt.Sprintf("%s: %t", p.AlwaysApplyField, len(rule.Globs) == 0))
		}
		lines = append(lines, "---", "")
	}
	if body != "" {
		lines = append(lines, body)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return stampProjection([]byte(out)), nil
}

// globFieldLines renders a rule's scope in the agent's frontmatter shape. Every
// pattern is quoted because a bare **/*.go is a YAML alias: the frontmatter then
// fails to parse, and Claude Code treats a rule with unparsable frontmatter as
// having no paths, so it would load everywhere.
func globFieldLines(patterns []string, p policy) []string {
	switch p.GlobStyle {
	case globBlockList:
		lines := make([]string, 0, len(patterns)+1)
		lines = append(lines, p.GlobField+":")
		for _, pattern := range patterns {
			lines = append(lines, "  - "+strconv.Quote(pattern))
		}
		return lines
	case globFlowList:
		quoted := make([]string, len(patterns))
		for i, pattern := range patterns {
			quoted[i] = strconv.Quote(pattern)
		}
		return []string{p.GlobField + ": [" + strings.Join(quoted, ", ") + "]"}
	default:
		return []string{p.GlobField + ": " + strconv.Quote(strings.Join(patterns, ","))}
	}
}

// maxNativePatterns bounds brace expansion, which is exponential in the number of
// groups and runs on team-authored text in the background. Eight three-way groups
// already reach 6,561 patterns; no real scope comes near this, and past it the rule
// costs an index row rather than a frontmatter nobody can read.
const maxNativePatterns = 256

// nativePatterns returns the patterns to write for this agent, and false when its
// field cannot hold them without changing what they match.
func nativePatterns(globs []string, p policy) ([]string, bool) {
	patterns := make([]string, 0, len(globs))
	for _, glob := range globs {
		expanded, ok := expandBraces(glob, maxNativePatterns-len(patterns))
		if !ok {
			return nil, false
		}
		patterns = append(patterns, expanded...)
	}
	if p.GlobStyle != globCommaString {
		return patterns, true
	}
	// A comma left over after expansion is part of a path, or of a group that never
	// closed. Joined, it would read as two patterns that match different files.
	for _, pattern := range patterns {
		if strings.Contains(pattern, ",") {
			return nil, false
		}
	}
	return patterns, true
}

// maxBraceGlobBytes bounds one glob before brace expansion. The longest real scope
// glob seen is under 200 bytes; 1 KiB keeps the worst-case scan near a million steps.
const maxBraceGlobBytes = 1024

// maxBraceGroups bounds the '{' count: each one can start a rescan of the rest, and
// recursion repeats that per expanded pattern. Real globs hold one or two groups.
const maxBraceGroups = 16

// expandBraces rewrites a glob's brace groups as separate patterns, keeping their
// order: src/{client,admin}/*.{ts,css} becomes four patterns. A group with no comma or
// no closing brace is left as text, since it offers no alternatives. The second
// return is false when the result would exceed limit.
func expandBraces(glob string, limit int) ([]string, bool) {
	// Finding a group rescans from each '{', so a hostile glob of unmatched braces costs
	// time quadratic in its length while expanding to one pattern. Real globs are short;
	// a longer one falls back to the prime index instead of stalling startup.
	if limit <= 0 || len(glob) > maxBraceGlobBytes || strings.Count(glob, "{") > maxBraceGroups {
		return nil, false
	}
	open, end, options := firstBraceGroup(glob)
	if open < 0 {
		return []string{glob}, true
	}
	var expanded []string
	for _, option := range options {
		more, ok := expandBraces(glob[:open]+option+glob[end+1:], limit-len(expanded))
		if !ok {
			return nil, false
		}
		expanded = append(expanded, more...)
	}
	return expanded, true
}

// firstBraceGroup finds the first brace group holding a top-level comma and returns
// its bounds and alternatives, or -1 when there is none.
func firstBraceGroup(glob string) (open, end int, options []string) {
	for start := 0; start < len(glob); start++ {
		if glob[start] != '{' {
			continue
		}
		depth, last := 0, start+1
		var parts []string
		for i := start; i < len(glob); i++ {
			switch glob[i] {
			case '{':
				depth++
			case ',':
				if depth == 1 {
					parts = append(parts, glob[last:i])
					last = i + 1
				}
			case '}':
				depth--
				if depth == 0 {
					if len(parts) > 0 {
						return start, i, append(parts, glob[last:i])
					}
					i = len(glob) // no comma at this level; look for a group inside it
				}
			}
		}
	}
	return -1, -1, nil
}

// stampProjection records ownership without requiring a machine-local
// inventory. The trailer covers every byte before it, including frontmatter,
// so a local edit invalidates ownership and reconciliation preserves the file.
//
// The bytes live in skillmanager alongside the namespace contract: Team Skills
// need the identical mechanism now that their directory name no longer proves
// authorship, and two copies of a hash format is how they drift apart.
func stampProjection(content []byte) []byte {
	return skillmanager.TeamRuleStamp.Apply(content)
}

func verifiedProjection(content []byte) bool {
	return skillmanager.TeamRuleStamp.Verifies(content)
}

// projectionOwned reports whether content is a Team Rule projection ox itself
// wrote, and may therefore safely rewrite or delete. It stays a named
// predicate distinct from verifiedProjection so call sites read as an
// ownership check rather than a hash check.
func projectionOwned(content []byte) bool {
	return verifiedProjection(content)
}

func nativeFilename(rule teamdocs.TeamRule, p policy) string {
	base := strings.ToLower(rule.Name)
	var clean strings.Builder
	lastDash := false
	for _, r := range base {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			clean.WriteRune(r)
			lastDash = false
		} else if !lastDash && clean.Len() > 0 {
			clean.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(clean.String(), "-")
	if slug == "" {
		slug = "rule"
	}
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	sum := sha256.Sum256([]byte(rule.Name + "\x00" + filepath.ToSlash(rule.RelPath)))
	// SUFFIX, not prefix — the same shape Team Skills use, because one namespace
	// rule for everything ox projects is worth more than the few characters a
	// per-artifact exception would save.
	return fmt.Sprintf("%s-%x%s%s", slug, sum[:4], skillmanager.TeamSuffix, p.Extension)
}

func managedPathIgnored(ctx context.Context, projectRoot, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "--no-index", "-q", "--", rel)
	cmd.Dir = projectRoot
	return cmd.Run() == nil
}

// reconcileRoot converges a single agent's rule root. A per-file conflict (a
// path git tracks, or one holding content ox did not write) is recorded and
// skipped rather than aborting the whole root: one hand-authored file must
// not block every other rule in the same root from landing. Genuine I/O and
// git-check failures are different — they abort this root's reconciliation
// immediately, since there is no safe way to keep going once the filesystem
// or git itself stops answering reliably.
//
// desired is a map, so its keys are visited in sorted order: Go's randomized
// map iteration would otherwise make "which files land before a conflict"
// nondeterministic run to run.
func reconcileRoot(ctx context.Context, projectRoot, rootPath string, p policy, desired map[string][]byte) (written, removed []string, err error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, nil, err
	}

	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)

	var conflicts []error
	for _, name := range names {
		content := desired[name]
		current, readErr := root.ReadFile(name)
		if readErr == nil && bytes.Equal(current, content) {
			continue
		}
		if readErr != nil && !os.IsNotExist(readErr) {
			return written, removed, readErr
		}
		rel := filepath.ToSlash(filepath.Join(p.Root, name))
		tracked, trackErr := managedPathTracked(ctx, projectRoot, rel)
		if trackErr != nil {
			return written, removed, trackErr
		}
		if tracked {
			conflicts = append(conflicts, fmt.Errorf("%w: refusing to update tracked Team Rule projection %s", ErrProjectionConflict, rel))
			continue
		}
		if readErr == nil && !projectionOwned(current) {
			conflicts = append(conflicts, fmt.Errorf("%w: refusing to update %s: existing file is not a verified ox projection", ErrProjectionConflict, rel))
			continue
		}
		if err := atomicWrite(root, name, content); err != nil {
			return written, removed, err
		}
		written = append(written, name)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !managedProjectionName(name, p.Extension) {
			continue
		}
		if _, keep := desired[name]; keep {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(p.Root, name))
		tracked, trackErr := managedPathTracked(ctx, projectRoot, rel)
		if trackErr != nil {
			return written, removed, trackErr
		}
		if tracked {
			conflicts = append(conflicts, fmt.Errorf("%w: refusing to remove tracked Team Rule projection %s", ErrProjectionConflict, rel))
			continue
		}
		current, readErr := root.ReadFile(name)
		if readErr != nil {
			return written, removed, readErr
		}
		if !projectionOwned(current) {
			conflicts = append(conflicts, fmt.Errorf("%w: refusing to remove %s: existing file is not a verified ox projection", ErrProjectionConflict, rel))
			continue
		}
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return written, removed, err
		}
		removed = append(removed, name)
	}
	sort.Strings(written)
	sort.Strings(removed)
	return written, removed, errors.Join(conflicts...)
}

// managedPathTracked is gitutil.PathTracked, kept as a local name so the call
// sites below keep reading as a Team Rule concern.
func managedPathTracked(ctx context.Context, projectRoot, rel string) (bool, error) {
	return gitutil.PathTracked(ctx, projectRoot, rel)
}

func atomicWrite(root *os.Root, name string, content []byte) error {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := fmt.Sprintf(".%s.tmp-%x", name, nonce)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	clean := true
	defer func() {
		_ = f.Close()
		if clean {
			_ = root.Remove(temp)
		}
	}()
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		return err
	}
	clean = false
	return nil
}
