package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/fileutil"
)

// walkthroughPlaybackTimeout bounds the playback lookup. It sits in front of
// every walkthrough read, so a slow server must cost seconds, not the read.
const walkthroughPlaybackTimeout = 10 * time.Second

// The notes a reader sees when playback data could not be used. Each says what
// is being shown instead, so an absent crop is never mistaken for an absent
// moment.
const (
	playbackUnavailableNote = "Playback data is not available for this recording (it may predate it, or still be processing); showing the standard evidence index."
	playbackUnreadableNote  = "Playback data could not be read; showing the standard evidence index."
	playbackNewerNote       = "Playback data uses a newer format than this ox understands; update ox for joined moments and focus images. Showing the standard evidence index."
)

// loadWalkthroughPlayback asks the server for the recording's playback data.
// It returns the parsed document, or a note saying why there is none. Both
// empty means the lookup was not attempted (no project, team or login): that
// is the ordinary state of a machine the read would refuse anyway, and a note
// there would only be noise.
func loadWalkthroughPlayback(ctx context.Context, idArg string) (*read.Playback, string) {
	id, parseErr := read.ParseID(idArg)
	if parseErr != nil {
		return nil, ""
	}
	scope, err := resolveWalkthroughScope(id)
	if err != nil {
		slog.Debug("walkthrough playback skipped", "reason", err)
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, walkthroughPlaybackTimeout)
	defer cancel()
	raw, err := scope.client(walkthroughPlaybackTimeout).WalkthroughPlayback(ctx, scope.teamID, id.RecordingID)
	switch {
	case errors.Is(err, api.ErrWalkthroughPlaybackUnavailable):
		return nil, playbackUnavailableNote
	case err != nil:
		slog.Debug("walkthrough playback request failed", "error", err)
		reason := "request failed"
		switch {
		case errors.Is(err, api.ErrUnauthorized):
			reason = "sign-in expired; run ox login"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timed out"
		}
		return nil, fmt.Sprintf("Playback data could not be fetched (%s); showing the standard evidence index.", reason)
	}
	playback, err := read.ParsePlayback(raw)
	switch {
	case errors.Is(err, read.ErrPlaybackSpec):
		return nil, playbackNewerNote
	case err != nil:
		slog.Debug("walkthrough playback unreadable", "error", err)
		return nil, playbackUnreadableNote
	}
	return playback, ""
}

type namedImage struct {
	name string
	ref  *read.PlaybackImage
}

// resolveWalkthroughChapter turns --chapter into a time window using the
// playback data. A recording without chapters cannot honor it, and silently
// returning the whole recording would answer a different question, so that is
// an error carrying the reason playback data is missing, if there is one.
func resolveWalkthroughChapter(playback *read.Playback, note, title string) (from, to time.Duration, _ *read.Error) {
	if playback == nil {
		msg := "--chapter needs the chapters of a Visual walkthrough, which this recording does not have"
		if note != "" {
			msg += ": " + note
		}
		return 0, 0, read.NewError(read.ErrCodeInvalidSelector, msg)
	}
	return playback.ChapterWindow(title)
}

// fetchPlaybackImages downloads the focus image of every joined moment in the
// window, and its overview image too when asked. It stops at the same budget
// as keyframe fetching. The signed link stays inside the data it came with:
// it is used for the one request and never printed, and the request carries no
// credential of ours.
func fetchPlaybackImages(ctx context.Context, data *read.WalkthroughData, overview bool) []string {
	var warnings []string
	attempts := 0
	for _, m := range data.Moments {
		images := []namedImage{{"focus", m.Focus}}
		if overview {
			images = append(images, namedImage{"overview", m.Overview})
		}
		for _, img := range images {
			ref := img.ref
			if ref == nil || ref.LocalImage != "" || ref.URL == "" || ref.CachePath == "" {
				continue
			}
			if attempts >= walkthroughFetchBudget {
				return append(warnings, fmt.Sprintf("image fetch budget reached (%d); narrow the cue/time window for remaining images", walkthroughFetchBudget))
			}
			attempts++
			if err := downloadPlaybackImage(ctx, ref); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s image at %s unavailable: %v", img.name, m.At, err))
				continue
			}
			ref.LocalImage, ref.Availability, ref.FetchCommand = ref.CachePath, "local", ""
		}
	}
	return warnings
}

// downloadPlaybackImage stores one image in the cache. A response that is not
// a JPEG is refused: the cache only ever holds files a reader can open as an
// image, so a stub or an error page never reads as evidence.
func downloadPlaybackImage(ctx context.Context, ref *read.PlaybackImage) error {
	body, err := api.DownloadWalkthroughImage(ctx, ref.URL, read.PlaybackImageMaxBytes)
	if err != nil {
		return err
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(body)); err != nil {
		return fmt.Errorf("the download is not a readable JPEG")
	}
	if err := os.MkdirAll(filepath.Dir(ref.CachePath), 0o700); err != nil {
		return fmt.Errorf("create image cache: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(ref.CachePath, body, 0o600); err != nil {
		return fmt.Errorf("store image: %w", err)
	}
	return nil
}

// --- text rendering of joined moments ---

// timelineEntry is one printed item, positioned by its media-clock instant.
// A chapter header sorts before pages, pages before moments, and moments
// before spans at the same instant, so what a moment sits under is already
// printed when the moment is.
type timelineEntry struct {
	ms    int64
	order int
	lines []string
}

// renderWalkthroughTimeline prints pages, spans and (for joined data) moments
// in time order. The moment, span and sub-line shapes are the CLI's contract
// for agents that read text; keep them stable.
func renderWalkthroughTimeline(w io.Writer, d *read.WalkthroughData, withMoments bool) {
	var entries []timelineEntry
	for _, c := range d.Chapters {
		entries = append(entries, timelineEntry{ms: c.AtMS, order: -1, lines: []string{chapterLine(c)}})
	}
	for _, p := range d.Pages {
		entries = append(entries, timelineEntry{ms: p.AtMS, order: 0, lines: []string{pageLine(p)}})
	}
	if withMoments {
		for _, m := range d.Moments {
			entries = append(entries, timelineEntry{ms: m.AtMS, order: 1, lines: joinedMomentLines(m)})
		}
	}
	for _, sc := range d.Shortcuts {
		entries = append(entries, timelineEntry{ms: sc.AtMS, order: 1, lines: []string{shortcutLine(sc)}})
	}
	for _, s := range d.Spans {
		entries = append(entries, timelineEntry{ms: s.FromMS, order: 2, lines: []string{spanLine(s)}})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ms != entries[j].ms {
			return entries[i].ms < entries[j].ms
		}
		return entries[i].order < entries[j].order
	})
	for _, e := range entries {
		for _, line := range e.lines {
			fmt.Fprintln(w, line)
		}
	}
}

// subLineIndent aligns sub-lines under the moment line's kind column.
const subLineIndent = "      "

func joinedMomentLines(m read.WalkthroughMoment) []string {
	head := []string{walkthroughClock(m.AtMS)}
	if m.Cue > 0 {
		head = append(head, fmt.Sprintf("[%d]", m.Cue))
	}
	head = append(head, m.Kind)
	if what := joinedElementText(m); what != "" {
		head = append(head, what)
	}
	line := strings.Join(head, "  ")
	// Over selected text the cursor is always an I-beam: it says nothing the
	// selection line does not.
	if m.Cursor != "" && m.Kind != "selection" {
		line += "   cursor: " + m.Cursor
	}
	lines := []string{line}
	if m.Said != "" {
		lines = append(lines, fmt.Sprintf("%s%-8s%q", subLineIndent, "said:", m.Said))
	}
	lines = append(lines, imageLines(m)...)
	if t := m.Then; t != nil {
		label := plainQuotes(t.Label)
		// Where the screen changed is a region of the frame, not coordinates:
		// words are what an agent can use without a picture.
		if t.Label == screenChangedLabel && t.Box != nil {
			label += " (" + regionWords(*t.Box) + ")"
		}
		lines = append(lines, fmt.Sprintf("%s%-8s%s %.1f s later", subLineIndent, "then:", label, float64(t.AfterMS)/1000))
	}
	return lines
}

// screenChangedLabel is the outcome label for a change the screen made that
// nothing else explains.
const screenChangedLabel = "screen changed"

// regionWords names where a normalized box sits in the frame: the third of the
// frame its center falls in on each axis ("top-right", "left", "center"), or
// "most of the screen" when it covers most of it.
func regionWords(b read.ImageBox) string {
	if b.W*b.H >= 0.6 {
		return "most of the screen"
	}
	third := func(center float64, low, mid, high string) string {
		switch {
		case center < 1.0/3:
			return low
		case center > 2.0/3:
			return high
		}
		return mid
	}
	vertical := third(b.Y+b.H/2, "top", "", "bottom")
	horizontal := third(b.X+b.W/2, "left", "", "right")
	switch {
	case vertical != "" && horizontal != "":
		return vertical + "-" + horizontal
	case vertical != "":
		return vertical
	case horizontal != "":
		return horizontal
	}
	return "center"
}

func chapterLine(c read.WalkthroughChapter) string {
	line := "── " + c.Title + "  " + walkthroughClock(c.AtMS)
	if c.URL != "" {
		line += "  " + strings.TrimPrefix(strings.TrimPrefix(c.URL, "https://"), "http://")
	}
	return line
}

func shortcutLine(sc read.WalkthroughShortcut) string {
	line := fmt.Sprintf("%s  shortcut  %s", walkthroughClock(sc.AtMS), sc.Label)
	if sc.Count > 1 {
		line += fmt.Sprintf(" ×%d", sc.Count)
	}
	return line
}

// imageLines points at the moment's images: the file when it is on this
// machine, else the command that fetches it. A signed link is never printed.
// The overview line appears only once downloaded; asking for it is --overview,
// which the guidance says, so an undownloaded one adds nothing per moment.
func imageLines(m read.WalkthroughMoment) []string {
	var lines []string
	if f := m.Focus; f != nil {
		switch {
		case f.LocalImage != "":
			lines = append(lines, fmt.Sprintf("%s%-8s%s", subLineIndent, "focus:", f.LocalImage))
		case f.FetchCommand != "":
			lines = append(lines, fmt.Sprintf("%s%-8s%s", subLineIndent, "focus:", f.FetchCommand))
		default:
			lines = append(lines, fmt.Sprintf("%s%-8s(unavailable)", subLineIndent, "focus:"))
		}
	}
	if o := m.Overview; o != nil && o.LocalImage != "" {
		lines = append(lines, fmt.Sprintf("%s%-10s%s", subLineIndent, "overview:", o.LocalImage))
	}
	return lines
}

// joinedElementText names what a joined moment points at. A selection prints
// the selected text alone; anything else prints its role and name.
func joinedElementText(m read.WalkthroughMoment) string {
	e := m.Element
	if e == nil {
		return ""
	}
	name := e.Name
	if name == "" {
		name = e.Title
	}
	if m.Kind == "selection" {
		if name == "" {
			return ""
		}
		return fmt.Sprintf("%q", name)
	}
	if len(m.Elements) >= 2 {
		return pluralElementText(m.Elements)
	}
	if e.Role == "" && name == "" {
		return ""
	}
	if e.Ancestor {
		// The pointer was over something unnamed; this is the nearest named
		// thing around it, so say "in", not that it was clicked itself.
		if name != "" {
			return fmt.Sprintf("in %q", name)
		}
		return "in " + friendlyRole(e.Role)
	}
	text := friendlyRole(e.Role)
	if name != "" {
		text += fmt.Sprintf(" %q", name)
	}
	return text
}

// pluralElementText prints what a plural reference covered as "A", "B" +1:
// the first two names, then how many more there were.
func pluralElementText(elements []*read.ScreenElement) string {
	var parts []string
	for _, e := range elements[:2] {
		name := e.Name
		if name == "" {
			name = e.Title
		}
		if name == "" {
			parts = append(parts, friendlyRole(e.Role))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q", name))
	}
	text := strings.Join(parts, ", ")
	if more := len(elements) - 2; more > 0 {
		text += fmt.Sprintf(" +%d", more)
	}
	return text
}

// friendlyRole turns an accessibility role into the words a person says:
// AXButton is "button", AXTextField is "text field".
func friendlyRole(role string) string {
	role = strings.TrimPrefix(role, "AX")
	if role == "" {
		return "element"
	}
	runes := []rune(role)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			endsAcronym := unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || endsAcronym {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func spanLine(s read.WalkthroughSpan) string {
	secs := (s.ToMS - s.FromMS + 500) / 1000
	var what string
	switch s.Kind {
	case "quiet":
		what = fmt.Sprintf("%d s, nothing said, pointer still", secs)
	case "typing":
		what = fmt.Sprintf("%d s, typing", secs)
		if s.Element != nil {
			name := s.Element.Name
			if name == "" {
				name = s.Element.Title
			}
			if desc := strings.TrimSpace(friendlyRole(s.Element.Role) + " " + quoteIfSet(name)); desc != "" {
				what += " in " + desc
			}
		}
	default:
		what = fmt.Sprintf("%d s", secs)
	}
	return fmt.Sprintf("%s–%s  %-7s %s", walkthroughClock(s.FromMS), walkthroughClock(s.ToMS), s.Kind, what)
}

func pageLine(p read.WalkthroughPage) string {
	parts := []string{walkthroughClock(p.AtMS), "page"}
	if p.Title != "" {
		parts = append(parts, fmt.Sprintf("%q", p.Title))
	}
	line := strings.Join(parts, "  ")
	if p.URL != "" {
		line += " " + p.URL
	}
	return line
}

func quoteIfSet(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf("%q", s)
}

// walkthroughClock prints a media-clock instant as hh:mm:ss, rounded to the
// nearest second. Spans are trimmed 150 ms at each end by the producer, so
// rounding returns their whole-second boundaries. The JSON carries the exact
// millisecond.
func walkthroughClock(ms int64) string {
	s := (ms + 500) / 1000
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// plainQuotes turns typographic quotes into plain ones, so a label reads the
// same in a terminal and in a pipe.
func plainQuotes(s string) string {
	return strings.NewReplacer("“", `"`, "”", `"`, "‘", "'", "’", "'").Replace(s)
}
