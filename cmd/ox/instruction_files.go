package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
)

// Agent type constants — local to avoid importing agentx for a static registry.
const (
	agentClaudeCode = "claude-code"
	agentCodex      = "codex"
	agentGemini     = "gemini"
	agentCursor     = "cursor"
	agentWindsurf   = "windsurf"
	agentCopilot    = "copilot"
	agentKiro       = "kiro"
	agentCline      = "cline"
	agentOpenCode   = "opencode"
	agentAmp        = "amp"
	agentAider      = "aider"
)

// Marker format constants.
const (
	markerFormatMarkdown  = "markdown"
	markerFormatPlaintext = "plaintext"
)

// Additional injectStatus values for instruction file operations.
// These extend the iota block in init.go (injectedNew=0, alreadyPresent=1,
// injectedUpgrade=2, symlinkCreated=3).
const (
	injectedUpdate injectStatus = 10 // existing file was updated with markers
	injectedSkip   injectStatus = 11 // file was skipped (not created)
)

// Plaintext equivalents of the markdown markers for files that don't support HTML comments.
const (
	PlaintextPrimeCheckBlock  = "# ox:prime-check\n# BLOCKING: Run `ox agent prime` NOW before ANY other action.\n"
	PlaintextPrimeLine        = "# ox:prime — Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)"
	PlaintextPrimeMarker      = "# ox:prime —"
	PlaintextPrimeCheckMarker = "# ox:prime-check"
)

// InstructionFileSpec describes how a coding agent reads project-level instructions.
type InstructionFileSpec struct {
	AgentType    string
	DisplayName  string
	ProjectFiles []string // paths relative to git root
	MarkerFormat string   // "markdown" or "plaintext"
	// DetectFn checks if this agent is actively used in the project.
	// nil means always consider for injection.
	DetectFn func(gitRoot string) bool
	// Creatable means ox may create the file from scratch when the agent is
	// detected (e.g. .kiro/steering/ox.md when .kiro/ exists). Primary files
	// (AGENTS.md, CLAUDE.md) are always creatable regardless of this flag.
	Creatable bool
}

// InstructionFileTarget is a resolved instruction file that should receive markers.
type InstructionFileTarget struct {
	Path   string // relative to git root
	Spec   *InstructionFileSpec
	Exists bool
}

// InstructionFileResult reports what happened to a single instruction file.
type InstructionFileResult struct {
	File      string       // relative to git root
	Status    injectStatus // reuses the existing injectStatus type from init.go
	AgentType string
}

// InstructionFileRegistry lists all known coding agent instruction file conventions.
var InstructionFileRegistry = []InstructionFileSpec{
	{
		AgentType:    agentClaudeCode,
		DisplayName:  "Claude Code",
		ProjectFiles: []string{"AGENTS.md", "CLAUDE.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     func(root string) bool { return dirExists(filepath.Join(root, ".claude")) },
	},
	{
		AgentType:    agentCodex,
		DisplayName:  "Codex",
		ProjectFiles: []string{"AGENTS.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     nil, // shares AGENTS.md
	},
	{
		AgentType:    agentGemini,
		DisplayName:  "Gemini CLI",
		ProjectFiles: []string{"GEMINI.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn: func(root string) bool {
			return dirExists(filepath.Join(root, ".gemini")) ||
				fileExists(filepath.Join(root, "GEMINI.md"))
		},
	},
	{
		AgentType:    agentCursor,
		DisplayName:  "Cursor",
		ProjectFiles: []string{".cursorrules"},
		MarkerFormat: markerFormatPlaintext,
		DetectFn: func(root string) bool {
			return fileExists(filepath.Join(root, ".cursorrules")) ||
				dirExists(filepath.Join(root, ".cursor"))
		},
	},
	{
		AgentType:    agentWindsurf,
		DisplayName:  "Windsurf",
		ProjectFiles: []string{".windsurfrules"},
		MarkerFormat: markerFormatPlaintext,
		DetectFn: func(root string) bool {
			return fileExists(filepath.Join(root, ".windsurfrules")) ||
				dirExists(filepath.Join(root, ".windsurf"))
		},
	},
	{
		AgentType:    agentCopilot,
		DisplayName:  "GitHub Copilot",
		ProjectFiles: []string{filepath.Join(".github", "copilot-instructions.md")},
		MarkerFormat: markerFormatMarkdown,
		DetectFn: func(root string) bool {
			return fileExists(filepath.Join(root, ".github", "copilot-instructions.md"))
		},
	},
	{
		AgentType:    agentKiro,
		DisplayName:  "Kiro",
		ProjectFiles: []string{filepath.Join(".kiro", "steering", "ox.md")},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     func(root string) bool { return dirExists(filepath.Join(root, ".kiro")) },
		Creatable:    true, // ox creates .kiro/steering/ox.md when .kiro/ exists
	},
	{
		AgentType:    agentCline,
		DisplayName:  "Cline",
		ProjectFiles: []string{".clinerules"},
		MarkerFormat: markerFormatPlaintext,
		DetectFn:     func(root string) bool { return fileExists(filepath.Join(root, ".clinerules")) },
	},
	{
		AgentType:    agentOpenCode,
		DisplayName:  "OpenCode",
		ProjectFiles: []string{"AGENTS.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     nil, // shares AGENTS.md
	},
	{
		AgentType:    agentAmp,
		DisplayName:  "Amp",
		ProjectFiles: []string{"AGENTS.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     nil, // shares AGENTS.md
	},
	{
		AgentType:    agentAider,
		DisplayName:  "Aider",
		ProjectFiles: []string{"AGENTS.md"},
		MarkerFormat: markerFormatMarkdown,
		DetectFn:     nil, // shares AGENTS.md
	},
}

// primaryInstructionFiles are files ox has always created and may seed from scratch.
// All others require Creatable=true on their spec or must already exist on disk.
var primaryInstructionFiles = map[string]bool{
	"AGENTS.md": true,
	"CLAUDE.md": true,
}

// DetectedInstructionFiles returns all instruction files that should have markers
// based on detection heuristics. Deduplicates by resolved absolute path (through
// symlinks) so that e.g. CLAUDE.md symlinked to AGENTS.md appears only once.
// Returned paths are relative to gitRoot.
func DetectedInstructionFiles(gitRoot string) []InstructionFileTarget {
	if gitRoot == "" {
		return nil
	}

	seen := make(map[string]bool)
	var targets []InstructionFileTarget

	for i := range InstructionFileRegistry {
		spec := &InstructionFileRegistry[i]

		// skip if detection says this agent isn't active
		if spec.DetectFn != nil && !spec.DetectFn(gitRoot) {
			continue
		}

		for _, relPath := range spec.ProjectFiles {
			absPath := filepath.Join(gitRoot, relPath)
			// resolve symlinks so CLAUDE.md -> AGENTS.md deduplicates correctly
			resolved, err := filepath.EvalSymlinks(absPath)
			if err != nil {
				resolved = absPath // fallback to unresolved if symlink is broken
			}
			if seen[resolved] {
				continue
			}
			seen[resolved] = true

			targets = append(targets, InstructionFileTarget{
				Path:   relPath,
				Spec:   spec,
				Exists: fileExists(absPath),
			})
		}
	}

	return targets
}

// EnsureInstructionFileMarkers idempotently injects ox:prime markers into all
// detected instruction files. Files that don't exist are only created if they
// are primary files (AGENTS.md, CLAUDE.md) or the spec has Creatable=true.
func EnsureInstructionFileMarkers(gitRoot string) ([]InstructionFileResult, error) {
	if gitRoot == "" {
		return nil, nil
	}

	targets := DetectedInstructionFiles(gitRoot)
	var results []InstructionFileResult

	for _, t := range targets {
		canCreate := primaryInstructionFiles[t.Path] || t.Spec.Creatable

		// safety: don't create files from scratch unless allowed
		if !t.Exists && !canCreate {
			results = append(results, InstructionFileResult{
				File:      t.Path,
				Status:    injectedSkip,
				AgentType: t.Spec.AgentType,
			})
			continue
		}

		absPath := filepath.Join(gitRoot, t.Path)
		status, err := ensureInstructionFileMarker(absPath, t.Spec.MarkerFormat, t.Exists)
		if err != nil {
			return results, fmt.Errorf("failed to inject markers into %s: %w", t.Path, err)
		}

		results = append(results, InstructionFileResult{
			File:      t.Path,
			Status:    status,
			AgentType: t.Spec.AgentType,
		})
	}

	return results, nil
}

// RemoveInstructionFileMarkers removes ox markers from all known instruction files.
// Only removes ox-owned content; preserves everything else.
func RemoveInstructionFileMarkers(gitRoot string) ([]InstructionFileResult, error) {
	if gitRoot == "" {
		return nil, nil
	}

	// scan all possible files, not just detected ones — removal should be thorough
	seen := make(map[string]bool)
	var results []InstructionFileResult

	for i := range InstructionFileRegistry {
		spec := &InstructionFileRegistry[i]
		for _, relPath := range spec.ProjectFiles {
			if seen[relPath] {
				continue
			}
			seen[relPath] = true

			absPath := filepath.Join(gitRoot, relPath)
			if !fileExists(absPath) {
				continue
			}

			status, err := removeInstructionFileMarker(absPath, spec.MarkerFormat)
			if err != nil {
				return results, fmt.Errorf("failed to remove markers from %s: %w", absPath, err)
			}

			results = append(results, InstructionFileResult{
				File:      relPath,
				Status:    status,
				AgentType: spec.AgentType,
			})
		}
	}

	return results, nil
}

// HasInstructionFileMarker checks if a file contains an ox:prime marker in
// either markdown or plaintext format.
func HasInstructionFileMarker(filePath string) bool {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}
	s := string(content)
	return strings.Contains(s, OxPrimeMarker) || strings.Contains(s, PlaintextPrimeMarker)
}

func ensureInstructionFileMarker(filePath, format string, exists bool) (injectStatus, error) {
	// skip symlinks — the target file will be written separately
	if info, err := os.Lstat(filePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return alreadyPresent, nil
	}

	headerBlock, footerLine, checkMarker, primeMarker := markersForFormat(format)

	if exists {
		// refuse to modify read-only files -- surface the error early rather than
		// silently replacing via atomic rename (which bypasses file permissions)
		if info, err := os.Stat(filePath); err == nil && info.Mode().Perm()&0200 == 0 {
			return 0, fmt.Errorf("file %s is read-only (mode %s)", filePath, info.Mode().Perm())
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			return 0, err
		}
		s := string(content)

		hasHeader := strings.Contains(s, checkMarker)
		hasFooter := strings.Contains(s, primeMarker)
		if hasHeader && hasFooter {
			return alreadyPresent, nil
		}

		modified := s

		if !hasHeader {
			modified = headerBlock + "\n" + modified
		}

		if !hasFooter {
			modified = strings.TrimRight(modified, "\n\t ") + "\n\n" + footerLine + "\n"
		}

		// safety: modified content must be >= 50% of original (only meaningful for large files)
		if len(modified) < len(s)/2 && len(s) > 100 {
			return 0, fmt.Errorf("safety check failed: modified content (%d bytes) is less than half of original (%d bytes)", len(modified), len(s))
		}

		// preserve the existing file's permission bits — atomic rename would
		// otherwise broaden perms (e.g. 0600 -> 0644), a privacy regression
		mode := os.FileMode(0644)
		if info, statErr := os.Stat(filePath); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := fileutil.AtomicWriteBytes(filePath, []byte(modified), mode); err != nil {
			return 0, err
		}
		return injectedUpdate, nil
	}

	// create new file (only called for primary or Creatable files)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	content := headerBlock + "\n# AI Agent Instructions\n\n" + footerLine + "\n"
	if err := fileutil.AtomicWriteBytes(filePath, []byte(content), 0644); err != nil {
		return 0, err
	}
	return injectedNew, nil
}

func removeInstructionFileMarker(filePath, format string) (injectStatus, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}
	s := string(content)

	// check for markers in BOTH formats for thorough cleanup — HasInstructionFileMarker
	// checks both, so removal must match that behavior
	_, _, mdCheck, mdPrime := markersForFormat(markerFormatMarkdown)
	_, _, ptCheck, ptPrime := markersForFormat(markerFormatPlaintext)

	hasAny := strings.Contains(s, mdCheck) || strings.Contains(s, mdPrime) ||
		strings.Contains(s, ptCheck) || strings.Contains(s, ptPrime)
	if !hasAny {
		return alreadyPresent, nil
	}

	cleaned := s

	// remove all markdown header blocks (loop for duplicates)
	for strings.Contains(cleaned, mdCheck) {
		cleaned = removeMarkerBlock(cleaned, markerFormatMarkdown, true)
	}
	// remove all markdown footer lines
	for strings.Contains(cleaned, mdPrime) {
		cleaned = removeMarkerBlock(cleaned, markerFormatMarkdown, false)
	}
	// remove all plaintext header blocks
	for strings.Contains(cleaned, ptCheck) {
		cleaned = removeMarkerBlock(cleaned, markerFormatPlaintext, true)
	}
	// remove all plaintext footer lines
	for strings.Contains(cleaned, ptPrime) {
		cleaned = removeMarkerBlock(cleaned, markerFormatPlaintext, false)
	}

	// collapse excessive blank lines
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}
	cleaned = strings.TrimRight(cleaned, "\n\t ")
	if cleaned != "" {
		cleaned += "\n"
	}

	// safety: don't shrink large user content dramatically.
	// Only apply when the original had substantial non-marker content (>500 bytes),
	// since files that are mostly markers will naturally shrink below 50%.
	if len(cleaned) < len(s)/2 && len(s) > 500 {
		return 0, fmt.Errorf("safety check failed: cleaned content (%d bytes) is less than half of original (%d bytes)", len(cleaned), len(s))
	}

	// preserve the existing file's permission bits when rewriting
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(filePath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := fileutil.AtomicWriteBytes(filePath, []byte(cleaned), mode); err != nil {
		return 0, err
	}

	return injectedUpdate, nil
}

// removeMarkerBlock removes an ox header or footer block from content.
func removeMarkerBlock(content, format string, isHeader bool) string {
	var marker string
	if isHeader {
		if format == markerFormatMarkdown {
			marker = OxPrimeCheckMarker
		} else {
			marker = PlaintextPrimeCheckMarker
		}
	} else {
		if format == markerFormatMarkdown {
			marker = OxPrimeMarker
		} else {
			marker = PlaintextPrimeMarker
		}
	}

	idx := strings.Index(content, marker)
	if idx < 0 {
		return content
	}

	// find line start
	lineStart := idx
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}

	if isHeader {
		// header spans multiple lines — remove until first blank line or 3 lines max
		lineEnd := idx
		linesRemoved := 0
		for lineEnd < len(content) && linesRemoved < 3 {
			nlIdx := strings.Index(content[lineEnd:], "\n")
			if nlIdx < 0 {
				lineEnd = len(content)
				break
			}
			lineEnd += nlIdx + 1
			linesRemoved++
			// stop at blank line
			if lineEnd < len(content) && content[lineEnd] == '\n' {
				lineEnd++ // consume the blank line
				break
			}
		}
		return content[:lineStart] + content[lineEnd:]
	}

	// footer: remove the single line containing the marker
	lineEnd := idx + len(marker)
	for lineEnd < len(content) && content[lineEnd] != '\n' {
		lineEnd++
	}
	if lineEnd < len(content) {
		lineEnd++ // include trailing newline
	}
	return content[:lineStart] + content[lineEnd:]
}

// markersForFormat returns (headerBlock, footerLine, checkMarker, primeMarker)
// appropriate for the given format.
func markersForFormat(format string) (string, string, string, string) {
	if format == markerFormatPlaintext {
		return PlaintextPrimeCheckBlock, PlaintextPrimeLine, PlaintextPrimeCheckMarker, PlaintextPrimeMarker
	}
	return OxPrimeCheckBlock, OxPrimeLine, OxPrimeCheckMarker, OxPrimeMarker
}
