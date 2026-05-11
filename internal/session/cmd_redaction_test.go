package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCommandRedactor_AWSSsoLogin verifies that "aws sso login" output is
// replaced wholesale, even when the output is a multi-line credential block
// that no single regex would catch.
// Failure prevented: ASIA STS token + secret + session token captured into
// raw.jsonl from aws sso login output, then pushed to the Ledger.
func TestCommandRedactor_AWSSsoLogin(t *testing.T) {
	tests := []struct {
		name      string
		toolInput string
		shouldHit bool
	}{
		{"bare login", "aws sso login", true},
		{"with profile", "aws sso login --profile production", true},
		{"with extra whitespace", "  aws sso login --use-device-code", true},
		{"different aws command", "aws s3 ls", false},
		{"not aws", "echo aws sso login", false},
		{"sso-other (no boundary)", "aws sso login-other-cmd", false},
	}

	cr := NewCommandRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &SessionEntry{
				Type:       EntryTypeTool,
				ToolName:   "Bash",
				ToolInput:  tt.toolInput,
				ToolOutput: "Successfully logged in\nASIATESTTESTTESTTESTfakeSESSIONtoken123longertext",
			}
			redacted := cr.RedactEntry(entry)
			if tt.shouldHit {
				assert.True(t, redacted)
				assert.Equal(t, "[REDACTED:credential-output:aws-sso-login]", entry.ToolOutput)
				assert.NotContains(t, entry.ToolOutput, "ASIATEST")
			} else {
				assert.False(t, redacted)
				assert.Contains(t, entry.ToolOutput, "ASIATEST", "output incorrectly redacted")
			}
		})
	}
}

func TestCommandRedactor_AllDefaultRules(t *testing.T) {
	// Each default rule should match at least one canonical invocation.
	// This is a guard against typos in the prefix string and a documentation
	// test of how callers invoke each command.
	cases := []struct {
		input        string
		expectedSlug string
	}{
		{"aws sso login", "[REDACTED:credential-output:aws-sso-login]"},
		{"aws sts assume-role --role-arn arn:aws:iam::123", "[REDACTED:credential-output:aws-sts-assume-role]"},
		{"aws sts get-session-token --duration-seconds 3600", "[REDACTED:credential-output:aws-sts-get-session-token]"},
		{"aws configure export-credentials --profile prod", "[REDACTED:credential-output:aws-configure-export-credentials]"},
		{"gh auth login --hostname github.com", "[REDACTED:credential-output:gh-auth-login]"},
		{"gh auth token", "[REDACTED:credential-output:gh-auth-token]"},
		{"glab auth login", "[REDACTED:credential-output:glab-auth-login]"},
		{"glab auth token --hostname gitlab.example.com", "[REDACTED:credential-output:glab-auth-token]"},
		{"vault read auth/token/lookup-self", "[REDACTED:credential-output:vault-read-auth]"},
		{"op item get \"AWS Console\"", "[REDACTED:credential-output:op-item-get]"},
	}

	cr := NewCommandRedactor()
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			entry := &SessionEntry{
				Type:       EntryTypeTool,
				ToolInput:  c.input,
				ToolOutput: "sensitive credential content goes here",
			}
			assert.True(t, cr.RedactEntry(entry), "expected redaction for %q", c.input)
			assert.Equal(t, c.expectedSlug, entry.ToolOutput)
		})
	}
}

func TestCommandRedactor_NonMatchingCommands(t *testing.T) {
	// Common dev commands that must NOT be redacted (false-positive regression).
	cr := NewCommandRedactor()
	benign := []string{
		"ls -la",
		"git status",
		"go test ./...",
		"npm install",
		"docker ps",
		"kubectl get pods",
		"echo hello",
		"cat README.md",
		"grep -r foo",
		"aws s3 ls",                       // aws but not a credential command
		"aws ec2 describe-instances",      // aws but safe
		"gh pr list",                      // gh but not auth
		"glab issue list",                 // glab but not auth
		"op signin",                       // op but not item get
		"some-tool aws sso login as text", // not a prefix match
	}
	for _, cmd := range benign {
		t.Run(cmd, func(t *testing.T) {
			entry := &SessionEntry{
				Type:       EntryTypeTool,
				ToolInput:  cmd,
				ToolOutput: "some output here that should not be touched",
			}
			assert.False(t, cr.RedactEntry(entry), "%q triggered false positive", cmd)
			assert.Equal(t, "some output here that should not be touched", entry.ToolOutput)
		})
	}
}

func TestCommandRedactor_ScrubsContentToo(t *testing.T) {
	// Some adapters mirror tool output into Content. Both must be scrubbed.
	cr := NewCommandRedactor()
	entry := &SessionEntry{
		Type:       EntryTypeTool,
		ToolInput:  "gh auth token",
		ToolOutput: "ghp_realtoken1234567890abcdefghij",
		Content:    "ghp_realtoken1234567890abcdefghij",
	}
	assert.True(t, cr.RedactEntry(entry))
	assert.NotContains(t, entry.ToolOutput, "ghp_realtoken")
	assert.NotContains(t, entry.Content, "ghp_realtoken")
	assert.Contains(t, entry.ToolOutput, "[REDACTED:credential-output:gh-auth-token]")
}

func TestCommandRedactor_NilEntry(t *testing.T) {
	cr := NewCommandRedactor()
	assert.False(t, cr.RedactEntry(nil))
}

func TestCommandRedactor_EmptyToolInput(t *testing.T) {
	cr := NewCommandRedactor()
	entry := &SessionEntry{Type: EntryTypeTool, ToolOutput: "secret"}
	assert.False(t, cr.RedactEntry(entry))
}

func TestCommandRedactor_CustomRules(t *testing.T) {
	cr := NewCommandRedactorWithRules([]CommandRedactionRule{
		{Prefix: "my-secret-cmd", Slug: "[CUSTOM]"},
	})
	entry := &SessionEntry{
		Type:       EntryTypeTool,
		ToolInput:  "my-secret-cmd --flag",
		ToolOutput: "credential data here",
	}
	assert.True(t, cr.RedactEntry(entry))
	assert.Equal(t, "[CUSTOM]", entry.ToolOutput)
}

// TestCommandRedactor_BatchRedactEntries verifies the slice variant returns
// an accurate count.
func TestCommandRedactor_BatchRedactEntries(t *testing.T) {
	cr := NewCommandRedactor()
	entries := []SessionEntry{
		{Type: EntryTypeUser, Content: "user message"},
		{Type: EntryTypeTool, ToolInput: "aws sso login", ToolOutput: "sensitive"},
		{Type: EntryTypeTool, ToolInput: "ls -la", ToolOutput: "file1 file2"},
		{Type: EntryTypeTool, ToolInput: "gh auth token", ToolOutput: "ghp_xxx"},
	}
	count := cr.RedactEntries(entries)
	assert.Equal(t, 2, count, "expected 2 redactions, got %d", count)
	assert.Equal(t, "sensitive", "sensitive") // sanity
	assert.Equal(t, "[REDACTED:credential-output:aws-sso-login]", entries[1].ToolOutput)
	assert.Equal(t, "file1 file2", entries[2].ToolOutput)
	assert.Equal(t, "[REDACTED:credential-output:gh-auth-token]", entries[3].ToolOutput)
}
