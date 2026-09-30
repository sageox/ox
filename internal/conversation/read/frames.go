package read

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/vtt"
)

// KeyframesFileName is the server-written keyframe manifest of a screen
// recording, at the discussion-folder root. Absent for audio-only recordings.
const KeyframesFileName = "keyframes.json"

// Bounds on screen-derived inputs. Everything under a discussion folder is
// customer-writable and git-synced, so a hostile or runaway file must cost a
// bounded amount of work — and the cut is reported, never silent.
const (
	// maxScreenFileBytes caps any one keyframes.json or layer sidecar read.
	maxScreenFileBytes = 8 << 20
	// maxSidecarRows caps the JSONL rows parsed from one layer sidecar.
	maxSidecarRows = 50_000
	// maxKeyframes caps the frames considered from keyframes.json.
	maxKeyframes = 5_000
	// maxScreenTextRunes caps every screen-derived string in output.
	maxScreenTextRunes = 200
)

// TranscriptFrame is one keyframe attached to the cue it falls in. Every
// string is screen- or model-derived and has been cleaned (cleanScreenText).
type TranscriptFrame struct {
	// At is the frame's media-clock offset, WebVTT spelling.
	At string `json:"at"`
	// Why is how the frame was chosen (e.g. scene-change, periodic).
	Why         string `json:"why,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// Description is the one-sentence account of what is on screen.
	Description string `json:"description,omitempty"`
	// Image is the local path of the frame image, for programmatic use. In a
	// synced checkout the file is a stub.
	Image string `json:"image,omitempty"`
	// FetchCommand is the ready-to-run, shell-quoted `ox fetch` command
	// that downloads Image's real bytes.
	FetchCommand string `json:"fetch_command,omitempty"`
}

// keyframesFile is the subset of keyframes.json this reader consumes.
// Deliberately local (not pkg/discussion.LoadKeyframes): that loader
// re-opens by absolute path, bypassing the os.Root folder guard every read
// in this package goes through.
type keyframesFile struct {
	Keyframes []struct {
		TimestampSeconds float64 `json:"timestamp_seconds"`
		ExtractionMethod string  `json:"extraction_method"`
		ContentType      string  `json:"content_type"`
		Description      string  `json:"description"`
		Filename         string  `json:"filename"`
		S3Key            string  `json:"s3_key"`
	} `json:"keyframes"`
}

// hasKeyframes reports whether the folder carries a keyframes.json (a screen
// recording). Probed no-follow; any failure reads as "no".
func hasKeyframes(droot *os.Root) bool {
	info, err := droot.Lstat(KeyframesFileName)
	return err == nil && info.Mode().IsRegular()
}

// readBoundedFile reads rel through the folder root, refusing a symlinked
// final component (Lstat never follows it) and reading at most max bytes.
// truncated reports that the file was longer than max. A missing file
// surfaces fs.ErrNotExist.
func readBoundedFile(droot *os.Root, rel string, max int64) (data []byte, truncated bool, err error) {
	info, err := droot.Lstat(rel)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file (symlinks are never followed)", rel)
	}
	f, err := droot.Open(rel)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", rel, err)
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

// attachFrames reads keyframes.json and attaches each frame to the cue that
// owns it: the cue whose [start, end) contains the frame, else the nearest
// preceding cue (a frame before the first cue belongs to the first). Owner
// is decided over the whole transcript, then kept only when the owner is in
// the served window — so a --cues window gets exactly its own frames and a
// frame near a window edge is never attributed to the wrong cue.
func (r *Reader) attachFrames(droot *os.Root, folder string, all []vtt.Cue, out []TranscriptCue, warnings *[]string) {
	raw, truncated, err := readBoundedFile(droot, KeyframesFileName, maxScreenFileBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return // audio-only recording: no frames, no error
	case err != nil:
		*warnings = append(*warnings, "keyframes unreadable: "+err.Error())
		return
	case truncated:
		*warnings = append(*warnings, fmt.Sprintf("%s exceeds %d bytes; frames skipped", KeyframesFileName, maxScreenFileBytes))
		return
	}
	var kf keyframesFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s is not valid JSON: %v", KeyframesFileName, err))
		return
	}
	frames := kf.Keyframes
	if len(frames) > maxKeyframes {
		*warnings = append(*warnings, fmt.Sprintf("%s lists %d frames; only the first %d were considered", KeyframesFileName, len(frames), maxKeyframes))
		frames = frames[:maxKeyframes]
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].TimestampSeconds < frames[j].TimestampSeconds })

	pos := servedPositions(out)
	outOfRange := 0
	for _, f := range frames {
		at, ok := secondsOffset(f.TimestampSeconds)
		if !ok {
			outOfRange++
			continue
		}
		owner, ok := owningCue(all, at)
		if !ok {
			continue
		}
		i, served := pos[owner]
		if !served {
			continue
		}
		frame := TranscriptFrame{
			At:          formatVTTTimestamp(at),
			Why:         cleanScreenText(f.ExtractionMethod),
			ContentType: cleanScreenText(f.ContentType),
			Description: cleanScreenText(f.Description),
		}
		if rel, ok := keyframeImagePath(droot, keyframeImageName(f.Filename, f.S3Key)); ok {
			frame.Image = filepath.Join(r.discussionsRoot, folder, filepath.FromSlash(rel))
			frame.FetchCommand = "ox fetch " + shellQuoteIfNeeded(frame.Image)
		}
		out[i].Frames = append(out[i].Frames, frame)
	}
	if outOfRange > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: %d frames with an out-of-range timestamp skipped", KeyframesFileName, outOfRange))
	}
}

// maxMediaOffset bounds any media-clock offset read from screen data. No
// recording runs two days; anything beyond is corrupt, and bounding it
// before the float-to-Duration conversion keeps that conversion from
// overflowing.
const maxMediaOffset = 48 * time.Hour

// secondsOffset converts an untrusted seconds value to a media offset,
// rejecting non-finite, negative, and out-of-range values.
func secondsOffset(sec float64) (time.Duration, bool) {
	if math.IsNaN(sec) || sec < 0 || sec > maxMediaOffset.Seconds() {
		return 0, false
	}
	return time.Duration(sec * float64(time.Second)), true
}

// shellQuoteIfNeeded single-quotes a path containing anything beyond a
// conservative safe set, so a printed ox fetch command can be pasted or
// executed as-is even when a folder name carries $, backticks, ; or spaces.
func shellQuoteIfNeeded(p string) string {
	safe := p != "" && !strings.ContainsFunc(p, func(r rune) bool {
		return !strings.ContainsRune("/.-_~+,@", r) &&
			(r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
	if safe {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// servedPositions maps a served cue's 1-based ordinal to its index in out.
func servedPositions(out []TranscriptCue) map[int]int {
	pos := make(map[int]int, len(out))
	for i, c := range out {
		pos[c.N] = i
	}
	return pos
}

// owningCue returns the ordinal of the cue that owns instant at: among the
// cues with a usable interval (malformed cues keep Start=End=0 and are
// ignored), the one starting nearest before or at the instant — so an
// instant inside a cue belongs to it, and one in a gap or after the last cue
// belongs to the cue that precedes it. An instant before every cue belongs
// to the earliest cue. File order is not trusted: out-of-order VTT resolves
// by start time.
func owningCue(all []vtt.Cue, at time.Duration) (int, bool) {
	var preceding, earliest *vtt.Cue
	for i := range all {
		c := &all[i]
		if !c.HasTiming() {
			continue
		}
		if earliest == nil || c.Start < earliest.Start {
			earliest = c
		}
		if c.Start > at {
			continue
		}
		// Latest start wins; on a tie, prefer the cue that still contains
		// the instant, then file order.
		if preceding == nil || c.Start > preceding.Start ||
			(c.Start == preceding.Start && at < c.End && at >= preceding.End) {
			preceding = c
		}
	}
	switch {
	case preceding != nil:
		return preceding.Index, true
	case earliest != nil:
		return earliest.Index, true
	default:
		return 0, false
	}
}

// keyframeImageName picks the repo-relative image name for a keyframe.
// Current manifests carry `filename` (keyframes/NNN-hash.jpg); the earliest
// ones carry only `s3_key`, whose basename is the same file under keyframes/.
// Either way the result is untrusted and goes through keyframeImagePath.
func keyframeImageName(filename, s3Key string) string {
	if filename != "" || s3Key == "" {
		return filename
	}
	return "keyframes/" + path.Base(s3Key)
}

// keyframeImagePath validates keyframes.json's untrusted filename: a local
// forward-slash path under keyframes/, naming a regular file (probed
// no-follow through the folder root). Anything else yields no image rather
// than a path the caller would hand to ox fetch.
func keyframeImagePath(droot *os.Root, name string) (string, bool) {
	if name == "" || len(name) > maxFolderNameLen || strings.ContainsAny(name, "\\:\x00") {
		return "", false
	}
	if cleanScreenText(name) != name {
		return "", false // control or escape characters in a path: refuse
	}
	clean := path.Clean(name)
	if clean != name || !strings.HasPrefix(clean, "keyframes/") || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false
	}
	info, err := droot.Lstat(filepath.FromSlash(clean))
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return clean, true
}

// cleanScreenText makes a screen- or model-derived string safe to put in
// front of an AI coworker or a terminal: escape sequences and control
// characters dropped (cli.SanitizeTerminalText, after turning line breaks
// into spaces so words do not fuse), whitespace collapsed, and length capped.
// It is still untrusted DATA — cleaning bounds its shape, not its meaning.
func cleanScreenText(s string) string {
	if s == "" {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(cli.SanitizeTerminalText(s)), " ")
	if rs := []rune(s); len(rs) > maxScreenTextRunes {
		s = string(rs[:maxScreenTextRunes]) + "…"
	}
	return s
}
