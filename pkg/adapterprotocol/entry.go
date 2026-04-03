package adapterprotocol

import (
	"fmt"
	"time"
)

// EntrySchemaVersion identifies the raw.jsonl entry format version.
// Bumped when field semantics change in a backward-incompatible way.
const EntrySchemaVersion = 1

// validRoles is the set of accepted Role values for RawEntry.
var validRoles = map[string]bool{
	RoleUser:      true,
	RoleAssistant: true,
	RoleSystem:    true,
	RoleTool:      true,
}

// EntryIssue describes a problem found during entry validation.
type EntryIssue struct {
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Message string `json:"message"`
	IsError bool   `json:"is_error"` // false = warning
}

func (i EntryIssue) String() string {
	level := "warning"
	if i.IsError {
		level = "error"
	}
	if i.Index >= 0 {
		return fmt.Sprintf("entry %d: %s: %s (%s)", i.Index, i.Field, i.Message, level)
	}
	return fmt.Sprintf("%s: %s (%s)", i.Field, i.Message, level)
}

// Validate checks that a RawEntry has valid field values.
// Returns nil if the entry is valid.
func (e *RawEntry) Validate() error {
	issues := e.Check(-1)
	for _, issue := range issues {
		if issue.IsError {
			return fmt.Errorf("%s: %s", issue.Field, issue.Message)
		}
	}
	return nil
}

// Check returns all issues (errors and warnings) for a single entry.
// The index parameter is used for error reporting (-1 if not in a batch).
func (e *RawEntry) Check(index int) []EntryIssue {
	var issues []EntryIssue

	if !validRoles[e.Role] {
		issues = append(issues, EntryIssue{
			Index: index, Field: "role", IsError: true,
			Message: fmt.Sprintf("invalid role %q (expected user, assistant, system, or tool)", e.Role),
		})
	}

	if e.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339, e.Timestamp); err != nil {
			if _, err2 := time.Parse(time.RFC3339Nano, e.Timestamp); err2 != nil {
				issues = append(issues, EntryIssue{
					Index: index, Field: "timestamp", IsError: true,
					Message: fmt.Sprintf("not valid RFC3339: %s", e.Timestamp),
				})
			}
		}
	}

	switch e.Role {
	case RoleUser, RoleAssistant, RoleSystem:
		if e.Content == "" {
			issues = append(issues, EntryIssue{
				Index: index, Field: "content", IsError: false,
				Message: fmt.Sprintf("%s entry has empty content", e.Role),
			})
		}
	case RoleTool:
		if e.ToolName == "" && e.ToolOutput == "" {
			issues = append(issues, EntryIssue{
				Index: index, Field: "tool_name", IsError: false,
				Message: "tool entry missing both tool_name and tool_output",
			})
		}
	}

	return issues
}

// ValidateEntries checks a batch of entries and returns all issues found.
// Returns nil if all entries are valid.
func ValidateEntries(entries []RawEntry) []EntryIssue {
	var issues []EntryIssue
	for i := range entries {
		issues = append(issues, entries[i].Check(i)...)
	}
	return issues
}
