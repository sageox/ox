package read

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/vtt"
)

// PlaybackSpec is the playback document version this reader understands. A
// different version is refused with ErrPlaybackSpec rather than guessed at:
// joining fields whose meaning changed would put wrong evidence in front of
// the reader.
const PlaybackSpec = "walkthrough-playback/1"

// ErrPlaybackSpec marks a playback document written for a version of the
// format this ox does not know.
var ErrPlaybackSpec = errors.New("unsupported walkthrough playback version")

// PlaybackImageMaxBytes bounds one downloaded image. Crops are capped at a few
// hundred kilobytes by the producer; this is a ceiling against a broken or
// hostile response, not a target.
const PlaybackImageMaxBytes = 8 << 20

// Bounds on an untrusted playback document. A five-minute walkthrough holds a
// few dozen moments; anything near these caps is corrupt or hostile.
const (
	maxPlaybackMoments    = 2000
	maxPlaybackSpans      = 500
	maxPlaybackPages      = 500
	maxPlaybackChapters   = 200
	maxPlaybackShortcuts  = 500
	maxShortcutLabelRunes = 24
	maxShortcutCount      = 999
	// maxPlaybackElements is the producer's cap on a plural moment's elements.
	maxPlaybackElements = 3
	maxSaidRunes        = 600
	maxKindRunes        = 24
)

// ImageBox is a rectangle in normalized (0..1) video-frame coordinates.
type ImageBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// PlaybackImage is one evidence image of a joined moment: a focus crop, or
// the whole frame with the crop outlined. LocalImage is set once the bytes are
// on this machine; until then FetchCommand (focus only) downloads them.
type PlaybackImage struct {
	Width        int       `json:"width,omitempty"`
	Height       int       `json:"height,omitempty"`
	Box          *ImageBox `json:"box,omitempty"`
	Availability string    `json:"availability,omitempty"`
	LocalImage   string    `json:"local_image,omitempty"`
	FetchCommand string    `json:"fetch_command,omitempty"`

	// URL is a short-lived signed download link: a credential. It is kept off
	// every output (json:"-"), and the text renderer never prints it.
	URL string `json:"-"`
	// CachePath is where a download of this image is stored.
	CachePath string `json:"-"`
}

// MomentOutcome is what the screen did right after a click. Box is where, in
// normalized frame coordinates, when all that is known is that the screen
// changed.
type MomentOutcome struct {
	AfterMS int64     `json:"after_ms"`
	Label   string    `json:"label"`
	Box     *ImageBox `json:"box,omitempty"`
}

// WalkthroughChapter is a stretch of the recording about one page or topic.
// It runs from At to the next chapter's start.
type WalkthroughChapter struct {
	Title string `json:"title"`
	At    string `json:"at"`
	URL   string `json:"url,omitempty"`

	AtMS int64 `json:"-"`
}

// WalkthroughShortcut is a keyboard shortcut the narrator pressed, recorded
// only when they turned shortcut display on. Count is how many times in a
// row.
type WalkthroughShortcut struct {
	At    string `json:"at"`
	Label string `json:"label"`
	Count int    `json:"count"`

	AtMS int64 `json:"-"`
}

// WalkthroughSpan is a stretch of the recording with nothing to watch (quiet)
// or the narrator typing (typing).
type WalkthroughSpan struct {
	Kind    string         `json:"kind"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Label   string         `json:"label,omitempty"`
	Skip    bool           `json:"skip,omitempty"`
	Speed   int            `json:"speed,omitempty"`
	Element *ScreenElement `json:"element,omitempty"`

	FromMS int64 `json:"-"`
	ToMS   int64 `json:"-"`
}

// WalkthroughPage is a navigation: the page the captured window moved to.
type WalkthroughPage struct {
	At    string `json:"at"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`

	AtMS int64 `json:"-"`
}

// Playback is a parsed, sanitized playback document.
type Playback struct {
	recordingID string
	generatedAt string
	// generation keys the image cache: a rebuilt document may carry different
	// crops under the same moment ids, and must never be served stale ones.
	generation string
	duration   time.Duration
	target     *WalkthroughTarget
	moments    []playbackMoment
	spans      []WalkthroughSpan
	pages      []WalkthroughPage
	chapters   []WalkthroughChapter
	shortcuts  []WalkthroughShortcut
	// camera and audio only record that those tracks exist. Camera frames are
	// never offered to agents, and neither track's link is kept.
	camera bool
	audio  bool
}

// HasMoments reports whether the document joined any moment. Without one the
// reader keeps its own moments, so a recording the producer could not join is
// not emptied.
func (p *Playback) HasMoments() bool { return p != nil && len(p.moments) > 0 }

type playbackMoment struct {
	id         string
	at         time.Duration
	kind       string
	cue        int
	said       string
	deixisWord string
	strength   string
	cursor     string
	element    *ScreenElement
	elements   []*ScreenElement
	then       *MomentOutcome
	focus      *playbackImage
	overview   *playbackImage
}

type playbackImage struct {
	width, height int
	box           *ImageBox
	url           string
}

// --- wire shape ---

type playbackDoc struct {
	Spec        string             `json:"spec"`
	RecordingID string             `json:"recording_id"`
	GeneratedAt string             `json:"generated_at"`
	DurationMS  float64            `json:"duration_ms"`
	Target      *playbackTargetDoc `json:"target"`
	// Entries are decoded one by one (decodeEntries), so one with a field of
	// the wrong type is dropped instead of failing the whole document.
	Moments []json.RawMessage `json:"moments"`
	Spans   []json.RawMessage `json:"spans"`
	Pages   []json.RawMessage `json:"pages"`

	Chapters  []json.RawMessage `json:"chapters"`
	Shortcuts []json.RawMessage `json:"shortcuts"`
	// Camera and Audio are read only for their presence: the tracks are not
	// something an agent is given.
	Camera       json.RawMessage `json:"camera"`
	Audio        json.RawMessage `json:"audio"`
	Capabilities struct {
		Camera bool `json:"camera"`
		Audio  bool `json:"audio"`
	} `json:"capabilities"`
}

// decodeEntries decodes each raw entry into T, skipping those that do not fit,
// and stops at limit decoded entries.
func decodeEntries[T any](raws []json.RawMessage, limit int) []T {
	var out []T
	for _, raw := range raws {
		if len(out) == limit {
			break
		}
		var entry T
		if json.Unmarshal(raw, &entry) == nil {
			out = append(out, entry)
		}
	}
	return out
}

type playbackTargetDoc struct {
	Kind   string `json:"kind"`
	App    string `json:"app"`
	Title  string `json:"title"`
	SizePT *struct {
		W float64 `json:"w"`
		H float64 `json:"h"`
	} `json:"size_pt"`
}

type playbackElementDoc struct {
	Role   string `json:"role"`
	Name   string `json:"name"`
	DOMID  string `json:"dom_id"`
	Within bool   `json:"within"`
}

type playbackImageDoc struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Box    *ImageBox `json:"box"`
	URL    string    `json:"url"`
}

type playbackMomentDoc struct {
	ID         string  `json:"id"`
	TMS        float64 `json:"t_ms"`
	Kind       string  `json:"kind"`
	Cue        int     `json:"cue"`
	Said       string  `json:"said"`
	Deixis     bool    `json:"deixis"`
	DeixisWord *struct {
		Text string `json:"text"`
	} `json:"deixis_word"`
	Strength      string               `json:"strength"`
	Element       *playbackElementDoc  `json:"element"`
	Elements      []playbackElementDoc `json:"elements"`
	Cursor        string               `json:"cursor"`
	FocusImage    *playbackImageDoc    `json:"focus_image"`
	OverviewImage *playbackImageDoc    `json:"overview_image"`
	Then          *struct {
		AfterMS float64   `json:"after_ms"`
		Label   string    `json:"label"`
		Box     *ImageBox `json:"box"`
	} `json:"then"`
}

type playbackSpanDoc struct {
	Kind    string              `json:"kind"`
	StartMS float64             `json:"start_ms"`
	EndMS   float64             `json:"end_ms"`
	Skip    bool                `json:"skip"`
	Speed   float64             `json:"speed"`
	Label   string              `json:"label"`
	Element *playbackElementDoc `json:"element"`
}

type playbackChapterDoc struct {
	StartMS float64 `json:"start_ms"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
}

type playbackShortcutDoc struct {
	TMS   float64 `json:"t_ms"`
	Label string  `json:"label"`
	Count float64 `json:"count"`
}

type playbackPageDoc struct {
	TMS   float64 `json:"t_ms"`
	URL   string  `json:"url"`
	Title string  `json:"title"`
}

// ParsePlayback parses and sanitizes a playback document. Everything in it is
// untrusted evidence: text is cleaned and capped, times are range-checked, and
// a malformed entry is dropped rather than failing the whole read.
func ParsePlayback(raw []byte) (*Playback, error) {
	var doc playbackDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("playback data is not valid JSON: %w", err)
	}
	if doc.Spec != PlaybackSpec {
		return nil, fmt.Errorf("%w %q", ErrPlaybackSpec, cleanScreenTextN(doc.Spec, 40))
	}
	p := &Playback{
		recordingID: doc.RecordingID,
		generatedAt: cleanScreenTextN(doc.GeneratedAt, 40),
	}
	p.generation = digest([]byte(doc.RecordingID + "\n" + doc.GeneratedAt))[:16]
	if d, ok := millisOffset(doc.DurationMS); ok {
		p.duration = d
	}
	p.target = playbackTarget(doc.Target)

	for _, m := range decodeEntries[playbackMomentDoc](doc.Moments, maxPlaybackMoments) {
		at, ok := millisOffset(m.TMS)
		if !ok {
			continue
		}
		pm := playbackMoment{
			id:      m.ID,
			at:      at,
			kind:    strings.ToLower(cleanScreenTextN(m.Kind, maxKindRunes)),
			cue:     max(m.Cue, 0),
			said:    cleanScreenTextN(m.Said, maxSaidRunes),
			cursor:  strings.ToLower(cleanScreenTextN(m.Cursor, maxKindRunes)),
			element: playbackElement(m.Element),
			focus:   playbackImageOf(m.FocusImage),
		}
		pm.overview = playbackImageOf(m.OverviewImage)
		if w := m.DeixisWord; w != nil {
			pm.deixisWord = cleanScreenTextN(w.Text, 40)
		}
		switch {
		case m.Strength == "strong" || m.Strength == "weak":
			pm.strength = m.Strength
		case m.Deixis:
			// A document that predates strength still said the narrator pointed.
			pm.strength = "strong"
		}
		pm.elements = playbackElements(m.Elements)
		if t := m.Then; t != nil && t.AfterMS >= 0 && t.AfterMS <= 60_000 {
			if label := cleanScreenTextN(t.Label, 120); label != "" {
				pm.then = &MomentOutcome{AfterMS: int64(t.AfterMS), Label: label}
				if validBox(t.Box) {
					pm.then.Box = t.Box
				}
			}
		}
		if pm.kind == "" {
			continue
		}
		p.moments = append(p.moments, pm)
	}
	sort.SliceStable(p.moments, func(i, j int) bool { return p.moments[i].at < p.moments[j].at })

	for _, s := range decodeEntries[playbackSpanDoc](doc.Spans, maxPlaybackSpans) {
		from, okFrom := millisOffset(s.StartMS)
		to, okTo := millisOffset(s.EndMS)
		kind := strings.ToLower(cleanScreenTextN(s.Kind, maxKindRunes))
		if !okFrom || !okTo || to <= from || kind == "" {
			continue
		}
		span := WalkthroughSpan{
			Kind: kind, From: formatVTTTimestamp(from), To: formatVTTTimestamp(to),
			FromMS: from.Milliseconds(), ToMS: to.Milliseconds(),
			Label: cleanScreenTextN(s.Label, 120), Skip: s.Skip,
			Element: playbackElement(s.Element),
		}
		if s.Speed >= 1 && s.Speed <= 16 {
			span.Speed = int(s.Speed)
		}
		p.spans = append(p.spans, span)
	}
	sort.SliceStable(p.spans, func(i, j int) bool { return p.spans[i].FromMS < p.spans[j].FromMS })

	for _, pg := range decodeEntries[playbackPageDoc](doc.Pages, maxPlaybackPages) {
		at, ok := millisOffset(pg.TMS)
		title, link := cleanScreenText(pg.Title), cleanScreenText(pg.URL)
		if !ok || title == "" && link == "" {
			continue
		}
		p.pages = append(p.pages, WalkthroughPage{At: formatVTTTimestamp(at), AtMS: at.Milliseconds(), Title: title, URL: link})
	}
	sort.SliceStable(p.pages, func(i, j int) bool { return p.pages[i].AtMS < p.pages[j].AtMS })

	for _, c := range decodeEntries[playbackChapterDoc](doc.Chapters, maxPlaybackChapters) {
		at, ok := millisOffset(c.StartMS)
		title := cleanScreenText(c.Title)
		if !ok || title == "" {
			continue
		}
		p.chapters = append(p.chapters, WalkthroughChapter{Title: title, At: formatVTTTimestamp(at), AtMS: at.Milliseconds(), URL: cleanScreenText(c.URL)})
	}
	sort.SliceStable(p.chapters, func(i, j int) bool { return p.chapters[i].AtMS < p.chapters[j].AtMS })

	for _, sc := range decodeEntries[playbackShortcutDoc](doc.Shortcuts, maxPlaybackShortcuts) {
		at, ok := millisOffset(sc.TMS)
		label := cleanScreenTextN(sc.Label, maxShortcutLabelRunes)
		if !ok || label == "" {
			continue
		}
		count := 1
		if sc.Count >= 1 && sc.Count <= maxShortcutCount {
			count = int(sc.Count)
		}
		p.shortcuts = append(p.shortcuts, WalkthroughShortcut{At: formatVTTTimestamp(at), AtMS: at.Milliseconds(), Label: label, Count: count})
	}
	sort.SliceStable(p.shortcuts, func(i, j int) bool { return p.shortcuts[i].AtMS < p.shortcuts[j].AtMS })

	p.camera = doc.Capabilities.Camera || isJSONObject(doc.Camera)
	p.audio = doc.Capabilities.Audio || isJSONObject(doc.Audio)
	return p, nil
}

// isJSONObject reports whether raw holds a JSON object, as opposed to being
// absent or null.
func isJSONObject(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{'
}

// ChapterWindow returns the media-clock window of the chapter named title: from
// its start to just before the next chapter's (the last runs to the end of the
// recording). Titles match case-insensitively and exactly first, then by one
// unambiguous substring. A title that matches nothing, or more than one
// chapter, is an error that lists the choices, never a guess.
func (p *Playback) ChapterWindow(title string) (from, to time.Duration, err *Error) {
	if p == nil || len(p.chapters) == 0 {
		return 0, 0, newError(ErrCodeInvalidSelector, "this recording has no chapters to select")
	}
	want := normalizeChapterTitle(title)
	var exact, partial []int
	for i, c := range p.chapters {
		have := normalizeChapterTitle(c.Title)
		switch {
		case have == want:
			exact = append(exact, i)
		case want != "" && strings.Contains(have, want):
			partial = append(partial, i)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return 0, 0, newError(ErrCodeInvalidSelector, fmt.Sprintf("no chapter is titled %q; chapters: %s", cleanScreenText(title), p.chapterList(nil)))
	case 1:
	default:
		return 0, 0, newError(ErrCodeInvalidSelector, fmt.Sprintf("%d chapters match %q (%s); use --from/--to for one of them", len(matches), cleanScreenText(title), p.chapterList(matches)))
	}
	i := matches[0]
	from = time.Duration(p.chapters[i].AtMS) * time.Millisecond
	to = maxMediaOffset
	if p.duration > 0 {
		to = p.duration
	}
	if i+1 < len(p.chapters) {
		to = time.Duration(p.chapters[i+1].AtMS)*time.Millisecond - time.Millisecond
	}
	return from, max(to, from), nil
}

func normalizeChapterTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// chapterList names chapters (all when only is nil) with their start, capped
// so a long list cannot flood an error.
func (p *Playback) chapterList(only []int) string {
	var parts []string
	add := func(i int) {
		if len(parts) < 12 {
			parts = append(parts, fmt.Sprintf("%q at %s", p.chapters[i].Title, strings.TrimSuffix(p.chapters[i].At, ".000")))
		}
	}
	if only == nil {
		for i := range p.chapters {
			add(i)
		}
	} else {
		for _, i := range only {
			add(i)
		}
	}
	return strings.Join(parts, ", ")
}

// millisOffset converts an untrusted millisecond value to a media offset.
func millisOffset(ms float64) (time.Duration, bool) {
	if math.IsNaN(ms) || ms < 0 || ms > float64(maxMediaOffset.Milliseconds()) {
		return 0, false
	}
	return time.Duration(ms * float64(time.Millisecond)), true
}

// playbackTarget maps the document's target the way the layer envelope's is
// mapped: an area has no app or title of its own.
func playbackTarget(t *playbackTargetDoc) *WalkthroughTarget {
	if t == nil {
		return nil
	}
	out := &WalkthroughTarget{App: cleanScreenText(t.App), Title: cleanScreenText(t.Title)}
	switch t.Kind {
	case targetKindWindow:
		out.Kind = targetKindWindow
	case targetKindArea:
		out.Kind, out.App, out.Title = targetKindArea, "", ""
	}
	if s := t.SizePT; s != nil && s.W > 0 && s.H > 0 && s.W < 1e6 && s.H < 1e6 {
		out.Width, out.Height = int(s.W), int(s.H)
	}
	if *out == (WalkthroughTarget{}) {
		return nil
	}
	return out
}

// playbackElement maps a document element. Name is the playback spelling of
// the accessibility title; for a text selection it holds the selected text.
func playbackElement(e *playbackElementDoc) *ScreenElement {
	if e == nil {
		return nil
	}
	out := &ScreenElement{Role: cleanScreenText(e.Role), Name: cleanScreenText(e.Name), DOMID: cleanScreenText(e.DOMID)}
	if *out == (ScreenElement{}) {
		return nil
	}
	out.Ancestor = e.Within
	out.Unnamed = out.Name == "" && out.DOMID == ""
	return out
}

// playbackElements maps a plural moment's elements. One element is just the
// moment's own element again, so fewer than two means no list.
func playbackElements(docs []playbackElementDoc) []*ScreenElement {
	var out []*ScreenElement
	for i := range docs {
		if len(out) == maxPlaybackElements {
			break
		}
		if e := playbackElement(&docs[i]); e != nil {
			out = append(out, e)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func playbackImageOf(img *playbackImageDoc) *playbackImage {
	if img == nil {
		return nil
	}
	out := &playbackImage{url: img.URL}
	if img.Width > 0 && img.Width <= 32768 && img.Height > 0 && img.Height <= 32768 {
		out.width, out.height = img.Width, img.Height
	}
	if validBox(img.Box) {
		out.box = img.Box
	}
	return out
}

func inUnit(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 1 }

// validBox reports whether b is a rectangle inside the normalized frame. A
// sliver of float rounding past the edge is allowed.
func validBox(b *ImageBox) bool {
	return b != nil && inUnit(b.X) && inUnit(b.Y) && inUnit(b.W) && inUnit(b.H) && b.X+b.W <= 1.0001 && b.Y+b.H <= 1.0001
}

// joinPlayback joins the playback document into data for the selected window.
// cues is the transcript the window's cue range refers to; it may be nil.
//
// Joined moments replace the reader's own only when the document has some: a
// walkthrough without pointer data has no joined moments, and replacing its
// keyframes with nothing would leave the reader blind.
func (r *Reader) joinPlayback(data *WalkthroughData, id *ID, cues []vtt.Cue, opts WalkthroughOptions) {
	p := opts.Playback
	if p == nil {
		if opts.PlaybackNote != "" {
			data.Notes = append(data.Notes, opts.PlaybackNote)
		}
		return
	}
	if p.recordingID != "" && p.recordingID != id.RecordingID {
		data.Notes = append(data.Notes, "Playback data names a different recording and was ignored; showing the standard evidence index.")
		return
	}
	if !data.ScreenRecording {
		// The document proves this is a walkthrough even when its screen files
		// have not synced yet; the "no screen evidence" note no longer holds.
		data.ScreenRecording = true
		data.Notes = nil
	}
	if p.target != nil {
		t := *p.target
		data.Target = &t
	}
	if p.duration > 0 {
		data.Duration = formatVTTTimestamp(p.duration)
	}
	data.Sources.Camera, data.Sources.Audio = p.camera, p.audio
	if opts.Transcript {
		return
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultMomentLimit
	}
	lo, hi := time.Duration(0), maxMediaOffset
	switch {
	case opts.HasWindow:
		lo, hi = opts.FromOffset, opts.ToOffset
	case opts.CueFirst != 0:
		lo, hi = cueSpan(cues, opts.CueFirst, opts.CueLast)
	}

	if p.HasMoments() {
		var joined []WalkthroughMoment
		for _, pm := range p.moments {
			if opts.CueFirst != 0 && (pm.cue < opts.CueFirst || pm.cue > opts.CueLast) ||
				opts.HasWindow && (pm.at < opts.FromOffset || pm.at > opts.ToOffset) {
				continue
			}
			joined = append(joined, r.joinedMoment(p, id, pm))
		}
		data.Window.Total = len(joined)
		data.Window.Truncated = len(joined) > limit
		if data.Window.Truncated {
			joined = joined[:limit]
		}
		data.Moments = append([]WalkthroughMoment{}, joined...)
		data.Joined = true
	}

	for _, s := range p.spans {
		if time.Duration(s.ToMS)*time.Millisecond < lo || time.Duration(s.FromMS)*time.Millisecond > hi {
			continue
		}
		data.Spans = append(data.Spans, s)
	}
	for _, pg := range p.pages {
		if at := time.Duration(pg.AtMS) * time.Millisecond; at < lo || at > hi {
			continue
		}
		data.Pages = append(data.Pages, pg)
	}
	// A chapter is in the window if any part of it is: it runs until the next
	// one starts.
	for i, c := range p.chapters {
		start := time.Duration(c.AtMS) * time.Millisecond
		end := maxMediaOffset
		if i+1 < len(p.chapters) {
			end = time.Duration(p.chapters[i+1].AtMS) * time.Millisecond
		}
		if end <= lo || start > hi {
			continue
		}
		data.Chapters = append(data.Chapters, c)
	}
	for _, sc := range p.shortcuts {
		if at := time.Duration(sc.AtMS) * time.Millisecond; at < lo || at > hi {
			continue
		}
		data.Shortcuts = append(data.Shortcuts, sc)
	}
	if total := len(data.Chapters); total > limit {
		data.Chapters = data.Chapters[:limit]
		data.Notes = append(data.Notes, fmt.Sprintf("Chapters are capped at %d of %d; read the rest with --chapter, or narrow with --cues N-M or --from/--to.", limit, total))
	}
	if len(data.Shortcuts) > limit {
		data.Shortcuts = data.Shortcuts[:limit]
		data.Notes = append(data.Notes, fmt.Sprintf("Shortcuts are capped at %d; narrow with --cues N-M or --from/--to.", limit))
	}
	if len(data.Spans) > limit {
		data.Spans = data.Spans[:limit]
		data.Notes = append(data.Notes, fmt.Sprintf("Spans are capped at %d; narrow with --cues N-M or --from/--to.", limit))
	}
	if len(data.Pages) > limit {
		data.Pages = data.Pages[:limit]
		data.Notes = append(data.Notes, fmt.Sprintf("Pages are capped at %d; narrow with --cues N-M or --from/--to.", limit))
	}
	if data.Joined && data.Revision != "" {
		// The pin binds images and source words; the joined said text and cue
		// numbers come from the playback document, built from the transcript as
		// it stood at generated_at.
		data.Notes = append(data.Notes, "Moments are joined from playback data generated "+p.generatedAt+"; said text and cue numbers come from that data, not from the pinned revision. Read the pinned words with --transcript --revision.")
	}
}

// joinedMoment builds the output moment for one document moment.
func (r *Reader) joinedMoment(p *Playback, id *ID, pm playbackMoment) WalkthroughMoment {
	at := formatVTTTimestamp(pm.at)
	m := WalkthroughMoment{
		At: at, AtMS: pm.at.Milliseconds(), at: pm.at, Cue: pm.cue, Kind: pm.kind,
		Said: pm.said, DeixisWord: pm.deixisWord, Strength: pm.strength,
		Cursor: pm.cursor, Element: pm.element, Elements: pm.elements, Then: pm.then,
	}
	fetch := fmt.Sprintf("ox walkthrough %s --from %s --to %s --fetch", id.ConversationID, at, at)
	m.Focus = r.playbackImageRef(p, id, "focus", pm.id, pm.focus, fetch)
	m.Overview = r.playbackImageRef(p, id, "overview", pm.id, pm.overview, "")
	return m
}

// playbackImageRef resolves one image: already on this machine, downloadable,
// or unavailable. fetch is the command that downloads it, when there is one.
func (r *Reader) playbackImageRef(p *Playback, id *ID, kind, momentID string, img *playbackImage, fetch string) *PlaybackImage {
	if img == nil {
		return nil
	}
	ref := &PlaybackImage{URL: img.url, Availability: "unavailable"}
	path, ok := r.playbackImagePath(p, id, kind, momentID)
	if !ok {
		ref.URL = ""
		return ref
	}
	ref.CachePath = path
	switch {
	case cachedJPEG(path):
		// Size and crop box describe pixels the reader can now open; before
		// that they would only spend context on an image it may never fetch.
		ref.Width, ref.Height, ref.Box = img.width, img.height, img.box
		ref.LocalImage, ref.Availability = path, "local"
	case img.url != "":
		ref.Availability, ref.FetchCommand = "unfetched", fetch
	}
	return ref
}

// playbackImagePath is where an image of this document is cached. It sits
// beside the discussions tree, where `ox fetch` keeps its cache, keyed by
// recording and document generation so a rebuilt document never reads an old
// crop. The moment id comes from the document, so it is held to the same
// character set as other server-supplied ids before it names a file.
func (r *Reader) playbackImagePath(p *Playback, id *ID, kind, momentID string) (string, bool) {
	if !validEvidenceID(momentID) || momentID == "." || momentID == ".." {
		return "", false
	}
	return filepath.Join(filepath.Dir(r.discussionsRoot), ".sageox", "cache", "walkthrough-playback",
		id.RecordingID, p.generation, kind, momentID+".jpg"), true
}

// cachedJPEG reports whether path holds a complete JPEG, so a truncated or
// substituted file fails here instead of being offered to a reader as evidence.
func cachedJPEG(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > PlaybackImageMaxBytes {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && CheckPlaybackJPEG(b) == nil
}

// CheckPlaybackJPEG decodes every pixel of b. Reading only the header would
// pass a file cut off after it, and such a file would then be offered as
// evidence and skipped by every later download.
func CheckPlaybackJPEG(b []byte) error {
	_, err := jpeg.Decode(bytes.NewReader(b))
	return err
}
