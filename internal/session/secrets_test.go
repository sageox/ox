package session

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPatterns(t *testing.T) {
	patterns := DefaultPatterns()
	require.NotEmpty(t, patterns)

	// verify all patterns compile (they should since we use MustCompile style)
	for _, p := range patterns {
		assert.NotNil(t, p.Pattern, "pattern %q has nil regex", p.Name)
		assert.NotEmpty(t, p.Name, "pattern has empty name")
		assert.NotEmpty(t, p.Redact, "pattern has empty redact string")
	}
}

func TestNewRedactor(t *testing.T) {
	r := NewRedactor()
	require.NotNil(t, r)
	assert.NotEmpty(t, r.patterns, "expected default patterns to be loaded")
}

func TestNewRedactorWithPatterns(t *testing.T) {
	customPatterns := []SecretPattern{
		{Name: "custom", Pattern: regexp.MustCompile(`CUSTOM_[A-Z]+`), Redact: "[CUSTOM]"},
	}
	r := NewRedactorWithPatterns(customPatterns)
	assert.Len(t, r.patterns, 1)
}

func TestAddPattern(t *testing.T) {
	r := NewRedactorWithPatterns(nil)
	r.AddPattern(SecretPattern{
		Name:    "test",
		Pattern: regexp.MustCompile(`TEST_SECRET`),
		Redact:  "[TEST]",
	})
	assert.Len(t, r.patterns, 1)
}

func TestRedactString_AWSAccessKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "valid aws access key",
			input:    "my key is AKIAIOSFODNN7EXAMPLE",
			expected: "my key is [REDACTED_AWS_KEY]",
			wantFind: true,
		},
		{
			name:     "aws key in json",
			input:    `{"access_key": "AKIAIOSFODNN7EXAMPLE"}`,
			expected: `{"access_key": "[REDACTED_AWS_KEY]"}`,
			wantFind: true,
		},
		{
			name:     "aws key in env var",
			input:    "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
			expected: "AWS_ACCESS_KEY_ID=[REDACTED_AWS_KEY]",
			wantFind: true,
		},
		{
			name:     "too short to be aws key",
			input:    "AKIAIOSFODNN7EX",
			expected: "AKIAIOSFODNN7EX",
			wantFind: false,
		},
		{
			name:     "lowercase not aws key",
			input:    "akiaiosfodnn7example",
			expected: "akiaiosfodnn7example",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasAWSKey := containsPattern(found, "aws_access_key")
			assert.Equal(t, tt.wantFind, hasAWSKey)
		})
	}
}

func TestRedactString_AWSSTSKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "valid aws sts session key",
			input:    "my key is ASIAIOSFODNN7EXAMPLE",
			expected: "my key is [REDACTED_AWS_STS_KEY]",
			wantFind: true,
		},
		{
			name:     "aws sts key in export",
			input:    `export AWS_ACCESS_KEY_ID="ASIAIOSFODNN7EXAMPLE"`,
			expected: `export AWS_ACCESS_KEY_ID="[REDACTED_AWS_STS_KEY]"`,
			wantFind: true,
		},
		{
			name:     "aws sts key in json",
			input:    `{"access_key": "ASIAIOSFODNN7EXAMPLE"}`,
			expected: `{"access_key": "[REDACTED_AWS_STS_KEY]"}`,
			wantFind: true,
		},
		{
			name:     "too short to be aws sts key",
			input:    "ASIAIOSFODNN7EX",
			expected: "ASIAIOSFODNN7EX",
			wantFind: false,
		},
		{
			name:     "lowercase not aws sts key",
			input:    "asiaiosfodnn7example",
			expected: "asiaiosfodnn7example",
			wantFind: false,
		},
		{
			name:     "akia not flagged as sts",
			input:    "my key is AKIAIOSFODNN7EXAMPLE",
			expected: "my key is [REDACTED_AWS_KEY]",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasSTS := containsPattern(found, "aws_sts_session_key")
			assert.Equal(t, tt.wantFind, hasSTS)
		})
	}
}

func TestRedactString_AWSSecretKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "aws secret with equals",
			input:    "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			expected: "[REDACTED_AWS_SECRET]",
			wantFind: true,
		},
		{
			name:     "aws secret with colon",
			input:    "aws_secret_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			expected: "[REDACTED_AWS_SECRET]",
			wantFind: true,
		},
		{
			name:     "aws secret in quotes",
			input:    `aws_secret_access_key="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`,
			expected: "[REDACTED_AWS_SECRET]",
			wantFind: true,
		},
		{
			name:     "random 40 char string not secret",
			input:    "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			expected: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasAWSSecret := containsPattern(found, "aws_secret_key")
			assert.Equal(t, tt.wantFind, hasAWSSecret)
		})
	}
}

func TestRedactString_GitHubTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "github personal access token",
			input:    "token: ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
			expected: "token: [REDACTED_GITHUB_TOKEN]",
			wantFind: true,
		},
		{
			name:     "github oauth token",
			input:    "gho_1234567890abcdefghijklmnopqrstuvwxyz12",
			expected: "[REDACTED_GITHUB_TOKEN]",
			wantFind: true,
		},
		{
			name:     "github server token",
			input:    "ghs_1234567890abcdefghijklmnopqrstuvwxyz12",
			expected: "[REDACTED_GITHUB_TOKEN]",
			wantFind: true,
		},
		{
			name:     "github refresh token",
			input:    "ghr_1234567890abcdefghijklmnopqrstuvwxyz12",
			expected: "[REDACTED_GITHUB_TOKEN]",
			wantFind: true,
		},
		{
			name:     "github user token",
			input:    "ghu_1234567890abcdefghijklmnopqrstuvwxyz12",
			expected: "[REDACTED_GITHUB_TOKEN]",
			wantFind: true,
		},
		{
			name:     "github fine-grained pat",
			input:    "github_pat_11ABCDEFGH0123456789_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdefghijklmnopqrs",
			expected: "[REDACTED_GITHUB_PAT]",
			wantFind: true,
		},
		{
			name:     "too short not github token",
			input:    "ghp_tooshort",
			expected: "ghp_tooshort",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasGitHub := containsPattern(found, "github_token") || containsPattern(found, "github_fine_grained_pat")
			assert.Equal(t, tt.wantFind, hasGitHub)
		})
	}
}

func TestRedactString_PrivateKeys(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "rsa private key header",
			input:    "-----BEGIN RSA PRIVATE KEY-----",
			expected: "[REDACTED_PRIVATE_KEY]",
			wantFind: true,
		},
		{
			name:     "ec private key header",
			input:    "-----BEGIN EC PRIVATE KEY-----",
			expected: "[REDACTED_PRIVATE_KEY]",
			wantFind: true,
		},
		{
			name:     "openssh private key",
			input:    "-----BEGIN OPENSSH PRIVATE KEY-----",
			expected: "[REDACTED_PRIVATE_KEY]",
			wantFind: true,
		},
		{
			name:     "generic private key",
			input:    "-----BEGIN PRIVATE KEY-----",
			expected: "[REDACTED_PRIVATE_KEY]",
			wantFind: true,
		},
		{
			name:     "public key not redacted",
			input:    "-----BEGIN PUBLIC KEY-----",
			expected: "-----BEGIN PUBLIC KEY-----",
			wantFind: false,
		},
		{
			name:     "certificate not redacted",
			input:    "-----BEGIN CERTIFICATE-----",
			expected: "-----BEGIN CERTIFICATE-----",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasPrivateKey := containsPattern(found, "private_key_header") || containsPattern(found, "private_key_generic")
			assert.Equal(t, tt.wantFind, hasPrivateKey)
		})
	}
}

func TestRedactString_ExportStatements(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{
			name:     "export github token",
			input:    "export GITHUB_TOKEN=abc123xyz",
			wantFind: true,
		},
		{
			name:     "export api key",
			input:    "export API_KEY='mysecretkey'",
			wantFind: true,
		},
		{
			name:     "export password",
			input:    `export PASSWORD="supersecret"`,
			wantFind: true,
		},
		{
			name:     "export aws secret",
			input:    "export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfi",
			wantFind: true,
		},
		{
			name:     "export path not secret",
			input:    "export PATH=/usr/local/bin",
			wantFind: false,
		},
		{
			name:     "export home not secret",
			input:    "export HOME=/home/user",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := r.RedactString(tt.input)
			hasExport := containsPattern(found, "export_secret") || containsPattern(found, "export_aws_secret")
			assert.Equal(t, tt.wantFind, hasExport, "found: %v", found)
		})
	}
}

func TestRedactString_ConnectionStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantFind bool
	}{
		{
			name:     "postgres connection string",
			input:    "DATABASE_URL=postgres://user:password123@localhost:5432/mydb",
			expected: "DATABASE_URL=[REDACTED_CONNECTION_STRING]",
			wantFind: true,
		},
		{
			name:     "mongodb connection string",
			input:    "mongodb://admin:secretpass@mongo.example.com:27017/production",
			expected: "[REDACTED_CONNECTION_STRING]",
			wantFind: true,
		},
		{
			name:     "redis connection string",
			input:    "redis://default:mypassword@redis.example.com:6379",
			expected: "[REDACTED_CONNECTION_STRING]",
			wantFind: true,
		},
		{
			name:     "mysql connection string",
			input:    "mysql://root:topsecret@db.example.com/app",
			expected: "[REDACTED_CONNECTION_STRING]",
			wantFind: true,
		},
		{
			name:     "url without password not redacted",
			input:    "https://example.com/api",
			expected: "https://example.com/api",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.Equal(t, tt.expected, output)
			hasConnStr := containsPattern(found, "connection_string")
			assert.Equal(t, tt.wantFind, hasConnStr)
		})
	}
}

func TestRedactString_JWT(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{
			name:     "valid jwt format",
			input:    "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			wantFind: true,
		},
		{
			name:     "not a jwt",
			input:    "eyJhbGciOiJIUzI1NiJ9",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := r.RedactString(tt.input)
			hasJWT := containsPattern(found, "jwt_token")
			assert.Equal(t, tt.wantFind, hasJWT)
		})
	}
}

func TestRedactString_BearerTokens(t *testing.T) {
	// Bearer-token inputs may match either the specific authorization_bearer
	// pattern (clean "Authorization: Bearer X" shape) or the legacy bearer_token
	// pattern (looser "bearer: ..." shape). Test the intent — that the credential
	// is redacted — not the specific detector that fires.
	tests := []struct {
		name           string
		input          string
		acceptable     []string // any of these pattern names satisfies the test
		wantRedactedIn string   // substring expected in redacted output
		wantFind       bool
	}{
		{
			name:           "bearer in header",
			input:          "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			acceptable:     []string{"authorization_bearer", "bearer_token"},
			wantRedactedIn: "[REDACTED_BEARER_TOKEN]",
			wantFind:       true,
		},
		{
			name:           "bearer with equals",
			input:          "authorization=bearer abc123def456ghi789jkl012mno345",
			acceptable:     []string{"authorization_bearer", "bearer_token"},
			wantRedactedIn: "[REDACTED_BEARER_TOKEN]",
			wantFind:       true,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			matchedSome := false
			for _, want := range tt.acceptable {
				if containsPattern(found, want) {
					matchedSome = true
					break
				}
			}
			assert.Equal(t, tt.wantFind, matchedSome, "found: %v", found)
			if tt.wantFind {
				assert.Contains(t, output, tt.wantRedactedIn)
			}
		})
	}
}

func TestRedactString_GenericSecrets(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantPattern string
		wantFind    bool
	}{
		{
			name:        "api key",
			input:       "api_key=abcdef1234567890abcdef",
			wantPattern: "generic_api_key",
			wantFind:    true,
		},
		{
			name:        "apikey no underscore",
			input:       "apikey: xyz789abc123def456ghi789",
			wantPattern: "generic_api_key",
			wantFind:    true,
		},
		{
			name:        "access token",
			input:       `access_token="mytoken1234567890abcd"`,
			wantPattern: "generic_token",
			wantFind:    true,
		},
		{
			name:        "password in quotes",
			input:       `password="verysecret123"`,
			wantPattern: "generic_password",
			wantFind:    true,
		},
		{
			name:        "client secret",
			input:       "client_secret=abc123def456789012345",
			wantPattern: "generic_secret",
			wantFind:    true,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := r.RedactString(tt.input)
			hasPattern := containsPattern(found, tt.wantPattern)
			assert.Equal(t, tt.wantFind, hasPattern, "found: %v", found)
		})
	}
}

func TestRedactString_SlackTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{
			name:     "slack bot token",
			input:    "SLACK_TOKEN=xoxb-123456789012-123456789012-abcdefghij",
			wantFind: true,
		},
		{
			name:     "slack user token",
			input:    "xoxp-000000000-000000000-000000000-fakeee",
			wantFind: true,
		},
		{
			name:     "not slack token",
			input:    "xox-tooshort",
			wantFind: false,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := r.RedactString(tt.input)
			hasSlack := containsPattern(found, "slack_token")
			assert.Equal(t, tt.wantFind, hasSlack)
		})
	}
}

func TestRedactString_StripeKeys(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{
			name:     "stripe live secret key",
			input:    "sk_live_xxFAKEKEYxxxxxxxxxxxxxxxn",
			wantFind: true,
		},
		{
			name:     "stripe test secret key",
			input:    "sk_test_xxFAKEKEYxxxxxxxxxxxxxxx",
			wantFind: true,
		},
		{
			name:     "stripe restricted key",
			input:    "rk_live_xxFAKEKEYxxxxxxxxxxxxxxx",
			wantFind: true,
		},
	}

	r := NewRedactor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := r.RedactString(tt.input)
			hasStripe := containsPattern(found, "stripe_key")
			assert.Equal(t, tt.wantFind, hasStripe)
		})
	}
}

func TestRedactString_MultipleSecrets(t *testing.T) {
	input := `
AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz12
`
	r := NewRedactor()
	output, found := r.RedactString(input)

	assert.Contains(t, output, "[REDACTED_AWS_KEY]")
	assert.Contains(t, output, "[REDACTED_AWS_SECRET]")
	assert.Contains(t, output, "[REDACTED_GITHUB_TOKEN]")
	assert.GreaterOrEqual(t, len(found), 3, "expected at least 3 patterns found, got %d: %v", len(found), found)
}

func TestRedactString_NoSecrets(t *testing.T) {
	inputs := []string{
		"This is just normal text",
		"No secrets here",
		"PATH=/usr/local/bin",
		"export HOME=/home/user",
		"https://example.com",
		"user@example.com",
	}

	r := NewRedactor()
	for _, input := range inputs {
		output, found := r.RedactString(input)
		assert.Equal(t, input, output, "expected no change for %q", input)
		assert.Empty(t, found, "expected no secrets in %q", input)
	}
}

func TestRedactEntry(t *testing.T) {
	r := NewRedactor()

	t.Run("nil entry", func(t *testing.T) {
		redacted := r.RedactEntry(nil)
		assert.False(t, redacted, "expected false for nil entry")
	})

	t.Run("entry with secret", func(t *testing.T) {
		entry := &Entry{
			Timestamp: time.Now(),
			Type:      EntryTypeUser,
			Content:   "My AWS key is AKIAIOSFODNN7EXAMPLE",
		}
		redacted := r.RedactEntry(entry)
		assert.True(t, redacted)
		assert.Contains(t, entry.Content, "[REDACTED_AWS_KEY]")
	})

	t.Run("entry without secret", func(t *testing.T) {
		entry := &Entry{
			Timestamp: time.Now(),
			Type:      EntryTypeAssistant,
			Content:   "Hello, how can I help you?",
		}
		redacted := r.RedactEntry(entry)
		assert.False(t, redacted)
	})
}

func TestRedactEntries(t *testing.T) {
	r := NewRedactor()

	t.Run("entries with mixed content", func(t *testing.T) {
		entries := []Entry{
			{Timestamp: time.Now(), Type: EntryTypeUser, Content: "My key is AKIAIOSFODNN7EXAMPLE"},
			{Timestamp: time.Now(), Type: EntryTypeAssistant, Content: "I'll help with that"},
			{Timestamp: time.Now(), Type: EntryTypeUser, Content: "password='secret123456'"},
			{Timestamp: time.Now(), Type: EntryTypeSystem, Content: "Normal system message"},
		}
		count := r.RedactEntries(entries)
		assert.Equal(t, 2, count)
		assert.Contains(t, entries[0].Content, "[REDACTED_AWS_KEY]")
		assert.Equal(t, "I'll help with that", entries[1].Content)
	})

	t.Run("entries with tool input", func(t *testing.T) {
		entries := []Entry{
			{
				Timestamp: time.Now(),
				Type:      EntryTypeTool,
				Content:   "Result output",
				ToolName:  "bash",
				ToolInput: "export GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
			},
		}
		count := r.RedactEntries(entries)
		assert.Equal(t, 1, count)
		assert.Contains(t, entries[0].ToolInput, "[REDACTED")
	})
}

func TestContainsSecrets(t *testing.T) {
	r := NewRedactor()

	tests := []struct {
		input    string
		expected bool
	}{
		{"AKIAIOSFODNN7EXAMPLE", true},
		{"ghp_1234567890abcdefghijklmnopqrstuvwxyz12", true},
		{"-----BEGIN RSA PRIVATE KEY-----", true},
		{"Normal text", false},
		{"", false},
	}

	for _, tt := range tests {
		result := r.ContainsSecrets(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

func TestScanForSecrets(t *testing.T) {
	r := NewRedactor()

	input := "AKIAIOSFODNN7EXAMPLE and ghp_1234567890abcdefghijklmnopqrstuvwxyz12"
	found := r.ScanForSecrets(input)

	assert.True(t, containsPattern(found, "aws_access_key"))
	assert.True(t, containsPattern(found, "github_token"))
}

func TestRedactWithAllowlist(t *testing.T) {
	r := NewRedactor()

	t.Run("allowlist preserves known values", func(t *testing.T) {
		// this looks like a secret pattern but is actually a known-safe value
		input := "api_key=test_placeholder_value123"
		allowlist := []string{"test_placeholder_value123"}

		output, _ := r.RedactWithAllowlist(input, allowlist)

		// the allowlisted value should be preserved
		assert.Contains(t, output, "test_placeholder_value123")
	})

	t.Run("non-allowlisted secrets still redacted", func(t *testing.T) {
		input := "key1=AKIAIOSFODNN7EXAMPLE key2=allowed_value"
		allowlist := []string{"allowed_value"}

		output, found := r.RedactWithAllowlist(input, allowlist)

		assert.Contains(t, output, "[REDACTED_AWS_KEY]")
		assert.Contains(t, output, "allowed_value")
		assert.True(t, containsPattern(found, "aws_access_key"))
	})
}

func TestRedactStringWithDetails(t *testing.T) {
	r := NewRedactor()

	input := "Key: AKIAIOSFODNN7EXAMPLE"
	output, results := r.RedactStringWithDetails(input)

	assert.Contains(t, output, "[REDACTED_AWS_KEY]")
	require.NotEmpty(t, results)

	found := false
	for _, result := range results {
		if result.PatternName == "aws_access_key" {
			found = true
			assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", result.Original)
		}
	}
	assert.True(t, found, "expected aws_access_key in results")
}

func TestRedactor_ConcurrentSafe(t *testing.T) {
	r := NewRedactor()
	input := "AKIAIOSFODNN7EXAMPLE"

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				r.RedactString(input)
				r.ContainsSecrets(input)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestEdgeCases(t *testing.T) {
	r := NewRedactor()

	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"whitespace only", "   \n\t  "},
		{"unicode text", "Hello \u4e16\u754c AKIAIOSFODNN7EXAMPLE"},
		{"very long string", strings.Repeat("a", 10000) + "AKIAIOSFODNN7EXAMPLE"},
		{"newlines around secret", "\n\nAKIAIOSFODNN7EXAMPLE\n\n"},
		{"tabs around secret", "\t\tAKIAIOSFODNN7EXAMPLE\t\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// should not panic
			output, _ := r.RedactString(tt.input)
			if tt.input != "" && strings.Contains(tt.input, "AKIAIOSFODNN7EXAMPLE") {
				assert.Contains(t, output, "[REDACTED_AWS_KEY]")
			}
		})
	}
}

func TestFalsePositives(t *testing.T) {
	r := NewRedactor()

	// these should NOT be flagged as secrets
	falsePositives := []string{
		// short random strings
		"abc123",
		"test1234",
		// common identifiers
		"user_id=12345",
		"session_id=abcd-efgh",
		// file paths
		"/home/user/.config/settings",
		// urls without credentials
		"https://api.example.com/v1/users",
		// common words
		"the password field is required",
		"enter your api key below",
		// uuid-like strings that aren't heroku keys
		"550e8400-e29b", // partial UUID
	}

	for _, input := range falsePositives {
		output, found := r.RedactString(input)
		assert.Equal(t, input, output, "false positive: %q was modified to %q (patterns: %v)", input, output, found)
	}
}

// helper function
func containsPattern(found []string, pattern string) bool {
	for _, f := range found {
		if f == pattern {
			return true
		}
	}
	return false
}

// Tests for RedactHistorySecrets

func TestRedactHistorySecrets_APIKeyInContent(t *testing.T) {
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "user",
				Content:   "My API key is api_key=abcdef1234567890abcdef",
				Timestamp: time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_API_KEY]")
	assert.NotContains(t, history.Entries[0].Content, "abcdef1234567890abcdef")
}

func TestRedactHistorySecrets_PasswordInToolInput(t *testing.T) {
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "tool",
				Content:   "Executing command",
				ToolName:  "bash",
				ToolInput: `export PASSWORD="supersecret123"`,
				Timestamp: time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].ToolInput, "[REDACTED")
	assert.NotContains(t, history.Entries[0].ToolInput, "supersecret123")
}

func TestRedactHistorySecrets_TokenInToolOutput(t *testing.T) {
	// use a GitHub token format which is guaranteed to match
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:        1,
				Type:       "tool",
				Content:    "Command completed",
				ToolName:   "bash",
				ToolInput:  "cat config.json",
				ToolOutput: `{"token": "ghp_1234567890abcdefghijklmnopqrstuvwxyz12"}`,
				Timestamp:  time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].ToolOutput, "[REDACTED_GITHUB_TOKEN]")
	assert.NotContains(t, history.Entries[0].ToolOutput, "ghp_1234567890abcdefghijklmnopqrstuvwxyz12")
}

func TestRedactHistorySecrets_MultipleSecrets(t *testing.T) {
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "user",
				Content:   "AWS key: AKIAIOSFODNN7EXAMPLE",
				Timestamp: time.Now(),
			},
			{
				Seq:       2,
				Type:      "assistant",
				Content:   "Found connection string: postgres://user:password@localhost:5432/db",
				Timestamp: time.Now(),
			},
			{
				Seq:        3,
				Type:       "tool",
				Content:    "Output",
				ToolName:   "bash",
				ToolInput:  "export GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
				ToolOutput: `{"secret": "client_secret=abc123def456789012345"}`,
				Timestamp:  time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 3, count, "expected 3 entries with secrets")

	// verify each secret was redacted
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_AWS_KEY]")
	assert.Contains(t, history.Entries[1].Content, "[REDACTED_CONNECTION_STRING]")
	assert.Contains(t, history.Entries[2].ToolInput, "[REDACTED")
	assert.Contains(t, history.Entries[2].ToolOutput, "[REDACTED_SECRET]")

	// verify original secrets are gone
	assert.NotContains(t, history.Entries[0].Content, "AKIAIOSFODNN7EXAMPLE")
	assert.NotContains(t, history.Entries[1].Content, "password@localhost")
	assert.NotContains(t, history.Entries[2].ToolInput, "ghp_1234567890abcdefghijklmnopqrstuvwxyz12")
}

func TestRedactHistorySecrets_NoSecrets(t *testing.T) {
	originalContent := "This is just a normal conversation"
	originalToolInput := "ls -la"
	originalToolOutput := "file.txt"

	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "user",
				Content:   originalContent,
				Timestamp: time.Now(),
			},
			{
				Seq:        2,
				Type:       "tool",
				Content:    "Command output",
				ToolName:   "bash",
				ToolInput:  originalToolInput,
				ToolOutput: originalToolOutput,
				Timestamp:  time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 0, count)
	assert.Equal(t, originalContent, history.Entries[0].Content)
	assert.Equal(t, originalToolInput, history.Entries[1].ToolInput)
	assert.Equal(t, originalToolOutput, history.Entries[1].ToolOutput)
}

func TestRedactHistorySecrets_EmptyHistory(t *testing.T) {
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 0, count)
	assert.Empty(t, history.Entries)
}

func TestRedactHistorySecrets_NilHistory(t *testing.T) {
	// should handle nil gracefully without panicking
	count := RedactHistorySecrets(nil)
	assert.Equal(t, 0, count)
}

func TestRedactHistorySecrets_NilMeta(t *testing.T) {
	// history with nil meta but valid entries should still work
	history := &CapturedHistory{
		Meta: nil,
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "user",
				Content:   "Key: AKIAIOSFODNN7EXAMPLE",
				Timestamp: time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_AWS_KEY]")
}

func TestRedactHistorySecrets_EmptyFields(t *testing.T) {
	// entries with empty ToolInput/ToolOutput should not cause issues
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:        1,
				Type:       "tool",
				Content:    "Key: AKIAIOSFODNN7EXAMPLE",
				ToolName:   "bash",
				ToolInput:  "", // empty
				ToolOutput: "", // empty
				Timestamp:  time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_AWS_KEY]")
}

func TestRedactHistorySecrets_AllFieldsWithSecrets(t *testing.T) {
	// test that Content, ToolInput, and ToolOutput are all scanned
	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:        1,
				Type:       "tool",
				Content:    "AWS: AKIAIOSFODNN7EXAMPLE",
				ToolName:   "bash",
				ToolInput:  `password="verysecret123"`,
				ToolOutput: "ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
				Timestamp:  time.Now(),
			},
		},
	}

	count := RedactHistorySecrets(history)
	assert.Equal(t, 1, count) // count is per entry, not per field

	// verify all fields were scanned and redacted
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_AWS_KEY]")
	assert.Contains(t, history.Entries[0].ToolInput, "[REDACTED_PASSWORD]")
	assert.Contains(t, history.Entries[0].ToolOutput, "[REDACTED_GITHUB_TOKEN]")
}

func TestRedactHistoryEntries_DirectCall(t *testing.T) {
	// test the Redactor method directly for consistency with RedactHistorySecrets
	r := NewRedactor()

	entries := []HistoryEntry{
		{
			Seq:       1,
			Type:      "user",
			Content:   "AKIAIOSFODNN7EXAMPLE",
			Timestamp: time.Now(),
		},
		{
			Seq:       2,
			Type:      "assistant",
			Content:   "No secrets here",
			Timestamp: time.Now(),
		},
	}

	count := r.RedactHistoryEntries(entries)
	assert.Equal(t, 1, count)
	assert.Contains(t, entries[0].Content, "[REDACTED_AWS_KEY]")
	assert.Equal(t, "No secrets here", entries[1].Content)
}

func TestRedactCapturedHistory_DirectCall(t *testing.T) {
	// test the Redactor method directly
	r := NewRedactor()

	history := &CapturedHistory{
		Meta: &HistoryMeta{
			SchemaVersion: HistorySchemaVersion,
			Source:        "test",
			AgentID:       "test-agent",
		},
		Entries: []HistoryEntry{
			{
				Seq:       1,
				Type:      "user",
				Content:   "Token: ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
				Timestamp: time.Now(),
			},
		},
	}

	count := r.RedactCapturedHistory(history)
	assert.Equal(t, 1, count)
	assert.Contains(t, history.Entries[0].Content, "[REDACTED_GITHUB_TOKEN]")
}

func TestRedactCapturedHistory_NilHistory(t *testing.T) {
	r := NewRedactor()
	count := r.RedactCapturedHistory(nil)
	assert.Equal(t, 0, count)
}

// --- New detectors added by ox-ceuj ---
// Each subtest exercises a positive case (must be caught) and at least one
// negative / skip-list case (must not be caught) so we know each detector
// has both signal and specificity.

func TestRedactString_SoxShareSession(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{"sets cookie", "Set-Cookie: sox_share_session=AbCdEf1234567890XyZ", true},
		{"in url", "https://sageox.ai/s/foo?sox_share_session=abcdefghij1234567890", true},
		{"too short", "sox_share_session=short", false},
		{"different cookie", "sox_other=AbCdEf1234567890XyZ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			has := containsPattern(found, "sox_share_session")
			assert.Equal(t, tt.wantFind, has, "found: %v", found)
			if tt.wantFind {
				assert.Contains(t, output, "[REDACTED_SOX_SHARE_SESSION]")
			}
		})
	}
}

func TestRedactString_AuthorizationHeaders(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name     string
		input    string
		want     string // pattern name expected
		slug     string // redaction slug expected in output
		wantFind bool
	}{
		{
			name:     "authorization bearer header",
			input:    "Authorization: Bearer ya29.a0ARrdaM_THIS_IS_A_LONG_TOKEN_xyz",
			want:     "authorization_bearer",
			slug:     "[REDACTED_BEARER_TOKEN]",
			wantFind: true,
		},
		{
			name:     "authorization bearer extra whitespace",
			input:    "Authorization:    Bearer abc123def456ghi789jkl012mno",
			want:     "authorization_bearer",
			slug:     "[REDACTED_BEARER_TOKEN]",
			wantFind: true,
		},
		{
			name:     "authorization basic header",
			input:    "Authorization: Basic dXNlcjpwYXNzd29yZDEyMzQ1Ng==",
			want:     "authorization_basic",
			slug:     "[REDACTED_BASIC_AUTH]",
			wantFind: true,
		},
		{
			name:     "not an auth header",
			input:    "Authorization is required for this endpoint.",
			want:     "authorization_bearer",
			slug:     "",
			wantFind: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			has := containsPattern(found, tt.want)
			assert.Equal(t, tt.wantFind, has, "found: %v", found)
			if tt.wantFind {
				assert.Contains(t, output, tt.slug)
			}
		})
	}
}

func TestRedactString_GitLabVariants(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name string
		in   string
		want string
		slug string
	}{
		{"OAuth gloas-", "token=gloas-abcdef1234567890", "gitlab_oauth_token", "[REDACTED_GITLAB_OAUTH]"},
		{"Deploy gldt-", "GL_TOKEN=gldt-deadbeef1234567890", "gitlab_deploy_token", "[REDACTED_GITLAB_DEPLOY_TOKEN]"},
		{"Runner glrt-", "runner_token: glrt-feedface12345678", "gitlab_runner_token", "[REDACTED_GITLAB_RUNNER_TOKEN]"},
		{"Feed glft-", "feed=glft-cafebabe1234567890", "gitlab_feed_token", "[REDACTED_GITLAB_FEED_TOKEN]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.in)
			assert.True(t, containsPattern(found, tt.want), "want %s, found: %v", tt.want, found)
			assert.Contains(t, output, tt.slug)
		})
	}
	t.Run("not a gitlab variant", func(t *testing.T) {
		_, found := r.RedactString("token=gl-something-else-12345")
		for _, name := range []string{"gitlab_oauth_token", "gitlab_deploy_token", "gitlab_runner_token", "gitlab_feed_token"} {
			assert.False(t, containsPattern(found, name), "false positive on %s: %v", name, found)
		}
	})
}

func TestRedactString_SageOxAndAgentXKeys(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name     string
		input    string
		want     string
		slug     string
		wantFind bool
	}{
		{
			name:     "sageox api key",
			input:    "OX_API_KEY=mk_abcdef1234567890wxyz0987654321",
			want:     "sageox_api_key",
			slug:     "[REDACTED_SAGEOX_API_KEY]",
			wantFind: true,
		},
		{
			name:     "agentx key full length",
			input:    "AGENTX_KEY=axk_abcdefghij1234567890ABCDEFGHIJkl",
			want:     "agentx_key",
			slug:     "[REDACTED_AGENTX_KEY]",
			wantFind: true,
		},
		{
			name:     "mk in middle of word not matched",
			input:    "filename=foobar_mk_short.txt",
			want:     "sageox_api_key",
			slug:     "",
			wantFind: false,
		},
		{
			name:     "axk too short",
			input:    "axk_short",
			want:     "agentx_key",
			slug:     "",
			wantFind: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			has := containsPattern(found, tt.want)
			assert.Equal(t, tt.wantFind, has, "found: %v", found)
			if tt.wantFind {
				assert.Contains(t, output, tt.slug)
			}
		})
	}
}

func TestRedactString_URLWithPassword(t *testing.T) {
	r := NewRedactor()
	// Each case asserts the SECRET BYTES are gone (intent), and where the
	// url_with_password detector is the only one that could fire (no inner
	// token-shape match), asserts that specific slug.
	type tc struct {
		name           string
		input          string
		secret         string // bytes that must NOT appear in output
		wantUrlPattern bool   // true only when no inner-token detector could preempt
	}
	tests := []tc{
		{
			name:           "http with basic auth",
			input:          "curl http://admin:supersecret123@internal.example.com/api",
			secret:         "supersecret123",
			wantUrlPattern: true,
		},
		{
			name:           "https with non-prefix password",
			input:          "https://user:plainpassword123456@host.example.com/foo",
			secret:         "plainpassword123456",
			wantUrlPattern: true,
		},
		{
			name:           "https with gitlab pat embedded (gitlab_token preempts)",
			input:          "git remote add origin https://oauth2:glpat-abc123def456ghi789jk@git.sageox.ai/foo/bar.git",
			secret:         "glpat-abc123def456ghi789jk",
			wantUrlPattern: false, // gitlab_token fires first, url_with_password doesn't get a chance
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, found := r.RedactString(tt.input)
			assert.NotContains(t, output, tt.secret, "secret leaked through: found=%v output=%q", found, output)
			if tt.wantUrlPattern {
				assert.True(t, containsPattern(found, "url_with_password"), "expected url_with_password, found: %v", found)
				assert.Contains(t, output, "[REDACTED_URL_WITH_CREDENTIALS]")
			}
		})
	}

	t.Run("skip list cases not flagged", func(t *testing.T) {
		// SkipIf substrings must suppress redaction by url_with_password.
		// Note: other detectors may still fire on the same input if they match
		// independently. We assert specifically that url_with_password did NOT.
		skipCases := []string{
			"remote: https://gh_token_value_here:x-oauth-basic@github.com/foo/bar.git",
			"https://token123456:x-access-token@github.com/foo/bar.git",
			"https://user:****@host.example.com/foo",
			"https://user:[REDACTED]@host.example.com/foo",
			"https://git.sageox.ai/foo/bar.git", // no userinfo
		}
		for _, input := range skipCases {
			_, found := r.RedactString(input)
			assert.False(t, containsPattern(found, "url_with_password"),
				"url_with_password incorrectly fired on skip-list case %q: %v", input, found)
		}
	})
}

func TestRedactString_EnvAssignmentFallback(t *testing.T) {
	r := NewRedactor()
	// env_assignment fills the gap for env-dump-shaped vars NOT covered by
	// existing generic patterns. It must NOT preempt them.
	tests := []struct {
		name     string
		input    string
		wantFind bool
	}{
		{"github token assignment", "GITHUB_TOKEN=ghp_alphanumericvalue1234567890XYZ", true}, // ghp_ matches first; env_assignment may also match
		{"gitlab token assignment", "GITLAB_TOKEN=somelong-tokenvalue-1234567890abc", true},
		{"postgres password assignment", `POSTGRES_PASSWORD="reallylongpassword1234"`, true},
		{"unrelated assignment", "FOO_VAR=hello_world", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, _ := r.RedactString(tt.input)
			// intent: secret should be gone from output regardless of which detector fires
			if tt.wantFind {
				// match any [REDACTED_*] slug
				assert.True(t, strings.Contains(output, "[REDACTED"), "expected some redaction, got %q", output)
			} else {
				assert.NotContains(t, output, "[REDACTED")
			}
		})
	}
}

func TestRedactString_PreservesNonSecrets(t *testing.T) {
	// Regression guard: legitimate Ledger content must not get redacted.
	r := NewRedactor()
	legitimate := []string{
		"abc12345-6789-1abc-def0-1234567890ab",                                                // UUID — already covered by heroku_key pattern; intentional false positive
		"commit a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",                                     // git SHA
		"https://git.sageox.ai/foo/bar.git",                                                   // bare URL
		"[REDACTED_AWS_KEY] was found",                                                        // already-redacted slug
		"This is a regular sentence containing no credentials at all whatsoever you can grep", // prose
	}
	for _, input := range legitimate {
		// allow already-known overmatches (UUID/heroku_key) by checking we don't introduce NEW false positives
		_, found := r.RedactString(input)
		// must not match any of the new ox-ceuj detectors
		newDetectors := []string{
			"sox_share_session",
			"gitlab_oauth_token", "gitlab_deploy_token", "gitlab_runner_token", "gitlab_feed_token",
			"sageox_api_key", "agentx_key",
			"authorization_bearer", "authorization_basic",
			"url_with_password",
			"env_assignment",
		}
		for _, d := range newDetectors {
			assert.False(t, containsPattern(found, d), "input %q false-matched %s (full: %v)", input, d, found)
		}
	}
}

// Benchmarks

func BenchmarkRedactString_NoSecrets(b *testing.B) {
	r := NewRedactor()
	input := "This is a normal string with no secrets whatsoever."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RedactString(input)
	}
}

func BenchmarkRedactString_WithSecrets(b *testing.B) {
	r := NewRedactor()
	input := "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE and token=ghp_1234567890abcdefghijklmnopqrstuvwxyz12"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RedactString(input)
	}
}

func BenchmarkRedactString_LongText(b *testing.B) {
	r := NewRedactor()
	input := strings.Repeat("This is normal text. ", 1000) + "AKIAIOSFODNN7EXAMPLE"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RedactString(input)
	}
}

func BenchmarkContainsSecrets(b *testing.B) {
	r := NewRedactor()
	input := "This is a normal string with no secrets whatsoever."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ContainsSecrets(input)
	}
}
