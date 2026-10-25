package githubmirror

import (
	"fmt"
	"strings"
)

// maxSlugLen mirrors the bulletin board's slug rule ^[a-z0-9][a-z0-9-]{0,79}$.
const maxSlugLen = 80

// SourceKey is the server's identity for one item:
// "github.com/{owner}/{name}/pull/{n}" or "github.com/{owner}/{name}/issues/{n}",
// owner and name lowercased.
func SourceKey(owner, name, kind string, number int) string {
	segment := "issues"
	if kind == KindPullRequest {
		segment = "pull"
	}
	return fmt.Sprintf("github.com/%s/%s/%s/%d", strings.ToLower(owner), strings.ToLower(name), segment, number)
}

// Slug is the post slug "{owner}-{name}-{pr|issue}-{n}": lowercased, every run
// of characters outside [a-z0-9] collapsed to one '-', at most 80 characters
// (the owner-name prefix is truncated, never the "-pr-{n}" suffix). It is for
// people browsing the folder; the server groups posts by SourceKey.
func Slug(owner, name, kind string, number int) string {
	short := "issue"
	if kind == KindPullRequest {
		short = "pr"
	}
	suffix := fmt.Sprintf("%s-%d", short, number)
	prefix := slugify(owner + "-" + name)
	if budget := maxSlugLen - len(suffix) - 1; len(prefix) > budget {
		prefix = strings.TrimRight(prefix[:budget], "-")
	}
	if prefix == "" {
		return suffix
	}
	return prefix + "-" + suffix
}

// maxInt64Digits is the length of the longest item number Slug can be given
// (math.MaxInt64 has 19 digits).
const maxInt64Digits = 19

// slugPrefixBudget is how much of the "{owner}-{name}" stem survives in every
// slug, whatever the kind and item number: Slug gives the stem
// maxSlugLen - len("{pr|issue}-{n}") - 1, which is smallest for the longest
// suffix, "issue-" plus a 19-digit number.
const slugPrefixBudget = maxSlugLen - len("issue-") - maxInt64Digits - 1

// SlugPrefix is a string every slug of the repo starts with, for any kind and
// any item number: a file-name prefilter, never a full identity (source_key
// decides). A name that fits is "{owner}-{name}-". A name too long to always
// fit is cut to the part Slug keeps for every item number, with no dash
// appended, because the stem ends there and the slug may continue with more
// of it. "" means the owner and name have no slug-worthy characters.
func SlugPrefix(owner, name string) string {
	stem := slugify(owner + "-" + name)
	if stem == "" {
		return ""
	}
	if len(stem) <= slugPrefixBudget {
		return stem + "-"
	}
	// slugify output is ASCII, so cutting by byte is safe. The cut may end in
	// a dash. That is still a prefix: where Slug trims a dash off its own cut,
	// it puts one back as the separator before the suffix.
	return stem[:slugPrefixBudget]
}

// slugify lowercases s and collapses every run of characters outside
// [a-z0-9] into a single '-', trimming dashes at both ends.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// IsBot reports whether a GitHub account is a bot: Type "Bot" or a login
// ending in "[bot]".
func IsBot(a Author) bool {
	return strings.EqualFold(a.Type, "Bot") || strings.HasSuffix(strings.ToLower(a.Login), "[bot]")
}

// TrustOf maps an author to TrustBot, TrustMember (OWNER, MEMBER,
// COLLABORATOR) or TrustExternal (everything else, including unknown).
func TrustOf(a Author) string {
	if IsBot(a) {
		return TrustBot
	}
	switch strings.ToUpper(a.Association) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return TrustMember
	default:
		return TrustExternal
	}
}
