package kb

// parse.go — input-parsing helpers for KB slug arguments received from the
// CLI. Storage and JSON shapes intentionally stay bare ("marketing"); the
// "#" prefix is purely a human-display convention. NormalizeSlugArg lets
// users type `#marketing` or `marketing` interchangeably without leaking the
// prefix into resolvers, paths, or wire formats.

import "strings"

// NormalizeSlugArg strips a leading "#" from a user-supplied slug argument.
// Use on input received from CLI argv before resolving the slug.
// "#marketing" -> "marketing"; "marketing" -> "marketing"; "" -> "".
//
// Only one leading "#" is stripped — "##weird" becomes "#weird". That is
// intentional: a double-prefix is almost certainly a typo we'd rather
// surface as a "kb not found" error than silently coerce away.
func NormalizeSlugArg(s string) string {
	return strings.TrimPrefix(s, "#")
}
