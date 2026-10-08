package read

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
	lfspointer "github.com/sageox/ox/internal/lfs/pointer"
	"github.com/sageox/ox/internal/vtt"
)

// EvidenceCapabilities describes delivered inputs, never recorder-brand guesses.
type EvidenceCapabilities struct {
	Video         bool `json:"video"`
	Pointer       bool `json:"pointer"`
	AXTree        bool `json:"ax_tree"`
	KeyframeHints bool `json:"keyframe_hints"`
}

type EvidenceCoverage struct {
	Status      string   `json:"status"`
	TotalFrames int      `json:"total_frames"`
	MaxFrames   int      `json:"max_frames"`
	Notes       []string `json:"notes,omitempty"`
}

type evidenceIndex struct {
	Spec               string               `json:"spec"`
	Revision           string               `json:"revision"`
	SourceRevision     string               `json:"source_revision"`
	TranscriptRevision string               `json:"transcript_revision"`
	Capabilities       EvidenceCapabilities `json:"capabilities"`
	Coverage           EvidenceCoverage     `json:"coverage"`
	FramesFile         string               `json:"frames_file"`
	TranscriptFile     string               `json:"transcript_file"`
}

type evidenceFrame struct {
	ID               string  `json:"id"`
	TimestampSeconds float64 `json:"timestamp_seconds"`
	SHA256           string  `json:"sha256"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	Path             string  `json:"path"`
	Reason           string  `json:"reason"`
}

type WalkthroughTranscript struct {
	Cues       []TranscriptCue `json:"cues"`
	Total      int             `json:"total"`
	Returned   int             `json:"returned"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Revision   string          `json:"revision"`
}

// walkthroughEvidence reads immutable producer snapshots. Pin failures never
// fall through to today's transcript: the same cue number can mean new words.
func (r *Reader) walkthroughEvidence(start time.Time, id *ID, rw row, root *os.Root, opts WalkthroughOptions) (*Envelope, bool) {
	discovery, err := format.DiscoverLayersIn(root)
	if err != nil {
		return r.finishError(start, newError(ErrCodeReadError, err.Error()), nil), true
	}
	var selected *format.DiscoveredLayer
	var index evidenceIndex
	for i := range discovery.Layers {
		layer := &discovery.Layers[i]
		if layer.Envelope.Status != "active" && (opts.Revision == "" || layer.Envelope.Status != "superseded") {
			continue
		}
		if layer.Envelope.Kind != "keyframe" || (layer.Envelope.Spec != "keyframe/2" && layer.Envelope.Spec != "sageox://layer-spec/keyframe/2") || layer.Layout != format.LayoutFolder {
			continue
		}
		raw, cut, e := readBoundedFile(root, path.Join(path.Dir(layer.Path), "index.json"), maxScreenFileBytes)
		var candidate evidenceIndex
		if e != nil || cut || json.Unmarshal(raw, &candidate) != nil || candidate.Spec != "keyframe/2" {
			return r.finishError(start, newError(ErrCodeReadError, "immutable keyframe index unreadable"), nil), true
		}
		if opts.Revision != "" && candidate.Revision != opts.Revision {
			continue
		}
		if selected == nil || layer.Envelope.LayerID > selected.Envelope.LayerID {
			selected, index = layer, candidate
		}
	}
	if selected == nil {
		if opts.Revision != "" {
			return r.finishError(start, newError("revision_unavailable", "requested evidence revision is not available locally; sync and retry without substituting another revision"), nil), true
		}
		return nil, false
	}
	if !validDigest(index.Revision) || !validDigest(index.SourceRevision) || !validDigest(index.TranscriptRevision) || index.FramesFile != "frames.json" || index.TranscriptFile != "transcript.vtt" {
		return r.finishError(start, newError(ErrCodeReadError, "invalid immutable evidence identity or payload path"), nil), true
	}
	dir := path.Dir(selected.Path)
	raw, cut, err := readBoundedFile(root, path.Join(dir, index.TranscriptFile), maxScreenFileBytes)
	if err != nil || cut || digest(raw) != index.TranscriptRevision {
		return r.finishError(start, newError(ErrCodeReadError, "pinned transcript missing or its hash does not match the evidence revision"), nil), true
	}
	cues, err := vtt.Parse(raw)
	if err != nil {
		return r.finishError(start, newError(ErrCodeReadError, "pinned transcript is not valid WebVTT"), nil), true
	}
	data := &WalkthroughData{ConversationID: id.ConversationID, Title: r.conversationTitle(rw, root), ScreenRecording: true, Revision: index.Revision, SourceRevision: index.SourceRevision, Capabilities: &index.Capabilities, Coverage: &index.Coverage, Moments: []WalkthroughMoment{}}
	var warnings []string
	manifest, _, _ := format.LoadManifestIn(root)
	opts = applyCitationWindow(opts, id, cues, manifest, &warnings)
	transcript, pageErr := walkthroughTranscriptPage(cues, index.TranscriptRevision, index.Revision, opts)
	if pageErr != nil {
		return r.finishError(start, pageErr, warnings), true
	}
	data.Transcript = transcript
	raw, cut, err = readBoundedFile(root, path.Join(dir, index.FramesFile), maxScreenFileBytes)
	var frames []evidenceFrame
	if err != nil || cut || json.Unmarshal(raw, &frames) != nil || len(frames) > maxKeyframes {
		return r.finishError(start, newError(ErrCodeReadError, "immutable frame catalog unreadable or exceeds bounds"), warnings), true
	}
	// The revision binds all catalog and native-observation bytes, not merely
	// the transcript. This rejects mixed snapshots after partial or corrupt sync.
	observations, obsCut, obsErr := readBoundedFile(root, path.Join(dir, "observations.json"), maxScreenFileBytes)
	expectedRevision := digest([]byte(index.SourceRevision + "\n" + index.TranscriptRevision + "\n" + digest(raw) + "\n" + digest(observations) + "\nwalkthrough-evidence-v2"))
	if obsErr != nil || obsCut || expectedRevision != index.Revision || !json.Valid(observations) {
		return r.finishError(start, newError(ErrCodeReadError, "evidence revision does not match its frame catalog and observations"), warnings), true
	}
	data.Observations = filterEvidenceObservations(observations, cues, opts)
	inv := &KeyframeInventory{Count: len(frames)}
	data.Sources.Keyframes = inv
	data.Sources.Layers = []WalkthroughLayer{{Kind: "keyframe", LayerID: selected.Envelope.LayerID, Revision: selected.Envelope.Revision}}
	for _, f := range frames {
		at, valid := secondsOffset(f.TimestampSeconds)
		if !valid || !validDigest(f.SHA256) || !validEvidenceID(f.ID) || f.Width <= 0 || f.Height <= 0 || f.Width > 32768 || f.Height > 32768 || f.Path != "frame-"+f.SHA256+".jpg" {
			return r.finishError(start, newError(ErrCodeReadError, "invalid immutable frame identity, geometry or path"), warnings), true
		}
		if opts.Transcript {
			continue
		}
		cue, _ := owningCue(cues, at)
		if opts.CueFirst > 0 && (cue < opts.CueFirst || cue > opts.CueLast) || opts.HasWindow && (at < opts.FromOffset || at > opts.ToOffset) {
			continue
		}
		ref := &KeyframeRef{ID: f.ID, SHA256: f.SHA256, Width: f.Width, Height: f.Height, Why: cleanScreenText(f.Reason), Availability: "unavailable"}
		rel := path.Join(dir, f.Path)
		ref.Image = filepath.Join(r.discussionsRoot, rw.entry.Folder, filepath.FromSlash(rel))
		// A filename or stub is not image evidence. Verify bytes against the bound
		// hash before telling a vision-capable reader it can open this path.
		local := r.localImage(root, rw.entry.Folder, rel, ref.Image)
		if local != "" && r.verifiedEvidenceImage(local, f.SHA256, f.Width, f.Height) {
			ref.LocalImage = local
			ref.Availability = "local"
			inv.Local++
		} else if head, _, e := readBoundedFile(root, rel, maxPointerProbeBytes); e == nil {
			if oid, _, parseErr := lfspointer.Parse(string(head)); parseErr == nil && strings.TrimPrefix(oid, "sha256:") == f.SHA256 {
				ref.FetchCommand = "ox fetch " + shellQuoteIfNeeded(ref.Image)
				ref.Availability = "unfetched"
			}
		}
		data.Moments = append(data.Moments, WalkthroughMoment{At: formatVTTTimestamp(at), Cue: cue, Kind: MomentKeyframe, Frame: ref})
	}
	data.Window.Total = len(data.Moments)
	if opts.CueFirst > 0 {
		data.Window.Cues = []int{opts.CueFirst, opts.CueLast}
	}
	if opts.HasWindow {
		data.Window.From = formatVTTTimestamp(opts.FromOffset)
		data.Window.To = formatVTTTimestamp(opts.ToOffset)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultMomentLimit
	}
	if len(data.Moments) > limit {
		data.Moments = data.Moments[:limit]
		data.Window.Truncated = true
	}
	if opts.Transcript {
		data.Moments = []WalkthroughMoment{}
	}
	if len(data.Moments) == 0 && !opts.Transcript {
		data.Notes = append(data.Notes, "No retained images in this window. Source words remain available; missing frames do not mean there is no feedback.")
	}
	if !index.Capabilities.Pointer {
		data.Notes = append(data.Notes, "No native pointer data was delivered. Raw uploads normally lack mouse positions, clicks and dwell times.")
	}
	guidance := walkthroughGuidance(id.ConversationID, data) + fmt.Sprintf(" Pin subsequent reads with --revision %s. Read source words: ox walkthrough %s --transcript --revision %s.", index.Revision, id.ConversationID, index.Revision)
	if transcript.NextCursor != "" {
		guidance += fmt.Sprintf(" Next transcript page: ox walkthrough %s --transcript --revision %s --cursor %s.", id.ConversationID, index.Revision, transcript.NextCursor)
	}
	guidance += fmt.Sprintf(" For a named visual deficiency, request bounded server decoding: ox walkthrough %s --revision %s --extract --cues N-M --max-frames 5. For temporal ambiguity widen the window; for a transient state request a denser short window; for unreadable detail use --max-width 4096. This spends server compute, uses no semantic LLM pass, and must not be retried without a budget or new evidence need.", id.ConversationID, index.Revision)
	return r.finishSuccess(start, data, guidance, warnings), true
}

func walkthroughTranscriptPage(cues []vtt.Cue, revision, evidenceRevision string, opts WalkthroughOptions) (*WalkthroughTranscript, *Error) {
	selected := make([]vtt.Cue, 0, len(cues))
	for _, c := range cues {
		if opts.CueFirst > 0 && (c.Index < opts.CueFirst || c.Index > opts.CueLast) || opts.HasWindow && (c.End < opts.FromOffset || c.Start > opts.ToOffset) {
			continue
		}
		selected = append(selected, c)
	}
	offset := 0
	// The cursor binds the source, selector and offset. A cursor from another
	// question cannot silently skip the beginning of this question's transcript.
	scope := digest([]byte(fmt.Sprintf("%s:%s:%d:%d:%t:%d:%d", revision, evidenceRevision, opts.CueFirst, opts.CueLast, opts.HasWindow, opts.FromOffset, opts.ToOffset)))
	if opts.Cursor != "" {
		parts := strings.Split(opts.Cursor, ":")
		if len(parts) != 2 || parts[0] != scope {
			return nil, newError(ErrCodeInvalidSelector, "cursor does not match this revision and window")
		}
		n, e := strconv.Atoi(parts[1])
		if e != nil || n < 0 || n > len(selected) {
			return nil, newError(ErrCodeInvalidSelector, "invalid transcript cursor")
		}
		offset = n
	}
	end := min(offset+DefaultCueWindow, len(selected))
	out := &WalkthroughTranscript{Total: len(selected), Returned: end - offset, Revision: revision, Cues: []TranscriptCue{}}
	for _, c := range selected[offset:end] {
		out.Cues = append(out.Cues, TranscriptCue{N: c.Index, Start: formatVTTTimestamp(c.Start), End: formatVTTTimestamp(c.End), Speaker: c.Speaker, Text: c.Text})
	}
	if end < len(selected) {
		out.NextCursor = scope + ":" + strconv.Itoa(end)
	}
	return out, nil
}

func validDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func (r *Reader) verifiedEvidenceImage(filename, hash string, width, height int) bool {
	// Use the same rooted filesystem boundary for checkout and cache images.
	// Opening a validated absolute path again would reintroduce a symlink race.
	teamRoot, e := os.OpenRoot(filepath.Dir(r.discussionsRoot))
	if e != nil {
		return false
	}
	defer teamRoot.Close()
	rel, e := filepath.Rel(filepath.Dir(r.discussionsRoot), filename)
	if e != nil {
		return false
	}
	info, e := teamRoot.Lstat(rel)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return false
	}
	f, e := teamRoot.Open(rel)
	if e != nil {
		return false
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 32<<20+1))
	if e != nil || len(b) > 32<<20 || digest(b) != hash {
		return false
	}
	config, e := jpeg.DecodeConfig(bytes.NewReader(b))
	return e == nil && config.Width == width && config.Height == height
}

// Preserve producer-native facts without joining them to mutable current layers.
// The reader clips time-series observations to the requested window and caps
// disclosure; omitted samples remain in the pinned snapshot, not guessed.
func filterEvidenceObservations(raw []byte, cues []vtt.Cue, opts WalkthroughOptions) map[string]any {
	var observations map[string]any
	if json.Unmarshal(raw, &observations) != nil {
		return nil
	}
	lo, hi := 0.0, maxMediaOffset.Seconds()
	if opts.HasWindow {
		lo, hi = opts.FromOffset.Seconds(), opts.ToOffset.Seconds()
	}
	if opts.CueFirst > 0 {
		a, b := cueSpan(cues, opts.CueFirst, opts.CueLast)
		lo, hi = a.Seconds(), b.Seconds()
	}
	clip := func(value any) any {
		rows, ok := value.([]any)
		if !ok {
			return value
		}
		kept := []any{}
		total := 0
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			at, ok := m["s"].(float64)
			if !ok || at < lo || at > hi {
				continue
			}
			total++
			if len(kept) < DefaultMomentLimit {
				kept = append(kept, m)
			}
		}
		return map[string]any{"rows": kept, "total": total, "returned": len(kept), "truncated": total > len(kept)}
	}
	if hints, ok := observations["hints"]; ok {
		observations["hints"] = clip(hints)
	}
	if screen, ok := observations["screen_context"].(map[string]any); ok {
		for _, key := range []string{"pointer", "ax_nodes"} {
			if rows, ok := screen[key]; ok {
				screen[key] = clip(rows)
			}
		}
	}
	// Only the producer's factual fields cross this boundary. Unknown fields
	// in a customer-writable checkout must not bypass disclosure budgets.
	out := map[string]any{}
	for _, key := range []string{"layers", "hints", "screen_context", "invalid_envelopes"} {
		if value, ok := observations[key]; ok {
			out[key] = boundedObservation(value, 0)
		}
	}
	return out
}

func boundedObservation(value any, depth int) any {
	if depth > 6 {
		return nil
	}
	switch v := value.(type) {
	case string:
		return cleanScreenText(v)
	case []any:
		out := make([]any, 0, min(len(v), DefaultMomentLimit))
		for _, item := range v[:min(len(v), DefaultMomentLimit)] {
			out = append(out, boundedObservation(item, depth+1))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if len(out) >= 24 {
				break
			}
			out[cleanScreenText(key)] = boundedObservation(item, depth+1)
		}
		return out
	default:
		return value
	}
}

func validEvidenceID(s string) bool {
	return len(s) > 0 && len(s) <= 160 && !strings.ContainsFunc(s, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-", r)
	})
}
