package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAttribution(t *testing.T) {
	attr := DefaultAttribution()

	require.NotNil(t, attr.Plan, "default plan attribution should not be nil")
	assert.NotEmpty(t, *attr.Plan, "default plan attribution should not be empty")
	require.NotNil(t, attr.Commit, "default commit attribution should not be nil")
	assert.NotEmpty(t, *attr.Commit, "default commit attribution should not be empty")
	require.NotNil(t, attr.PR, "default PR attribution should not be nil")
	assert.NotEmpty(t, *attr.PR, "default PR attribution should not be empty")

	// verify it contains expected content (Co-Authored-By format for GitHub recognition)
	expectedPlan := "This plan was informed by SageOx context"
	expectedCommit := "Co-Authored-By: SageOx <ox@sageox.ai>"
	expectedPR := "Co-Authored-By: [SageOx](https://github.com/SageOx)"

	assert.Equal(t, expectedPlan, *attr.Plan, "unexpected default plan attribution")
	assert.Equal(t, expectedCommit, *attr.Commit, "unexpected default commit attribution")
	assert.Equal(t, expectedPR, *attr.PR, "unexpected default PR attribution")
}

func TestIsCommitSet(t *testing.T) {
	tests := []struct {
		name     string
		attr     *Attribution
		expected bool
	}{
		{
			name:     "nil attribution",
			attr:     nil,
			expected: false,
		},
		{
			name:     "empty attribution struct",
			attr:     &Attribution{},
			expected: false,
		},
		{
			name:     "commit set to empty string",
			attr:     &Attribution{Commit: StringPtr("")},
			expected: true,
		},
		{
			name:     "commit set to value",
			attr:     &Attribution{Commit: StringPtr("custom")},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.attr.IsCommitSet()
			assert.Equal(t, tt.expected, result, "IsCommitSet()")
		})
	}
}

func TestIsPRSet(t *testing.T) {
	tests := []struct {
		name     string
		attr     *Attribution
		expected bool
	}{
		{
			name:     "nil attribution",
			attr:     nil,
			expected: false,
		},
		{
			name:     "empty attribution struct",
			attr:     &Attribution{},
			expected: false,
		},
		{
			name:     "pr set to empty string",
			attr:     &Attribution{PR: StringPtr("")},
			expected: true,
		},
		{
			name:     "pr set to value",
			attr:     &Attribution{PR: StringPtr("custom")},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.attr.IsPRSet()
			assert.Equal(t, tt.expected, result, "IsPRSet()")
		})
	}
}

func TestMergeAttribution(t *testing.T) {
	defaultPlan := "This plan was informed by SageOx context"
	defaultCommit := "Co-Authored-By: SageOx <ox@sageox.ai>"
	defaultPR := "Co-Authored-By: [SageOx](https://github.com/SageOx)"

	tests := []struct {
		name           string
		project        *Attribution
		user           *Attribution
		expectedPlan   string
		expectedCommit string
		expectedPR     string
	}{
		{
			name:           "both nil returns defaults",
			project:        nil,
			user:           nil,
			expectedPlan:   defaultPlan,
			expectedCommit: defaultCommit,
			expectedPR:     defaultPR,
		},
		{
			name:           "user config only - both fields",
			project:        nil,
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "user commit",
			expectedPR:     "user pr",
		},
		{
			name:           "project config only - both fields",
			project:        &Attribution{Commit: StringPtr("project commit"), PR: StringPtr("project pr")},
			user:           nil,
			expectedPlan:   defaultPlan,
			expectedCommit: "project commit",
			expectedPR:     "project pr",
		},
		{
			name:           "project overrides user - both fields",
			project:        &Attribution{Commit: StringPtr("project commit"), PR: StringPtr("project pr")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "project commit",
			expectedPR:     "project pr",
		},
		{
			name:           "project overrides partial - commit only",
			project:        &Attribution{Commit: StringPtr("project commit")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "project commit",
			expectedPR:     "user pr",
		},
		{
			name:           "project overrides partial - PR only",
			project:        &Attribution{PR: StringPtr("project pr")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "user commit",
			expectedPR:     "project pr",
		},
		{
			name:           "project disables commit with empty string",
			project:        &Attribution{Commit: StringPtr("")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "",
			expectedPR:     "user pr",
		},
		{
			name:           "project disables PR with empty string",
			project:        &Attribution{PR: StringPtr("")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "user commit",
			expectedPR:     "",
		},
		{
			name:           "project disables both",
			project:        &Attribution{Commit: StringPtr(""), PR: StringPtr("")},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "",
			expectedPR:     "",
		},
		{
			name:           "user disables, project unset - uses user's disabled",
			project:        nil,
			user:           &Attribution{Commit: StringPtr(""), PR: StringPtr("")},
			expectedPlan:   defaultPlan,
			expectedCommit: "",
			expectedPR:     "",
		},
		{
			name:           "user disables, project re-enables",
			project:        &Attribution{Commit: StringPtr("project"), PR: StringPtr("project pr")},
			user:           &Attribution{Commit: StringPtr(""), PR: StringPtr("")},
			expectedPlan:   defaultPlan,
			expectedCommit: "project",
			expectedPR:     "project pr",
		},
		{
			name:           "empty project struct falls through to user",
			project:        &Attribution{},
			user:           &Attribution{Commit: StringPtr("user commit"), PR: StringPtr("user pr")},
			expectedPlan:   defaultPlan,
			expectedCommit: "user commit",
			expectedPR:     "user pr",
		},
		{
			name:           "empty both structs uses defaults",
			project:        &Attribution{},
			user:           &Attribution{},
			expectedPlan:   defaultPlan,
			expectedCommit: defaultCommit,
			expectedPR:     defaultPR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeAttribution(tt.project, tt.user)
			assert.Equal(t, tt.expectedPlan, result.Plan, "Plan")
			assert.Equal(t, tt.expectedCommit, result.Commit, "Commit")
			assert.Equal(t, tt.expectedPR, result.PR, "PR")
		})
	}
}

func TestGetCommit(t *testing.T) {
	tests := []struct {
		name     string
		attr     *Attribution
		expected string
	}{
		{
			name:     "nil attribution",
			attr:     nil,
			expected: "",
		},
		{
			name:     "nil commit field",
			attr:     &Attribution{},
			expected: "",
		},
		{
			name:     "empty string commit",
			attr:     &Attribution{Commit: StringPtr("")},
			expected: "",
		},
		{
			name:     "with value",
			attr:     &Attribution{Commit: StringPtr("test")},
			expected: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.attr.GetCommit()
			assert.Equal(t, tt.expected, result, "GetCommit()")
		})
	}
}

func TestGetPR(t *testing.T) {
	tests := []struct {
		name     string
		attr     *Attribution
		expected string
	}{
		{
			name:     "nil attribution",
			attr:     nil,
			expected: "",
		},
		{
			name:     "nil pr field",
			attr:     &Attribution{},
			expected: "",
		},
		{
			name:     "empty string pr",
			attr:     &Attribution{PR: StringPtr("")},
			expected: "",
		},
		{
			name:     "with value",
			attr:     &Attribution{PR: StringPtr("test")},
			expected: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.attr.GetPR()
			assert.Equal(t, tt.expected, result, "GetPR()")
		})
	}
}

func TestSessionAttribution(t *testing.T) {
	// default: session is nil (auto)
	attr := DefaultAttribution()
	assert.Nil(t, attr.Session, "default session should be nil (auto)")

	// merge: nil defaults to "auto"
	result := MergeAttribution(nil, nil)
	assert.Equal(t, "auto", result.Session, "merged session should default to auto")

	// user disables session
	disabled := ""
	result = MergeAttribution(nil, &Attribution{Session: &disabled})
	assert.Equal(t, "", result.Session, "user should be able to disable session")

	// project re-enables session
	auto := "auto"
	result = MergeAttribution(&Attribution{Session: &auto}, &Attribution{Session: &disabled})
	assert.Equal(t, "auto", result.Session, "project should override user")

	// helpers
	assert.False(t, (*Attribution)(nil).IsSessionSet())
	assert.False(t, (&Attribution{}).IsSessionSet())
	assert.True(t, (&Attribution{Session: &disabled}).IsSessionSet())
	assert.Equal(t, "", (*Attribution)(nil).GetSession())
	assert.Equal(t, "", (&Attribution{Session: &disabled}).GetSession())
	assert.Equal(t, "auto", (&Attribution{Session: &auto}).GetSession())
}

func TestStringPtr(t *testing.T) {
	s := "test"
	ptr := StringPtr(s)
	require.NotNil(t, ptr, "StringPtr returned nil")
	assert.Equal(t, s, *ptr, "StringPtr value")
}

// TestCommitAttributionFormat ensures the commit attribution uses the exact
// "Co-Authored-By: " format required by GitHub for contributor recognition.
// GitHub will NOT recognize other formats like "Guided-by:" or "Authored-by:".
func TestCommitAttributionFormat(t *testing.T) {
	attr := DefaultAttribution()

	// CRITICAL: GitHub requires exactly "Co-Authored-By: Name <email>" format
	// Any deviation (wrong prefix, missing colon, wrong spacing) breaks recognition
	commit := *attr.Commit

	assert.True(t, strings.HasPrefix(commit, "Co-Authored-By: "),
		"commit attribution must start with 'Co-Authored-By: ' for GitHub recognition, got: %q", commit)

	// Must have email in angle brackets
	assert.Contains(t, commit, "<", "commit attribution must include email in <brackets>")
	assert.Contains(t, commit, ">", "commit attribution must include email in <brackets>")

	// Verify the full expected format
	expected := "Co-Authored-By: SageOx <ox@sageox.ai>"
	assert.Equal(t, expected, commit, "commit attribution")
}
