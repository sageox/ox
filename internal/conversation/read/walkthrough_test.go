package read

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Desktop-walkthrough fixture: testdata/walkthrough-desktop/discussions holds
// one walkthrough laid out exactly as SageOx Desktop produces it (pointer,
// ax-tree, and keyframe-hints layers with ms-precise clock.t0, coverage gaps
// and target_initial on the envelope, string snapshot ids, hover and click
// snapshots) plus server keyframes — two stubs and one real image — and one
// audio-only discussion. The pointer layer's t0 is 500 ms after the
// manifest's whole-second t0, so every expected offset below also proves the
// layer clock wins.
const (
	desktopWalkRoot   = "testdata/walkthrough-desktop/discussions"
	desktopWalkFolder = "2026-09-30-10-00-saved-walkthrough"
	desktopWalkCnv    = "cnv_01a0f488-0000-7000-8000-0000000000b1"
	desktopAudioCnv   = "cnv_01a0f488-0000-7000-8000-0000000000b2"
)

// stageDesktopWalkthrough copies the fixture to <tmp>/discussions, so the
// ox fetch cache path (<tmp>/.sageox/cache/discussions/...) belongs to the
// test, and returns the discussions root.
func stageDesktopWalkthrough(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), DiscussionsDirName)
	src := copyTree(t, desktopWalkRoot)
	if err := os.Rename(src, root); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return root
}

func walkthroughData(t *testing.T, env *Envelope) *WalkthroughData {
	t.Helper()
	if !env.Success {
		t.Fatalf("walkthrough failed: %+v", env.Error)
	}
	d, ok := env.Data.(*WalkthroughData)
	if !ok {
		t.Fatalf("data is %T", env.Data)
	}
	return d
}

func readWalkthrough(t *testing.T, root, id string, opts WalkthroughOptions) (*Envelope, *WalkthroughData) {
	t.Helper()
	env := New(root, time.Time{}).Walkthrough(id, opts)
	return env, walkthroughData(t, env)
}

// momentLine renders a moment as "at cue kind subject" for compact asserts.
func momentLine(m WalkthroughMoment) string {
	subject := ""
	switch {
	case m.Element != nil:
		subject = m.Element.Role + ":" + m.Element.Title
		if m.Element.Within != nil {
			subject += "<" + m.Element.Within.Title
		}
	case m.Page != nil:
		subject = m.Page.Title + "|" + m.Page.URL
	case m.Frame != nil:
		subject = m.Frame.Why
	case m.Mark != nil:
		subject = m.Mark.By
	}
	return strings.Join([]string{m.At, itoa(m.Cue), m.Kind, subject}, " ")
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// --- A. The timeline an AI coworker reads ---

// TestWalkthroughMomentsFromDesktopLayers pins the whole derived timeline
// for a desktop-produced walkthrough: clicks named by the ax-tree, one dwell
// per deliberate rest (a short rest dropped), page changes from the top
// document only, keyframes in time order, each on its owning cue.
// Failure prevented: an agent asked "what was clicked when they said X" gets
// the wrong element, a phantom page change, or a moment on the wrong cue.
func TestWalkthroughMomentsFromDesktopLayers(t *testing.T) {
	_, d := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{})

	want := []string{
		"00:00:00.000 1 keyframe periodic",
		"00:00:00.500 1 page Team home|https://sageox.test/team/t1",
		"00:00:06.000 2 click AXLink:Saved",
		"00:00:06.500 2 keyframe scene-change",
		"00:00:07.000 2 page Saved|https://sageox.test/team/t1/saved",
		"00:00:11.000 3 dwell AXImage:Team mural for 2026-09-30",
		"00:00:12.000 3 keyframe periodic",
		"00:00:17.000 4 click AXGroup:<Pinned items",
		"00:00:18.000 4 click AXGroup:",
		"00:00:21.000 5 page Team home|https://sageox.test/team/t1",
	}
	var got []string
	for _, m := range d.Moments {
		got = append(got, momentLine(m))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("moments:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if d.Moments[5].DwellMS != 2500 {
		t.Errorf("dwell_ms = %d, want 2500", d.Moments[5].DwellMS)
	}
	if !d.Moments[8].Element.Unnamed || d.Moments[8].Element.Within != nil {
		t.Errorf("a group whose only named ancestor carries the window title must stay unnamed, got %+v", d.Moments[8].Element)
	}
	if d.Window.Total != len(want) || d.Window.Truncated {
		t.Errorf("window = %+v", d.Window)
	}
}

// TestWalkthroughInventoryAndTarget: the payload says which window was
// recorded and exactly which screen data exists, so an agent knows what it
// can and cannot ask before reading a single moment.
// Failure prevented: the CLI hides that layers exist (Ryan's "it doesn't
// know it's there").
func TestWalkthroughInventoryAndTarget(t *testing.T) {
	_, d := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{})

	if !d.ScreenRecording || d.Title != "Saved page walkthrough" || d.Duration != "00:00:25.200" {
		t.Errorf("header = %v %q %q", d.ScreenRecording, d.Title, d.Duration)
	}
	if d.Target == nil || *d.Target != (WalkthroughTarget{Kind: "window", App: "Browser", Title: "Team", Width: 1440, Height: 900}) {
		t.Errorf("target = %+v", d.Target)
	}
	if kf := d.Sources.Keyframes; kf == nil || *kf != (KeyframeInventory{Count: 3, Described: 2, Local: 1}) {
		t.Errorf("keyframes = %+v", d.Sources.Keyframes)
	}
	var kinds []string
	for _, l := range d.Sources.Layers {
		kinds = append(kinds, l.Kind)
	}
	if strings.Join(kinds, ",") != "pointer,ax-tree,keyframe-hints" {
		t.Errorf("layers = %v", kinds)
	}
	if len(d.Notes) != 1 || !strings.Contains(d.Notes[0], "1 of 3 keyframes have no description") {
		t.Errorf("notes = %q", d.Notes)
	}
	wantGaps := []WalkthroughGap{
		{From: "00:00:01.000", To: "00:00:02.000", Reason: "pointer-outside-target"},
		{From: "00:00:23.000", To: "00:00:24.000", Reason: "pointer-outside-target"},
	}
	if len(d.PointerGaps) != 2 || d.PointerGaps[0] != wantGaps[0] || d.PointerGaps[1] != wantGaps[1] {
		t.Errorf("pointer gaps = %+v", d.PointerGaps)
	}
}

// TestWalkthroughKeyframeImages: a real image in the checkout is opened in
// place, a stub carries the ox fetch command, and a stub that an earlier ox
// fetch already downloaded points at the cache copy — only when its size
// matches the pointer.
// Failure prevented: an agent re-downloads what is on disk, or is handed a
// half-written cache file as the image.
func TestWalkthroughKeyframeImages(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	cacheDir := filepath.Join(filepath.Dir(root), ".sageox", "cache", DiscussionsDirName, desktopWalkFolder, "keyframes")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 000-aaaa's pointer promises 7 bytes; 001-bbbb's promises 9.
	writeFile(t, filepath.Join(cacheDir, "000-aaaa.jpg"), "1234567")
	writeFile(t, filepath.Join(cacheDir, "001-bbbb.jpg"), "short")

	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	frames := map[string]*KeyframeRef{}
	for _, m := range d.Moments {
		if m.Frame != nil {
			frames[m.At] = m.Frame
		}
	}
	if f := frames["00:00:00.000"]; f.LocalImage != filepath.Join(cacheDir, "000-aaaa.jpg") || f.FetchCommand != "" {
		t.Errorf("fetched stub = %+v, want the cache copy and no fetch command", f)
	}
	if f := frames["00:00:06.500"]; f.LocalImage != "" || !strings.HasPrefix(f.FetchCommand, "ox fetch ") || !strings.HasSuffix(f.FetchCommand, "001-bbbb.jpg") {
		t.Errorf("stub with a wrong-size cache copy = %+v, want the fetch command", f)
	}
	if f := frames["00:00:12.000"]; f.LocalImage != filepath.Join(root, desktopWalkFolder, "keyframes", "002-cccc.jpg") || f.FetchCommand != "" {
		t.Errorf("real image = %+v, want it opened in place", f)
	}
	if kf := d.Sources.Keyframes; kf.Local != 2 {
		t.Errorf("local = %d, want 2", kf.Local)
	}
}

// TestTranscriptFramesShareLocalImage: transcript --frames uses the same
// image resolution, so the two views never disagree about what is local.
// Failure prevented: a frame shown with a fetch command in one view and as
// local in the other.
func TestTranscriptFramesShareLocalImage(t *testing.T) {
	env := New(desktopWalkRoot, time.Time{}).Transcript(desktopWalkCnv, TranscriptOptions{Frames: true, CueFirst: 3, CueLast: 3})
	data := transcriptData(t, env)
	if len(data.Cues) != 1 || len(data.Cues[0].Frames) != 1 {
		t.Fatalf("cue 3 = %+v", data.Cues)
	}
	f := data.Cues[0].Frames[0]
	if f.LocalImage == "" || f.FetchCommand != "" || f.Image != f.LocalImage {
		t.Errorf("real image frame = %+v", f)
	}
}

// --- B. Privacy: typed text and addresses stay out ---

// TestWalkthroughNeverEmitsTypedTextOrQuery: AX values and every URL part
// beyond scheme, host, and path never leave the reader.
// Failure prevented: a password, search, or token the presenter typed or
// carried in a link lands in an AI coworker's context.
func TestWalkthroughNeverEmitsTypedTextOrQuery(t *testing.T) {
	env, _ := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{})
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET", "token=", "#frag", "user:pw", "pw@"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("payload leaks %q:\n%s", secret, b)
		}
	}
}

func TestPageURL(t *testing.T) {
	cases := map[string]string{
		"https://a.test/x/y?q=1#f":          "https://a.test/x/y",
		"http://u:p@a.test:8080/p":          "http://a.test:8080/p",
		"file:///Users/me/secret.txt":       "",
		"javascript:alert(1)":               "",
		"":                                  "",
		"https://a.test/\u001b]0;pwn\u0007": "",
	}
	for in, want := range cases {
		if got := pageURL(in); got != want {
			t.Errorf("pageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- C. Windows and limits ---

// TestWalkthroughCueWindow: --cues serves exactly the moments whose owning
// cue is in range, and only the pointer gaps inside those cues.
// Failure prevented: "what was on screen during cues 2-3" returns moments
// from elsewhere in the recording.
func TestWalkthroughCueWindow(t *testing.T) {
	_, d := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{CueFirst: 2, CueLast: 3})
	if len(d.Moments) != 5 {
		t.Fatalf("moments = %d, want 5", len(d.Moments))
	}
	for _, m := range d.Moments {
		if m.Cue < 2 || m.Cue > 3 {
			t.Errorf("moment outside cues 2-3: %s", momentLine(m))
		}
	}
	if len(d.PointerGaps) != 0 {
		t.Errorf("gaps outside the window served: %+v", d.PointerGaps)
	}
	if len(d.Window.Cues) != 2 || d.Window.Cues[0] != 2 || d.Window.Cues[1] != 3 {
		t.Errorf("window = %+v", d.Window)
	}
}

func TestWalkthroughTimeWindow(t *testing.T) {
	_, d := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{HasWindow: true, FromOffset: 10 * time.Second, ToOffset: 18 * time.Second})
	var got []string
	for _, m := range d.Moments {
		got = append(got, m.At)
	}
	if strings.Join(got, ",") != "00:00:11.000,00:00:12.000,00:00:17.000,00:00:18.000" {
		t.Errorf("moments = %v", got)
	}
	if d.Window.From != "00:00:10.000" || d.Window.To != "00:00:18.000" {
		t.Errorf("window = %+v", d.Window)
	}
}

// TestWalkthroughLimitTruncatesAndSaysSo: the cap is honest — total counts
// everything in the window and guidance says how to narrow.
// Failure prevented: a long walkthrough silently loses its second half.
func TestWalkthroughLimitTruncatesAndSaysSo(t *testing.T) {
	env, d := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{Limit: 3})
	if len(d.Moments) != 3 || d.Window.Total != 10 || !d.Window.Truncated {
		t.Errorf("window = %+v, moments = %d", d.Window, len(d.Moments))
	}
	if !strings.Contains(env.Guidance, "--cues N-M") {
		t.Errorf("guidance does not say how to narrow: %q", env.Guidance)
	}
}

func TestWalkthroughInvalidSelectors(t *testing.T) {
	env := New(desktopWalkRoot, time.Time{}).Walkthrough(desktopWalkCnv, WalkthroughOptions{CueFirst: 3, CueLast: 2})
	if env.Success || env.Error.Code != ErrCodeInvalidSelector {
		t.Errorf("reversed range = %+v", env.Error)
	}
	env = New(desktopWalkRoot, time.Time{}).Walkthrough("not-an-id", WalkthroughOptions{})
	if env.Success || env.Error.Code != ErrCodeInvalidID {
		t.Errorf("bad id = %+v", env.Error)
	}
}

// --- D. Degradation: missing screen data is data, and is said ---

// TestWalkthroughKeyframesOnly is the state production walkthroughs are in
// while the layer upload is off for an account: the video and its keyframes
// arrived, the pointer and ax-tree layers did not.
// Failure prevented: the command errors, or reads as if nothing was clicked.
func TestWalkthroughKeyframesOnly(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.RemoveAll(filepath.Join(root, desktopWalkFolder, "layers")); err != nil {
		t.Fatal(err)
	}
	env, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	if !d.ScreenRecording || len(d.Moments) != 3 || d.Target != nil || len(d.Sources.Layers) != 0 {
		t.Fatalf("keyframes-only = %+v", d)
	}
	for _, m := range d.Moments {
		if m.Kind != MomentKeyframe {
			t.Errorf("unexpected %s moment", m.Kind)
		}
	}
	if !hasNote(d, "No pointer layer on disk") {
		t.Errorf("notes do not say the pointer layer is missing: %q", d.Notes)
	}
	if len(env.Warnings) != 0 {
		t.Errorf("a missing layer is not a warning: %q", env.Warnings)
	}
}

// TestWalkthroughLayersWithoutKeyframes: the server's keyframe extraction
// produced nothing, but the presenter's clicks and pages still answer
// "what was on screen".
func TestWalkthroughLayersWithoutKeyframes(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.Remove(filepath.Join(root, desktopWalkFolder, KeyframesFileName)); err != nil {
		t.Fatal(err)
	}
	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	if !d.ScreenRecording || d.Sources.Keyframes != nil || len(d.Moments) != 7 {
		t.Fatalf("no-keyframes = %+v", d)
	}
	if !hasNote(d, "No keyframes on disk") {
		t.Errorf("notes = %q", d.Notes)
	}
}

// TestWalkthroughPointerWithoutAXTree: clicks still land on the timeline,
// unnamed, and the note says why.
func TestWalkthroughPointerWithoutAXTree(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.RemoveAll(filepath.Join(root, desktopWalkFolder, "layers", "ax-tree.clyr_01a0f484-0000-7000-8000-0000000000c2")); err != nil {
		t.Fatal(err)
	}
	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	clicks := 0
	for _, m := range d.Moments {
		if m.Kind == MomentPage {
			t.Errorf("page moment without an ax-tree: %s", momentLine(m))
		}
		if m.Kind == MomentClick {
			clicks++
			if !m.Element.Unnamed {
				t.Errorf("click named without an ax-tree: %+v", m.Element)
			}
		}
	}
	if clicks != 3 || !hasNote(d, "No accessibility layer") {
		t.Errorf("clicks = %d, notes = %q", clicks, d.Notes)
	}
}

// TestWalkthroughWithoutTranscript: moments are still served by time, with
// no cue numbers; a cue range is a typed transcript error.
func TestWalkthroughWithoutTranscript(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.Remove(filepath.Join(root, desktopWalkFolder, "transcript.vtt")); err != nil {
		t.Fatal(err)
	}
	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	if len(d.Moments) != 10 || d.Moments[0].Cue != 0 || !hasNote(d, "No transcript yet") {
		t.Errorf("no-transcript = %d moments, cue %d, notes %q", len(d.Moments), d.Moments[0].Cue, d.Notes)
	}
	env := New(root, time.Time{}).Walkthrough(desktopWalkCnv, WalkthroughOptions{CueFirst: 1, CueLast: 2})
	if env.Success || env.Error.Code != ErrCodeTranscriptNotAvailable {
		t.Errorf("cue range without a transcript = %+v", env.Error)
	}
}

// TestWalkthroughUnreadableTranscript: a transcript that exists but does
// not parse is reported as unreadable, never as "no transcript yet".
func TestWalkthroughUnreadableTranscript(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.WriteFile(filepath.Join(root, desktopWalkFolder, "transcript.vtt"), []byte("not webvtt"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	if hasNote(d, "No transcript yet") || !hasNote(d, "could not be read") {
		t.Errorf("unreadable transcript notes = %q", d.Notes)
	}
	if len(env.Warnings) == 0 {
		t.Errorf("unreadable transcript left no warning")
	}
}

// TestWalkthroughOnAudioDiscussion: an ordinary discussion is not an
// error — it says it has no screen data and points at the transcript.
func TestWalkthroughOnAudioDiscussion(t *testing.T) {
	env, d := readWalkthrough(t, desktopWalkRoot, desktopAudioCnv, WalkthroughOptions{})
	if d.ScreenRecording || len(d.Moments) != 0 || !hasNote(d, "Not a screen walkthrough") {
		t.Errorf("audio = %+v", d)
	}
	if !strings.Contains(env.Guidance, "ox conversation transcript") {
		t.Errorf("guidance = %q", env.Guidance)
	}
}

// TestShowNamesWalkthroughForLayersOnlyRecording: a walkthrough whose
// keyframes never arrived is still announced as one.
// Failure prevented: show stays silent about screen data because the
// server's extractor failed.
func TestShowNamesWalkthroughForLayersOnlyRecording(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	if err := os.Remove(filepath.Join(root, desktopWalkFolder, KeyframesFileName)); err != nil {
		t.Fatal(err)
	}
	env := New(root, time.Time{}).Show(desktopWalkCnv)
	if !strings.Contains(env.Guidance, "ox conversation walkthrough "+desktopWalkCnv) {
		t.Errorf("show guidance = %q", env.Guidance)
	}
	audio := New(root, time.Time{}).Show(desktopAudioCnv)
	if strings.Contains(audio.Guidance, "walkthrough") {
		t.Errorf("audio show guidance names a walkthrough: %q", audio.Guidance)
	}
}

// --- E. Bounds ---

// TestPointerMomentsDwellRules pins the dwell episode rules directly: a
// rest ends at a hidden row, a transition, a move, or a new element, and
// only rests of minDwell or longer count.
func TestPointerMomentsDwellRules(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	rows := strings.Join([]string{
		`{"t_ms":10000,"vis":true,"dwell_ms":1500,"ax_ref":"a"}`,
		`{"t_ms":10500,"vis":true,"dwell_ms":2000,"ax_ref":"a"}`,
		`{"t_ms":11000,"vis":false}`,
		`{"t_ms":13000,"vis":true,"dwell_ms":2500,"ax_ref":"a"}`,
		`{"t_ms":13500,"vis":true,"dwell_ms":600,"ax_ref":"b"}`,
		`{"t_ms":14000,"vis":true,"dwell_ms":2100,"ax_ref":"b"}`,
		`{"t_ms":14500,"target":{"size":{"w":1,"h":1}},"vis":true,"dwell_ms":3500,"ax_ref":"b"}`,
		`{"t_ms":15000,"vis":true,"dwell_ms":1999,"ax_ref":"c"}`,
		`{"t_ms":20000,"vis":true,"dwell_ms":1e300,"ax_ref":"d"}`,
	}, "\n")
	writeFile(t, filepath.Join(root, desktopWalkFolder, "layers", "pointer.clyr_01a0f484-0000-7000-8000-0000000000c1", "pointer.jsonl"), rows)
	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	var dwells []string
	for _, m := range d.Moments {
		if m.Kind == MomentDwell {
			dwells = append(dwells, m.At+"/"+itoa(int(m.DwellMS)))
		}
	}
	// a rests until the hidden row (began 8.5s, peak 2s); a's fresh rest
	// after it ends when b's element takes over; b's rest is cut by the
	// transition; c is under minDwell; d's absurd value is not a rest.
	want := "00:00:08.500/2000,00:00:10.500/2500,00:00:12.900/2100"
	if strings.Join(dwells, ",") != want {
		t.Errorf("dwells = %v, want %s", dwells, want)
	}
}

// TestDwellNamedWhenNodeRowArrivesWithFirstSample: the producer writes an
// element's node row when a pointer row first references it, and on a 2 Hz
// grid that first row can already carry dwell_ms. The rest is back-dated
// to where it began, but the element is named at the first row.
// Failure prevented: a deliberate rest on a never-hovered element reads as
// "unnamed" although the accessibility layer names it.
func TestDwellNamedWhenNodeRowArrivesWithFirstSample(t *testing.T) {
	root := stageDesktopWalkthrough(t)
	layers := filepath.Join(root, desktopWalkFolder, "layers")
	writeFile(t, filepath.Join(layers, "pointer.clyr_01a0f484-0000-7000-8000-0000000000c1", "pointer.jsonl"), strings.Join([]string{
		`{"t_ms":10000,"vis":true,"dwell_ms":400,"ax_ref":"axn_new"}`,
		`{"t_ms":10500,"vis":true,"dwell_ms":900,"ax_ref":"axn_new"}`,
		`{"t_ms":11000,"vis":true,"dwell_ms":1400,"ax_ref":"axn_new"}`,
		`{"t_ms":11500,"vis":true,"dwell_ms":1900,"ax_ref":"axn_new"}`,
		`{"t_ms":12000,"vis":true,"dwell_ms":2400,"ax_ref":"axn_new"}`,
		`{"t_ms":12500,"vis":false}`,
	}, "\n"))
	writeFile(t, filepath.Join(layers, "ax-tree.clyr_01a0f484-0000-7000-8000-0000000000c2", "ax.jsonl"), strings.Join([]string{
		`{"t_ms":10000,"snapshot":"axs_1","reason":"hover","hit":"axn_new"}`,
		`{"t_ms":10000,"snapshot":"axs_1","ax_ref":"axn_new","role":"AXButton","title":"Export"}`,
	}, "\n"))
	_, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{})
	for _, m := range d.Moments {
		if m.Kind != MomentDwell {
			continue
		}
		if m.At != "00:00:09.600" || m.Element.Unnamed || m.Element.Title != "Export" {
			t.Errorf("dwell = %s %+v, want 00:00:09.600 on the Export button", m.At, m.Element)
		}
		return
	}
	t.Fatal("no dwell moment")
}

// TestLongAXTreeKeepsLateSnapshots: interval snapshots re-walk the same
// elements every few seconds; repeats must not exhaust the kept-row cap
// before the recording's later pages and elements are read.
// Failure prevented: a long walkthrough loses every page change and
// element name after its first several minutes.
func TestLongAXTreeKeepsLateSnapshots(t *testing.T) {
	if testing.Short() {
		t.Skip("short: writes and parses a 70k-row accessibility sidecar")
	}
	root := stageDesktopWalkthrough(t)
	var b strings.Builder
	snapshots := (maxSidecarRows / 3) + 1000 // three node rows each: well past the cap
	for i := 0; i < snapshots; i++ {
		ms := 1000 + i
		fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_%d","reason":"interval"}`+"\n", ms, i)
		fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_%d","ax_ref":"axn_win","depth":0,"role":"AXWindow","title":"Team - Browser"}`+"\n", ms, i)
		fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_%d","ax_ref":"axn_web","parent":"axn_win","depth":1,"role":"AXWebArea","title":"Team home","url":"https://sageox.test/team/t1"}`+"\n", ms, i)
		fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_%d","ax_ref":"axn_btn","parent":"axn_web","depth":2,"role":"AXButton","title":"Save"}`+"\n", ms, i)
	}
	late := 1000 + snapshots
	fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_late","reason":"interval"}`+"\n", late)
	fmt.Fprintf(&b, `{"t_ms":%d,"snapshot":"axs_late","ax_ref":"axn_web","parent":"axn_win","depth":1,"role":"AXWebArea","title":"Settings","url":"https://sageox.test/team/t1/settings"}`+"\n", late)
	writeFile(t, filepath.Join(root, desktopWalkFolder, "layers", "ax-tree.clyr_01a0f484-0000-7000-8000-0000000000c2", "ax.jsonl"), b.String())

	env, d := readWalkthrough(t, root, desktopWalkCnv, WalkthroughOptions{Limit: 1000})
	for _, w := range env.Warnings {
		if strings.Contains(w, "usable rows") {
			t.Errorf("repeated rows hit the kept-row cap: %s", w)
		}
	}
	var pages []string
	for _, m := range d.Moments {
		if m.Kind == MomentPage {
			pages = append(pages, m.Page.Title)
		}
	}
	if strings.Join(pages, ",") != "Team home,Settings" {
		t.Errorf("pages = %v, want the late Settings page reached", pages)
	}
}

// TestWalkthroughFollowsCitationSelectors: a sageox:// citation carries its
// own window, as it does for transcript; explicit flags still win.
// Failure prevented: an AI coworker following a cited moment gets the whole
// recording instead of the cited cues.
func TestWalkthroughFollowsCitationSelectors(t *testing.T) {
	base := "sageox://" + desktopWalkCnv + "/clyr_01a0f48b-0000-7000-8000-0000000000d1@1#"
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	ms := func(sec float64) int64 { return t0.Add(time.Duration(sec * float64(time.Second))).UnixMilli() }

	cases := []struct {
		name, uri string
		opts      WalkthroughOptions
		want      []int // cues of the served moments
	}{
		{"cue range", base + "cue=2-3", WalkthroughOptions{}, []int{2, 2, 2, 3, 3}},
		{"time range", base + fmt.Sprintf("t=%d--%d", ms(16), ms(19)), WalkthroughOptions{}, []int{4, 4}},
		{"instant picks its cue", base + fmt.Sprintf("t=%d", ms(11.5)), WalkthroughOptions{}, []int{3, 3}},
		{"explicit flags win", base + "cue=2-3", WalkthroughOptions{CueFirst: 5, CueLast: 5}, []int{5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, d := readWalkthrough(t, desktopWalkRoot, c.uri, c.opts)
			var got []int
			for _, m := range d.Moments {
				got = append(got, m.Cue)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("cues = %v, want %v", got, c.want)
			}
		})
	}
}

// --- Area takes and presenter marks ---

// Area fixture: testdata/walkthrough-area/discussions holds one SageOx Desktop
// AREA take — target_initial {kind:"area", size, backing_scale, display_id,
// rect} with no app or title, pointer x/y relative to the rect — whose
// keyframe-hints layer carries three presenter marks (written out of order,
// one at the same instant as a click) beside inferred click and dwell hints.
// It has no keyframes yet.
const (
	areaWalkRoot = "testdata/walkthrough-area/discussions"
	areaWalkCnv  = "cnv_01a0f490-0000-7000-8000-0000000000d1"
)

// TestWalkthroughAreaTarget: an area take reads as "a screen area" of a size,
// never as a window with a blank app and title.
// Failure prevented: an agent tells the user "you recorded an untitled
// window" or names the one app it happens to see as the whole recording.
func TestWalkthroughAreaTarget(t *testing.T) {
	env, d := readWalkthrough(t, areaWalkRoot, areaWalkCnv, WalkthroughOptions{})

	if d.Target == nil || *d.Target != (WalkthroughTarget{Kind: "area", Width: 800, Height: 600}) {
		t.Errorf("target = %+v", d.Target)
	}
	if !hasNote(d, "a screen area (800 × 600)") {
		t.Errorf("notes must say a screen area was recorded, got %q", d.Notes)
	}
	if !strings.Contains(env.Guidance, "a screen area") {
		t.Errorf("guidance = %q", env.Guidance)
	}
	_, w := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{})
	if hasNote(w, "screen area") {
		t.Errorf("a window take must not read as an area, got %q", w.Notes)
	}
}

// TestWalkthroughMarksAreMoments: a presenter mark (keyframe-hints reason
// "mark") is its own moment, in time order, ahead of anything else at the
// same instant, and distinct from the inferred click and dwell hints, which
// stay out of the timeline (the pointer layer already says what was clicked).
// Failure prevented: the one moment the presenter flagged on purpose reads
// like any other click, or not at all.
func TestWalkthroughMarksAreMoments(t *testing.T) {
	env, d := readWalkthrough(t, areaWalkRoot, areaWalkCnv, WalkthroughOptions{})

	want := []string{
		"00:00:00.500 1 page Checkout flow – Figma|",
		"00:00:02.000 1 mark presenter",
		"00:00:04.000 1 mark presenter",
		"00:00:04.000 1 click AXButton:Pay now",
		"00:00:09.000 2 mark presenter",
	}
	var got []string
	var seqs []int
	for _, m := range d.Moments {
		got = append(got, momentLine(m))
		if m.Mark != nil {
			seqs = append(seqs, m.Mark.Seq)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("moments:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if fmt.Sprint(seqs) != "[1 2 3]" {
		t.Errorf("mark seqs = %v", seqs)
	}
	if !strings.Contains(env.Guidance, "marked by the presenter") {
		t.Errorf("guidance must point at marks, got %q", env.Guidance)
	}
	if _, w := readWalkthrough(t, desktopWalkRoot, desktopWalkCnv, WalkthroughOptions{}); strings.Contains(fmt.Sprint(w.Moments), MomentMark) {
		t.Errorf("a take without marks has no mark moments")
	}

	// A cue window keeps only the marks inside it.
	_, c := readWalkthrough(t, areaWalkRoot, areaWalkCnv, WalkthroughOptions{CueFirst: 2, CueLast: 2})
	if len(c.Moments) != 1 || c.Moments[0].Kind != MomentMark {
		t.Errorf("cue 2 moments = %+v", c.Moments)
	}
}

func hasNote(d *WalkthroughData, sub string) bool {
	for _, n := range d.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
