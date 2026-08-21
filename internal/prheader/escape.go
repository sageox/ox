package prheader

import "strings"

// htmlEscaper covers the five HTML/XML predefined entities. Order matters: '&'
// must be replaced first or a literal "<" would become "&amp;lt;".
// strings.Replacer applies its pairs in a single left-to-right scan (not
// sequential passes), so this is correct as written — '&' is still listed first
// for a human reader's sake. Escaping both quote styles keeps the same function
// safe for attribute values (href, srcset, alt) and text content alike.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

// escapeHTML makes untrusted content (a team name, a config-derived URL) safe to
// place in either an attribute value or text content: a team name of
// `</b><script>alert(1)</script>` becomes inert text, never parsed markup.
func escapeHTML(s string) string { return htmlEscaper.Replace(s) }

// noWrap replaces spaces in ALREADY-ESCAPED text with non-breaking spaces so a
// multi-word token ("SageOx Internal", "prior art") never wraps mid-token,
// keeping the credit line clean. Run it AFTER escapeHTML — the HTML escaper does
// not touch spaces, so order is safe either way, but escaping first keeps the
// entity `&nbsp;` from being double-escaped.
func noWrap(escaped string) string { return strings.ReplaceAll(escaped, " ", "&nbsp;") }
