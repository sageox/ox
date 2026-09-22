package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/theme"
)

// TestPrintJSONTo_PipedOutputIsByteExactJSON is the load-bearing one: an AI
// coworker or `jq` reading a piped `--json` payload must receive parseable
// bytes with no escape sequence anywhere.
//
// Failure prevented: a colorized payload reaching a parser, which fails with
// "invalid character '\x1b'" — a break that looks like a corrupt command
// rather than a rendering choice.
func TestPrintJSONTo_PipedOutputIsByteExactJSON(t *testing.T) {
	// `go test` stdout is not a TTY, so this exercises the real agent path.
	if theme.ColorEnabled() {
		t.Skip("color profile forced on; this test asserts the no-TTY path")
	}
	var buf bytes.Buffer
	PrintJSONTo(&buf, map[string]any{"name": "post-cutoff", "installed": false, "count": 3})

	if strings.ContainsRune(buf.String(), '\x1b') {
		t.Fatalf("piped JSON contains an escape sequence: %q", buf.String())
	}
	var round map[string]any
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("piped JSON does not parse: %v\n%s", err, buf.String())
	}
	if !strings.HasSuffix(buf.String(), "}\n") {
		t.Errorf("want exactly one trailing newline, got %q", buf.String())
	}
}

// TestColorizeJSON_PreservesEveryByte proves the colorizer only adds escape
// sequences: stripping them must reproduce the input exactly.
//
// Failure prevented: a lexer bug that drops or duplicates content while still
// looking plausible on screen. Colors can regress; bytes cannot.
func TestColorizeJSON_PreservesEveryByte(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"scalars", map[string]any{"s": "x", "n": -12.5, "t": true, "f": false, "z": nil}},
		// A value containing JSON punctuation is why this lexes instead of
		// pattern-matching: `{`, `:` and an escaped quote inside a string must
		// not be read as structure.
		{"punctuation in a value", map[string]any{"summary": `a {brace}: and a \"quote\" and, a comma`}},
		{"nested", map[string]any{"addons": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}}},
		{"empty containers", map[string]any{"list": []any{}, "obj": map[string]any{}}},
		{"unicode", map[string]any{"em": "70–500ms — typed", "cut": "…"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain, err := json.MarshalIndent(tc.value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			plain = append(plain, '\n')

			colored := colorizeJSON(string(plain))
			if stripped := stripANSI(colored); stripped != string(plain) {
				t.Errorf("colorizer changed content\n got: %q\nwant: %q", stripped, string(plain))
			}
		})
	}
}

// TestColorizeJSON_DistinguishesKeysFromValues guards the one judgment the
// lexer makes. A string is a key only when the next non-space byte is ':';
// a value that merely looks like a key must not be styled as one.
//
// Failure prevented: every string rendering identically, which is the same as
// no colorization at all — the feature would ship looking like it worked.
func TestColorizeJSON_DistinguishesKeysFromValues(t *testing.T) {
	const doc = "{\n  \"name\": \"name\"\n}\n"
	got := colorizeJSON(doc)

	key := jsonKeyStyle.Render(`"name"`)
	val := jsonStringStyle.Render(`"name"`)
	if key == val {
		t.Skip("no color in this environment; key/value styles are indistinguishable by construction")
	}
	if !strings.Contains(got, key) {
		t.Errorf("key not styled as a key: %q", got)
	}
	if !strings.Contains(got, val) {
		t.Errorf("value not styled as a value: %q", got)
	}
}

// stripANSI removes SGR sequences so a test can compare content independent of
// styling. Deliberately local to the test: production code never needs to undo
// its own color, and an exported stripper would invite exactly that.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // consume the 'm'
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestEndOfJSONString covers the one piece of real lexing judgment: where a
// string literal ends. TestColorizeJSON_PreservesEveryByte cannot catch a bug
// here — every byte is emitted either way, only the styling boundaries move —
// so the boundary needs its own assertion.
//
// Failure prevented: an escaped quote inside a value ends the string early, so
// the remainder of a sentence is lexed as structure. A summary containing
// `\"quote\"` then paints its own tail as keys and punctuation.
func TestEndOfJSONString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // the literal the scanner should consume
	}{
		{"plain", `"name" : 1`, `"name"`},
		{"escaped quote", `"a \" b" : 1`, `"a \" b"`},
		{"escaped backslash before quote", `"ends with \\" : 1`, `"ends with \\"`},
		{"punctuation inside", `"a {b}: c, d" : 1`, `"a {b}: c, d"`},
		{"unterminated", `"no end`, `"no end`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in[:endOfJSONString(tc.in, 0)]; got != tc.want {
				t.Errorf("endOfJSONString consumed %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMarshalJSONIndent_IsAlwaysPlainAndNewlineTerminated pins the contract the
// measuring callers depend on: the bytes they count are the bytes an AI
// coworker consumes, never a colorized variant, and there is exactly one
// trailing newline whether the old site used MarshalIndent (adds none) or
// Encoder.Encode (adds one).
//
// Failure prevented: a payload-size metric that silently starts counting
// escape sequences, or a double blank line appearing under every `--json`
// command after migration.
func TestMarshalJSONIndent_IsAlwaysPlainAndNewlineTerminated(t *testing.T) {
	got, err := MarshalJSONIndent(map[string]any{"name": "post-cutoff", "count": 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(got), '\x1b') {
		t.Errorf("MarshalJSONIndent colorized its output: %q", got)
	}
	if !strings.HasSuffix(string(got), "}\n") || strings.HasSuffix(string(got), "\n\n") {
		t.Errorf("want exactly one trailing newline, got %q", got)
	}
	if !strings.Contains(string(got), "\n  \"count\"") {
		t.Errorf("want two-space indent, got %q", got)
	}
}

// TestWriteJSBytes_PassesPlainBytesThrough covers the seam used by callers with
// a non-default encoder — SetEscapeHTML(false), or a --pretty flag choosing
// compact output. Their bytes must reach the writer untouched when color is
// off, including a compact single-line encoding.
//
// Failure prevented: the printer "helpfully" re-indenting or re-escaping a
// payload whose encoding was chosen deliberately, turning a URL's & into
// & after migration.
func TestWriteJSONBytes_PassesPlainBytesThrough(t *testing.T) {
	if theme.ColorEnabled() {
		t.Skip("color profile forced on; this test asserts the no-TTY path")
	}
	// Compact, HTML-unescaped, single newline — none of which the default
	// encoder would produce.
	const compact = "{\"url\":\"https://x.test/a?b=1&c=2\"}\n"
	var buf bytes.Buffer
	if err := WriteJSONBytes(&buf, []byte(compact)); err != nil {
		t.Fatal(err)
	}
	if buf.String() != compact {
		t.Errorf("bytes were altered\n got: %q\nwant: %q", buf.String(), compact)
	}
}

// TestPrintJSONTo_ReportsEncodeFailure verifies the error path exists, since
// every migrated call site now returns it. A value json cannot encode must
// surface, not vanish into a silent empty line.
func TestPrintJSONTo_ReportsEncodeFailure(t *testing.T) {
	var buf bytes.Buffer
	err := PrintJSONTo(&buf, map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("PrintJSONTo returned nil for an unencodable value")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q despite the encode failing", buf.String())
	}
}

// TestJSONPalette_EveryTypeGetsADistinctColor is the regression test for the
// defect that shipped: keys used ColorPrimary and strings used ColorSuccess,
// which are the SAME hex (#7AAA77) in the sage-dominated SageOx palette, and
// numbers used ColorAccent (#99C693), a third near-identical green. Three of
// five token types rendered the same, so the reader had only bold to go on.
//
// Failure prevented: a future palette edit quietly collapsing two types onto
// one color again. Hue is the channel a reader actually parses; losing it
// makes the colorization decorative.
func TestJSONPalette_EveryTypeGetsADistinctColor(t *testing.T) {
	if !theme.ColorEnabled() {
		t.Skip("no color in this environment; every style is identical by construction")
	}
	// Compare FOREGROUND COLORS, not rendered strings. The original defect was
	// keys and strings sharing a hex and differing only by bold — comparing
	// Render() output would have called that "distinct" and missed the bug
	// entirely. Hue is the claim under test; weight is not a substitute for it.
	// VALUE types only. Punctuation deliberately shares null's dim: both
	// recede, and a comma can never be mistaken for a value because position
	// already separates them. Keys, strings, numbers, booleans and null all
	// appear in the SAME slot after a colon, so those five must differ by hue
	// and nothing else may be traded for it.
	colors := map[string]any{
		"key":    jsonKeyStyle.GetForeground(),
		"string": jsonStringStyle.GetForeground(),
		"number": jsonNumberStyle.GetForeground(),
		"bool":   jsonBoolStyle.GetForeground(),
		"null":   jsonNullStyle.GetForeground(),
	}
	seen := make(map[string]string, len(colors))
	for name, c := range colors {
		key := fmt.Sprintf("%v", c)
		if other, dup := seen[key]; dup {
			t.Errorf("%q and %q resolve to the same color (%s) — bold is not a substitute for hue", name, other, key)
		}
		seen[key] = name
	}
}

// TestColorizeJSON_BracesArePairedByDepth verifies a closing brace is tinted to
// match its opener. Tinting a closer at the inner depth would pair every brace
// with the wrong one, making the nesting cue worse than none.
func TestColorizeJSON_BracesArePairedByDepth(t *testing.T) {
	if !theme.ColorEnabled() {
		t.Skip("no color in this environment; depth tints are identical by construction")
	}
	// Two levels, so depth 0 and depth 1 tints must both appear and match.
	got := colorizeJSON("{\n  \"a\": {\n    \"b\": 1\n  }\n}\n")

	outer := depthStyle(0).Render("{")
	inner := depthStyle(1).Render("{")
	if outer == inner {
		t.Fatal("depth 0 and depth 1 tints are identical; the cue carries no information")
	}
	for _, want := range []string{outer, depthStyle(0).Render("}"), inner, depthStyle(1).Render("}")} {
		if !strings.Contains(got, want) {
			t.Errorf("missing depth-paired brace %q in %q", want, got)
		}
	}
}

// TestJSONNullStyle_IsVisuallyAbsent pins null rendering as dim + italic.
// A null that looks like an ordinary value reads as something someone set,
// which is the opposite of what it means.
func TestJSONNullStyle_IsVisuallyAbsent(t *testing.T) {
	if !theme.ColorEnabled() {
		t.Skip("no color in this environment")
	}
	if jsonNullStyle.Render("null") == jsonStringStyle.Render("null") {
		t.Error("null renders like a string value")
	}
	if !strings.Contains(jsonNullStyle.Render("null"), "\x1b[3") {
		t.Errorf("null is not italic: %q", jsonNullStyle.Render("null"))
	}
}
