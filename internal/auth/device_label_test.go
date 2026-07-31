package auth

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deviceLabelShape is the contract every emitted label must satisfy:
// allowlisted characters only, at most one "@" (contributed by the
// composer, never by a part).
var deviceLabelShape = regexp.MustCompile(`^[A-Za-z0-9._-]+(@[A-Za-z0-9._-]+)?$`)

func TestOptedOutOfDeviceLabel(t *testing.T) {
	tests := []struct {
		value string
		set   bool
		want  bool
		why   string
	}{
		{set: false, want: false, why: "unset is the default — labels on"},
		{value: "", set: true, want: false, why: "empty reads as unset"},
		{value: "1", set: true, want: true, why: "the documented opt-out"},
		{value: "true", set: true, want: true, why: "obvious alias"},
		{value: "TRUE", set: true, want: true, why: "case-insensitive"},
		{value: "  1  ", set: true, want: true, why: "tolerate stray whitespace from shell config"},
		{value: "0", set: true, want: false, why: "SAGEOX_NO_DEVICE_LABEL=0 must mean labels ON, as it reads"},
		{value: "false", set: true, want: false, why: "same"},
		{value: "no", set: true, want: false, why: "same"},
	}
	for _, tt := range tests {
		name := tt.value
		if !tt.set {
			name = "<unset>"
		}
		t.Run(name, func(t *testing.T) {
			if tt.set {
				t.Setenv(EnvVarNoDeviceLabel, tt.value)
			}
			assert.Equal(t, tt.want, optedOutOfDeviceLabel(), tt.why)
		})
	}
}

func TestDeviceLabel_OptOutProducesNothing(t *testing.T) {
	t.Setenv(EnvVarNoDeviceLabel, "1")
	t.Setenv("USER", "ryan")

	assert.Empty(t, deviceLabel(),
		"opting out must yield an empty label so omitempty drops the field entirely")
}

func TestDeviceLabel_ComposesUserAtHost(t *testing.T) {
	t.Setenv("USER", "ryan")

	got := deviceLabel()

	require.NotEmpty(t, got)
	assert.Regexp(t, deviceLabelShape, got)
	assert.True(t, strings.HasPrefix(got, "ryan@") || got == "ryan",
		"the username must lead; got %q", got)
}

func TestDeviceLabel_FallsBackWhenUsernameUnavailable(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")

	got := deviceLabel()

	// hostname may legitimately be empty in some sandboxes; either way
	// the result must still satisfy the shape contract.
	if got != "" {
		assert.Regexp(t, deviceLabelShape, got)
		assert.NotContains(t, got, "@", "with no username there is nothing to join")
	}
}

func TestSanitizeLabelPart(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
		why   string
	}{
		{"plain", "laptop", 63, "laptop", "the common case is untouched"},
		{"allowed punctuation", "my-box_1.2", 63, "my-box_1.2", "dots, dashes and underscores are safe everywhere"},
		{
			"header injection", "a\r\nX-Evil: 1", 63, "a-X-Evil-1",
			"CRLF must not survive into an HTTP header — this is why the filter is an allowlist",
		},
		{"null byte", "a\x00b", 63, "a-b", "control characters are excluded by construction"},
		{"sql-ish", "; DROP TABLE", 63, "DROP-TABLE", "neutralized, and leading separators trimmed"},
		{
			"at sign inside a part", "ryan@corp", 63, "ryan-corp",
			"an @ in a PART is neutralized, so the single @ in a composed label is always the composer's",
		},
		{"non-ascii", "café", 63, "caf", "non-ASCII is dropped, trailing dash trimmed"},
		{"collapses runs", "a!!!b", 63, "a-b", "one separator, not three"},
		{"trims edges", "---host---", 63, "host", "no leading or trailing separators"},
		{"all separators", "!!!", 63, "", "nothing usable means empty, not a bare dash"},
		{"empty", "", 63, "", ""},
		{"truncates", strings.Repeat("a", 200), 63, strings.Repeat("a", 63), "a 200-char hostname is cut to the DNS label max"},
		{"truncation trims trailing separator", "abcde-fgh", 6, "abcde", "must not end on a dash after cutting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLabelPart(tt.in, tt.limit)
			assert.Equal(t, tt.want, got, tt.why)
			assert.LessOrEqual(t, utf8.RuneCountInString(got), tt.limit)
		})
	}
}

// TestDeviceLabel_HostileHostnameStillProducesASafeLabel is the property
// test. A hostname is attacker-influenceable on a shared machine, and the
// label is transmitted and then rendered in a web UI.
func TestDeviceLabel_HostileHostnameStillProducesASafeLabel(t *testing.T) {
	hostile := []string{
		"a\r\nX-Injected: yes",
		`"><script>alert(1)</script>`,
		strings.Repeat("x", 500),
		"../../etc/passwd",
		"user@evil@host",
		"\x00\x01\x02",
	}

	for _, h := range hostile {
		t.Run(h[:min(len(h), 12)], func(t *testing.T) {
			user := sanitizeLabelPart(h, maxDeviceLabelUser)
			host := sanitizeLabelPart(h, maxDeviceLabelHost)

			for _, part := range []string{user, host} {
				if part == "" {
					continue
				}
				assert.Regexp(t, `^[A-Za-z0-9._-]+$`, part)
				assert.NotContains(t, part, "@")
				assert.NotContains(t, part, "\r")
				assert.NotContains(t, part, "\n")
			}

			assert.LessOrEqual(t, utf8.RuneCountInString(user), maxDeviceLabelUser)
			assert.LessOrEqual(t, utf8.RuneCountInString(host), maxDeviceLabelHost)

			if user != "" && host != "" {
				composed := user + "@" + host
				assert.Regexp(t, deviceLabelShape, composed)
				assert.Equal(t, 1, strings.Count(composed, "@"))
				assert.LessOrEqual(t, utf8.RuneCountInString(composed),
					maxDeviceLabelUser+1+maxDeviceLabelHost)
			}
		})
	}
}
