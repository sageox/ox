package read

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const axLayerDir = "layers/ax-tree.clyr_019ffe10-0000-7000-8000-0000000000a2"

// hasWarning reports whether any warning contains sub.
func hasWarning(env *Envelope, sub string) bool {
	for _, w := range env.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// countFrames totals the frames attached across every served cue.
func countFrames(data *TranscriptData) int {
	n := 0
	for _, c := range data.Cues {
		n += len(c.Frames)
	}
	return n
}

// TestKeyframesFileGuards: keyframes.json comes off a customer-writable,
// git-synced tree. Every way it can be unusable yields no frames and a
// warning that names the cause — never an error, never a partial parse.
func TestKeyframesFileGuards(t *testing.T) {
	cases := []struct {
		name  string
		stage func(t *testing.T, root string)
		warn  string
	}{
		{"invalid JSON", func(t *testing.T, root string) {
			writeWalkthroughFile(t, root, KeyframesFileName, `{"keyframes": [`)
		}, "is not valid JSON"},
		{"over the byte cap", func(t *testing.T, root string) {
			writeWalkthroughFile(t, root, KeyframesFileName, `{"keyframes": []}`+strings.Repeat(" ", maxScreenFileBytes))
		}, fmt.Sprintf("exceeds %d bytes", maxScreenFileBytes)},
		{"a directory, not a file", func(t *testing.T, root string) {
			p := filepath.Join(root, walkthroughFolder, KeyframesFileName)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "keyframes unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyTree(t, walkthroughRoot)
			tc.stage(t, root)
			env, data := walkthroughTranscript(t, root, TranscriptOptions{Frames: true})
			if n := countFrames(data); n != 0 {
				t.Errorf("%d frames attached from an unusable keyframes.json", n)
			}
			if !hasWarning(env, tc.warn) {
				t.Errorf("warnings %q lack %q", env.Warnings, tc.warn)
			}
		})
	}
}

// TestSymlinkedKeyframesFileIsRefused: a keyframes.json symlink is never
// followed, even to a valid manifest inside the folder.
func TestSymlinkedKeyframesFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := copyTree(t, walkthroughRoot)
	dir := filepath.Join(root, walkthroughFolder)
	if err := os.Rename(filepath.Join(dir, KeyframesFileName), filepath.Join(dir, "real-keyframes.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-keyframes.json", filepath.Join(dir, KeyframesFileName)); err != nil {
		t.Fatal(err)
	}
	env, data := walkthroughTranscript(t, root, TranscriptOptions{Frames: true})
	if n := countFrames(data); n != 0 {
		t.Errorf("%d frames read through a symlinked keyframes.json", n)
	}
	if !hasWarning(env, "not a regular file") {
		t.Errorf("warnings %q do not name the refused symlink", env.Warnings)
	}
}

// TestKeyframeCountIsBounded: a manifest listing more than maxKeyframes
// frames is cut to the bound and the cut is reported.
func TestKeyframeCountIsBounded(t *testing.T) {
	root := copyTree(t, walkthroughRoot)
	type kf struct {
		TimestampSeconds float64 `json:"timestamp_seconds"`
		Description      string  `json:"description"`
	}
	frames := make([]kf, maxKeyframes+1)
	for i := range frames {
		frames[i] = kf{TimestampSeconds: 1, Description: "f"}
	}
	raw, err := json.Marshal(map[string]any{"keyframes": frames})
	if err != nil {
		t.Fatal(err)
	}
	writeWalkthroughFile(t, root, KeyframesFileName, string(raw))
	env, data := walkthroughTranscript(t, root, TranscriptOptions{Frames: true, Full: true})
	if n := countFrames(data); n != maxKeyframes {
		t.Errorf("attached %d frames, want the bound %d", n, maxKeyframes)
	}
	if !hasWarning(env, fmt.Sprintf("only the first %d were considered", maxKeyframes)) {
		t.Errorf("warnings %q do not report the frame bound", env.Warnings)
	}
}

// TestHostileSidecarRefsAreNeverJoined: content refs that escape the layer
// folder (traversal, absolute, backslash) are skipped, and the default
// sidecar is read instead.
func TestHostileSidecarRefsAreNeverJoined(t *testing.T) {
	for _, ref := range []string{"../../../../etc/passwd.jsonl", "/etc/passwd.jsonl", `..\\..\\x.jsonl`, "layers/../../x.jsonl"} {
		t.Run(ref, func(t *testing.T) {
			root := copyTree(t, walkthroughRoot)
			envPath := pointerLayerDir + "/layer.json"
			raw, err := os.ReadFile(filepath.Join(root, walkthroughFolder, filepath.FromSlash(envPath)))
			if err != nil {
				t.Fatal(err)
			}
			quoted, _ := json.Marshal(ref)
			writeWalkthroughFile(t, root, envPath, strings.Replace(string(raw), `"path": "pointer.jsonl"`, `"path": `+string(quoted), 1))
			_, data := walkthroughTranscript(t, root, TranscriptOptions{Frames: true})
			if len(data.Cues[1].Pointing) != 2 {
				t.Errorf("hostile ref %q: cue 2 pointing = %+v, want the default sidecar's 2 events", ref, data.Cues[1].Pointing)
			}
		})
	}
}

// TestMalformedAXRowsAreReported: an unparseable ax-tree row is counted and
// reported, and the rest of the file still resolves elements.
func TestMalformedAXRowsAreReported(t *testing.T) {
	root := copyTree(t, walkthroughRoot)
	p := filepath.Join(root, walkthroughFolder, filepath.FromSlash(axLayerDir), "ax.jsonl")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte("{not json\n\n"), raw...), 0o644); err != nil {
		t.Fatal(err)
	}
	env, data := walkthroughTranscript(t, root, TranscriptOptions{Frames: true})
	if !hasWarning(env, "malformed") {
		t.Errorf("warnings %q do not report the malformed AX row", env.Warnings)
	}
	named := false
	for _, c := range data.Cues {
		for _, p := range c.Pointing {
			if p.Title != "" {
				named = true
			}
		}
	}
	if !named {
		t.Error("one malformed AX row stopped every element from resolving")
	}
}

// TestRowMediaTimeBounds pins the timestamp rules row by row: t_utc as an
// RFC 3339 string or epoch-ms number, t_ms as the fallback, and every
// out-of-range or non-finite value rejected rather than overflowing.
func TestRowMediaTimeBounds(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	ms := func(v float64) *float64 { return &v }
	epoch := func(d time.Duration) json.RawMessage {
		return json.RawMessage(fmt.Sprintf("%d", t0.Add(d).UnixMilli()))
	}
	cases := []struct {
		name  string
		tUTC  json.RawMessage
		tMS   *float64
		hasT0 bool
		want  time.Duration
		res   rowResult
	}{
		{"rfc3339 string", json.RawMessage(`"2026-08-18T01:00:05Z"`), nil, true, 5 * time.Second, rowKept},
		{"unparseable string", json.RawMessage(`"yesterday"`), nil, true, 0, rowMalformed},
		{"epoch-ms number", epoch(7 * time.Second), nil, true, 7 * time.Second, rowKept},
		{"negative epoch-ms", json.RawMessage(`-5`), nil, true, 0, rowMalformed},
		{"epoch-ms past the bound", json.RawMessage(`1e14`), nil, true, 0, rowMalformed},
		{"neither string nor number", json.RawMessage(`{}`), nil, true, 0, rowMalformed},
		{"slightly before t0 clamps to zero", epoch(-500 * time.Millisecond), nil, true, 0, rowKept},
		{"well before t0 is rejected", epoch(-time.Minute), nil, true, 0, rowMalformed},
		{"past the media bound", epoch(49 * time.Hour), nil, true, 0, rowMalformed},
		{"t_ms fallback", nil, ms(1500), false, 1500 * time.Millisecond, rowKept},
		{"t_ms NaN", nil, ms(math.NaN()), false, 0, rowMalformed},
		{"t_ms +Inf", nil, ms(math.Inf(1)), false, 0, rowMalformed},
		{"t_ms far negative", nil, ms(-10_000), false, 0, rowMalformed},
		{"t_ms past the media bound", nil, ms(float64((49 * time.Hour).Milliseconds())), false, 0, rowMalformed},
		{"t_utc only, no clock origin", json.RawMessage(`"2026-08-18T01:00:05Z"`), nil, false, 0, rowNoClock},
		{"no timestamp at all", nil, nil, false, 0, rowMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, res := rowMediaTime(tc.tUTC, tc.tMS, t0, tc.hasT0)
			if res != tc.res || (res == rowKept && got != tc.want) {
				t.Errorf("rowMediaTime = (%v, %v), want (%v, %v)", got, res, tc.want, tc.res)
			}
		})
	}
}

// TestPickPointerSamples: clicks before dwells, longest dwell first, one
// event per element, at most maxPointingPerCue, a hover only when nothing
// else exists, and the result back in time order.
func TestPickPointerSamples(t *testing.T) {
	s := func(sec float64, click bool, dwell float64, ref string) pointerSample {
		return pointerSample{at: time.Duration(sec * float64(time.Second)), click: click, dwell: dwell, axRef: ref}
	}
	cases := []struct {
		name string
		in   []pointerSample
		want []string // "sec:ref" in output order
	}{
		{"click beats dwell, time-ordered", []pointerSample{s(1, false, 900, "a"), s(3, true, 0, "b")}, []string{"1:a", "3:b"}},
		{"longest dwell wins the second slot", []pointerSample{s(1, true, 0, "a"), s(2, false, 100, "b"), s(4, false, 900, "c")}, []string{"1:a", "4:c"}},
		{"one event per element", []pointerSample{s(1, true, 0, "a"), s(2, true, 0, "a"), s(3, false, 50, "b")}, []string{"1:a", "3:b"}},
		{"capped at maxPointingPerCue", []pointerSample{s(1, true, 0, "a"), s(2, true, 0, "b"), s(3, true, 0, "c")}, []string{"1:a", "2:b"}},
		{"hover only when nothing else", []pointerSample{s(1, false, 0, ""), s(2, false, 0, "h")}, []string{"2:h"}},
		{"hover ignored beside a click", []pointerSample{s(1, false, 0, "h"), s(2, true, 0, "a")}, []string{"2:a"}},
		{"no element, no hover", []pointerSample{s(1, false, 0, "")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, p := range pickPointerSamples(tc.in) {
				got = append(got, fmt.Sprintf("%g:%s", p.at.Seconds(), p.axRef))
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("picked %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShareLinkGuidance: the share-link error tells the reader what to paste
// instead, rather than the generic invalid-id advice.
func TestShareLinkGuidance(t *testing.T) {
	g := errorGuidance(ErrCodeShareLinkUnresolvable)
	for _, want := range []string{"share link", "rec_"} {
		if !strings.Contains(g, want) {
			t.Errorf("share-link guidance %q lacks %q", g, want)
		}
	}
	if g == errorGuidance(ErrCodeInvalidID) {
		t.Error("share-link guidance is the generic invalid-id text")
	}
}
