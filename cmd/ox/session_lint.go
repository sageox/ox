package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// lintResult holds the outcome of linting a session's raw.jsonl.
type lintResult struct {
	SessionName string   `json:"session_name"`
	Valid       bool     `json:"valid"`
	Errors      []string `json:"errors,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	EntryCount  int      `json:"entry_count"`
	TypeCounts  map[string]int `json:"type_counts"`
	HasHeader   bool     `json:"has_header"`
	HasFooter   bool     `json:"has_footer"`
}

// validRawEntryTypes are the types the web viewer can display.
var validRawEntryTypes = map[string]bool{
	"user":      true,
	"assistant": true,
	"system":    true,
	"tool":      true,
}

var sessionLintCmd = &cobra.Command{
	Use:   "lint [session-name]",
	Short: "Validate session raw.jsonl for web viewer compatibility",
	Long: `Validate that a session's raw.jsonl is correctly formatted for the web viewer.

Checks that every entry has a valid type (user, assistant, system, tool),
required fields are present, and the format matches what the web viewer
parser expects.

Use --file to lint a standalone JSONL file instead of a ledger session.
Use --all to lint all sessions in the ledger.

Examples:
  ox session lint OxRvKb
  ox session lint 2026-03-11T22-13-rsnodgrass-OxRvKb
  ox session lint --file /tmp/raw.jsonl
  ox session lint --all`,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		filePath, _ := cmd.Flags().GetString("file")
		all, _ := cmd.Flags().GetBool("all")

		if filePath != "" {
			result := lintRawJSONLFile(filePath, filepath.Base(filePath))
			return printLintResults([]lintResult{result}, jsonOutput)
		}

		ledgerPath, err := resolveLedgerPath()
		if err != nil {
			return err
		}

		sessionsDir := filepath.Join(ledgerPath, "sessions")

		if all {
			return lintAllSessions(sessionsDir, jsonOutput)
		}

		if len(args) == 0 {
			return fmt.Errorf("provide a session name, --file path, or --all")
		}

		sessionName, err := resolveSessionInDir(sessionsDir, args[0])
		if err != nil {
			return err
		}

		rawPath := filepath.Join(sessionsDir, sessionName, "raw.jsonl")

		// if file is an LFS pointer, report it
		if lfs.IsPointerFile(rawPath) {
			result := lintResult{
				SessionName: sessionName,
				Valid:       false,
				Errors:      []string{"raw.jsonl is an LFS pointer (not hydrated). Run 'ox session download' first."},
			}
			return printLintResults([]lintResult{result}, jsonOutput)
		}

		result := lintRawJSONLFile(rawPath, sessionName)
		return printLintResults([]lintResult{result}, jsonOutput)
	},
}

func init() {
	sessionCmd.AddCommand(sessionLintCmd)
	sessionLintCmd.Flags().String("file", "", "lint a standalone JSONL file instead of a ledger session")
	sessionLintCmd.Flags().Bool("all", false, "lint all sessions in the ledger")
}

// lintRawJSONLFile validates a raw.jsonl file for web viewer compatibility.
func lintRawJSONLFile(path, name string) lintResult {
	result := lintResult{
		SessionName: name,
		Valid:       true,
		Errors:      make([]string, 0),
		Warnings:    make([]string, 0),
		TypeCounts:  make(map[string]int),
	}

	f, err := os.Open(path)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("cannot open file: %s", err))
		return result
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	lineNum := 0
	lastSeq := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		lineNum++

		if len(line) == 0 {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: invalid JSON: %s", lineNum, err))
			continue
		}

		entryType, _ := entry["type"].(string)

		// skip header/footer (valid structural entries)
		if entryType == "header" {
			result.HasHeader = true
			continue
		}
		if entryType == "footer" {
			result.HasFooter = true
			continue
		}

		// skip _meta entries (capture-prior format)
		if _, hasMeta := entry["_meta"]; hasMeta {
			continue
		}

		result.EntryCount++
		result.TypeCounts[entryType]++

		// validate type
		if !validRawEntryTypes[entryType] {
			result.Valid = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("line %d: invalid type=%q (expected user, assistant, system, or tool)", lineNum, entryType))
			if len(result.Errors) > 20 {
				result.Errors = append(result.Errors, "... (truncated, too many errors)")
				break
			}
			continue
		}

		// validate timestamp
		if _, hasTS := entry["ts"]; !hasTS {
			if _, hasTimestamp := entry["timestamp"]; !hasTimestamp {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("line %d: missing ts/timestamp field", lineNum))
			}
		}

		// validate seq
		if seqVal, hasSeq := entry["seq"]; hasSeq {
			var seq int
			switch v := seqVal.(type) {
			case float64:
				seq = int(v)
			}
			if seq > 0 && seq <= lastSeq {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("line %d: seq=%d not monotonically increasing (last=%d)", lineNum, seq, lastSeq))
			}
			if seq > 0 {
				lastSeq = seq
			}
		}

		// validate content for non-tool entries
		if entryType == "user" || entryType == "assistant" || entryType == "system" {
			content, _ := entry["content"].(string)
			if content == "" {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("line %d: %s entry has empty content", lineNum, entryType))
			}
		}

		// validate tool entries
		if entryType == "tool" {
			toolName, _ := entry["tool_name"].(string)
			toolOutput, _ := entry["tool_output"].(string)
			if toolName == "" && toolOutput == "" {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("line %d: tool entry missing both tool_name and tool_output", lineNum))
			}
		}

		// cap warnings
		if len(result.Warnings) > 50 {
			result.Warnings = append(result.Warnings, "... (truncated)")
			break
		}
	}

	if err := scanner.Err(); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("read error: %s", err))
	}

	if result.EntryCount == 0 && result.Valid {
		result.Valid = false
		result.Errors = append(result.Errors, "no entries found in file")
	}

	return result
}

// lintAllSessions validates all sessions in the ledger.
func lintAllSessions(sessionsDir string, jsonOutput bool) error {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return fmt.Errorf("read sessions dir: %w", err)
	}

	var results []lintResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		rawPath := filepath.Join(sessionsDir, entry.Name(), "raw.jsonl")
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			continue
		}

		if lfs.IsPointerFile(rawPath) {
			results = append(results, lintResult{
				SessionName: entry.Name(),
				Valid:       false,
				Errors:      []string{"raw.jsonl is an LFS pointer (not hydrated)"},
			})
			continue
		}

		results = append(results, lintRawJSONLFile(rawPath, entry.Name()))
	}

	return printLintResults(results, jsonOutput)
}

// printLintResults outputs lint results as JSON or human-readable text.
func printLintResults(results []lintResult, jsonOutput bool) error {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	allValid := true
	for _, r := range results {
		if !r.Valid {
			allValid = false
		}
		printLintResult(r)
	}

	if len(results) > 1 {
		valid := 0
		for _, r := range results {
			if r.Valid {
				valid++
			}
		}
		fmt.Printf("\n%d/%d sessions valid\n", valid, len(results))
	}

	if !allValid {
		return fmt.Errorf("lint failed")
	}
	return nil
}

func printLintResult(r lintResult) {
	if r.Valid {
		fmt.Printf("%s %s  %d entries", ui.RenderPassIcon(), r.SessionName, r.EntryCount)
		if len(r.TypeCounts) > 0 {
			var parts []string
			for _, t := range []string{"user", "assistant", "tool", "system"} {
				if c, ok := r.TypeCounts[t]; ok {
					parts = append(parts, fmt.Sprintf("%s:%d", t, c))
				}
			}
			fmt.Printf("  (%s)", strings.Join(parts, " "))
		}
		fmt.Println()
	} else {
		fmt.Printf("%s %s\n", ui.RenderFailIcon(), r.SessionName)
		for _, e := range r.Errors {
			fmt.Printf("    %s\n", e)
		}
	}

	if len(r.Warnings) > 0 {
		for _, w := range r.Warnings {
			fmt.Printf("    %s %s\n", ui.RenderWarnIcon(), w)
		}
	}
}
