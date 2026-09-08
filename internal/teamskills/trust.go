// Package teamskills implements the trust boundary for skills that come from a
// team-context repository rather than from the ox binary.
//
// The distinction matters because the two have different provenance. A CLI skill
// is anchored to the binary the user installed and reviewed by the same release
// process as the code. A TEAM skill comes from a remote that any teammate can
// push to, and a skill can execute shell. `security/SECURITY.md` already declares
// team-context content untrusted for prompting regardless of author, and nothing
// on the pull path verifies signatures.
//
// So team skills are split by what they can DO, not by who wrote them:
//
//   - Prose — no bundled scripts, no inline command execution, no allowed-tools —
//     materializes automatically. It is exactly the trust level team RULES already
//     have, and prime already injects those.
//   - Executable content requires an explicit approval PINNED TO A DIGEST. Change
//     the content and the approval no longer applies, because it was approval of
//     specific bytes, not of a name.
//
// The ecosystem argues for the gate rather than against having team content at
// all: no registry signs skills, and an independent scan of 3,984 published
// skills found 36.8% carrying at least one flaw and 76 confirmed malicious.
package teamskills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// File is one file belonging to a team skill, as read out of the team-context
// checkout. Path is relative to the skill directory.
type File struct {
	Path    string
	Content []byte
}

// Skill is a candidate team skill discovered in a team-context checkout.
type Skill struct {
	Name  string
	Files []File
}

// Capability is a reason a skill is considered executable.
type Capability string

const (
	// CapBundledScript — the skill ships files under scripts/. Materializing them
	// puts runnable content on disk that a coding agent is invited to execute.
	CapBundledScript Capability = "bundled-script"

	// CapAllowedTools — frontmatter grants the skill tool access up front.
	CapAllowedTools Capability = "allowed-tools"

	// CapInlineCommand — the body carries Claude's inline command-execution syntax,
	// which runs a shell command when the skill is loaded rather than when a human
	// chooses to run something.
	CapInlineCommand Capability = "inline-command"
)

// scriptExtensions are file types a coding agent can be told to run directly.
//
// Deliberately broad. The cost of a false positive is one approval prompt; the
// cost of a false negative is runnable code materialized as "prose" with nobody
// having decided. A .js in a skill may well be a code sample — but SKILL.md can
// just as easily say "run node helper.js", and nothing downstream distinguishes
// the two.
var scriptExtensions = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".py": true, ".rb": true, ".pl": true, ".lua": true,
	".ps1": true, ".bat": true, ".cmd": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".php": true,
}

// scriptDirNames are directory names that mean "runnable" at ANY depth.
var scriptDirNames = map[string]bool{"scripts": true, "script": true, "bin": true}

// IsExecutableFile reports whether a skill file can cause code to run, and why.
//
// One predicate, used by classification, by materialization, and by the file
// mode. They MUST agree: a file classified as prose but written executable — or
// filtered on one path and not the other — is a gap between two definitions of
// the same thing, which is exactly how `bin/deploy.sh` and `tools/scripts/run.sh`
// shipped as prose while only a root-level `scripts/` was checked.
func IsExecutableFile(relPath string, content []byte) (bool, string) {
	clean := path.Clean(strings.ReplaceAll(relPath, "\\", "/"))

	// A script directory at any depth, not only at the root.
	for _, seg := range strings.Split(clean, "/") {
		if scriptDirNames[strings.ToLower(seg)] {
			return true, "under " + seg + "/"
		}
	}
	if scriptExtensions[strings.ToLower(path.Ext(clean))] {
		return true, "script extension " + path.Ext(clean)
	}
	// A shebang makes any file runnable whatever it is called.
	if len(content) >= 2 && content[0] == '#' && content[1] == '!' {
		return true, "shebang"
	}
	return false, ""
}

// Verdict is the trust classification of one candidate skill.
type Verdict struct {
	// Executable is true when the skill can cause code to run. Such a skill needs
	// a digest-pinned approval before ox will materialize it.
	Executable bool

	// Capabilities lists every reason Executable is true, so a human deciding on an
	// approval can see what they are approving rather than a yes/no.
	Capabilities []Capability

	// Evidence maps each capability to the concrete thing that triggered it — a
	// file path or the offending line — because "this skill is executable" is not
	// reviewable and "scripts/deploy.sh" is.
	Evidence map[Capability][]string

	// Digest identifies the exact bytes classified. An approval is pinned to it.
	Digest string
}

// inlineCommandLine matches Claude's inline command-execution syntax: a line
// whose content begins with !`…`. Anchored to line start (after optional
// whitespace and list markers) so ordinary prose containing a backtick or an
// exclamation mark is not swept up — a false positive here blocks a legitimate
// prose skill, which teaches people to approve everything.
var inlineCommandLine = regexp.MustCompile(`(?m)^\s*(?:[-*+]\s+|>\s*)?!` + "`")

// allowedToolsKey matches an allowed-tools frontmatter key.
var allowedToolsKey = regexp.MustCompile(`(?mi)^\s*allowed[-_]tools\s*:`)

// Classify decides whether a team skill may materialize without human approval.
//
// It is deliberately conservative in one direction only: anything that can cause
// execution is executable. It does NOT try to judge whether the command looks
// dangerous — that is a losing game, and the whole point of the approval is that
// a human reads it.
func Classify(s Skill) Verdict {
	v := Verdict{Evidence: map[Capability][]string{}, Digest: Digest(s)}

	add := func(c Capability, ev string) {
		if _, seen := v.Evidence[c]; !seen {
			v.Capabilities = append(v.Capabilities, c)
		}
		v.Evidence[c] = append(v.Evidence[c], ev)
		v.Executable = true
	}

	for _, f := range s.Files {
		clean := path.Clean(strings.ReplaceAll(f.Path, "\\", "/"))

		if executable, why := IsExecutableFile(clean, f.Content); executable {
			add(CapBundledScript, clean+" ("+why+")")
			continue
		}

		// Frontmatter and inline-command checks apply to markdown; a non-runnable
		// asset is inert until something runs it, and the predicate above already
		// covers anything that is runnable.
		if !strings.HasSuffix(clean, ".md") {
			continue
		}
		normalized := strings.ReplaceAll(string(f.Content), "\r\n", "\n")
		front, _ := splitFrontmatter(normalized)
		if allowedToolsKey.MatchString(front) {
			add(CapAllowedTools, clean)
		}
		// Scan the FULL document, not just the body. splitFrontmatter returns an
		// empty body for unterminated frontmatter — deliberately, so a hidden
		// allowed-tools grant is still seen — and scanning only the body would then
		// miss every inline command in exactly that malformed shape. A frontmatter
		// line cannot legitimately start with !` anyway, so there is nothing to
		// exclude.
		for _, line := range inlineCommandLines(normalized) {
			add(CapInlineCommand, clean+": "+line)
		}
	}

	sort.Slice(v.Capabilities, func(i, j int) bool { return v.Capabilities[i] < v.Capabilities[j] })
	for c := range v.Evidence {
		sort.Strings(v.Evidence[c])
	}
	return v
}

// inlineCommandLines returns the offending lines, trimmed, so the evidence names
// what a reviewer would need to read.
func inlineCommandLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if inlineCommandLine.MatchString(line) {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 120 {
				trimmed = trimmed[:120] + "…"
			}
			out = append(out, trimmed)
		}
	}
	return out
}

// splitFrontmatter separates a leading `---` YAML block from the body.
//
// Only a block at the very start counts. A `---` later in the document is a
// horizontal rule, and treating it as frontmatter would let a body-level
// "allowed-tools:" line be read as a grant — or, worse, let a real grant hide
// behind one.
func splitFrontmatter(content string) (front, body string) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", normalized
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// Unterminated frontmatter: treat the whole document as frontmatter rather
		// than as body. Reading it as body would skip the allowed-tools check.
		return rest, ""
	}
	return rest[:end], strings.TrimPrefix(rest[end+len("\n---"):], "\n")
}

// Digest is the content identity an approval is pinned to.
//
// It covers every file path AND its bytes, sorted, so neither reordering nor
// renaming nor editing can preserve a digest. An approval that survived any of
// those would be approval of a name rather than of content.
func Digest(s Skill) string {
	files := make([]File, len(s.Files))
	copy(files, s.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	h := sha256.New()
	fmt.Fprintf(h, "skill:%s\n", s.Name)
	for _, f := range files {
		fmt.Fprintf(h, "file:%s:%d\n", path.Clean(strings.ReplaceAll(f.Path, "\\", "/")), len(f.Content))
		h.Write(f.Content)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Describe renders a one-line, human-readable reason a skill needs approval.
func (v Verdict) Describe() string {
	if !v.Executable {
		return "prose only"
	}
	parts := make([]string, 0, len(v.Capabilities))
	for _, c := range v.Capabilities {
		parts = append(parts, string(c)+" ("+strings.Join(v.Evidence[c], ", ")+")")
	}
	return strings.Join(parts, "; ")
}
