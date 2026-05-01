package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
)

// OxPrimeMarker is the grep-able HTML comment marker for verification (footer).
// HTML comment format: invisible when rendered, easy to grep/verify.
const OxPrimeMarker = "<!-- ox:prime -->"

// OxPrimeLine is the canonical single-line directive (footer).
// This replaces the old multi-line SageOxPrimeLine block.
const OxPrimeLine = "<!-- ox:prime --> Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)"

// OxPrimeCheckMarker is the header marker (belt-and-suspenders redundancy).
// Placed at top of file for primacy effect - agents see it first.
const OxPrimeCheckMarker = "<!-- ox:prime-check -->"

// OxPrimeCheckBlock is the header block injected at line 1.
// BLOCKING directive: agents must run prime BEFORE responding to the user.
// Strong wording is intentional — without it, LLMs treat the user's question as higher priority.
const OxPrimeCheckBlock = `<!-- ox:prime-check -->
**BLOCKING**: Run ` + "`ox agent prime`" + ` NOW before ANY other action. Do NOT respond to the user first. Enables SageOx queries.
`

// HasOxPrimeMarker checks if the ox:prime footer marker exists in AGENTS.md or CLAUDE.md.
// Designed for frequent anti-entropy calls - minimal I/O, fast return on first match.
func HasOxPrimeMarker(gitRoot string) bool {
	if gitRoot == "" {
		return false
	}

	// check AGENTS.md first (primary file)
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if content, err := os.ReadFile(agentsPath); err == nil {
		if strings.Contains(string(content), OxPrimeMarker) {
			return true
		}
	}

	// check CLAUDE.md as fallback
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if content, err := os.ReadFile(claudePath); err == nil {
		if strings.Contains(string(content), OxPrimeMarker) {
			return true
		}
	}

	return false
}

// HasOxPrimeCheckMarker checks if the ox:prime-check header marker exists in AGENTS.md or CLAUDE.md.
func HasOxPrimeCheckMarker(gitRoot string) bool {
	if gitRoot == "" {
		return false
	}

	// check AGENTS.md first (primary file)
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if content, err := os.ReadFile(agentsPath); err == nil {
		if strings.Contains(string(content), OxPrimeCheckMarker) {
			return true
		}
	}

	// check CLAUDE.md as fallback
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if content, err := os.ReadFile(claudePath); err == nil {
		if strings.Contains(string(content), OxPrimeCheckMarker) {
			return true
		}
	}

	return false
}

// HasBothPrimeMarkers checks if both header and footer markers exist.
func HasBothPrimeMarkers(gitRoot string) bool {
	return HasOxPrimeMarker(gitRoot) && HasOxPrimeCheckMarker(gitRoot)
}

// EnsureOxPrimeMarker adds both header and footer markers if missing from AGENTS.md or CLAUDE.md.
// It also handles upgrade from legacy SageOxPrimeLine block to the new format.
// Returns (injected bool, error) where injected is true if any marker was added or upgraded.
//
// Concurrency: deliberately lock-free. The realistic contended writer
// here is the user's editor (vim, VS Code) saving the file out from
// under us; flock between two ox processes does nothing about that
// case because the editor doesn't participate in advisory locks.
// What flock WOULD protect — two ox processes both seeing "missing"
// between read and write — produces at worst one duplicate marker
// line, which the next caller's strings.Contains check makes
// idempotent on the very next pass. The cost of a lock outweighs the
// benefit. See ox-l07b for the original analysis.
//
// Defense in depth: we recompute marker presence after each read so a
// concurrent ox process injecting the same markers doesn't cause us
// to double-inject. The atomic write inside ensureMarkersInFile
// guarantees readers always see either the pre-write or post-write
// state, never a torn intermediate.
func EnsureOxPrimeMarker(gitRoot string) (bool, error) {
	if gitRoot == "" {
		return false, nil
	}

	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")

	// fast path: both markers already present in either file → nothing to do.
	if HasOxPrimeMarker(gitRoot) && HasOxPrimeCheckMarker(gitRoot) {
		return false, nil
	}

	// try AGENTS.md first (primary file)
	if injected, err := ensureMarkersInExistingFile(agentsPath); err != nil {
		return false, err
	} else if injected {
		return true, nil
	}

	// try CLAUDE.md
	if injected, err := ensureMarkersInExistingFile(claudePath); err != nil {
		return false, err
	} else if injected {
		return true, nil
	}

	// neither file exists — seed AGENTS.md with both markers.
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		content := OxPrimeCheckBlock + "\n# AI Agent Instructions\n\n" + OxPrimeLine + "\n"
		if err := fileutil.AtomicWriteBytes(agentsPath, []byte(content), 0644); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

// ensureMarkersInExistingFile reads filePath, computes marker presence,
// and only invokes the rewrite path when injection is genuinely needed.
// Idempotent — repeated calls converge.
func ensureMarkersInExistingFile(filePath string) (bool, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // caller will try the other file or seed
		}
		return false, err
	}
	needHeader := !strings.Contains(string(content), OxPrimeCheckMarker)
	needFooter := !strings.Contains(string(content), OxPrimeMarker)
	if !needHeader && !needFooter {
		return false, nil
	}
	return ensureMarkersInFile(filePath, string(content), needHeader, needFooter)
}

// ensureMarkersInFile adds header and/or footer markers to a file.
// It handles removing legacy SageOxPrimeLine blocks during the upgrade.
//
// SAFETY: This function is ADDITIVE ONLY for user content.
// - User content is never deleted or truncated
// - Only SageOx legacy blocks (our own content) are replaced with new format
// - New content must be at least as long as original (safety check)
//
// Returns (injected bool, error).
func ensureMarkersInFile(filePath, content string, needHeader, needFooter bool) (bool, error) {
	if !needHeader && !needFooter {
		return false, nil
	}

	// check for legacy SageOxPrimeLine block and remove it
	// legacy block starts with "## SageOx Infrastructure Guidance (REQUIRED)"
	cleaned := content
	legacyStart := "## SageOx Infrastructure Guidance (REQUIRED)"
	if idx := strings.Index(cleaned, legacyStart); idx != -1 {
		// find the end of the legacy block (ends with the command returns message)
		endMarker := "The command returns an agent_id confirming SageOx is active."
		if endIdx := strings.Index(cleaned[idx:], endMarker); endIdx != -1 {
			fullEndIdx := idx + endIdx + len(endMarker)
			// skip any trailing newlines and the next line if it continues the block
			for fullEndIdx < len(cleaned) && (cleaned[fullEndIdx] == '\n' || cleaned[fullEndIdx] == '\r') {
				fullEndIdx++
			}
			// check for continuation lines about "When asked if you're using SageOx"
			if fullEndIdx < len(cleaned) {
				remaining := cleaned[fullEndIdx:]
				if strings.HasPrefix(remaining, "When asked if you're using SageOx") {
					// find end of this line
					if nlIdx := strings.Index(remaining, "\n"); nlIdx != -1 {
						fullEndIdx += nlIdx + 1
					} else {
						fullEndIdx = len(cleaned)
					}
				}
			}
			// remove the legacy block
			cleaned = cleaned[:idx] + cleaned[fullEndIdx:]
		}
	}

	// also remove any legacy single-line patterns
	legacyPatterns := []string{
		"- **SageOx**: Run `ox agent prime` on session start, after compaction, and after clear for infrastructure superpowers.",
		"- **SageOx**: Run `ox agent prime` on session start, after compaction, and after clear for team context.",
		"**SageOx**: Run `ox agent prime` on session start",
	}
	for _, pattern := range legacyPatterns {
		if idx := strings.Index(cleaned, pattern); idx != -1 {
			// find and remove the entire line
			start := idx
			for start > 0 && cleaned[start-1] != '\n' {
				start--
			}
			end := idx + len(pattern)
			for end < len(cleaned) && cleaned[end] != '\n' {
				end++
			}
			if end < len(cleaned) {
				end++ // include the newline
			}
			cleaned = cleaned[:start] + cleaned[end:]
		}
	}

	// clean up any resulting multiple blank lines
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}

	// trim trailing whitespace and add proper ending
	cleaned = strings.TrimRight(cleaned, "\n\t ") + "\n"

	// add header at beginning if needed (and not already present)
	if needHeader && !strings.Contains(cleaned, OxPrimeCheckMarker) {
		cleaned = OxPrimeCheckBlock + "\n" + cleaned
	}

	// add footer at end if needed (and not already present)
	if needFooter && !strings.Contains(cleaned, OxPrimeMarker) {
		cleaned = cleaned + "\n" + OxPrimeLine + "\n"
	}

	// safety check: we should be additive - never shrink user content significantly.
	// Legacy block removal can reduce size slightly, but markers add more than enough.
	// If the result is somehow much smaller, something went wrong - abort.
	if len(cleaned) < len(content)/2 && len(content) > 100 {
		return false, fmt.Errorf("safety check failed: modified content (%d bytes) is less than half of original (%d bytes), aborting to protect user content", len(cleaned), len(content))
	}

	if err := fileutil.AtomicWriteBytes(filePath, []byte(cleaned), 0644); err != nil {
		return false, err
	}

	return true, nil
}
