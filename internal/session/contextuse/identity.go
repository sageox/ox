package contextuse

import (
	"net/url"
	"regexp"
	"strings"
)

// One SageOx item travels under several names: a conversation is cnv_<u>,
// its rec_<u> twin (same uuid, prefix swap), a sageox:// citation, or a
// https://<host>/c/<id> link; a plan is pln_<id>, its slug, or its dated
// directory. This file maps every name to one canonical ref so a use recorded
// under one name attaches to the item surfaced under another. It is pure and
// offline (stdlib only) so any reader — contextuse, oxactivity, consultscan —
// can share it without pulling in the conversation reader.

// uuidPattern is a hyphenated uuid of any version, either hex case.
const uuidPattern = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

// ConversationIDPattern finds a conversation id anywhere in a string: cnv_
// and rec_ (one uuid, two prefixes) and the legacy dsc_ discussion id seen in
// old sageox:// citations. Submatch 1 is the prefix, submatch 2 the uuid. It
// is unanchored, for scanning arguments and links; use FindConversationID to
// get the canonical form.
var ConversationIDPattern = regexp.MustCompile(`\b(cnv|rec|dsc)_(` + uuidPattern + `)\b`)

// exactConversationID matches a string that is a conversation id and nothing
// else.
var exactConversationID = regexp.MustCompile(`^(cnv|rec|dsc)_(` + uuidPattern + `)$`)

// sageoxScheme is the citation URI scheme (internal/conversation/uri.Scheme).
const sageoxScheme = "sageox://"

// CanonicalRef returns the one name every spelling of an item maps to. It is
// pure and does no I/O.
//
// Conversation names become cnv_<uuid> with the uuid lowercased — whatever
// refType says, because ref_type is sometimes missing:
//
//	cnv_<u>, rec_<u>
//	sageox://cnv_<u>[/clyr_<u>[@<rev>]][#<selectors>]
//	sageox://team_<t>/dsc_<u>[@<rev>]           legacy; stays dsc_<u>
//	http(s)://<any host>/c/{cnv_|rec_}<u>[/...][?...][#...]
//	http(s)://<any host>/team/<t>/media/recordings/{cnv_|rec_}<u>[/...]
//	http(s)://<any host>/kb/<kb>/recordings/{cnv_|rec_}<u>[/...]
//
// Everything else is trimmed of surrounding space and trailing slashes and
// otherwise returned unchanged: case is preserved (compare with SameItem,
// which folds case), and a pln_ id and its slug stay distinct names that
// only an alias joins.
func CanonicalRef(refType, ref string) string {
	_ = refType // reserved: identity is decided by the ref's own shape today
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if id, ok := canonicalConversationID(ref); ok {
		return id
	}
	if strings.HasPrefix(ref, sageoxScheme) {
		if id, ok := conversationFromURI(ref[len(sageoxScheme):]); ok {
			return id
		}
	}
	if id, ok := conversationFromLink(ref); ok {
		return id
	}
	return strings.TrimRight(ref, "/")
}

// FindConversationID returns the canonical form of the first conversation id
// embedded anywhere in s (see ConversationIDPattern), or "" when there is
// none.
func FindConversationID(s string) string {
	m := ConversationIDPattern.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return canonicalPrefix(m[1]) + strings.ToLower(m[2])
}

// SameItem reports whether a and b name the same item: CanonicalRef(a) equals,
// ignoring case, CanonicalRef(b) or the canonical form of one of b's aliases.
// An empty a names nothing and matches nothing.
func SameItem(refType, a, b string, aliases ...string) bool {
	ca := CanonicalRef(refType, a)
	if ca == "" {
		return false
	}
	if strings.EqualFold(ca, CanonicalRef(refType, b)) {
		return true
	}
	for _, al := range aliases {
		if strings.EqualFold(ca, CanonicalRef(refType, al)) {
			return true
		}
	}
	return false
}

// Names returns every canonical name of the surfaced item: its canonical ref
// first, then its canonical aliases, deduped ignoring case, empties dropped.
func (s SurfacedRef) Names() []string {
	return canonicalNames(s.RefType, s.Ref, s.Aliases)
}

// Matches reports whether ref names this surfaced item.
func (s SurfacedRef) Matches(ref string) bool {
	return SameItem(s.RefType, ref, s.Ref, s.Aliases...)
}

// canonicalNames is Names for a bare ref and its aliases.
func canonicalNames(refType, ref string, aliases []string) []string {
	out := make([]string, 0, 1+len(aliases))
	seen := map[string]bool{}
	for _, n := range append([]string{ref}, aliases...) {
		c := CanonicalRef(refType, n)
		k := strings.ToLower(c)
		if c == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// canonicalConversationID maps an exact cnv_/rec_/dsc_ id to its canonical
// form.
func canonicalConversationID(s string) (string, bool) {
	m := exactConversationID.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return canonicalPrefix(m[1]) + strings.ToLower(m[2]), true
}

// canonicalPrefix folds rec_ onto its cnv_ twin; dsc_ is a distinct legacy id.
func canonicalPrefix(p string) string {
	if p == "rec" {
		return "cnv_"
	}
	return p + "_"
}

// conversationFromURI reads the conversation out of a sageox:// address
// (scheme already stripped): the first path segment, or for the legacy
// team-scoped form the dsc_ segment after team_<t>, with any @revision,
// query, or #selectors ignored.
func conversationFromURI(rest string) (string, bool) {
	if i := strings.IndexAny(rest, "#?"); i >= 0 {
		rest = rest[:i]
	}
	segs := strings.Split(rest, "/")
	first := segs[0]
	if strings.HasPrefix(first, "team_") && len(segs) >= 2 {
		first = segs[1]
	}
	if i := strings.IndexByte(first, '@'); i >= 0 {
		first = first[:i]
	}
	return canonicalConversationID(first)
}

// conversationFromLink reads the conversation out of an http(s) recording
// link on any host. Only the path is consulted; the segment that names the
// recording must itself be an exact cnv_/rec_ id.
func conversationFromLink(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	var candidate string
	switch {
	case len(segs) >= 2 && segs[0] == "c":
		candidate = segs[1]
	case len(segs) >= 5 && segs[0] == "team" && segs[2] == "media" && segs[3] == "recordings":
		candidate = segs[4]
	case len(segs) >= 4 && segs[0] == "kb" && segs[2] == "recordings":
		candidate = segs[3]
	default:
		return "", false
	}
	if strings.HasPrefix(candidate, "dsc_") {
		return "", false // links name recordings; dsc_ is citation-only
	}
	return canonicalConversationID(candidate)
}
