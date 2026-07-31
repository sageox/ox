package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
)

// adapterBlockMarkers enumerates every marker pair ever emitted by an
// ox adapter into AGENTS.md / CONVENTIONS.md. Each entry is "recognized"
// for detection and removal purposes — whether the adapter still emits
// the marker today or it's legacy.
//
// Keep this list in sync with the constants in:
//   - cmd/ox-adapter-pi/hooks.go
//   - cmd/ox-adapter-amp/hooks.go
//   - cmd/ox-adapter-aider/hooks.go
//   - cmd/ox/hooks_pi.go
//
// The universal markers <!-- ox:prime-check --> and <!-- ox:prime --> are
// intentionally NOT in this list. They are the agent-agnostic contract and
// must survive the fix.
var adapterBlockMarkers = []struct {
	startMarker string
	endMarker   string
	adapter     string // short human label for the finding detail
}{
	// current namespaced markers — one per adapter
	{"<!-- ox:prime:pi:start -->", "<!-- ox:prime:pi:end -->", "Pi"},
	{"<!-- ox:prime:amp:start -->", "<!-- ox:prime:amp:end -->", "Amp"},
	{"<!-- ox:prime:aider:start -->", "<!-- ox:prime:aider:end -->", "Aider"},
	// pre-#527 in-process-only Pi marker (hooks_pi.go used this before the unification)
	{"<!-- ox:pi-prime:start -->", "<!-- ox:pi-prime:end -->", "Pi (legacy in-process marker)"},
	// pre-#527 generic marker — used by Pi, Amp, and Aider external adapters.
	// The block's authorship is ambiguous, so the finding simply labels it "generic".
	// MUST be scanned LAST so namespaced markers are matched first when both happen
	// to be present in the same file during a transitional upgrade.
	{"<!-- ox:prime:start -->", "<!-- ox:prime:end -->", "generic (pre-#527)"},
}

// adapterBlockFinding summarizes what a single scan found in one file.
type adapterBlockFinding struct {
	file           string   // repo-relative path
	blocks         []string // adapter labels, in discovery order
	hasAgentEnv    bool     // true if any removed block contained AGENT_ENV=<adapter>
	symlinkPartner string   // repo-relative path of the symlinked counterpart, if any
}

// checkAdapterPrimeBlocks scans AGENTS.md / CONVENTIONS.md / CLAUDE.md at
// the repo root for adapter-specific prime blocks. With fix=true, it
// removes them in-place while preserving the universal ox:prime-check
// and ox:prime markers.
//
// Findings are elevated to warning severity when an AGENT_ENV=<adapter>
// hardcode is present inside the block (the #527 cross-agent poisoning
// pattern) or when the host file is symlinked to another instruction
// file (the #527 symlink amplifier).
func checkAdapterPrimeBlocks(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Adapter prime blocks", "not in git repo", "")
	}

	// Candidate files and the symlink mapping between them. AGENTS.md and
	// CLAUDE.md are often linked together via ox init; if one is a symlink
	// we dedupe so we don't "find" and "fix" the same physical file twice.
	candidates := []string{"AGENTS.md", "CLAUDE.md", "CONVENTIONS.md"}

	// map real-path → repo-relative name we first encountered, so symlinked
	// siblings collapse to one scan target
	seen := map[string]string{}
	// map repo-relative name → symlink partner (empty if not symlinked)
	partners := map[string]string{}

	var scanOrder []string
	for _, name := range candidates {
		path := filepath.Join(gitRoot, name)
		lst, err := os.Lstat(path)
		if err != nil {
			continue
		}
		// resolve symlinks to detect AGENTS.md↔CLAUDE.md aliasing
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			realPath = path
		}
		if prior, dup := seen[realPath]; dup {
			// this candidate points at the same file as `prior` — record
			// the pairing as a partner for the finding detail
			partners[prior] = name
			continue
		}
		seen[realPath] = name
		scanOrder = append(scanOrder, name)
		if lst.Mode()&os.ModeSymlink != 0 {
			// symlink — note its target for the partner mapping
			if tgt, readErr := os.Readlink(path); readErr == nil {
				partners[name] = filepath.Base(tgt)
			}
		}
	}

	var findings []adapterBlockFinding
	// fix-mode error accumulator: partial success is reported alongside
	// failures so users see which files were repaired and which weren't,
	// instead of aborting at the first bad write.
	type fixFailure struct {
		file string
		err  error
	}
	var fixFailures []fixFailure
	var fixSucceeded []string

	for _, name := range scanOrder {
		path := filepath.Join(gitRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		finding := scanAdapterBlocks(content)
		if len(finding.blocks) == 0 {
			continue
		}
		finding.file = name
		finding.symlinkPartner = partners[name]
		findings = append(findings, finding)

		if fix {
			cleaned := content
			for _, m := range adapterBlockMarkers {
				cleaned = stripBlock(cleaned, m.startMarker, m.endMarker)
			}
			if cleaned != content {
				if err := fileutil.AtomicWriteBytes(path, []byte(cleaned), 0644); err != nil {
					fixFailures = append(fixFailures, fixFailure{file: name, err: err})
				} else {
					fixSucceeded = append(fixSucceeded, name)
				}
			}
		}
	}

	if len(findings) == 0 {
		return PassedCheck("Adapter prime blocks", "no adapter-specific blocks in AGENTS.md/CONVENTIONS.md")
	}

	// build the message + detail
	msg := summarizeBlockFindings(findings)
	detailLines := []string{
		"Adapter-specific prime blocks in a shared instruction file can mis-route Claude Code (and other agents) through the wrong adapter. See #527.",
	}
	for _, f := range findings {
		line := fmt.Sprintf("%s: %s", f.file, strings.Join(f.blocks, ", "))
		if f.hasAgentEnv {
			line += " — contains AGENT_ENV=<adapter> hardcode (active poisoning)"
		}
		if f.symlinkPartner != "" {
			line += fmt.Sprintf(" — symlinked to %s (contaminates both)", f.symlinkPartner)
		}
		detailLines = append(detailLines, "  "+line)
	}
	switch {
	case !fix:
		detailLines = append(detailLines, "Run `ox doctor --fix` to remove these blocks while preserving the universal ox:prime markers.")
	case len(fixFailures) == 0:
		detailLines = append(detailLines, "Blocks were removed. Universal ox:prime markers were preserved.")
	default:
		if len(fixSucceeded) > 0 {
			detailLines = append(detailLines,
				fmt.Sprintf("Partial success — repaired: %s", strings.Join(fixSucceeded, ", ")))
		}
		for _, ff := range fixFailures {
			detailLines = append(detailLines,
				fmt.Sprintf("  FAILED to write %s: %v", ff.file, ff.err))
		}
	}
	detail := strings.Join(detailLines, "\n")

	if fix {
		if len(fixFailures) > 0 {
			return FailedCheck("Adapter prime blocks",
				fmt.Sprintf("fix failed for %d of %d file(s)", len(fixFailures), len(fixFailures)+len(fixSucceeded)),
				detail)
		}
		return PassedCheck("Adapter prime blocks", "removed "+msg)
	}
	return WarningCheck("Adapter prime blocks", msg, detail)
}

// scanAdapterBlocks returns the set of adapter blocks detected in content.
// AGENT_ENV hardcodes are flagged when found inside any recognized block.
func scanAdapterBlocks(content string) adapterBlockFinding {
	var f adapterBlockFinding
	seen := map[string]struct{}{}
	for _, m := range adapterBlockMarkers {
		startIdx := strings.Index(content, m.startMarker)
		if startIdx == -1 {
			continue
		}
		endIdx := strings.Index(content, m.endMarker)
		if endIdx == -1 || endIdx < startIdx {
			continue
		}
		if _, dup := seen[m.adapter]; !dup {
			f.blocks = append(f.blocks, m.adapter)
			seen[m.adapter] = struct{}{}
		}
		// look inside the block for a hardcoded AGENT_ENV=<adapter>
		body := content[startIdx:endIdx]
		for _, lit := range []string{"AGENT_ENV=pi", "AGENT_ENV=amp", "AGENT_ENV=aider", "AGENT_ENV=codex", "AGENT_ENV=gemini", "AGENT_ENV=droid", "AGENT_ENV=opencode", "AGENT_ENV=goose"} {
			if strings.Contains(body, lit) {
				f.hasAgentEnv = true
				break
			}
		}
	}
	return f
}

func summarizeBlockFindings(findings []adapterBlockFinding) string {
	var total int
	var files []string
	for _, f := range findings {
		total += len(f.blocks)
		files = append(files, f.file)
	}
	plural := ""
	if total != 1 {
		plural = "s"
	}
	return fmt.Sprintf("%d adapter block%s found in %s", total, plural, strings.Join(files, ", "))
}

// stripBlock removes one start...end block (inclusive) from content,
// collapsing surrounding blank lines. Returns content unchanged if either
// marker is absent, or if no end marker appears AFTER the start marker —
// the latter guards against an orphan end marker earlier in the file
// silently dropping arbitrary text between the orphan and the real start
// when `ox doctor --fix` runs on corrupted input (the exact scenario this
// check is built for). Mirrors the guard in scanAdapterBlocks.
// CodeRabbit review on #543.
func stripBlock(content, startMarker, endMarker string) string {
	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return content
	}
	// search for endMarker AFTER the start marker so an orphan end marker
	// earlier in the file can't form an inverted range
	rel := strings.Index(content[startIdx+len(startMarker):], endMarker)
	if rel == -1 {
		return content
	}
	endIdx := startIdx + len(startMarker) + rel + len(endMarker)

	before := strings.TrimRight(content[:startIdx], "\n")
	after := strings.TrimLeft(content[endIdx:], "\n")

	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after + "\n"
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}
