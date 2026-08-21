package uri

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	cnv  = "cnv_019c6cfb-f24e-7202-a479-cc147df6319e"
	clyr = "clyr_019f66b8-d99e-7a7a-b32b-3da93f9d54f2"
)

func addr(mutate func(*Address)) *Address {
	a := &Address{Conversation: cnv}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want *Address
	}{
		{
			name: "conversation only",
			in:   "sageox://" + cnv,
			want: addr(nil),
		},
		{
			name: "conversation and layer",
			in:   "sageox://" + cnv + "/" + clyr,
			want: addr(func(a *Address) { a.Layer = clyr }),
		},
		{
			name: "layer with revision pin",
			in:   "sageox://" + cnv + "/" + clyr + "@4",
			want: addr(func(a *Address) { a.Layer = clyr; a.Revision = 4 }),
		},
		{
			name: "epoch-ms instant",
			in:   "sageox://" + cnv + "/" + clyr + "@4#t=1765432100000",
			want: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 4
				a.Selectors.Time = &TimeSelector{StartMS: 1765432100000, EndMS: 1765432100000}
			}),
		},
		{
			name: "epoch-ms range",
			in:   "sageox://" + cnv + "/" + clyr + "@4#t=1765432100000--1765432200000",
			want: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 4
				a.Selectors.Time = &TimeSelector{StartMS: 1765432100000, EndMS: 1765432200000, IsRange: true}
			}),
		},
		{
			name: "epoch-ms negative instant (pre-1970)",
			in:   "sageox://" + cnv + "#t=-1000",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: -1000, EndMS: -1000}
			}),
		},
		{
			name: "epoch-ms range with negative endpoints",
			in:   "sageox://" + cnv + "#t=-5000---3000",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: -5000, EndMS: -3000, IsRange: true}
			}),
		},
		{
			name: "epoch-ms at positive JS-Date bound",
			in:   "sageox://" + cnv + "#t=8640000000000000",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: MaxEpochMillis, EndMS: MaxEpochMillis}
			}),
		},
		{
			name: "epoch-ms at negative JS-Date bound",
			in:   "sageox://" + cnv + "#t=-8640000000000000",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: -MaxEpochMillis, EndMS: -MaxEpochMillis}
			}),
		},
		{
			name: "RFC 3339 instant reads forever",
			in:   "sageox://" + cnv + "#t=2026-02-17T19:03:29Z",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: 1771355009000, EndMS: 1771355009000}
			}),
		},
		{
			name: "RFC 3339 range in the wild-corpus spelling",
			in:   "sageox://" + cnv + "/" + clyr + "@4#t=2026-02-17T19:03:29.10728Z--2026-02-17T19:03:34.30728Z",
			want: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 4
				// Sub-millisecond fractions truncate to the millisecond.
				a.Selectors.Time = &TimeSelector{StartMS: 1771355009107, EndMS: 1771355014307, IsRange: true}
			}),
		},
		{
			name: "RFC 3339 with numeric offset normalizes to UTC epoch",
			in:   "sageox://" + cnv + "#t=2026-02-17T20:03:29+01:00",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: 1771355009000, EndMS: 1771355009000}
			}),
		},
		{
			name: "mixed-spelling range (RFC start, epoch end)",
			in:   "sageox://" + cnv + "#t=2026-02-17T19:03:29Z--1771355014307",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: 1771355009000, EndMS: 1771355014307, IsRange: true}
			}),
		},
		{
			name: "single cue ordinal",
			in:   "sageox://" + cnv + "/" + clyr + "#cue=12",
			want: addr(func(a *Address) {
				a.Layer = clyr
				a.Selectors.Cue = &OrdinalRange{From: 12, To: 12}
			}),
		},
		{
			name: "cue range is 1-based inclusive",
			in:   "sageox://" + cnv + "/" + clyr + "@2#cue=12-16",
			want: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 2
				a.Selectors.Cue = &OrdinalRange{From: 12, To: 16, IsRange: true}
			}),
		},
		{
			name: "degenerate cue range N-N is legal",
			in:   "sageox://" + cnv + "#cue=7-7",
			want: addr(func(a *Address) {
				a.Selectors.Cue = &OrdinalRange{From: 7, To: 7, IsRange: true}
			}),
		},
		{
			name: "cue ordinal at the 9-digit bound",
			in:   "sageox://" + cnv + "#cue=999999999",
			want: addr(func(a *Address) {
				a.Selectors.Cue = &OrdinalRange{From: 999999999, To: 999999999}
			}),
		},
		{
			name: "turn parses (compatibility)",
			in:   "sageox://" + cnv + "#turn=3-5",
			want: addr(func(a *Address) {
				a.Selectors.Turn = &OrdinalRange{From: 3, To: 5, IsRange: true}
			}),
		},
		{
			name: "t and cue together",
			in:   "sageox://" + cnv + "/" + clyr + "@4#t=100--200&cue=12-16",
			want: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 4
				a.Selectors.Time = &TimeSelector{StartMS: 100, EndMS: 200, IsRange: true}
				a.Selectors.Cue = &OrdinalRange{From: 12, To: 16, IsRange: true}
			}),
		},
		{
			name: "unknown selector passes through",
			in:   "sageox://" + cnv + "/" + clyr + "#topic=tp_01a017b1-f1f0-7ad3-9bd1-a5be4b557d61",
			want: addr(func(a *Address) {
				a.Layer = clyr
				a.Selectors.Unknown = []Selector{{Key: "topic", Value: "tp_01a017b1-f1f0-7ad3-9bd1-a5be4b557d61"}}
			}),
		},
		{
			name: "unknown selector with empty value passes through",
			in:   "sageox://" + cnv + "#flag=",
			want: addr(func(a *Address) {
				a.Selectors.Unknown = []Selector{{Key: "flag", Value: ""}}
			}),
		},
		{
			name: "unknown selectors keep input order around known keys",
			in:   "sageox://" + cnv + "#zeta=1&t=5&alpha=2",
			want: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: 5, EndMS: 5}
				a.Selectors.Unknown = []Selector{{Key: "zeta", Value: "1"}, {Key: "alpha", Value: "2"}}
			}),
		},
		{
			name: "revision at the 9-digit bound",
			in:   "sageox://" + cnv + "/" + clyr + "@999999999",
			want: addr(func(a *Address) { a.Layer = clyr; a.Revision = 999999999 }),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse(%q)\n got  %+v\n want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	longValue := strings.Repeat("x", MaxSelectorValueLength+1)
	manySelectors := make([]string, MaxSelectors+1)
	for i := range manySelectors {
		manySelectors[i] = "k" + string(rune('a'+i)) + "=1"
	}
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"empty string", "", ErrScheme},
		{"wrong scheme", "https://" + cnv, ErrScheme},
		{"uppercase scheme", "SAGEOX://" + cnv, ErrScheme},
		{"scheme only", "sageox://", ErrConversationID},
		{"bare uuid without prefix", "sageox://019c6cfb-f24e-7202-a479-cc147df6319e", ErrConversationID},
		{"wrong id prefix", "sageox://rec_019c6cfb-f24e-7202-a479-cc147df6319e", ErrConversationID},
		{"uuid v4 not v7", "sageox://cnv_019c6cfb-f24e-4202-a479-cc147df6319e", ErrConversationID},
		{"uppercase uuid hex", "sageox://cnv_019C6CFB-F24E-7202-A479-CC147DF6319E", ErrConversationID},
		{"truncated uuid", "sageox://cnv_019c6cfb-f24e-7202-a479", ErrConversationID},
		{"uuid bad variant nibble", "sageox://cnv_019c6cfb-f24e-7202-c479-cc147df6319e", ErrConversationID},
		{"path traversal instead of id", "sageox://../../etc/passwd", ErrConversationID},
		{"bad layer prefix", "sageox://" + cnv + "/layer_019f66b8-d99e-7a7a-b32b-3da93f9d54f2", ErrLayerID},
		{"empty layer segment", "sageox://" + cnv + "/", ErrLayerID},
		{"revision zero", "sageox://" + cnv + "/" + clyr + "@0", ErrRevision},
		{"revision empty", "sageox://" + cnv + "/" + clyr + "@", ErrRevision},
		{"revision negative", "sageox://" + cnv + "/" + clyr + "@-1", ErrRevision},
		{"revision leading zero", "sageox://" + cnv + "/" + clyr + "@04", ErrRevision},
		{"revision over 9 digits", "sageox://" + cnv + "/" + clyr + "@1000000000", ErrRevision},
		{"revision non-numeric", "sageox://" + cnv + "/" + clyr + "@two", ErrRevision},
		{"revision on conversation (no layer)", "sageox://" + cnv + "@2", ErrConversationID},
		{"empty fragment", "sageox://" + cnv + "#", ErrSelector},
		{"selector without equals", "sageox://" + cnv + "#cue", ErrSelector},
		{"selector with empty key", "sageox://" + cnv + "#=5", ErrSelector},
		{"duplicate known key", "sageox://" + cnv + "#cue=1&cue=2", ErrDuplicateSelector},
		{"duplicate unknown key", "sageox://" + cnv + "#topic=a&topic=b", ErrDuplicateSelector},
		{"too many selectors", "sageox://" + cnv + "#" + strings.Join(manySelectors, "&"), ErrTooManySelectors},
		{"selector value too long", "sageox://" + cnv + "#topic=" + longValue, ErrSelector},
		{"cue zero", "sageox://" + cnv + "#cue=0", ErrSelector},
		{"cue range from zero", "sageox://" + cnv + "#cue=0-5", ErrSelector},
		{"cue reversed range", "sageox://" + cnv + "#cue=16-12", ErrReversedRange},
		{"cue negative", "sageox://" + cnv + "#cue=-3", ErrSelector},
		{"cue leading zero", "sageox://" + cnv + "#cue=012", ErrSelector},
		{"cue over 9 digits", "sageox://" + cnv + "#cue=1000000000", ErrSelector},
		{"cue non-numeric", "sageox://" + cnv + "#cue=abc", ErrSelector},
		{"cue empty", "sageox://" + cnv + "#cue=", ErrSelector},
		{"turn zero", "sageox://" + cnv + "#turn=0", ErrSelector},
		{"turn reversed range", "sageox://" + cnv + "#turn=9-2", ErrReversedRange},
		{"t empty", "sageox://" + cnv + "#t=", ErrSelector},
		{"t garbage", "sageox://" + cnv + "#t=yesterday", ErrSelector},
		{"t epoch over JS-Date bound", "sageox://" + cnv + "#t=8640000000000001", ErrTimeBounds},
		{"t epoch under negative JS-Date bound", "sageox://" + cnv + "#t=-8640000000000001", ErrTimeBounds},
		{"t epoch over 16 digits", "sageox://" + cnv + "#t=10000000000000000", ErrTimeBounds},
		{"t epoch leading zeros", "sageox://" + cnv + "#t=0123", ErrSelector},
		{"t negative zero", "sageox://" + cnv + "#t=-0", ErrSelector},
		{"t reversed epoch range", "sageox://" + cnv + "#t=200--100", ErrReversedRange},
		{"t reversed RFC range", "sageox://" + cnv + "#t=2026-02-17T19:03:34Z--2026-02-17T19:03:29Z", ErrReversedRange},
		{"t dangling range separator", "sageox://" + cnv + "#t=100--", ErrSelector},
		{"t RFC date without time", "sageox://" + cnv + "#t=2026-02-17", ErrSelector},
		{"t RFC missing timezone", "sageox://" + cnv + "#t=2026-02-17T19:03:29", ErrSelector},
		{"uri over max length", "sageox://" + cnv + "#topic=" + strings.Repeat("y", MaxURILength), ErrTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want error %v", tt.in, got, tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Parse(%q) error = %v, want errors.Is(%v)", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestEncodeCanonical(t *testing.T) {
	tests := []struct {
		name string
		in   *Address
		want string
	}{
		{
			name: "conversation only",
			in:   addr(nil),
			want: "sageox://" + cnv,
		},
		{
			name: "revision pin",
			in:   addr(func(a *Address) { a.Layer = clyr; a.Revision = 4 }),
			want: "sageox://" + cnv + "/" + clyr + "@4",
		},
		{
			name: "selector order is t, cue, turn, unknown",
			in: addr(func(a *Address) {
				a.Layer, a.Revision = clyr, 4
				a.Selectors.Unknown = []Selector{{Key: "zeta", Value: "1"}}
				a.Selectors.Turn = &OrdinalRange{From: 2, To: 2}
				a.Selectors.Cue = &OrdinalRange{From: 12, To: 16, IsRange: true}
				a.Selectors.Time = &TimeSelector{StartMS: 100, EndMS: 200, IsRange: true}
			}),
			want: "sageox://" + cnv + "/" + clyr + "@4#t=100--200&cue=12-16&turn=2&zeta=1",
		},
		{
			name: "t always encodes as epoch-ms",
			in: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: 1771355009107, EndMS: 1771355014307, IsRange: true}
			}),
			want: "sageox://" + cnv + "#t=1771355009107--1771355014307",
		},
		{
			name: "negative epoch range",
			in: addr(func(a *Address) {
				a.Selectors.Time = &TimeSelector{StartMS: -5000, EndMS: -3000, IsRange: true}
			}),
			want: "sageox://" + cnv + "#t=-5000---3000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Encode(); got != tt.want {
				t.Fatalf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRFCToEpochCanonicalization proves the Amendment-2 migration path:
// an RFC 3339 citation decodes, and re-encoding writes epoch-ms only.
func TestRFCToEpochCanonicalization(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "instant",
			in:   "sageox://" + cnv + "#t=2026-02-17T19:03:29Z",
			want: "sageox://" + cnv + "#t=1771355009000",
		},
		{
			name: "range with sub-ms fractions truncated",
			in:   "sageox://" + cnv + "/" + clyr + "@4#t=2026-02-17T19:03:29.10728Z--2026-02-17T19:03:34.30728Z",
			want: "sageox://" + cnv + "/" + clyr + "@4#t=1771355009107--1771355014307",
		},
		{
			name: "microsecond fraction truncates, never rounds",
			in:   "sageox://" + cnv + "#t=2026-02-17T19:03:29.999999Z",
			want: "sageox://" + cnv + "#t=1771355009999",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.in, err)
			}
			if got := a.Encode(); got != tt.want {
				t.Fatalf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRoundTripIdentity: decode∘encode is the identity on canonical URIs.
func TestRoundTripIdentity(t *testing.T) {
	canonical := []string{
		"sageox://" + cnv,
		"sageox://" + cnv + "/" + clyr,
		"sageox://" + cnv + "/" + clyr + "@4",
		"sageox://" + cnv + "/" + clyr + "@4#t=1771355009107",
		"sageox://" + cnv + "/" + clyr + "@4#t=1771355009107--1771355014307",
		"sageox://" + cnv + "/" + clyr + "@4#t=100--200&cue=12-16",
		"sageox://" + cnv + "#t=-8640000000000000--8640000000000000",
		"sageox://" + cnv + "#cue=7",
		"sageox://" + cnv + "#cue=7-7",
		"sageox://" + cnv + "#t=5&cue=1-3&turn=2-4",
		"sageox://" + cnv + "/" + clyr + "#topic=tp_01a017b1-f1f0-7ad3-9bd1-a5be4b557d61",
		"sageox://" + cnv + "#t=1&cue=2&turn=3&zeta=z&alpha=a",
	}
	for _, in := range canonical {
		t.Run(in, func(t *testing.T) {
			a, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", in, err)
			}
			out := a.Encode()
			if out != in {
				t.Fatalf("decode∘encode not identity:\n in  %q\n out %q", in, out)
			}
			b, err := Parse(out)
			if err != nil {
				t.Fatalf("re-Parse(%q) error: %v", out, err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("re-parse mismatch:\n a %+v\n b %+v", a, b)
			}
		})
	}
}

func TestSelectorsIsZero(t *testing.T) {
	tests := []struct {
		name string
		sels Selectors
		want bool
	}{
		{"empty", Selectors{}, true},
		{"time set", Selectors{Time: &TimeSelector{StartMS: 1, EndMS: 1}}, false},
		{"cue set", Selectors{Cue: &OrdinalRange{From: 1, To: 1}}, false},
		{"turn set", Selectors{Turn: &OrdinalRange{From: 1, To: 1}}, false},
		{"unknown set", Selectors{Unknown: []Selector{{Key: "k", Value: "v"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sels.IsZero(); got != tt.want {
				t.Fatalf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}
