package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// sessionValidation holds the result of validating processed session data.
type sessionValidation struct {
	Warnings []string       `json:"warnings,omitempty"`
	Errors   []string       `json:"errors,omitempty"`
	Counts   map[string]int `json:"counts"` // entry type counts
}

// hasIssues returns true if there are any errors or warnings.
func (v *sessionValidation) hasIssues() bool {
	return len(v.Errors) > 0 || len(v.Warnings) > 0
}

// summary returns a one-line summary of the validation result.
func (v *sessionValidation) summary() string {
	if !v.hasIssues() {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(v.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", len(v.Errors)))
	}
	if len(v.Warnings) > 0 {
		parts = append(parts, fmt.Sprintf("%d warnings", len(v.Warnings)))
	}
	return "session data issues: " + strings.Join(parts, ", ")
}

// validSessionEntryTypes are types the web viewer can display.
var validSessionEntryTypes = map[string]bool{
	"user":      true,
	"assistant": true,
	"system":    true,
	"tool":      true,
}

// invalidLeakedTypes are internal Claude Code types that should never appear
// in processed session data. Their presence indicates an adapter bug.
var invalidLeakedTypes = map[string]bool{
	"queue-operation":       true,
	"file-history-snapshot": true,
	"progress":              true,
	"summary":               true,
	"last-prompt":           true,
}

// validateEntries checks processed session entries for data quality issues.
// Runs after the adapter has converted raw entries but before upload.
func validateEntries(entries []session.Entry) *sessionValidation {
	v := &sessionValidation{
		Counts: make(map[string]int),
	}

	for _, entry := range entries {
		t := string(entry.Type)
		v.Counts[t]++

		// check for leaked internal types
		if invalidLeakedTypes[t] {
			v.Errors = append(v.Errors,
				fmt.Sprintf("internal type %q leaked through adapter (should be filtered)", t))
			if len(v.Errors) > 20 {
				break
			}
			continue
		}

		// check for unknown types
		if !validSessionEntryTypes[t] {
			v.Warnings = append(v.Warnings,
				fmt.Sprintf("unexpected entry type %q (web viewer may not display it)", t))
		}
	}

	// check for missing user messages (a session should have at least one)
	if v.Counts["user"] == 0 && len(entries) > 5 {
		v.Warnings = append(v.Warnings,
			"no user messages found — session may be missing human prompts")
	}

	// check for missing assistant messages
	if v.Counts["assistant"] == 0 && len(entries) > 5 {
		v.Warnings = append(v.Warnings,
			"no assistant messages found — session may be missing AI responses")
	}

	return v
}

// validateRawJSONLFile checks a written raw.jsonl file for data quality issues.
// Used by session upload to validate before uploading to the ledger.
func validateRawJSONLFile(path string) *sessionValidation {
	v := &sessionValidation{
		Counts: make(map[string]int),
	}

	// check for LFS pointer
	if lfs.IsPointerFile(path) {
		v.Errors = append(v.Errors, "raw.jsonl is an LFS stub (not actual content)")
		return v
	}

	f, err := os.Open(path)
	if err != nil {
		v.Errors = append(v.Errors, fmt.Sprintf("cannot read raw.jsonl: %s", err))
		return v
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	totalEntries := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("invalid JSON in raw.jsonl: %s", err))
			if len(v.Errors) > 5 {
				break
			}
			continue
		}

		entryType, _ := entry["type"].(string)

		// skip structural entries
		if entryType == "header" || entryType == "footer" {
			continue
		}
		if _, hasMeta := entry["_meta"]; hasMeta {
			continue
		}

		totalEntries++
		v.Counts[entryType]++

		// check for leaked internal types
		if invalidLeakedTypes[entryType] {
			v.Errors = append(v.Errors,
				fmt.Sprintf("internal type %q in raw.jsonl (adapter did not filter it)", entryType))
			if len(v.Errors) > 20 {
				break
			}
		}

		// check for unknown types
		if !validSessionEntryTypes[entryType] && !invalidLeakedTypes[entryType] {
			v.Warnings = append(v.Warnings,
				fmt.Sprintf("unexpected entry type %q in raw.jsonl", entryType))
		}
	}

	if err := scanner.Err(); err != nil {
		v.Errors = append(v.Errors, fmt.Sprintf("error reading raw.jsonl: %s", err))
	}

	if totalEntries == 0 {
		v.Errors = append(v.Errors, "raw.jsonl has no entries")
	}

	if v.Counts["user"] == 0 && totalEntries > 5 {
		v.Warnings = append(v.Warnings,
			"no user messages in raw.jsonl — session may be missing human prompts")
	}

	return v
}

// validateHTMLConsistency checks that session.html was generated from the
// current raw.jsonl by comparing entry counts.
func validateHTMLConsistency(htmlPath, rawPath string) *sessionValidation {
	v := &sessionValidation{
		Counts: make(map[string]int),
	}

	// both must exist
	if _, err := os.Stat(htmlPath); os.IsNotExist(err) {
		return v // no HTML to validate
	}
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		return v // no raw to compare against
	}

	// skip if either is an LFS pointer
	if lfs.IsPointerFile(htmlPath) || lfs.IsPointerFile(rawPath) {
		return v
	}

	// count entries in raw.jsonl
	rawCounts := countRawEntryTypes(rawPath)
	if rawCounts == nil {
		return v
	}

	// quick check: if HTML is suspiciously small compared to raw entry count
	htmlInfo, err := os.Stat(htmlPath)
	if err != nil {
		return v
	}

	totalRaw := 0
	for _, c := range rawCounts {
		totalRaw += c
	}

	// heuristic: a well-formed session.html should be at least ~500 bytes per entry
	// (HTML wrapper, CSS, each entry rendered). If it's much smaller, something is wrong.
	if totalRaw > 10 && htmlInfo.Size() < int64(totalRaw*100) {
		v.Warnings = append(v.Warnings,
			fmt.Sprintf("session.html (%d bytes) seems too small for %d entries — may be stale or malformed",
				htmlInfo.Size(), totalRaw))
	}

	return v
}

// countRawEntryTypes counts entry types in a raw.jsonl file.
func countRawEntryTypes(path string) map[string]int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	counts := make(map[string]int)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]any
		if json.Unmarshal(line, &entry) != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		if entryType == "header" || entryType == "footer" {
			continue
		}
		if _, hasMeta := entry["_meta"]; hasMeta {
			continue
		}
		counts[entryType]++
	}

	return counts
}
