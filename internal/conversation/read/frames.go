package read

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// Image is the local path of the frame image. In a synced checkout the
	// file is a stub; `ox fetch <image>` downloads the real bytes.
	Image string `json:"image,omitempty"`
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
	for _, f := range frames {
		if f.TimestampSeconds < 0 {
			continue
		}
		at := time.Duration(f.TimestampSeconds * float64(time.Second))
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
		if rel, ok := keyframeImagePath(droot, f.Filename); ok {
			frame.Image = filepath.Join(r.discussionsRoot, folder, filepath.FromSlash(rel))
		}
		out[i].Frames = append(out[i].Frames, frame)
	}
}

// servedPositions maps a served cue's 1-based ordinal to its index in out.
func servedPositions(out []TranscriptCue) map[int]int {
	pos := make(map[int]int, len(out))
	for i, c := range out {
		pos[c.N] = i
	}
	return pos
}

// owningCue returns the ordinal of the cue that owns instant at: the cue
// whose [Start, End) contains it, else the nearest cue starting before it,
// else the first cue.
func owningCue(all []vtt.Cue, at time.Duration) (int, bool) {
	if len(all) == 0 {
		return 0, false
	}
	owner := all[0].Index
	for _, c := range all {
		if c.Start <= at && at < c.End {
			return c.Index, true
		}
		if c.Start <= at {
			owner = c.Index
		}
	}
	return owner, true
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
