// Package uri parses and encodes sageox:// citation URIs (ADR-121 plus
// Amendments 1 and 2).
//
// Grammar:
//
//	sageox://cnv_<uuidv7>[/clyr_<uuidv7>[@<revision>]][#<selector>[&<selector>...]]
//
// Selectors are key=value pairs carried in the fragment. Known keys:
//
//	t=    an instant or a closed range ("A--B") on the recording clock.
//	      Canonical spelling is epoch-milliseconds; RFC 3339 timestamps stay
//	      read-compatible forever (pre-Amendment-2 citations are live data).
//	      Sub-millisecond fractions truncate to the millisecond. Values are
//	      bounded to the JavaScript Date range (|ms| <= 8.64e15, <= 16 digits).
//	cue=  a 1-based cue ordinal or inclusive ordinal range ("N-M") into the
//	      layer's transcript revision.
//	turn= parsed for compatibility; ox never authors turn selectors.
//
// Unknown selector keys are carried through un-fatally and survive a
// decode/encode round trip. Canonical encoding orders selectors t, cue, turn,
// then unknown keys in their original order, and always spells t= in epoch-ms.
//
// The package is dependency-free by design (stdlib only): citation URIs arrive
// inside untrusted, customer-writable team-context content, so every field is
// strictly validated before use.
package uri

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Grammar bounds (ADR-121 Amendment 1).
const (
	// MaxURILength is the maximum byte length of a citation URI.
	MaxURILength = 2048
	// MaxSelectors is the maximum number of key=value selector pairs.
	MaxSelectors = 16
	// MaxSelectorValueLength is the maximum byte length of one selector value.
	MaxSelectorValueLength = 256
	// MaxOrdinalDigits bounds cue/turn ordinals and revision numbers.
	MaxOrdinalDigits = 9
	// MaxEpochMillis is the JavaScript Date bound: |ms| <= 8.64e15.
	MaxEpochMillis = int64(8_640_000_000_000_000)
	// MaxEpochDigits bounds the digit count of an epoch-ms value.
	MaxEpochDigits = 16
)

// Scheme is the citation URI scheme, including the "://" separator.
const Scheme = "sageox://"

// Typed parse failures. All errors returned by Parse wrap exactly one of
// these; match with errors.Is.
var (
	ErrTooLong           = errors.New("citation URI exceeds maximum length")
	ErrScheme            = errors.New("citation URI must start with sageox://")
	ErrConversationID    = errors.New("invalid conversation id")
	ErrLayerID           = errors.New("invalid layer id")
	ErrRevision          = errors.New("invalid revision")
	ErrSelector          = errors.New("invalid selector")
	ErrDuplicateSelector = errors.New("duplicate selector key")
	ErrTooManySelectors  = errors.New("too many selectors")
	ErrReversedRange     = errors.New("reversed range")
	ErrTimeBounds        = errors.New("timestamp outside JavaScript Date bounds")
)

// Address is a parsed sageox:// citation.
type Address struct {
	// Conversation is the full conversation id ("cnv_<uuidv7>"). Always set.
	Conversation string
	// Layer is the full layer id ("clyr_<uuidv7>"), or "" when the citation
	// addresses the conversation alone.
	Layer string
	// Revision is the layer revision pin (>= 1), or 0 when unpinned.
	// A revision is only legal when Layer is set.
	Revision int
	// Selectors holds the fragment selectors, if any.
	Selectors Selectors
}

// Selectors holds the recognized and unknown fragment selectors of a citation.
type Selectors struct {
	// Time is the t= selector, or nil.
	Time *TimeSelector
	// Cue is the cue= selector, or nil.
	Cue *OrdinalRange
	// Turn is the turn= selector, or nil. Parsed for compatibility; ox never
	// authors turn selectors.
	Turn *OrdinalRange
	// Unknown carries unrecognized selector keys in input order. They are
	// preserved verbatim and survive a decode/encode round trip.
	Unknown []Selector
}

// IsZero reports whether no selector of any kind is present.
func (s Selectors) IsZero() bool {
	return s.Time == nil && s.Cue == nil && s.Turn == nil && len(s.Unknown) == 0
}

// Selector is one unrecognized key=value fragment pair, preserved verbatim.
type Selector struct {
	Key   string
	Value string
}

// TimeSelector is a t= selector: an instant or a closed range on the
// recording clock, normalized to epoch-milliseconds UTC.
type TimeSelector struct {
	// StartMS is the instant, or the range start, in epoch-milliseconds.
	StartMS int64
	// EndMS equals StartMS for an instant; for a range it is the inclusive
	// end and is >= StartMS.
	EndMS int64
	// IsRange distinguishes a closed range from an instant.
	IsRange bool
}

// OrdinalRange is a cue= or turn= selector: a 1-based ordinal or inclusive
// ordinal range.
type OrdinalRange struct {
	// From is the ordinal, or the range start. Always >= 1.
	From int64
	// To equals From for a single ordinal; for a range it is the inclusive
	// end and is >= From.
	To int64
	// IsRange distinguishes an explicit range from a single ordinal.
	IsRange bool
}

// Parse decodes a sageox:// citation URI into an Address. Input is untrusted:
// every id is validated as prefix + UUIDv7, all grammar bounds are enforced,
// and both t= spellings (epoch-ms and RFC 3339) are accepted.
func Parse(raw string) (*Address, error) {
	if len(raw) > MaxURILength {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrTooLong, len(raw), MaxURILength)
	}
	if !strings.HasPrefix(raw, Scheme) {
		return nil, fmt.Errorf("%w: %q", ErrScheme, truncateForError(raw))
	}
	rest := raw[len(Scheme):]

	var fragment string
	hasFragment := false
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest, fragment = rest[:i], rest[i+1:]
		hasFragment = true
	}

	addr := &Address{}
	path := rest
	layerPart := ""
	hasLayer := false
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path, layerPart = path[:i], path[i+1:]
		hasLayer = true
	}
	if !isValidPrefixedUUIDv7(path, "cnv_") {
		return nil, fmt.Errorf("%w: %q", ErrConversationID, truncateForError(path))
	}
	addr.Conversation = path
	if hasLayer {
		if err := parseLayer(layerPart, addr); err != nil {
			return nil, err
		}
	}

	if hasFragment {
		sels, err := parseSelectors(fragment)
		if err != nil {
			return nil, err
		}
		addr.Selectors = sels
	}
	return addr, nil
}

// parseLayer parses "clyr_<uuidv7>[@<revision>]" into addr.
func parseLayer(s string, addr *Address) error {
	layer := s
	if i := strings.IndexByte(s, '@'); i >= 0 {
		layer = s[:i]
		revStr := s[i+1:]
		rev, err := parseCanonicalUint(revStr)
		if err != nil {
			return fmt.Errorf("%w: %q: %w", ErrRevision, truncateForError(revStr), err)
		}
		if rev == 0 {
			return fmt.Errorf("%w: revision 0 is not addressable", ErrRevision)
		}
		addr.Revision = int(rev)
	}
	if !isValidPrefixedUUIDv7(layer, "clyr_") {
		return fmt.Errorf("%w: %q", ErrLayerID, truncateForError(layer))
	}
	addr.Layer = layer
	return nil
}

// parseSelectors parses the fragment ("key=value&key=value...").
func parseSelectors(fragment string) (Selectors, error) {
	var sels Selectors
	if fragment == "" {
		return sels, fmt.Errorf("%w: empty fragment", ErrSelector)
	}
	pairs := strings.Split(fragment, "&")
	if len(pairs) > MaxSelectors {
		return sels, fmt.Errorf("%w: %d (max %d)", ErrTooManySelectors, len(pairs), MaxSelectors)
	}
	seen := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			return Selectors{}, fmt.Errorf("%w: %q is not key=value", ErrSelector, truncateForError(pair))
		}
		key, value := pair[:eq], pair[eq+1:]
		if seen[key] {
			return Selectors{}, fmt.Errorf("%w: %q", ErrDuplicateSelector, key)
		}
		seen[key] = true
		if len(value) > MaxSelectorValueLength {
			return Selectors{}, fmt.Errorf("%w: %q value is %d bytes (max %d)", ErrSelector, key, len(value), MaxSelectorValueLength)
		}
		switch key {
		case "t":
			ts, err := parseTimeSelector(value)
			if err != nil {
				return Selectors{}, err
			}
			sels.Time = ts
		case "cue":
			or, err := parseOrdinalRange(key, value)
			if err != nil {
				return Selectors{}, err
			}
			sels.Cue = or
		case "turn":
			or, err := parseOrdinalRange(key, value)
			if err != nil {
				return Selectors{}, err
			}
			sels.Turn = or
		default:
			sels.Unknown = append(sels.Unknown, Selector{Key: key, Value: value})
		}
	}
	return sels, nil
}

// parseTimeSelector parses a t= value: an instant or a closed "A--B" range,
// each side spelled either as epoch-milliseconds (canonical) or RFC 3339
// (read-forever compatibility), discriminated lexically.
func parseTimeSelector(value string) (*TimeSelector, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: t= value is empty", ErrSelector)
	}
	// Range split: "--" never occurs inside a valid scalar (RFC 3339 uses
	// single hyphens; an epoch-ms scalar has at most one leading '-'), so try
	// each occurrence and require both sides to parse.
	for i := 0; i+1 < len(value); i++ {
		if value[i] != '-' || value[i+1] != '-' {
			continue
		}
		start, errA := parseTimeScalar(value[:i])
		end, errB := parseTimeScalar(value[i+2:])
		if errA != nil || errB != nil {
			continue
		}
		if end < start {
			return nil, fmt.Errorf("%w: t=%s ends before it starts", ErrReversedRange, truncateForError(value))
		}
		return &TimeSelector{StartMS: start, EndMS: end, IsRange: true}, nil
	}
	ms, err := parseTimeScalar(value)
	if err != nil {
		return nil, err
	}
	return &TimeSelector{StartMS: ms, EndMS: ms}, nil
}

// parseTimeScalar parses one timestamp in either spelling into epoch-ms.
func parseTimeScalar(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: empty timestamp", ErrSelector)
	}
	if isEpochLexeme(s) {
		return parseEpochMillis(s)
	}
	// RFC 3339 (Amendment 2: read-compatible forever). Sub-millisecond
	// fractions truncate to the millisecond.
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is neither epoch-ms nor RFC 3339", ErrSelector, truncateForError(s))
	}
	ms := t.UnixMilli()
	if ms > MaxEpochMillis || ms < -MaxEpochMillis {
		return 0, fmt.Errorf("%w: %q", ErrTimeBounds, truncateForError(s))
	}
	return ms, nil
}

// isEpochLexeme reports whether s is lexically an epoch-ms literal
// (an optional leading '-' followed by digits only).
func isEpochLexeme(s string) bool {
	body := strings.TrimPrefix(s, "-")
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return false
		}
	}
	return true
}

// parseEpochMillis parses a canonical epoch-ms literal, enforcing the
// JavaScript Date bounds (|ms| <= 8.64e15, <= 16 digits, no leading zeros,
// no negative zero).
func parseEpochMillis(s string) (int64, error) {
	digits := strings.TrimPrefix(s, "-")
	if len(digits) > MaxEpochDigits {
		return 0, fmt.Errorf("%w: %q has more than %d digits", ErrTimeBounds, truncateForError(s), MaxEpochDigits)
	}
	if len(digits) > 1 && digits[0] == '0' {
		return 0, fmt.Errorf("%w: %q has leading zeros", ErrSelector, truncateForError(s))
	}
	if s == "-0" {
		return 0, fmt.Errorf("%w: negative zero", ErrSelector)
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrSelector, truncateForError(s), err)
	}
	if ms > MaxEpochMillis || ms < -MaxEpochMillis {
		return 0, fmt.Errorf("%w: %q", ErrTimeBounds, truncateForError(s))
	}
	return ms, nil
}

// parseOrdinalRange parses a cue= or turn= value: a 1-based ordinal or an
// inclusive "N-M" range.
func parseOrdinalRange(key, value string) (*OrdinalRange, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: %s= value is empty", ErrSelector, key)
	}
	if i := strings.IndexByte(value, '-'); i >= 0 {
		from, errA := parseOrdinal(key, value[:i])
		if errA != nil {
			return nil, errA
		}
		to, errB := parseOrdinal(key, value[i+1:])
		if errB != nil {
			return nil, errB
		}
		if to < from {
			return nil, fmt.Errorf("%w: %s=%s ends before it starts", ErrReversedRange, key, value)
		}
		return &OrdinalRange{From: from, To: to, IsRange: true}, nil
	}
	n, err := parseOrdinal(key, value)
	if err != nil {
		return nil, err
	}
	return &OrdinalRange{From: n, To: n}, nil
}

// parseOrdinal parses one 1-based ordinal (>= 1, <= 9 digits, canonical
// decimal).
func parseOrdinal(key, s string) (int64, error) {
	n, err := parseCanonicalUint(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %w", ErrSelector, key, truncateForError(s), err)
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: %s ordinals are 1-based; 0 is not addressable", ErrSelector, key)
	}
	return n, nil
}

// parseCanonicalUint parses a canonical non-negative decimal: digits only, no
// sign, no leading zeros, at most MaxOrdinalDigits digits.
func parseCanonicalUint(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	if len(s) > MaxOrdinalDigits {
		return 0, fmt.Errorf("more than %d digits", MaxOrdinalDigits)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, errors.New("leading zeros")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("not a decimal number")
		}
	}
	// bitSize 32: the MaxOrdinalDigits cap already bounds values to
	// 999,999,999 < 2^31-1, so callers may narrow to int safely on any
	// platform.
	return strconv.ParseInt(s, 10, 32)
}

// Encode renders the canonical spelling of the address: selectors ordered
// t, cue, turn, then unknown keys in original order; t= in epoch-ms only;
// ranges ascending. decode(encode(a)) is the identity for any valid Address.
func (a *Address) Encode() string {
	var b strings.Builder
	b.WriteString(Scheme)
	b.WriteString(a.Conversation)
	if a.Layer != "" {
		b.WriteByte('/')
		b.WriteString(a.Layer)
		if a.Revision > 0 {
			b.WriteByte('@')
			b.WriteString(strconv.Itoa(a.Revision))
		}
	}
	if a.Selectors.IsZero() {
		return b.String()
	}
	b.WriteByte('#')
	first := true
	sep := func() {
		if !first {
			b.WriteByte('&')
		}
		first = false
	}
	if t := a.Selectors.Time; t != nil {
		sep()
		b.WriteString("t=")
		b.WriteString(strconv.FormatInt(t.StartMS, 10))
		if t.IsRange {
			b.WriteString("--")
			b.WriteString(strconv.FormatInt(t.EndMS, 10))
		}
	}
	if c := a.Selectors.Cue; c != nil {
		sep()
		b.WriteString("cue=")
		writeOrdinalRange(&b, c)
	}
	if tr := a.Selectors.Turn; tr != nil {
		sep()
		b.WriteString("turn=")
		writeOrdinalRange(&b, tr)
	}
	for _, u := range a.Selectors.Unknown {
		sep()
		b.WriteString(u.Key)
		b.WriteByte('=')
		b.WriteString(u.Value)
	}
	return b.String()
}

func writeOrdinalRange(b *strings.Builder, r *OrdinalRange) {
	b.WriteString(strconv.FormatInt(r.From, 10))
	if r.IsRange {
		b.WriteByte('-')
		b.WriteString(strconv.FormatInt(r.To, 10))
	}
}

// isValidPrefixedUUIDv7 reports whether s is prefix immediately followed by a
// canonical lowercase UUIDv7 (version nibble 7, RFC 4122 variant).
func isValidPrefixedUUIDv7(s, prefix string) bool {
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	return isUUIDv7(s[len(prefix):])
}

// isUUIDv7 validates a canonical lowercase UUIDv7:
// xxxxxxxx-xxxx-7xxx-Nxxx-xxxxxxxxxxxx with N in [89ab].
func isUUIDv7(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '7' {
				return false
			}
		case 19:
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return false
			}
		default:
			if !isLowerHex(c) {
				return false
			}
		}
	}
	return true
}

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// truncateForError bounds untrusted input reproduced in error messages.
func truncateForError(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
