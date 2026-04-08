package claude

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// MergeMemory semantically merges new memory files into existing ledger memory.
// Shells out to `claude -p` with a merge prompt.
//
// Conflict resolution: ALWAYS favor the fresh Claude-generated memory (new).
// Claude's recent observations reflect the current codebase state and are more
// likely to be correct than older ledger content. Unique insights from the
// existing ledger are preserved only if not contradicted by the new memory.
//
// Returns the merged file contents as a map of relative path → content.
// Falls back to returning the new files unmodified if claude CLI is unavailable.
func MergeMemory(ctx context.Context, existing, newFiles map[string][]byte) (map[string][]byte, error) {
	prompt := buildMergePrompt(existing, newFiles)

	// shell out to claude -p for one-shot merge
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// if claude CLI is not available, fall back to new files (prefer fresh)
		if isNotFoundErr(err) {
			return newFiles, nil
		}
		return nil, fmt.Errorf("claude merge failed: %s: %w", stderr.String(), err)
	}

	return ParseMergeOutput(stdout.String())
}

// CLIAvailable checks if the `claude` CLI is installed.
func CLIAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// buildMergePrompt constructs the prompt for claude -p merge.
func buildMergePrompt(existing, newFiles map[string][]byte) string {
	var b strings.Builder

	b.WriteString("Given two sets of project memory files:\n\n")
	b.WriteString("EXISTING (currently in ledger):\n")
	writeFileSet(&b, existing)

	b.WriteString("\nNEW (from fresh local workspace):\n")
	writeFileSet(&b, newFiles)

	b.WriteString(`
Merge into a single coherent set of memory files. Rules:
- When conflicts exist, ADOPT the fresh Claude-generated memory (NEW)
- Claude's own recent observations are more likely to be current and correct
- Fold in unique insights from EXISTING that are not contradicted by NEW
- Remove stale or contradicted patterns
- Consolidate duplicate/similar learnings
- Keep MEMORY.md under 200 lines (Claude's budget)
- Preserve topic file structure (separate files for separate topics)

Output format — for EACH file, use this exact format:
--- filename.md ---
<file contents>
--- END ---

Output ONLY the merged files, nothing else.`)

	return b.String()
}

// writeFileSet writes a map of files in a readable format for the merge prompt.
func writeFileSet(b *strings.Builder, files map[string][]byte) {
	// sort for deterministic output
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(b, "\n--- %s ---\n%s\n--- END ---\n", name, string(files[name]))
	}
}

// ParseMergeOutput parses the structured output from claude -p merge.
// Exported for testing from the memoryimport package.
func ParseMergeOutput(output string) (map[string][]byte, error) {
	files := make(map[string][]byte)

	lines := strings.Split(output, "\n")
	var currentFile string
	var content strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "--- ") && strings.HasSuffix(line, " ---") {
			inner := strings.TrimPrefix(line, "--- ")
			inner = strings.TrimSuffix(inner, " ---")

			if inner == "END" {
				// close current file
				if currentFile != "" {
					files[currentFile] = []byte(strings.TrimRight(content.String(), "\n"))
					currentFile = ""
					content.Reset()
				}
				continue
			}

			// start new file
			if currentFile != "" {
				// close previous file (missing END marker)
				files[currentFile] = []byte(strings.TrimRight(content.String(), "\n"))
				content.Reset()
			}
			currentFile = inner
			continue
		}

		if currentFile != "" {
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	// handle trailing file without END marker
	if currentFile != "" {
		files[currentFile] = []byte(strings.TrimRight(content.String(), "\n"))
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("merge produced no files (raw output: %s)", truncate(output, 200))
	}

	return files, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func isNotFoundErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "executable file not found")
}
