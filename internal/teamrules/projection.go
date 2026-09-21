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

	"github.com/sageox/ox/internal/teamdocs"
)

const (
	managedPrefix         = "sageox-team-"
	projectionStampPrefix = "<!-- ox-team-rule-sha256:"
	projectionStampSuffix = "; managed by ox from Team Context; edit the source rule, not this projection. -->"
)

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

type policy struct {
	Agent            string
	Root             string
	Extension        string
	GlobField        string
	AlwaysApplyField string
}

// These mappings are agentx's documented native rule surfaces. A policy is
// activated only when its rule root already exists in the repository; ox never
// creates an unrelated agent's footprint during background convergence.
var policies = []policy{
	{Agent: "claude", Root: ".claude/rules", Extension: ".md", GlobField: "globs"},
	{Agent: "cursor", Root: ".cursor/rules", Extension: ".mdc", GlobField: "globs", AlwaysApplyField: "alwaysApply"},
	{Agent: "copilot", Root: ".github/instructions", Extension: ".md", GlobField: "applyTo"},
	{Agent: "cline", Root: ".clinerules", Extension: ".md", GlobField: "paths"},
	{Agent: "kiro", Root: ".kiro/steering", Extension: ".md", GlobField: "fileMatchPattern"},
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
// field receive an indexed prime entry instead. For capable agents, globs
// supersedes visibility because the native matcher owns activation.
func ModeForAgent(agent string, rule teamdocs.TeamRule) DeliveryMode {
	p, ok := policyFor(agent)
	if len(rule.Globs) > 0 {
		if ok && p.GlobField != "" {
			return DeliveryNative
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
		protected := managedPathIgnored(ctx, projectRoot, filepath.ToSlash(filepath.Join(p.Root, managedPrefix+"probe"+p.Extension)))
		for _, rule := range rules {
			mode := ModeForAgent(p.Agent, rule)
			if mode != DeliveryNative {
				if len(rule.Globs) > 0 && p.GlobField == "" {
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
			if !strings.HasPrefix(entry.Name(), managedPrefix) || !strings.HasSuffix(entry.Name(), p.Extension) {
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

// ForPrime removes rules already owned by a projection in the active agent's
// native root and converts any scoped fallback to indexed delivery. It is the
// second half of the exactly-once contract: projection chooses native-or-prime;
// session start never duplicates an owned native file while convergence is
// repairing stale bytes.
func ForPrime(projectRoot, agent string, rules []teamdocs.TeamRule) []teamdocs.TeamRule {
	out := make([]teamdocs.TeamRule, 0, len(rules))
	for _, rule := range rules {
		mode := ModeForAgent(agent, rule)
		if mode == DeliveryNative && nativePresent(projectRoot, agent, rule) {
			continue
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
			lines = append(lines, p.GlobField+": "+strconv.Quote(strings.Join(rule.Globs, ",")))
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

// stampProjection records ownership without requiring a machine-local
// inventory. The trailer covers every byte before it, including frontmatter,
// so a local edit invalidates ownership and reconciliation preserves the file.
func stampProjection(content []byte) []byte {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	if len(content) == 0 || content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	sum := sha256.Sum256(content)
	stamp := fmt.Sprintf("%s%x%s\n", projectionStampPrefix, sum, projectionStampSuffix)
	return append(append([]byte(nil), content...), []byte(stamp)...)
}

func verifiedProjection(content []byte) bool {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	marker := []byte("\n" + projectionStampPrefix)
	markerOffset := bytes.LastIndex(content, marker)
	if markerOffset < 0 {
		return false
	}
	payload := content[:markerOffset+1]
	stamp := content[markerOffset+1:]
	wantLength := len(projectionStampPrefix) + sha256.Size*2 + len(projectionStampSuffix) + 1
	if len(stamp) != wantLength || !bytes.HasSuffix(stamp, []byte(projectionStampSuffix+"\n")) {
		return false
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	gotHash := string(stamp[len(projectionStampPrefix) : len(projectionStampPrefix)+sha256.Size*2])
	return gotHash == wantHash
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
	return fmt.Sprintf("%s%s-%x%s", managedPrefix, slug, sum[:4], p.Extension)
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
		if !strings.HasPrefix(name, managedPrefix) || !strings.HasSuffix(name, p.Extension) {
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

// managedPathTracked reports whether git tracks rel inside projectRoot.
//
// `git ls-files --error-unmatch` exits 1 for the ordinary "no tracked path
// matched" answer and reserves every other outcome for a real failure: a
// canceled context, a git that is missing or too old, an unreadable index.
// Collapsing those into "untracked" is what let reconcile overwrite or delete a
// TRACKED projection, report it applied, and clear the pending state — leaving
// an uncommitted rule change with nothing scheduled to revisit it. So only exit
// 1 means untracked; anything else is returned as an error, which reaches the
// convergence coordinator unwrapped by ErrProjectionConflict and is therefore
// retried rather than settled.
//
// The context check comes first on purpose: killing the child on cancellation
// surfaces as a signal on Unix and as exit code 1 on Windows, so an expired
// deadline would otherwise be indistinguishable from a genuine "not tracked".
func managedPathTracked(ctx context.Context, projectRoot, rel string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = projectRoot
	runErr := cmd.Run()
	if runErr == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("check whether %s is tracked: %w", rel, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is tracked: %w", rel, runErr)
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
