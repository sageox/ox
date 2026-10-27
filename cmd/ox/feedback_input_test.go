package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFeedbackReport(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		valid bool
	}{
		{name: "preserve internal text", input: " \nSync fails\r\n\tFirst detail.\n\nSecond detail. \n", want: "Sync fails\r\n\tFirst detail.\n\nSecond detail.", valid: true},
		{name: "title only", input: "Sync fails", want: "Sync fails", valid: true},
		{name: "empty", input: " \t\n"},
		{name: "short title", input: "Too few\nA longer description"},
		{name: "maximum title", input: strings.Repeat("a", 200), want: strings.Repeat("a", 200), valid: true},
		{name: "long title", input: strings.Repeat("a", 201)},
		{name: "Unicode title", input: strings.Repeat("界", 8), want: strings.Repeat("界", 8), valid: true},
		{name: "maximum UTF16 title", input: strings.Repeat("😀", 100), want: strings.Repeat("😀", 100), valid: true},
		{name: "Unicode title", input: strings.Repeat("😀", 200), want: strings.Repeat("😀", 200), valid: true},
		{name: "maximum characters", input: "Sync fails\n" + strings.Repeat("a", 3989), want: "Sync fails\n" + strings.Repeat("a", 3989), valid: true},
		{name: "too many characters", input: "Sync fails\n" + strings.Repeat("a", 3990)},
		{name: "too many UTF16 units", input: "Sync fails\n" + strings.Repeat("a", 3988) + "😀"},
		{name: "maximum bytes", input: "Sync fails\n" + strings.Repeat("é", 1000) + strings.Repeat("a", 2085), want: "Sync fails\n" + strings.Repeat("é", 1000) + strings.Repeat("a", 2085), valid: true},
		{name: "too many bytes", input: "Sync fails\n" + strings.Repeat("é", 1000) + strings.Repeat("a", 2086)},
		{name: "invalid UTF8", input: "Sync fails\n\xff"},
		{name: "terminal escape", input: "Sync fails\n\x1b[31mhidden"},
		{name: "NUL", input: "Sync fails\n\x00"},
		{name: "delete", input: "Sync fails\n\x7f"},
		{name: "C1 control", input: "Sync fails\n\u0085"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readFeedbackReport(strings.NewReader(test.input))
			if !test.valid {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestReadFeedbackReportBoundsReader(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("a", feedbackMaxReportBytes*2))
	_, err := readFeedbackReport(reader)
	require.Error(t, err)
	assert.Equal(t, feedbackMaxReportBytes-1, reader.Len())
	_, err = readFeedbackReport(feedbackFailingReader{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private read detail")
}

type feedbackFailingReader struct{}

func (feedbackFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("private read detail")
}

var _ io.Reader = feedbackFailingReader{}

func TestValidateFeedbackType(t *testing.T) {
	for _, value := range []string{"bug", "feature-request", "feedback"} {
		assert.NoError(t, validateFeedbackType(value))
	}
	for _, value := range []string{"", "BUG", "feature", " bug"} {
		assert.Error(t, validateFeedbackType(value))
	}
}

func TestValidateFeedbackEmail(t *testing.T) {
	for _, value := range []string{"", "person@example.com", "person+feedback@example.com"} {
		assert.NoError(t, validateFeedbackEmail(value))
	}
	for _, value := range []string{" ", "person", "person@localhost", "Person A <person@example.com>", "person@example.com (Person A)", " person@example.com", "person@example.com\n", "one@example.com,two@example.com", strings.Repeat("a", 243) + "@example.com"} {
		assert.Error(t, validateFeedbackEmail(value))
	}
}

func TestValidateFeedbackLocalPayloadRejectsSecretsWithoutEcho(t *testing.T) {
	secrets := []string{
		"oxp_syntheticFeedbackSentinel123456",
		"ghp_syntheticFeedbackSentinel12345678901234",
		"API_KEY=syntheticOpaqueFeedbackSentinel-73xyAZ",
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("synthetic-key-material", 5) + "\n-----END PRIVATE KEY-----",
	}
	for index, secret := range secrets {
		payload, err := json.Marshal(map[string]string{"report": "Sync fails\n" + secret})
		require.NoError(t, err)
		for _, content := range [][]byte{[]byte(secret), payload} {
			err := validateFeedbackLocalPayload(content)
			require.Error(t, err, "synthetic secret case %d", index)
			assert.EqualError(t, err, "feedback contains a possible secret; remove credentials and retry")
		}
	}
	assert.NoError(t, validateFeedbackLocalPayload([]byte(`{"report":"Sync fails after the update"}`)))
}

func TestValidateFeedbackPayloadUsesBundledAndFreshRules(t *testing.T) {
	previous := feedbackLoadLatestRules
	t.Cleanup(func() { feedbackLoadLatestRules = previous })
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='future-feedback-provider'\nregex='future_feedback_synthetic_credential'\n"))
	require.NoError(t, err)
	calls := 0
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		calls++
		return rules, nil
	}
	assert.Error(t, validateFeedbackPayload(context.Background(), []byte("oxp_syntheticFeedbackSentinel123456")))
	assert.Zero(t, calls, "bundled rejection must precede any network access")
	assert.NoError(t, validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails after the update"}`)))
	assert.Equal(t, 1, calls)
	err = validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails","diagnostics":{"message":"future_feedback_synthetic_credential"}}`))
	require.Error(t, err)
	assert.EqualError(t, err, "feedback contains a possible secret; remove credentials and retry")
	assert.Equal(t, 2, calls)
}

// A current catalog is the bundled one plus what upstream added since; after
// the bundled check, feedback validates only the additions.
// Failure prevented: that narrower second check drops a rule the bundled
// catalog lacks, and a credential format added after this release is sent.
func TestValidateFeedbackPayloadRefusesWhatOnlyCurrentRulesCatch(t *testing.T) {
	previous := feedbackLoadLatestRules
	t.Cleanup(func() { feedbackLoadLatestRules = previous })
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "internal", "secretguard", "gitleaks.toml"))
	require.NoError(t, err)
	rules, err := secretguard.NewRules(append(data, "\n[[rules]]\nid = \"future-format\"\nregex = '''future_[a-z]{8}'''\n"...))
	require.NoError(t, err)
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	assert.NoError(t, validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails after the update"}`)))
	err = validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails","diagnostics":{"message":"future_qzxwvjkp"}}`))
	assert.EqualError(t, err, "feedback contains a possible secret; remove credentials and retry")
}

func TestValidateFeedbackPayloadFailsClosed(t *testing.T) {
	previous := feedbackLoadLatestRules
	t.Cleanup(func() { feedbackLoadLatestRules = previous })
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		return nil, errors.New("private network detail")
	}
	err := validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails"}`))
	require.Error(t, err)
	assert.EqualError(t, err, "cannot verify current secret detection rules; feedback was not submitted")
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		return nil, nil
	}
	assert.Error(t, validateFeedbackPayload(context.Background(), []byte(`{"report":"Sync fails"}`)))
}
