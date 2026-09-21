// Package teamrules projects Team Context rules into agent-native rule roots
// when the target can preserve their semantics, and otherwise leaves delivery
// to ox agent prime.
package teamrules

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

const managedPrefix = "sageox-team-"

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
// state are removed, so filtering and retirement converge rather than append.
func Reconcile(ctx context.Context, projectRoot string, rules []teamdocs.TeamRule) (Result, error) {
	result := newResult()
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
		written, removed, err := reconcileRoot(ctx, projectRoot, rootPath, p, desired)
		if err != nil {
			return result, fmt.Errorf("reconcile %s Team Rules: %w", p.Agent, err)
		}
		for _, name := range written {
			result.Written = append(result.Written, filepath.ToSlash(filepath.Join(p.Root, name)))
		}
		for _, name := range removed {
			result.Removed = append(result.Removed, filepath.ToSlash(filepath.Join(p.Root, name)))
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
			if strings.HasPrefix(entry.Name(), managedPrefix) && strings.HasSuffix(entry.Name(), p.Extension) {
				return true
			}
		}
	}
	return false
}

// ForPrime removes rules already present in the active agent's native root and
// converts any scoped fallback to indexed delivery. It is the second half of the
// exactly-once contract: projection chooses native-or-prime; session start never
// duplicates an owned native file while convergence is repairing stale bytes.
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

func nativePresent(projectRoot, agent string, rule teamdocs.TeamRule) bool {
	path, _ := NativePath(projectRoot, agent, rule)
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
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
	lines = append(lines, "<!-- Managed by ox from Team Context; edit the source rule, not this projection. -->")
	if body != "" {
		lines = append(lines, body)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out), nil
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
	for name, content := range desired {
		current, readErr := root.ReadFile(name)
		if readErr == nil && string(current) == string(content) {
			continue
		}
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, nil, readErr
		}
		rel := filepath.ToSlash(filepath.Join(p.Root, name))
		if managedPathTracked(ctx, projectRoot, rel) {
			return nil, nil, fmt.Errorf("refusing to update tracked Team Rule projection %s", rel)
		}
		if err := atomicWrite(root, name, content); err != nil {
			return nil, nil, err
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
		if managedPathTracked(ctx, projectRoot, rel) {
			return nil, nil, fmt.Errorf("refusing to remove tracked Team Rule projection %s", rel)
		}
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		removed = append(removed, name)
	}
	sort.Strings(written)
	sort.Strings(removed)
	return written, removed, nil
}

func managedPathTracked(ctx context.Context, projectRoot, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = projectRoot
	return cmd.Run() == nil
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
