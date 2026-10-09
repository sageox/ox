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

// SlugPrefix is the "{owner}-{name}-" prefix every slug for the repo starts
// with (before any truncation a very long owner/name would force).
func SlugPrefix(owner, name string) string {
	return slugify(owner+"-"+name) + "-"
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
