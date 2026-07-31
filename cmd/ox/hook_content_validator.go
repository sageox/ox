package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Hook content validation (ox-9y4k). External adapter binaries (ox-adapter-*)
// have unrestricted filesystem access — they're the ones that actually write
// hook content into .git/hooks/, .claude/, .gemini/, etc. A compromised or
// hijacked adapter could ship malicious hook bodies that run on every commit.
//
// Real prevention requires sandboxing the adapter (out of scope for this PR)
// or shifting hook generation into the ox binary with adapters supplying only
// parameters (a protocol change, also out of scope). What this file ships is
// the DETECTION pass: after adapter install, ox reads installed hooks and
// flags content that doesn't look like a standard ox hook template. The
// findings go through the doctor check below; ox doesn't auto-rewrite, but
// the user sees an actionable warning.
//
// Trade-off: this catches naive attacks (curl|sh, eval $(curl …), python -c
// "import …") but not sophisticated ones. The detection set is intentionally
// conservative — false positives on common patterns (e.g. legitimate `git
// describe | grep …`) would train users to ignore warnings. We flag only
// patterns that have no legitimate use in a pre-commit/prepare-commit-msg
// hook.

// hookSuspiciousPatterns is the deny-list. Each entry is a compiled regex
// and a human-readable description. Reviews welcome: every false positive
// reported in real ledgers is a chance to refine these.
var hookSuspiciousPatterns = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	// "curl | sh" or "wget -O- | bash" — remote code execution at hook time
	{
		regexp.MustCompile(`(?i)\b(curl|wget)\b[^|]{0,200}\|[^|]{0,40}\b(sh|bash|zsh|python\d?|perl|ruby)\b`),
		"hook pipes downloaded content into a shell/interpreter",
	},
	// $(curl …) or `curl …` — command substitution of a network fetch
	{
		regexp.MustCompile(`(?i)\$\(\s*(curl|wget)\b[^)]{0,200}\)`),
		"hook captures network fetch output via $() command substitution",
	},
	// `curl` in backticks — same threat, older shell style
	{
		regexp.MustCompile("(?i)`\\s*(curl|wget)\\b[^`]{0,200}`"),
		"hook captures network fetch output via backtick command substitution",
	},
	// base64 -d | sh — obfuscated payload execution
	{
		regexp.MustCompile(`(?i)\bbase64\s+(-d|--decode)[^|]{0,40}\|[^|]{0,40}\b(sh|bash|python\d?|perl)\b`),
		"hook decodes a base64 blob and pipes into an interpreter",
	},
	// eval with command substitution — common backdoor shape
	{
		regexp.MustCompile(`(?i)\beval\s+\$\(`),
		"hook uses eval $(…) which trivially executes arbitrary command output",
	},
	// python -c with significant content — could be a backdoor
	{
		regexp.MustCompile(`(?i)\bpython\d?\s+-c\s+['"][^'"]{60,}`),
		"hook executes a long python -c payload",
	},
	// posting credentials/auth headers off-host (heuristic: curl + Authorization
	// + a non-loopback host literal in the same line)
	{
		regexp.MustCompile(`(?i)\bcurl\b[^\n]{0,400}\b(authorization|cookie)\s*:`),
		"hook sets Authorization/Cookie header on outbound curl",
	},
}

// validateHookContent returns a list of warnings — one per pattern that
// matched in content. Empty list means the content looks clean per current
// detectors. False positives are possible; the doctor check reports rather
// than auto-fixes for that reason.
func validateHookContent(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	s := string(content)
	var warnings []string
	for _, sp := range hookSuspiciousPatterns {
		if sp.pattern.MatchString(s) {
			warnings = append(warnings, sp.reason)
		}
	}
	return warnings
}

// hookValidationFinding aggregates per-file warnings for a single hook.
type hookValidationFinding struct {
	Path     string
	Warnings []string
}

// validateInstalledHooks walks the standard hook locations and runs
// validateHookContent against each file. Returns all findings sorted by
// path so the doctor output is stable across runs.
//
// Locations scanned:
//   - <gitRoot>/.git/hooks/* — git-level hooks (prepare-commit-msg, etc.)
//   - <gitRoot>/.claude/hooks/* — Claude Code adapter hooks
//   - <gitRoot>/.gemini/, .codex/, .opencode/, .amp/ — other adapters
//   - <gitRoot>/.agents/plugins/*/{hooks,scripts} — Open Plugins directories
//     (Goose). Unlike the per-agent paths above, this one is a SHARED
//     namespace: any tool may drop a plugin there, and Goose executes whatever
//     the plugin's hooks.json names via `sh -c`. Scanning every plugin — not
//     just ox's own — is the point.
//
// Missing directories are silently skipped. Per ox-9y4k this is best-
// effort detection, not a security boundary.
func validateInstalledHooks(gitRoot string) ([]hookValidationFinding, error) {
	if gitRoot == "" {
		return nil, nil
	}
	hookDirs := []string{
		filepath.Join(gitRoot, ".git", "hooks"),
		filepath.Join(gitRoot, ".claude", "hooks"),
		filepath.Join(gitRoot, ".claude", "commands"),
		filepath.Join(gitRoot, ".gemini", "hooks"),
		filepath.Join(gitRoot, ".codex", "hooks"),
		filepath.Join(gitRoot, ".opencode", "hooks"),
		filepath.Join(gitRoot, ".amp", "hooks"),
	}
	hookDirs = append(hookDirs, openPluginHookDirs(gitRoot)...)

	var findings []hookValidationFinding
	for _, dir := range hookDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing dir — nothing to scan
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			// skip git's sample hook bodies; they're shipped by git itself
			// and not actionable here.
			if strings.HasSuffix(ent.Name(), ".sample") {
				continue
			}
			abs := filepath.Join(dir, ent.Name())
			content, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			warnings := validateHookContent(content)
			if len(warnings) == 0 {
				continue
			}
			rel, _ := filepath.Rel(gitRoot, abs)
			findings = append(findings, hookValidationFinding{
				Path:     rel,
				Warnings: warnings,
			})
		}
	}
	return findings, nil
}

// openPluginHookDirs enumerates the per-plugin hook and script directories
// under <gitRoot>/.agents/plugins/. Every installed plugin is scanned, not just
// ox's own: the directory is a shared namespace, and a hostile or careless
// plugin's command runs with the same privileges as ours.
func openPluginHookDirs(gitRoot string) []string {
	pluginsRoot := filepath.Join(gitRoot, ".agents", "plugins")
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil {
		return nil // no plugins installed
	}

	var dirs []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dirs = append(dirs,
			filepath.Join(pluginsRoot, ent.Name(), "hooks"),
			filepath.Join(pluginsRoot, ent.Name(), "scripts"),
		)
	}
	return dirs
}

// checkHookContentIntegrity is the doctor.Run for ox-9y4k. Reports
// suspicious patterns in installed hooks. Never modifies — the
// remediation is operator inspection + reinstall.
func checkHookContentIntegrity(_ bool) checkResult {
	name := "Hook content integrity"
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in git repo", "")
	}
	findings, err := validateInstalledHooks(gitRoot)
	if err != nil {
		return SkippedCheck(name, fmt.Sprintf("scan error: %v", err), "")
	}
	if len(findings) == 0 {
		return PassedCheck(name, "no suspicious patterns in installed hooks")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "found suspicious content in %d hook file(s):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "  %s\n", f.Path)
		for _, w := range f.Warnings {
			fmt.Fprintf(&b, "    - %s\n", w)
		}
	}
	guidance := "Review each flagged file. If you didn't author the content, " +
		"uninstall + reinstall the affected agent hooks via `ox hooks <agent> install`. " +
		"False positives can be reported in ox-9y4k follow-up issues."
	return FailedCheck(name, b.String(), guidance)
}
