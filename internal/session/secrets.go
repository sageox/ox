package session

import (
	"regexp"
	"strings"
	"sync"
)

// SecretPattern defines a pattern for detecting secrets
type SecretPattern struct {
	Name    string           // identifier for the pattern
	Pattern *regexp.Regexp   // compiled regex
	Redact  string           // replacement text, e.g., "[REDACTED_AWS_KEY]"
	Source  RedactRuleSource // origin: builtin, team, repo, or user (zero = builtin)
	// SkipIf lists substrings that, if present in a match, suppress redaction.
	// Used to whitelist already-masked or known-safe shapes (e.g. ":x-oauth-basic@"
	// for GitHub-style URLs, ":****@" for already-masked URLs) without writing
	// negative lookaheads (RE2 doesn't support them).
	SkipIf []string
	// Keywords lists lowercase substrings that anchor the pattern. If
	// non-empty, the regex is only evaluated when at least one keyword
	// appears (case-insensitive) in the input. Empty means always run.
	//
	// The win is enormous: gitleaks-derived patterns each carry a
	// distinctive anchor (e.g. "akia", "adafruit", "ops_"), so a quick
	// strings.Contains on a once-lowercased input shaves orders of
	// magnitude off the cost when no credentials are present. The
	// chokepoint runs ~200 patterns per WriteEntry; without the screen,
	// a 1 MB tool output takes tens of seconds. With the screen, the
	// no-match path is dominated by the lowercase pass.
	Keywords []string
}

// MatchesKeyword reports whether lowerInput contains at least one of p's
// keywords. lowerInput MUST already be lowercased — the caller is
// expected to lowercase the field once and reuse it across all
// patterns. Returns true if Keywords is empty (pattern is unconditional).
func (p *SecretPattern) MatchesKeyword(lowerInput string) bool {
	if len(p.Keywords) == 0 {
		return true
	}
	for _, kw := range p.Keywords {
		if strings.Contains(lowerInput, kw) {
			return true
		}
	}
	return false
}

// DefaultPatterns returns built-in secret patterns covering common credential types.
// Patterns are ordered roughly by specificity (more specific patterns first).
func DefaultPatterns() []SecretPattern {
	return []SecretPattern{
		// AWS Access Keys (AKIA... format, exactly 20 chars) — long-lived IAM access keys.
		{
			Name:    "aws_access_key",
			Pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
			Redact:  "[REDACTED_AWS_KEY]",
		},

		// AWS STS session access key id (ASIA... format, exactly 20 chars) — short-lived
		// temporary credentials minted by `aws sso login`, `aws sts assume-role`, etc.
		// Separate slug from aws_access_key so triage can tell IAM-static from STS-temporary
		// at a glance — the response posture differs (rotate vs. ride out TTL).
		// Forensic scan 2026-05-10 found 194 occurrences in committed raw.jsonl files that
		// slipped past the AKIA-only regex; this closes that gap.
		{
			Name:    "aws_sts_session_key",
			Pattern: regexp.MustCompile(`ASIA[0-9A-Z]{16}`),
			Redact:  "[REDACTED_AWS_STS_KEY]",
		},

		// AWS Secret Keys (40 char base64, usually after key= or similar)
		{
			Name:    "aws_secret_key",
			Pattern: regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret_key|secret_access_key)\s*[=:]\s*['"]?([A-Za-z0-9/+=]{40})['"]?`),
			Redact:  "[REDACTED_AWS_SECRET]",
		},

		// GitHub tokens (ghp_, gho_, ghs_, ghr_, ghu_ prefixes)
		{
			Name:    "github_token",
			Pattern: regexp.MustCompile(`gh[psortu]_[A-Za-z0-9_]{36,255}`),
			Redact:  "[REDACTED_GITHUB_TOKEN]",
		},

		// GitHub fine-grained PAT (github_pat_ prefix)
		{
			Name:    "github_fine_grained_pat",
			Pattern: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,255}`),
			Redact:  "[REDACTED_GITHUB_PAT]",
		},

		// GitLab personal access tokens (glpat- prefix)
		{
			Name:    "gitlab_token",
			Pattern: regexp.MustCompile(`glpat-[A-Za-z0-9\-_]{20,}`),
			Redact:  "[REDACTED_GITLAB_TOKEN]",
		},

		// GitLab OAuth access tokens (gloas- prefix)
		{
			Name:    "gitlab_oauth_token",
			Pattern: regexp.MustCompile(`gloas-[A-Za-z0-9\-_]{15,}`),
			Redact:  "[REDACTED_GITLAB_OAUTH]",
		},

		// GitLab deploy tokens (gldt- prefix)
		{
			Name:    "gitlab_deploy_token",
			Pattern: regexp.MustCompile(`gldt-[A-Za-z0-9\-_]{15,}`),
			Redact:  "[REDACTED_GITLAB_DEPLOY_TOKEN]",
		},

		// GitLab runner tokens (glrt- prefix)
		{
			Name:    "gitlab_runner_token",
			Pattern: regexp.MustCompile(`glrt-[A-Za-z0-9\-_]{15,}`),
			Redact:  "[REDACTED_GITLAB_RUNNER_TOKEN]",
		},

		// GitLab feed tokens (glft- prefix)
		{
			Name:    "gitlab_feed_token",
			Pattern: regexp.MustCompile(`glft-[A-Za-z0-9\-_]{15,}`),
			Redact:  "[REDACTED_GITLAB_FEED_TOKEN]",
		},

		// SageOx share-session cookies
		{
			Name:    "sox_share_session",
			Pattern: regexp.MustCompile(`sox_share_session=[A-Za-z0-9_\-]{15,}`),
			Redact:  "sox_share_session=[REDACTED_SOX_SHARE_SESSION]",
		},

		// SageOx API keys (mk_ prefix)
		{
			Name:    "sageox_api_key",
			Pattern: regexp.MustCompile(`\bmk_[A-Za-z0-9]{20,}`),
			Redact:  "[REDACTED_SAGEOX_API_KEY]",
		},

		// AgentX keys (axk_ prefix, exactly 32 chars after prefix)
		{
			Name:    "agentx_key",
			Pattern: regexp.MustCompile(`\baxk_[A-Za-z0-9_]{32}\b`),
			Redact:  "[REDACTED_AGENTX_KEY]",
		},

		// Slack tokens (xoxb-, xoxp-, xoxa-, xoxs-, xoxr-)
		{
			Name:    "slack_token",
			Pattern: regexp.MustCompile(`xox[abpsr]-[A-Za-z0-9\-]{10,}`),
			Redact:  "[REDACTED_SLACK_TOKEN]",
		},

		// Stripe API keys (sk_live_, sk_test_, pk_live_, pk_test_)
		{
			Name:    "stripe_key",
			Pattern: regexp.MustCompile(`[sr]k_(live|test)_[A-Za-z0-9]{24,}`),
			Redact:  "[REDACTED_STRIPE_KEY]",
		},

		// Twilio API keys and auth tokens
		{
			Name:    "twilio_key",
			Pattern: regexp.MustCompile(`SK[a-f0-9]{32}`),
			Redact:  "[REDACTED_TWILIO_KEY]",
		},

		// SendGrid API keys
		{
			Name:    "sendgrid_key",
			Pattern: regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{22}\.[A-Za-z0-9_\-]{43}`),
			Redact:  "[REDACTED_SENDGRID_KEY]",
		},

		// Mailchimp API keys
		{
			Name:    "mailchimp_key",
			Pattern: regexp.MustCompile(`[a-f0-9]{32}-us[0-9]{1,2}`),
			Redact:  "[REDACTED_MAILCHIMP_KEY]",
		},

		// NPM tokens
		{
			Name:    "npm_token",
			Pattern: regexp.MustCompile(`npm_[A-Za-z0-9]{36}`),
			Redact:  "[REDACTED_NPM_TOKEN]",
		},

		// PyPI tokens
		{
			Name:    "pypi_token",
			Pattern: regexp.MustCompile(`pypi-[A-Za-z0-9_\-]{50,}`),
			Redact:  "[REDACTED_PYPI_TOKEN]",
		},

		// Heroku API keys (UUIDs - careful with false positives)
		{
			Name:    "heroku_key",
			Pattern: regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`),
			Redact:  "[REDACTED_UUID]",
		},

		// Private keys (RSA, DSA, EC, OPENSSH)
		{
			Name:    "private_key_header",
			Pattern: regexp.MustCompile(`-----BEGIN\s+(RSA|DSA|EC|OPENSSH|PGP)?\s*PRIVATE KEY-----`),
			Redact:  "[REDACTED_PRIVATE_KEY]",
		},

		// Generic private key (fallback)
		{
			Name:    "private_key_generic",
			Pattern: regexp.MustCompile(`-----BEGIN PRIVATE KEY-----`),
			Redact:  "[REDACTED_PRIVATE_KEY]",
		},

		// Base64-encoded secrets in environment variables
		{
			Name:    "export_aws_secret",
			Pattern: regexp.MustCompile(`(?i)export\s+(AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN)\s*=\s*['"]?[A-Za-z0-9/+=]{20,}['"]?`),
			Redact:  "[REDACTED_EXPORT]",
		},

		// Generic export of sensitive env vars
		{
			Name:    "export_secret",
			Pattern: regexp.MustCompile(`(?i)export\s+(GITHUB_TOKEN|GITLAB_TOKEN|API_KEY|SECRET_KEY|AUTH_TOKEN|ACCESS_TOKEN|PRIVATE_KEY|PASSWORD|PASSWD|DB_PASSWORD|DATABASE_PASSWORD|MYSQL_PASSWORD|POSTGRES_PASSWORD|REDIS_PASSWORD|MONGO_PASSWORD)\s*=\s*['"]?[^'"\s]+['"]?`),
			Redact:  "[REDACTED_EXPORT]",
		},

		// Connection strings with embedded credentials (DB protocols)
		{
			Name:    "connection_string",
			Pattern: regexp.MustCompile(`(?i)(mongodb|postgres|postgresql|mysql|redis|amqp|mssql):\/\/[^:]+:[^@]+@[^\s'"]+`),
			Redact:  "[REDACTED_CONNECTION_STRING]",
		},

		// HTTP(S) URLs with embedded userinfo (PAT in clone URL, etc.)
		// SkipIf whitelists already-masked or GitHub-style sentinel passwords.
		{
			Name:    "url_with_password",
			Pattern: regexp.MustCompile(`https?://[^\s/:@]+:[^\s/:@]{6,}@[^\s'"]+`),
			Redact:  "[REDACTED_URL_WITH_CREDENTIALS]",
			SkipIf: []string{
				":x-oauth-basic@",
				":x-access-token@",
				":****@",
				":[REDACTED",
			},
		},

		// Bearer tokens in HTTP headers â clean shape: "Authorization: Bearer <token>"
		{
			Name:    "authorization_bearer",
			Pattern: regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+[A-Za-z0-9._=/+\-]{20,}`),
			Redact:  "Authorization: Bearer [REDACTED_BEARER_TOKEN]",
		},

		// Basic auth headers â clean shape: "Authorization: Basic <b64>"
		{
			Name:    "authorization_basic",
			Pattern: regexp.MustCompile(`(?i)Authorization:\s*Basic\s+[A-Za-z0-9+/=]{20,}`),
			Redact:  "Authorization: Basic [REDACTED_BASIC_AUTH]",
		},

		// Legacy bearer pattern retained for backward compatibility (covers
		// looser shapes like "bearer=<token>" without the Authorization prefix).
		{
			Name:    "bearer_token",
			Pattern: regexp.MustCompile(`(?i)(authorization|bearer)\s*[:=]\s*['"]?bearer\s+[A-Za-z0-9_\-\.]{20,}['"]?`),
			Redact:  "[REDACTED_BEARER_TOKEN]",
		},

		// Legacy basic auth pattern retained for backward compatibility.
		{
			Name:    "basic_auth",
			Pattern: regexp.MustCompile(`(?i)authorization\s*[:=]\s*['"]?basic\s+[A-Za-z0-9+/=]{10,}['"]?`),
			Redact:  "[REDACTED_BASIC_AUTH]",
		},

		// Generic API key patterns (must be after more specific patterns)
		{
			Name:    "generic_api_key",
			Pattern: regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[=:]\s*['"]?([A-Za-z0-9_\-]{20,})['"]?`),
			Redact:  "[REDACTED_API_KEY]",
		},

		// Generic token patterns
		{
			Name:    "generic_token",
			Pattern: regexp.MustCompile(`(?i)(access[_-]?token|auth[_-]?token|secret[_-]?token)\s*[=:]\s*['"]?([A-Za-z0-9_\-]{20,})['"]?`),
			Redact:  "[REDACTED_TOKEN]",
		},

		// Generic password patterns (careful: may have false positives)
		{
			Name:    "generic_password",
			Pattern: regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*['"]([^'"]{8,})['"]`),
			Redact:  "[REDACTED_PASSWORD]",
		},

		// Generic secret patterns
		{
			Name:    "generic_secret",
			Pattern: regexp.MustCompile(`(?i)(secret|secret_key|client_secret)\s*[=:]\s*['"]?([A-Za-z0-9_\-/+=]{16,})['"]?`),
			Redact:  "[REDACTED_SECRET]",
		},

		// JWT tokens (header.payload.signature format)
		{
			Name:    "jwt_token",
			Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*`),
			Redact:  "[REDACTED_JWT]",
		},

		// .env-style sensitive assignments â fallback for cases the more
		// specific patterns above didn't catch. Position deliberately AFTER
		// generic_api_key / generic_token / generic_password so the more
		// specific [REDACTED_*] slugs win when they apply.
		{
			Name:    "env_assignment",
			Pattern: regexp.MustCompile(`(?i)\b(GITHUB_TOKEN|GITLAB_TOKEN|DATABASE_PASSWORD|DB_PASSWORD|MYSQL_PASSWORD|POSTGRES_PASSWORD|REDIS_PASSWORD|MONGO_PASSWORD)\s*=\s*['"]?[A-Za-z0-9_\-\.=/+]{12,}['"]?`),
			Redact:  "[REDACTED_ENV_ASSIGNMENT]",
		},
	}
}

// Redactor handles secret detection and redaction
type Redactor struct {
	patterns []SecretPattern
	mu       sync.RWMutex
}

// NewRedactor creates a new Redactor with default patterns
func NewRedactor() *Redactor {
	return &Redactor{
		patterns: DefaultPatterns(),
	}
}

// NewRedactorWithPatterns creates a Redactor with custom patterns
func NewRedactorWithPatterns(patterns []SecretPattern) *Redactor {
	return &Redactor{
		patterns: patterns,
	}
}

// AddPattern adds an additional pattern to the redactor
func (r *Redactor) AddPattern(pattern SecretPattern) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns = append(r.patterns, pattern)
}

// RedactionResult contains details about a redaction
type RedactionResult struct {
	PatternName string // which pattern matched
	Original    string // the original matched text (for logging, be careful with this)
	Position    int    // position in the string where match was found
}

// matchSkipped returns true if a match should be skipped because it contains
// any of the pattern's SkipIf substrings.
func matchSkipped(match string, skipIf []string) bool {
	for _, skip := range skipIf {
		if skip != "" && strings.Contains(match, skip) {
			return true
		}
	}
	return false
}

// RedactString scans and redacts secrets from a string.
// Returns the redacted output and a list of pattern names that matched.
func (r *Redactor) RedactString(input string) (output string, found []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	output = input
	foundMap := make(map[string]bool)

	for _, p := range r.patterns {
		if p.Pattern == nil {
			continue
		}

		matched := false
		output = p.Pattern.ReplaceAllStringFunc(output, func(match string) string {
			if matchSkipped(match, p.SkipIf) {
				return match
			}
			matched = true
			return p.Redact
		})
		if matched {
			foundMap[p.Name] = true
		}
	}

	// convert map to slice
	for name := range foundMap {
		found = append(found, name)
	}

	return output, found
}

// RedactStringWithDetails provides detailed redaction results
func (r *Redactor) RedactStringWithDetails(input string) (output string, results []RedactionResult) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	output = input

	for _, p := range r.patterns {
		if p.Pattern == nil {
			continue
		}

		// find all matches with positions; record only those not skipped
		matches := p.Pattern.FindAllStringIndex(output, -1)
		for _, match := range matches {
			matched := output[match[0]:match[1]]
			if matchSkipped(matched, p.SkipIf) {
				continue
			}
			results = append(results, RedactionResult{
				PatternName: p.Name,
				Original:    matched,
				Position:    match[0],
			})
		}

		output = p.Pattern.ReplaceAllStringFunc(output, func(match string) string {
			if matchSkipped(match, p.SkipIf) {
				return match
			}
			return p.Redact
		})
	}

	return output, results
}

// RedactEntry redacts secrets from an Entry's content.
// Returns true if any secrets were found and redacted.
func (r *Redactor) RedactEntry(entry *Entry) (redacted bool) {
	if entry == nil {
		return false
	}

	output, found := r.RedactString(entry.Content)
	if len(found) > 0 {
		entry.Content = output
		return true
	}

	return false
}

// RedactEntries redacts all entries in a slice.
// Returns the count of entries that contained secrets (in any field).
func (r *Redactor) RedactEntries(entries []Entry) (count int) {
	for i := range entries {
		hadSecrets := false

		// redact Content
		output, found := r.RedactString(entries[i].Content)
		if len(found) > 0 {
			entries[i].Content = output
			hadSecrets = true
		}

		// redact ToolInput if present
		if entries[i].ToolInput != "" {
			inputOut, inputFound := r.RedactString(entries[i].ToolInput)
			if len(inputFound) > 0 {
				entries[i].ToolInput = inputOut
				hadSecrets = true
			}
		}

		// redact ToolOutput if present
		if entries[i].ToolOutput != "" {
			outputOut, outputFound := r.RedactString(entries[i].ToolOutput)
			if len(outputFound) > 0 {
				entries[i].ToolOutput = outputOut
				hadSecrets = true
			}
		}

		if hadSecrets {
			count++
		}
	}

	return count
}

// RedactHistoryEntries redacts secrets from HistoryEntry slices.
// Returns the count of entries that contained secrets (in any field).
func (r *Redactor) RedactHistoryEntries(entries []HistoryEntry) (count int) {
	for i := range entries {
		hadSecrets := false

		// redact Content
		output, found := r.RedactString(entries[i].Content)
		if len(found) > 0 {
			entries[i].Content = output
			hadSecrets = true
		}

		// redact ToolInput if present
		if entries[i].ToolInput != "" {
			inputOut, inputFound := r.RedactString(entries[i].ToolInput)
			if len(inputFound) > 0 {
				entries[i].ToolInput = inputOut
				hadSecrets = true
			}
		}

		// redact ToolOutput if present
		if entries[i].ToolOutput != "" {
			outputOut, outputFound := r.RedactString(entries[i].ToolOutput)
			if len(outputFound) > 0 {
				entries[i].ToolOutput = outputOut
				hadSecrets = true
			}
		}

		if hadSecrets {
			count++
		}
	}

	return count
}

// RedactCapturedHistory applies secret redaction to all entries in captured history.
// Returns the count of entries that contained secrets.
func (r *Redactor) RedactCapturedHistory(history *CapturedHistory) (count int) {
	if history == nil {
		return 0
	}
	return r.RedactHistoryEntries(history.Entries)
}

// ContainsSecrets checks if a string contains any secrets without modifying it
func (r *Redactor) ContainsSecrets(input string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.patterns {
		if p.Pattern == nil {
			continue
		}
		if len(p.SkipIf) == 0 {
			if p.Pattern.MatchString(input) {
				return true
			}
			continue
		}
		// honor SkipIf: a match that contains a skip substring doesn't count
		for _, m := range p.Pattern.FindAllString(input, -1) {
			if !matchSkipped(m, p.SkipIf) {
				return true
			}
		}
	}

	return false
}

// ScanForSecrets returns pattern names of all secrets found without redacting
func (r *Redactor) ScanForSecrets(input string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var found []string
	for _, p := range r.patterns {
		if p.Pattern == nil {
			continue
		}
		if len(p.SkipIf) == 0 {
			if p.Pattern.MatchString(input) {
				found = append(found, p.Name)
			}
			continue
		}
		for _, m := range p.Pattern.FindAllString(input, -1) {
			if !matchSkipped(m, p.SkipIf) {
				found = append(found, p.Name)
				break
			}
		}
	}

	return found
}

// RedactWithAllowlist redacts secrets but preserves strings in the allowlist.
// Useful for known-safe patterns that might trigger false positives.
func (r *Redactor) RedactWithAllowlist(input string, allowlist []string) (output string, found []string) {
	// create placeholder map for allowlist items
	placeholders := make(map[string]string)
	output = input

	// replace allowlist items with unique placeholders
	for i, allowed := range allowlist {
		placeholder := generatePlaceholder(i)
		placeholders[placeholder] = allowed
		output = strings.ReplaceAll(output, allowed, placeholder)
	}

	// perform normal redaction
	output, found = r.RedactString(output)

	// restore allowlist items
	for placeholder, original := range placeholders {
		output = strings.ReplaceAll(output, placeholder, original)
	}

	return output, found
}

// generatePlaceholder creates a unique placeholder that won't be matched by secret patterns
func generatePlaceholder(index int) string {
	return "\x00ALLOWLIST_" + string(rune('A'+index)) + "\x00"
}

// RedactMap redacts secrets from string values in a map.
// Useful for redacting raw JSON entries before storage.
// Returns true if any secrets were found and redacted.
func (r *Redactor) RedactMap(data map[string]any) bool {
	redacted := false
	for key, value := range data {
		switch v := value.(type) {
		case string:
			output, found := r.RedactString(v)
			if len(found) > 0 {
				data[key] = output
				redacted = true
			}
		case map[string]any:
			if r.RedactMap(v) {
				redacted = true
			}
		case []any:
			if r.RedactSlice(v) {
				redacted = true
			}
		}
	}
	return redacted
}

// RedactSlice redacts secrets from string values in a slice.
// Returns true if any secrets were found and redacted.
func (r *Redactor) RedactSlice(data []any) bool {
	redacted := false
	for i, value := range data {
		switch v := value.(type) {
		case string:
			output, found := r.RedactString(v)
			if len(found) > 0 {
				data[i] = output
				redacted = true
			}
		case map[string]any:
			if r.RedactMap(v) {
				redacted = true
			}
		case []any:
			if r.RedactSlice(v) {
				redacted = true
			}
		}
	}
	return redacted
}

// RedactHistorySecrets applies secret redaction to all entries in captured history
// using the default redactor. This is a convenience function for one-off usage.
// Returns the count of entries that contained secrets.
func RedactHistorySecrets(history *CapturedHistory) int {
	if history == nil {
		return 0
	}
	redactor := NewRedactor()
	return redactor.RedactCapturedHistory(history)
}
