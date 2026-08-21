package read

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
	"github.com/sageox/ox/internal/vtt"
)

// DefaultCueWindow is the no-selector transcript window (D15): the first 100
// cues, reported truncated when more exist. A full transcript requires the
// explicit --full.
const DefaultCueWindow = 100

// Pinning statuses (D8): the requested range is always served; the status
// tells citation-followers whether cue numbers may have drifted.
const (
	PinningPinned           = "pinned"
	PinningUnpinned         = "unpinned"
	PinningRevisionMismatch = "revision_mismatch"
)

// TranscriptOptions selects a transcript window. Explicit options win over
// selectors carried by a citation-URI id; with neither, the default window
// applies.
type TranscriptOptions struct {
	// CueFirst/CueLast are the 1-based inclusive cue range; 0 = unset.
	CueFirst, CueLast int
	// FromOffset/ToOffset are a closed media-clock window (offsets from the
	// recording start); used when HasWindow is set.
	FromOffset, ToOffset time.Duration
	HasWindow            bool
	// Full serves every cue. Intended for humans; agents request windows.
	Full bool
}

// TranscriptCue is one served cue.
type TranscriptCue struct {
	N       int    `json:"n"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text"`
}

// TranscriptWindow reports what was actually served.
type TranscriptWindow struct {
	// Cues is the [first, last] 1-based range served; omitted when empty.
	Cues []int `json:"cues,omitempty"`
	// Truncated marks the no-selector default window cut short of the file.
	Truncated bool `json:"truncated"`
	// Clamped marks a requested cue range clamped to the available cues.
	Clamped bool `json:"clamped,omitempty"`
}

// TranscriptData is the transcript envelope payload.
type TranscriptData struct {
	RevisionRequested int              `json:"revision_requested,omitempty"`
	RevisionCurrent   int              `json:"revision_current,omitempty"`
	Pinning           string           `json:"pinning"`
	Cues              []TranscriptCue  `json:"cues"`
	Window            TranscriptWindow `json:"window"`
}

// Transcript serves a VTT slice by cue range or time window (D8/D9). The
// requested range is always served from the current transcript; the envelope
// reports revision_requested / revision_current and a pinning status so
// citation-followers see drift honestly.
func (r *Reader) Transcript(rawID string, opts TranscriptOptions) *Envelope {
	start := r.now()
	id, idErr := ParseID(rawID)
	if idErr != nil {
		return r.finishError(start, idErr, nil)
	}
	if selErr := validateSelectors(opts); selErr != nil {
		return r.finishError(start, selErr, nil)
	}
	_, droot, lookErr := r.lookup(id.RecordingID)
	if lookErr != nil {
		return r.finishError(start, lookErr, nil)
	}
	defer droot.Close()

	var warnings []string

	// Manifest + layer metadata (D7: metadata only — content reads go to the
	// fixed root path). Both manifest names accepted; the D6 anomaly reports
	// through warnings.
	manifest, manifestWarnings, manErr := format.LoadManifestIn(droot)
	warnings = append(warnings, manifestWarnings...)
	if manErr != nil {
		warnings = append(warnings, "conversation manifest unreadable: "+manErr.Error())
	}
	revisionCurrent := r.currentTranscriptRevision(droot, id, &warnings)

	data := &TranscriptData{RevisionCurrent: revisionCurrent}
	if id.Address != nil && id.Address.Revision > 0 {
		data.RevisionRequested = id.Address.Revision
	}
	switch {
	case data.RevisionRequested == 0 || revisionCurrent == 0:
		data.Pinning = PinningUnpinned
		if data.RevisionRequested > 0 && revisionCurrent == 0 {
			warnings = append(warnings, fmt.Sprintf("citation pins revision %d but no transcript layer manifest is on disk; pinning cannot be verified", data.RevisionRequested))
		}
	case data.RevisionRequested == revisionCurrent:
		data.Pinning = PinningPinned
	default:
		data.Pinning = PinningRevisionMismatch
		warnings = append(warnings, fmt.Sprintf("citation pins transcript revision %d but revision %d is current; cue numbers may have drifted (D8: the requested range is still served)", data.RevisionRequested, revisionCurrent))
	}

	// The transcript itself, at its fixed root path (D7), read through the
	// open discussion-folder handle (derived from the validated discussions
	// root — never an absolute-path re-open) so a symlinked transcript.vtt
	// committed into the customer-writable tree can never pull content from
	// outside the folder: within-root links resolve, escaping links error.
	raw, readErr := readDiscussionFile(droot, format.TranscriptFileName)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return r.finishError(start, newError(ErrCodeTranscriptNotAvailable,
				fmt.Sprintf("%s has no %s yet; transcripts land before summaries, so a fresh recording may still be syncing", id.ConversationID, format.TranscriptFileName)), warnings)
		}
		return r.finishError(start, newError(ErrCodeReadError, fmt.Sprintf("read %s: %v", format.TranscriptFileName, readErr)), warnings)
	}
	cues, parseErr := vtt.Parse(raw)
	if parseErr != nil {
		return r.finishError(start, newError(ErrCodeTranscriptNotAvailable,
			fmt.Sprintf("%s of %s is not a readable WebVTT file: %v", format.TranscriptFileName, id.ConversationID, parseErr)), warnings)
	}

	served := resolveWindow(cues, id, opts, manifest, data, &warnings)
	data.Cues = make([]TranscriptCue, 0, len(served))
	for _, c := range served {
		data.Cues = append(data.Cues, TranscriptCue{
			N:       c.Index,
			Start:   formatVTTTimestamp(c.Start),
			End:     formatVTTTimestamp(c.End),
			Speaker: c.Speaker,
			Text:    c.Text,
		})
	}
	if len(served) > 0 {
		data.Window.Cues = []int{served[0].Index, served[len(served)-1].Index}
	}

	guidance := fmt.Sprintf("Wider context: ox conversation transcript %s --cues N-M. Overview: ox conversation show %s.", id.ConversationID, id.ConversationID)
	return r.finishSuccess(start, data, guidance, warnings)
}

// validateSelectors rejects structurally invalid explicit windows before any
// disk read (usage-shaped: never conflated with missing data).
func validateSelectors(opts TranscriptOptions) *Error {
	hasCues := opts.CueFirst != 0 || opts.CueLast != 0
	if hasCues {
		if opts.CueFirst < 1 || opts.CueLast < 1 {
			return newError(ErrCodeInvalidSelector, "cue ordinals are 1-based; 0 is not addressable")
		}
		if opts.CueFirst > opts.CueLast {
			return newError(ErrCodeInvalidSelector, fmt.Sprintf("cue range %d-%d ends before it starts", opts.CueFirst, opts.CueLast))
		}
	}
	if opts.HasWindow && opts.FromOffset > opts.ToOffset {
		return newError(ErrCodeInvalidSelector, "time window ends before it starts")
	}
	if hasCues && opts.HasWindow {
		return newError(ErrCodeInvalidSelector, "cue range and time window are mutually exclusive")
	}
	return nil
}

// resolveWindow picks the served slice: explicit options first, then
// citation-URI selectors, then the 100-cue default window (D15). Reversed
// URI ranges cannot occur (the parser rejects them).
func resolveWindow(cues []vtt.Cue, id *ID, opts TranscriptOptions, manifest *format.Manifest, data *TranscriptData, warnings *[]string) []vtt.Cue {
	switch {
	case opts.Full:
		return cues
	case opts.CueFirst != 0:
		out, clamped := vtt.SliceByCueRange(cues, opts.CueFirst, opts.CueLast)
		data.Window.Clamped = clamped
		return out
	case opts.HasWindow:
		return vtt.SliceByTimeWindow(cues, opts.FromOffset, opts.ToOffset)
	}

	if id.Address != nil {
		if c := id.Address.Selectors.Cue; c != nil {
			out, clamped := vtt.SliceByCueRange(cues, int(c.From), int(c.To))
			data.Window.Clamped = clamped
			return out
		}
		if t := id.Address.Selectors.Time; t != nil {
			return resolveTimeSelector(cues, t.StartMS, t.EndMS, t.IsRange, manifest, warnings, data)
		}
	}

	out, _ := vtt.SliceByCueRange(cues, 1, DefaultCueWindow)
	data.Window.Truncated = len(cues) > DefaultCueWindow
	return out
}

// resolveTimeSelector maps an absolute t= selector (epoch-ms on the
// recording clock, D9) onto the media clock via the manifest's t0 and
// slices. Without a usable t0 the selector cannot be resolved; the default
// window is served with a warning rather than failing (D8's relaxed
// posture).
func resolveTimeSelector(cues []vtt.Cue, startMS, endMS int64, isRange bool, manifest *format.Manifest, warnings *[]string, data *TranscriptData) []vtt.Cue {
	t0, ok := manifestT0(manifest)
	if !ok {
		*warnings = append(*warnings, "t= selector cannot be resolved: no recording clock t0 on disk; serving the default window instead")
		out, _ := vtt.SliceByCueRange(cues, 1, DefaultCueWindow)
		data.Window.Truncated = len(cues) > DefaultCueWindow
		return out
	}
	from := time.UnixMilli(startMS).Sub(t0)
	if !isRange {
		if c, found := vtt.CueAtInstant(cues, from); found {
			return []vtt.Cue{c}
		}
		return nil
	}
	to := time.UnixMilli(endMS).Sub(t0)
	return vtt.SliceByTimeWindow(cues, from, to)
}

// manifestT0 extracts the recording clock origin: clock.t0 first, started_at
// as fallback.
func manifestT0(m *format.Manifest) (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	for _, s := range []string{m.Clock.T0, m.StartedAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// currentTranscriptRevision discovers the current transcript-layer revision
// from the layer manifests (D7: manifests are metadata only). When the
// citation names a layer, that exact layer is consulted; otherwise the
// highest-revision transcript layer stands in. 0 = unknown (no manifests on
// disk — ordinary for older folders).
func (r *Reader) currentTranscriptRevision(droot *os.Root, id *ID, warnings *[]string) int {
	discovery, err := format.DiscoverLayersIn(droot)
	if err != nil {
		*warnings = append(*warnings, "layer discovery failed: "+err.Error())
		return 0
	}
	best := 0
	for _, l := range discovery.Layers {
		if id.Address != nil && id.Address.Layer != "" {
			if l.Envelope.LayerID == id.Address.Layer {
				return l.Envelope.Revision
			}
			continue
		}
		if l.Envelope.Kind == "transcript" && l.Envelope.Revision > best {
			best = l.Envelope.Revision
		}
	}
	return best
}

// formatVTTTimestamp renders a media-clock offset in the WebVTT
// hh:mm:ss.mmm spelling.
func formatVTTTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
