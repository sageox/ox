package session

import (
	"strings"
)

// CommandRedactionRule maps a shell-command prefix to a redaction slug.
// When a tool_result event captures the stdout of a command whose
// ToolInput starts with the Prefix, the entire ToolOutput is replaced
// with the slug — regex detectors are not enough because credential-
// emitting commands can produce multi-line, multi-shape output (env
// blocks, ASCII tables, JSON) that no single regex catches reliably.
//
// Defense-in-depth: per-regex detectors in DefaultPatterns still fire
// as a backstop; this whole-output policy is the primary boundary for
// these specific commands.
type CommandRedactionRule struct {
	// Prefix matches the START of ToolInput after leading whitespace.
	// Match is case-sensitive; commands are typically lowercase on the
	// command line. Comparison is on the joined argv string, so
	// "aws sso login" matches both "aws sso login" and
	// "aws sso login --profile foo".
	Prefix string

	// Slug is the placeholder substituted for the entire ToolOutput.
	// Convention: "[REDACTED:credential-output:<short-name>]"
	Slug string
}

// DefaultCommandRedactions returns the built-in allowlist of credential-
// emitting commands whose output should be redacted wholesale. Adding a
// new entry is a one-line change to this slice; please include a comment
// describing why the command emits credentials.
func DefaultCommandRedactions() []CommandRedactionRule {
	return []CommandRedactionRule{
		// AWS SSO produces an STS session (ASIA + secret + session token) at end.
		{Prefix: "aws sso login", Slug: "[REDACTED:credential-output:aws-sso-login]"},
		// STS calls return Credentials block with AccessKeyId/SecretAccessKey/SessionToken.
		{Prefix: "aws sts assume-role", Slug: "[REDACTED:credential-output:aws-sts-assume-role]"},
		{Prefix: "aws sts get-session-token", Slug: "[REDACTED:credential-output:aws-sts-get-session-token]"},
		// configure export-credentials prints "export AWS_ACCESS_KEY_ID=..." blocks.
		{Prefix: "aws configure export-credentials", Slug: "[REDACTED:credential-output:aws-configure-export-credentials]"},
		// GitHub CLI auth: login emits OTP/code, token emits raw PAT.
		{Prefix: "gh auth login", Slug: "[REDACTED:credential-output:gh-auth-login]"},
		{Prefix: "gh auth token", Slug: "[REDACTED:credential-output:gh-auth-token]"},
		// GitLab CLI auth: login emits PAT/OTP, token prints PAT.
		{Prefix: "glab auth login", Slug: "[REDACTED:credential-output:glab-auth-login]"},
		{Prefix: "glab auth token", Slug: "[REDACTED:credential-output:glab-auth-token]"},
		// Vault: reads from auth paths typically return tokens.
		{Prefix: "vault read auth/", Slug: "[REDACTED:credential-output:vault-read-auth]"},
		// 1Password CLI: item get can return raw passwords/tokens.
		{Prefix: "op item get", Slug: "[REDACTED:credential-output:op-item-get]"},
	}
}

// CommandRedactor applies whole-output redaction to tool entries whose
// ToolInput matches a known credential-emitting command. It is separate
// from Redactor (regex-based) but composes with it: callers typically
// run CommandRedactor first (cheap prefix match, broad coverage), then
// Redactor on whatever remains.
type CommandRedactor struct {
	rules []CommandRedactionRule
}

// NewCommandRedactor returns a redactor with the default ruleset.
func NewCommandRedactor() *CommandRedactor {
	return &CommandRedactor{rules: DefaultCommandRedactions()}
}

// NewCommandRedactorWithRules returns a redactor with a custom ruleset.
func NewCommandRedactorWithRules(rules []CommandRedactionRule) *CommandRedactor {
	return &CommandRedactor{rules: rules}
}

// matchRule returns the slug for a command line, or "" if no rule matches.
// The match is on the START of the trimmed ToolInput so any flags or
// extra args after the command don't defeat the prefix.
func (c *CommandRedactor) matchRule(toolInput string) string {
	trimmed := strings.TrimLeft(toolInput, " \t\n")
	for _, r := range c.rules {
		if r.Prefix == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, r.Prefix) {
			continue
		}
		// Two boundary modes:
		//   - Path-style prefix ending in "/": match any continuation
		//     (e.g. "vault read auth/" matches "vault read auth/token/...").
		//   - Word-style prefix: require whitespace boundary so e.g.
		//     "aws sso login" doesn't match "aws sso login-other-cmd".
		if strings.HasSuffix(r.Prefix, "/") {
			return r.Slug
		}
		rest := trimmed[len(r.Prefix):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' {
			return r.Slug
		}
	}
	return ""
}

// RedactEntry replaces ToolOutput with the slug if ToolInput matches a
// rule. Also clears Content for tool entries — some agents stash a copy
// of the output there. Returns true if any redaction was applied.
func (c *CommandRedactor) RedactEntry(entry *SessionEntry) bool {
	if entry == nil || entry.ToolInput == "" {
		return false
	}
	slug := c.matchRule(entry.ToolInput)
	if slug == "" {
		return false
	}
	redacted := false
	if entry.ToolOutput != "" {
		entry.ToolOutput = slug
		redacted = true
	}
	// Some adapters mirror the tool output in Content too — scrub it. We
	// don't replace Content for non-tool entries; the prefix gate above
	// already required ToolInput to be set, but the explicit type check
	// guards against future Entry shapes that decouple ToolInput from a
	// real tool call.
	if entry.Type == EntryTypeTool && entry.Content != "" && entry.Content != slug {
		entry.Content = slug
		redacted = true
	}
	return redacted
}

// RedactEntries applies whole-output redaction to a slice of entries.
// Returns the number of entries that had at least one field redacted.
func (c *CommandRedactor) RedactEntries(entries []SessionEntry) int {
	count := 0
	for i := range entries {
		if c.RedactEntry(&entries[i]) {
			count++
		}
	}
	return count
}
