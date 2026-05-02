package adapterprotocol

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawEntry_Validate(t *testing.T) {
	tests := []struct {
		name    string
		entry   RawEntry
		wantErr bool
	}{
		{
			name:    "valid user entry",
			entry:   RawEntry{Role: RoleUser, Content: "hello", Timestamp: "2026-01-15T10:00:00Z"},
			wantErr: false,
		},
		{
			name:    "valid assistant entry",
			entry:   RawEntry{Role: RoleAssistant, Content: "hi there"},
			wantErr: false,
		},
		{
			name:    "valid tool entry with name",
			entry:   RawEntry{Role: RoleTool, ToolName: "bash", ToolInput: "ls"},
			wantErr: false,
		},
		{
			name:    "valid tool entry with output only",
			entry:   RawEntry{Role: RoleTool, ToolOutput: "error", IsError: true},
			wantErr: false,
		},
		{
			name:    "valid system entry",
			entry:   RawEntry{Role: RoleSystem, Content: "context loaded"},
			wantErr: false,
		},
		{
			name:    "invalid role",
			entry:   RawEntry{Role: "human", Content: "hello"},
			wantErr: true,
		},
		{
			name:    "empty role",
			entry:   RawEntry{Content: "hello"},
			wantErr: true,
		},
		{
			name:    "invalid timestamp",
			entry:   RawEntry{Role: RoleUser, Content: "hello", Timestamp: "not-a-date"},
			wantErr: true,
		},
		{
			name:    "RFC3339Nano timestamp accepted",
			entry:   RawEntry{Role: RoleUser, Content: "hello", Timestamp: "2026-01-15T10:00:00.123456789Z"},
			wantErr: false,
		},
		{
			name:    "no timestamp is fine",
			entry:   RawEntry{Role: RoleUser, Content: "hello"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entry.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRawEntry_Validate_WarningsAreNotErrors(t *testing.T) {
	// empty content on user entry is a warning, not an error
	entry := RawEntry{Role: RoleUser, Content: ""}
	assert.NoError(t, entry.Validate(), "empty content is a warning, not a validation error")

	// tool entry missing both name and output is a warning
	entry = RawEntry{Role: RoleTool}
	assert.NoError(t, entry.Validate(), "tool missing name+output is a warning, not a validation error")
}

func TestValidateEntries(t *testing.T) {
	entries := []RawEntry{
		{Role: RoleUser, Content: "hello"},
		{Role: "invalid", Content: "bad"},
		{Role: RoleAssistant, Content: ""},                             // warning
		{Role: RoleTool},                                               // warning
		{Role: RoleUser, Content: "has content", Timestamp: "garbage"}, // error
	}

	issues := ValidateEntries(entries)

	var errors, warnings int
	for _, issue := range issues {
		if issue.IsError {
			errors++
		} else {
			warnings++
		}
	}
	assert.Equal(t, 2, errors, "should have 2 errors (invalid role, bad timestamp)")
	assert.Equal(t, 2, warnings, "should have 2 warnings (empty content, tool missing name)")

	// verify index is set correctly
	for _, issue := range issues {
		assert.GreaterOrEqual(t, issue.Index, 0)
	}
}

func TestValidateEntries_AllValid(t *testing.T) {
	entries := []RawEntry{
		{Role: RoleUser, Content: "hello", Timestamp: "2026-01-15T10:00:00Z"},
		{Role: RoleAssistant, Content: "hi"},
		{Role: RoleTool, ToolName: "bash", ToolInput: "ls"},
	}
	issues := ValidateEntries(entries)
	assert.Empty(t, issues)
}

func TestEntryIssue_String(t *testing.T) {
	issue := EntryIssue{Index: 3, Field: "role", Message: "invalid", IsError: true}
	assert.Contains(t, issue.String(), "entry 3")
	assert.Contains(t, issue.String(), "error")

	issue = EntryIssue{Index: -1, Field: "role", Message: "bad", IsError: false}
	assert.NotContains(t, issue.String(), "entry")
	assert.Contains(t, issue.String(), "warning")
}
