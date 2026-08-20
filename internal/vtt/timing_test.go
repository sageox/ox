package vtt

import (
	"reflect"
	"testing"
	"time"
)

// --- A. Timestamp grammar ---

// TestParseTimestamp verifies the WebVTT timestamp grammar: hh:mm:ss.mmm with
// optional hours, two-digit minutes/seconds < 60, exactly three fraction digits.
// Failure prevented: accepting garbage timestamps (or rejecting valid ones)
// would corrupt every time-window citation slice built on top.
func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "00:00.000", want: 0},
		{in: "00:05.000", want: 5 * time.Second},
		{in: "01:02.003", want: time.Minute + 2*time.Second + 3*time.Millisecond},
		{in: "00:00:00.000", want: 0},
		{in: "01:02:03.004", want: time.Hour + 2*time.Minute + 3*time.Second + 4*time.Millisecond},
		{in: "10:00:00.500", want: 10*time.Hour + 500*time.Millisecond},
		{in: "100:00:00.000", want: 100 * time.Hour}, // hours may exceed two digits
		{in: "9:59:59.999", want: 9*time.Hour + 59*time.Minute + 59*time.Second + 999*time.Millisecond},

		{in: "", wantErr: true},
		{in: "00:00", wantErr: true},         // no fraction
		{in: "00:00.00", wantErr: true},      // two fraction digits
		{in: "00:00.0000", wantErr: true},    // four fraction digits
		{in: "00:00.abc", wantErr: true},     // non-digit fraction
		{in: "0:00.000", wantErr: true},      // one-digit minutes
		{in: "00:0.000", wantErr: true},      // one-digit seconds
		{in: "00:60.000", wantErr: true},     // seconds >= 60
		{in: "60:00.000", wantErr: true},     // minutes >= 60
		{in: "00:60:00.000", wantErr: true},  // minutes >= 60 with hours
		{in: "aa:00:00.000", wantErr: true},  // non-digit hours
		{in: "-1:00:00.000", wantErr: true},  // negative hours
		{in: "1:2:3:4.000", wantErr: true},   // too many fields
		{in: "00.000", wantErr: true},        // seconds only
		{in: "00:00:00.000x", wantErr: true}, // trailing junk
		{in: " 00:00:00.000", wantErr: true}, // leading space (caller trims)
		{in: "00:００.000", wantErr: true},     // non-ASCII digits
		{in: "00:00:00,000", wantErr: true},  // SRT comma fraction
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTimestamp(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTimestamp(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTimestamp(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseTimestamp(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseTimestampPair verifies timestamp-line splitting, including cue
// settings after the end timestamp and malformed sides.
func TestParseTimestampPair(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantStart time.Duration
		wantEnd   time.Duration
		wantErr   bool
	}{
		{name: "plain", line: "00:00:01.000 --> 00:00:02.500", wantStart: time.Second, wantEnd: 2500 * time.Millisecond},
		{name: "no hours", line: "00:01.000 --> 00:02.000", wantStart: time.Second, wantEnd: 2 * time.Second},
		{name: "cue settings after end", line: "00:00:01.000 --> 00:00:02.000 align:start position:10%", wantStart: time.Second, wantEnd: 2 * time.Second},
		{name: "tight arrow", line: "00:00:01.000-->00:00:02.000", wantStart: time.Second, wantEnd: 2 * time.Second},
		{name: "malformed start", line: "garbage --> 00:00:02.000", wantErr: true},
		{name: "malformed end", line: "00:00:01.000 --> garbage", wantErr: true},
		{name: "missing end", line: "00:00:01.000 --> ", wantErr: true},
		{name: "no arrow", line: "00:00:01.000 00:00:02.000", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := parseTimestampPair(tt.line)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTimestampPair(%q) want error, got %v %v", tt.line, start, end)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTimestampPair(%q) unexpected error: %v", tt.line, err)
			}
			if start != tt.wantStart || end != tt.wantEnd {
				t.Errorf("parseTimestampPair(%q) = (%v, %v), want (%v, %v)", tt.line, start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

// --- B. Parse populates timing additively ---

// TestParsePopulatesTiming verifies Index/Start/End on parsed cues, including
// the malformed-timestamp contract (empty interval, cue still emitted).
// Failure prevented: cue-range citations pointing at the wrong ordinal.
func TestParsePopulatesTiming(t *testing.T) {
	input := "WEBVTT\n" +
		"\n" +
		"00:00:00.000 --> 00:00:05.000\n" +
		"<v Alice>First</v>\n" +
		"\n" +
		"bogus --> timestamps\n" +
		"<v Bob>Malformed but present</v>\n" +
		"\n" +
		"00:01:00.000 --> 00:01:10.000 align:start\n" +
		"<v Alice>Third</v>\n"

	cues, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []Cue{
		{Speaker: "Alice", Text: "First", Index: 1, Start: 0, End: 5 * time.Second},
		{Speaker: "Bob", Text: "Malformed but present", Index: 2, Start: 0, End: 0},
		{Speaker: "Alice", Text: "Third", Index: 3, Start: time.Minute, End: time.Minute + 10*time.Second},
	}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("cues = %+v, want %+v", cues, want)
	}
	if cues[1].HasTiming() {
		t.Error("malformed-timestamp cue must report HasTiming() == false")
	}
	if !cues[0].HasTiming() {
		t.Error("well-formed cue must report HasTiming() == true")
	}
}

// TestParseZeroLengthCueHasNoTiming pins the empty-interval rule: End <= Start
// means no media-clock presence even when the timestamps parsed.
func TestParseZeroLengthCueHasNoTiming(t *testing.T) {
	input := "WEBVTT\n\n00:00:05.000 --> 00:00:05.000\nInstantaneous\n"
	cues, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if cues[0].HasTiming() {
		t.Error("zero-length cue must report HasTiming() == false")
	}
}

// --- C. Cue-range slicing ---

func timedCues(n int) []Cue {
	// Cue i (1-based) spans [i*10s, i*10s+5s).
	cues := make([]Cue, n)
	for i := range cues {
		start := time.Duration(i+1) * 10 * time.Second
		cues[i] = Cue{
			Index: i + 1,
			Start: start,
			End:   start + 5*time.Second,
			Text:  "cue",
		}
	}
	return cues
}

// TestSliceByCueRange covers exact ranges, clamping, out-of-range emptiness,
// and reversed ranges. Failure prevented: a citation cue=5-7 silently
// returning the wrong cues or panicking on out-of-bounds input.
func TestSliceByCueRange(t *testing.T) {
	cues := timedCues(5)
	tests := []struct {
		name        string
		first, last int
		wantIdx     []int
		wantClamped bool
	}{
		{name: "exact full range", first: 1, last: 5, wantIdx: []int{1, 2, 3, 4, 5}},
		{name: "interior range", first: 2, last: 4, wantIdx: []int{2, 3, 4}},
		{name: "single cue", first: 3, last: 3, wantIdx: []int{3}},
		{name: "clamped high end", first: 4, last: 99, wantIdx: []int{4, 5}, wantClamped: true},
		{name: "clamped low end", first: 0, last: 2, wantIdx: []int{1, 2}, wantClamped: true},
		{name: "clamped both ends", first: -3, last: 100, wantIdx: []int{1, 2, 3, 4, 5}, wantClamped: true},
		{name: "entirely past end", first: 6, last: 9, wantIdx: nil, wantClamped: true},
		{name: "entirely below start", first: -5, last: 0, wantIdx: nil, wantClamped: true},
		{name: "reversed range", first: 4, last: 2, wantIdx: nil, wantClamped: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, clamped := SliceByCueRange(cues, tt.first, tt.last)
			var gotIdx []int
			for _, c := range out {
				gotIdx = append(gotIdx, c.Index)
			}
			if !reflect.DeepEqual(gotIdx, tt.wantIdx) {
				t.Errorf("indices = %v, want %v", gotIdx, tt.wantIdx)
			}
			if clamped != tt.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, tt.wantClamped)
			}
		})
	}
}

// TestSliceByCueRangeEmptyInput verifies graceful zero-value behavior before
// any cues exist.
func TestSliceByCueRangeEmptyInput(t *testing.T) {
	out, clamped := SliceByCueRange(nil, 1, 10)
	if out != nil || !clamped {
		t.Errorf("SliceByCueRange(nil, 1, 10) = (%v, %v), want (nil, true)", out, clamped)
	}
}

// TestSliceByCueRangeReturnsCopy verifies the returned slice does not alias
// the input backing array. Failure prevented: a caller mutating a slice
// result corrupting the cached full transcript.
func TestSliceByCueRangeReturnsCopy(t *testing.T) {
	cues := timedCues(3)
	out, _ := SliceByCueRange(cues, 1, 3)
	out[0].Text = "mutated"
	if cues[0].Text == "mutated" {
		t.Error("SliceByCueRange result aliases input backing array")
	}
}

// --- D. Time-window slicing ---

// TestSliceByTimeWindow covers the overlap contract: cue intervals half-open
// [Start, End), window closed [from, to], overlap = non-empty intersection.
// Failure prevented: off-by-one boundary bugs dropping or duplicating the
// cue a t= citation points at.
func TestSliceByTimeWindow(t *testing.T) {
	// Cues: 1:[10,15) 2:[20,25) 3:[30,35) 4:[40,45) 5:[50,55) (seconds)
	cues := timedCues(5)
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	tests := []struct {
		name     string
		from, to time.Duration
		wantIdx  []int
	}{
		{name: "window spans all", from: sec(0), to: sec(100), wantIdx: []int{1, 2, 3, 4, 5}},
		{name: "window inside one cue", from: sec(11), to: sec(12), wantIdx: []int{1}},
		{name: "window across two cues", from: sec(14), to: sec(21), wantIdx: []int{1, 2}},
		{name: "window in a gap", from: sec(16), to: sec(19), wantIdx: nil},
		{name: "window end touches cue start (closed window includes it)", from: sec(5), to: sec(10), wantIdx: []int{1}},
		{name: "window start at cue end (End exclusive, no overlap)", from: sec(15), to: sec(19), wantIdx: nil},
		{name: "window start at last instant of cue", from: sec(14), to: sec(16), wantIdx: []int{1}},
		{name: "degenerate window at cue start", from: sec(10), to: sec(10), wantIdx: []int{1}},
		{name: "degenerate window at cue end", from: sec(15), to: sec(15), wantIdx: nil},
		{name: "window before everything", from: sec(0), to: sec(4), wantIdx: nil},
		{name: "window after everything", from: sec(60), to: sec(70), wantIdx: nil},
		{name: "reversed window", from: sec(30), to: sec(10), wantIdx: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := SliceByTimeWindow(cues, tt.from, tt.to)
			var gotIdx []int
			for _, c := range out {
				gotIdx = append(gotIdx, c.Index)
			}
			if !reflect.DeepEqual(gotIdx, tt.wantIdx) {
				t.Errorf("indices = %v, want %v", gotIdx, tt.wantIdx)
			}
		})
	}
}

// TestSliceByTimeWindowSkipsUntimedCues verifies malformed-timestamp cues
// (empty intervals) never match a window, even one that covers t=0.
func TestSliceByTimeWindowSkipsUntimedCues(t *testing.T) {
	cues := []Cue{
		{Index: 1, Start: 0, End: 0, Text: "malformed"},
		{Index: 2, Start: 0, End: 5 * time.Second, Text: "real"},
	}
	out := SliceByTimeWindow(cues, 0, 10*time.Second)
	if len(out) != 1 || out[0].Index != 2 {
		t.Errorf("got %+v, want only cue 2", out)
	}
}

// TestSliceByTimeWindowOutOfOrder verifies slicing works on files whose
// timestamps are not monotonic — order preserved, membership by overlap.
func TestSliceByTimeWindowOutOfOrder(t *testing.T) {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	cues := []Cue{
		{Index: 1, Start: sec(30), End: sec(35)},
		{Index: 2, Start: sec(10), End: sec(15)},
		{Index: 3, Start: sec(20), End: sec(25)},
	}
	out := SliceByTimeWindow(cues, sec(12), sec(32))
	var gotIdx []int
	for _, c := range out {
		gotIdx = append(gotIdx, c.Index)
	}
	if !reflect.DeepEqual(gotIdx, []int{1, 2, 3}) {
		t.Errorf("indices = %v, want [1 2 3]", gotIdx)
	}
}

// --- E. Bare-instant resolution ---

// TestCueAtInstant covers the bare-instant rule: containing cue wins, else
// nearest following cue, else not found. Failure prevented: a t=<instant>
// citation in a silence gap resolving to nothing (or to the wrong side).
func TestCueAtInstant(t *testing.T) {
	// Cues: 1:[10,15) 2:[20,25) 3:[30,35) (seconds)
	cues := timedCues(3)
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	tests := []struct {
		name    string
		t       time.Duration
		wantIdx int
		wantOK  bool
	}{
		{name: "inside a cue", t: sec(12), wantIdx: 1, wantOK: true},
		{name: "exactly at cue start", t: sec(20), wantIdx: 2, wantOK: true},
		{name: "exactly at cue end resolves to following", t: sec(15), wantIdx: 2, wantOK: true},
		{name: "in a gap resolves to nearest following", t: sec(17), wantIdx: 2, wantOK: true},
		{name: "before all cues", t: sec(0), wantIdx: 1, wantOK: true},
		{name: "after all cues", t: sec(60), wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CueAtInstant(cues, tt.t)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Index != tt.wantIdx {
				t.Errorf("index = %d, want %d", got.Index, tt.wantIdx)
			}
		})
	}
}

// TestCueAtInstantOutOfOrder verifies "nearest following" means smallest
// Start >= t, not file order.
func TestCueAtInstantOutOfOrder(t *testing.T) {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	cues := []Cue{
		{Index: 1, Start: sec(40), End: sec(45)},
		{Index: 2, Start: sec(20), End: sec(25)},
	}
	got, ok := CueAtInstant(cues, sec(5))
	if !ok || got.Index != 2 {
		t.Errorf("got (%+v, %v), want cue 2", got, ok)
	}
}

// TestCueAtInstantSkipsUntimedCues verifies empty-interval cues cannot
// contain or follow an instant.
func TestCueAtInstantSkipsUntimedCues(t *testing.T) {
	cues := []Cue{{Index: 1, Start: 0, End: 0, Text: "malformed"}}
	if _, ok := CueAtInstant(cues, 0); ok {
		t.Error("untimed cue must not resolve an instant")
	}
	if _, ok := CueAtInstant(nil, 0); ok {
		t.Error("no cues must resolve to not-found")
	}
}
