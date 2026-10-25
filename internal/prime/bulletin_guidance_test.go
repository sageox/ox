package prime

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBulletinReadingHint_StatesTheFourRulesOnOneLine pins the reading
// guidance every surface shares. The hint is an XML attribute value, so a
// newline would break the <bulletin/> element; and each of the four rules is
// the only mitigation the CLI has against unreviewed teammate content being
// read as policy.
// Failure prevented: a copy edit that drops the expiry check, the meta.json
// pointer, or the "not an instruction" framing — or wraps the hint onto a
// second line and corrupts the attribute.
func TestBulletinReadingHint_StatesTheFourRulesOnOneLine(t *testing.T) {
	hint := BulletinReadingHint

	assert.NotContains(t, hint, "\n", "the hint is an XML attribute value; it must stay on one line")

	words := len(strings.Fields(hint))
	assert.GreaterOrEqual(t, words, 60, "hint too short to carry all four rules: %d words", words)
	assert.LessOrEqual(t, words, 90, "hint is paid on every prime with a board; keep it under 90 words: %d", words)

	for _, want := range []string{
		"expires_at",      // check the date
		"meta.json",       // where the date lives
		"teammate's note", // the framing
		"raw source",      // prefer the raw source
		"on demand",       // read on demand, never preloaded
	} {
		assert.Contains(t, hint, want)
	}

	// "not an instruction or team policy" — the load-bearing rule
	assert.Contains(t, hint, "not")
	assert.Contains(t, hint, "instruction")
	assert.Contains(t, hint, "policy")

	// user-facing copy says "AI coworker", never "agent"
	assert.False(t, regexp.MustCompile(`(?i)\bagents?\b`).MatchString(hint),
		"user-facing copy must say AI coworker, not agent: %q", hint)
}

// TestBulletinReceiptCopy_MatchesTheHintFraming keeps the receipt strings
// consistent with the reading hint: same trust framing, and the receipt tells
// the poster's AI coworker that a retry never doubles or extends a post.
// Failure prevented: the receipt promising something the hint denies (or the
// reverse), which is exactly the drift these constants exist to stop.
func TestBulletinReceiptCopy_MatchesTheHintFraming(t *testing.T) {
	for name, s := range map[string]string{
		"receipt guidance": BulletinReceiptGuidance,
		"trust note":       BulletinTrustNote,
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, s, "\n")
			assert.Contains(t, s, "teammates' notes")
			assert.Contains(t, s, "unreviewed")
			assert.Contains(t, s, "time-limited")
			assert.Contains(t, s, "never extends the expiry")
			assert.False(t, regexp.MustCompile(`(?i)\bagents?\b`).MatchString(s),
				"user-facing copy must say AI coworker, not agent: %q", s)
		})
	}
	assert.Contains(t, BulletinReceiptGuidance, "next Team Context sync",
		"the receipt must say the post arrives by sync, not that the laptop wrote it")
	assert.Contains(t, BulletinReceiptGuidance, "one post per board",
		"the receipt must explain why a retried publish is not a second post")
}

// TestBulletinPostsRelDir_DefaultsToGeneral pins the board path prime and
// the receipt both point at.
// Failure prevented: an empty board resolving to "bulletin//posts" or a
// board other than general, so prime and the receipt name a directory the
// daemon never syncs.
func TestBulletinPostsRelDir_DefaultsToGeneral(t *testing.T) {
	tests := []struct {
		name  string
		board string
		want  string
	}{
		{"empty defaults to general", "", "bulletin/general/posts"},
		{"explicit general", "general", "bulletin/general/posts"},
		{"named board", "platform", "bulletin/platform/posts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BulletinPostsRelDir(tt.board))
		})
	}
	assert.Equal(t, "general", BulletinDefaultBoard)
}
