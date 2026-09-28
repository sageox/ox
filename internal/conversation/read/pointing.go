package read

import (
	"bufio"
	"bytes"
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

	"github.com/sageox/ox/internal/conversation/format"
	"github.com/sageox/ox/internal/vtt"
)

// Client layer kinds a screen walkthrough carries next to its video.
const (
	layerKindPointer = "pointer"
	layerKindAXTree  = "ax-tree"

	defaultPointerSidecar = "pointer.jsonl"
	defaultAXSidecar      = "ax.jsonl"

	// maxPointingPerCue caps the pointer events surfaced for one cue.
	maxPointingPerCue = 2
)

// Pointing actions, strongest signal first.
const (
	PointingClick = "click"
	PointingDwell = "dwell"
	PointingHover = "hover"
)

// PointingEvent is what the narrator pointed at during a cue: the pointer
// sample resolved to the accessibility element under it. Only the element's
// role, name, and DOM id are ever surfaced — never an AX value or url, which
// can carry text the user typed.
type PointingEvent struct {
	At     string `json:"at"`
	Action string `json:"action"`
	Role   string `json:"role,omitempty"`
	Title  string `json:"title,omitempty"`
	DOMID  string `json:"dom_id,omitempty"`
	// Unnamed marks an element with neither a title nor a description (or
	// one the accessibility snapshot could not resolve).
	Unnamed bool `json:"unnamed,omitempty"`
}

// pointerRow is one pointer.jsonl row. A row carrying `target` is a
// geometry transition, not a sample, and is skipped.
type pointerRow struct {
	TUTC    json.RawMessage `json:"t_utc"`
	TMS     *float64        `json:"t_ms"`
	Vis     bool            `json:"vis"`
	DwellMS float64         `json:"dwell_ms"`
	AXRef   string          `json:"ax_ref"`
	Btn     *struct {
		Button string `json:"button"`
		Phase  string `json:"phase"`
	} `json:"btn"`
	Target json.RawMessage `json:"target"`
}

// axRow is one ax.jsonl node row. Fields not listed here (value, url, ...)
// are deliberately never decoded. Marker rows (hit / node_count) carry no
// role and are skipped.
type axRow struct {
	TUTC  json.RawMessage `json:"t_utc"`
	TMS   *float64        `json:"t_ms"`
	AXRef string          `json:"ax_ref"`
	Role  string          `json:"role"`
	Title string          `json:"title"`
	Desc  string          `json:"desc"`
	DOMID string          `json:"dom_id"`
}

// pointerSample is a pointer row mapped onto the media clock.
type pointerSample struct {
	at    time.Duration
	click bool
	dwell float64
	axRef string
}

// axNode is an AX node row mapped onto the media clock.
type axNode struct {
	at                       time.Duration
	role, title, desc, domID string
}

// attachPointing resolves, per served cue, what the narrator pointed at.
// Each sample belongs to its owning cue over the WHOLE transcript (the same
// rule frames use: the containing cue, else the nearest preceding one, so a
// sample in a gap or after the last cue is kept), and only cues in the
// served window emit. Missing layers mean no pointing and no error;
// unreadable or oversized layers are reported through warnings.
func attachPointing(droot *os.Root, manifest *format.Manifest, all []vtt.Cue, out []TranscriptCue, warnings *[]string) {
	if len(out) == 0 {
		return
	}
	discovery, err := format.DiscoverLayersIn(droot)
	if err != nil {
		*warnings = append(*warnings, "layer discovery failed: "+err.Error())
		return
	}
	pointerLayer := activeLayer(discovery, layerKindPointer)
	if pointerLayer == nil {
		return
	}
	samples := loadPointerSamples(droot, pointerLayer, manifest, warnings)
	if len(samples) == 0 {
		return
	}

	pos := servedPositions(out)
	byCue := map[int][]pointerSample{} // index in out -> its samples
	for _, s := range samples {
		owner, ok := owningCue(all, s.at)
		if !ok {
			continue
		}
		if i, served := pos[owner]; served {
			byCue[i] = append(byCue[i], s)
		}
	}

	// Pick events per served cue first, so the AX pass only keeps the nodes
	// actually referenced — the AX sidecar is the large one.
	type pick struct {
		cue    int
		sample pointerSample
	}
	var picks []pick
	wanted := map[string]bool{}
	for i := range out {
		for _, s := range pickPointerSamples(byCue[i]) {
			picks = append(picks, pick{cue: i, sample: s})
			if s.axRef != "" {
				wanted[s.axRef] = true
			}
		}
	}
	if len(picks) == 0 {
		return
	}

	var nodes map[string][]axNode
	if axLayer := activeLayer(discovery, layerKindAXTree); axLayer != nil && len(wanted) > 0 {
		nodes = loadAXNodes(droot, axLayer, manifest, wanted, warnings)
	}
	for _, p := range picks {
		out[p.cue].Pointing = append(out[p.cue].Pointing, resolvePointing(p.sample, nodes[p.sample.axRef]))
	}
}

// activeLayer returns the highest-revision active layer of kind, or nil.
func activeLayer(d *format.LayerDiscovery, kind string) *format.DiscoveredLayer {
	var best *format.DiscoveredLayer
	for i := range d.Layers {
		l := &d.Layers[i]
		if l.Envelope.Kind != kind || (l.Envelope.Status != "" && l.Envelope.Status != "active") {
			continue
		}
		if best == nil || l.Envelope.Revision > best.Envelope.Revision {
			best = l
		}
	}
	return best
}

// pickPointerSamples chooses at most maxPointingPerCue of one cue's visible
// samples (time-sorted): mouse-down clicks first (in time order), then the
// longest dwells, one event per element; when neither exists, the first
// sample over a known element stands in as a hover.
func pickPointerSamples(samples []pointerSample) []pointerSample {
	var clicks, dwells []pointerSample
	var hover *pointerSample
	for i := range samples {
		s := samples[i]
		switch {
		case s.click:
			clicks = append(clicks, s)
		case s.dwell > 0:
			dwells = append(dwells, s)
		case hover == nil && s.axRef != "":
			hover = &samples[i]
		}
	}
	sort.SliceStable(dwells, func(i, j int) bool { return dwells[i].dwell > dwells[j].dwell })

	var picked []pointerSample
	seen := map[string]bool{}
	for _, s := range append(clicks, dwells...) {
		if len(picked) == maxPointingPerCue {
			break
		}
		if s.axRef != "" && seen[s.axRef] {
			continue
		}
		seen[s.axRef] = true
		picked = append(picked, s)
	}
	if len(picked) == 0 && hover != nil {
		picked = append(picked, *hover)
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].at < picked[j].at })
	return picked
}

// resolvePointing turns a sample into its output event, naming the element
// from the latest AX node at or before the sample.
func resolvePointing(s pointerSample, candidates []axNode) PointingEvent {
	ev := PointingEvent{At: formatVTTTimestamp(s.at), Action: PointingHover}
	switch {
	case s.click:
		ev.Action = PointingClick
	case s.dwell > 0:
		ev.Action = PointingDwell
	}
	var node *axNode
	for i := range candidates { // sorted by time
		if candidates[i].at > s.at {
			break
		}
		node = &candidates[i]
	}
	if node == nil {
		ev.Unnamed = true
		return ev
	}
	ev.Role = cleanScreenText(node.role)
	ev.DOMID = cleanScreenText(node.domID)
	ev.Title = cleanScreenText(node.title)
	if ev.Title == "" {
		ev.Title = cleanScreenText(node.desc)
	}
	ev.Unnamed = ev.Title == ""
	return ev
}

// loadPointerSamples parses the pointer layer's sidecar into visible samples
// on the media clock.
func loadPointerSamples(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, warnings *[]string) []pointerSample {
	t0, hasT0 := layerT0(layer, manifest)
	var out []pointerSample
	forEachSidecarRow(droot, layer, defaultPointerSidecar, warnings, func(line []byte) rowResult {
		var row pointerRow
		if json.Unmarshal(line, &row) != nil {
			return rowMalformed
		}
		if len(row.Target) > 0 && string(row.Target) != "null" {
			return rowSkipped // geometry transition, not a sample
		}
		if !row.Vis {
			return rowSkipped
		}
		at, res := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if res != rowKept {
			return res
		}
		out = append(out, pointerSample{
			at:    at,
			click: row.Btn != nil && row.Btn.Phase == "down",
			dwell: row.DwellMS,
			axRef: row.AXRef,
		})
		return rowKept
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// loadAXNodes parses the ax-tree sidecar, keeping only node rows whose
// ax_ref is wanted, grouped by ref and sorted by media time. The file is
// streamed, so a long recording's late snapshots are reached; only kept
// nodes cost memory.
func loadAXNodes(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, wanted map[string]bool, warnings *[]string) map[string][]axNode {
	t0, hasT0 := layerT0(layer, manifest)
	nodes := map[string][]axNode{}
	forEachSidecarRow(droot, layer, defaultAXSidecar, warnings, func(line []byte) rowResult {
		var row axRow
		if json.Unmarshal(line, &row) != nil {
			return rowMalformed
		}
		if row.Role == "" || !wanted[row.AXRef] {
			return rowSkipped // marker row, or a node nobody pointed at
		}
		at, res := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if res != rowKept {
			return res
		}
		nodes[row.AXRef] = append(nodes[row.AXRef], axNode{at: at, role: row.Role, title: row.Title, desc: row.Desc, domID: row.DOMID})
		return rowKept
	})
	for ref := range nodes {
		sort.SliceStable(nodes[ref], func(i, j int) bool { return nodes[ref][i].at < nodes[ref][j].at })
	}
	return nodes
}

// rowResult is what a sidecar row visitor did with one line.
type rowResult int

const (
	// rowKept: the row was used and counts against the kept-row bound.
	rowKept rowResult = iota
	// rowSkipped: a well-formed row this pass does not need.
	rowSkipped
	// rowMalformed: unparseable JSON or an unusable timestamp.
	rowMalformed
	// rowNoClock: the row carries only t_utc and no usable clock origin
	// exists to measure it against.
	rowNoClock
)

// Sidecar bounds. The scan bounds cap work (bytes and lines read); the kept
// bound caps memory (rows a visitor retained). Each is reported when it
// bites. Variables, not constants, so tests can drive the cut paths.
var (
	maxSidecarBytes    int64 = 64 << 20
	maxSidecarLineSize       = 1 << 20
	maxSidecarScanRows       = 1_000_000
)

// forEachSidecarRow streams a layer's JSONL sidecar through the folder root
// (symlinks refused) and calls fn per non-empty line. A file longer than
// maxSidecarBytes is cut there and its partial final line dropped; scanning
// stops after maxSidecarScanRows lines or maxSidecarRows kept rows. Every
// cut, malformed rows, and rows that had no clock origin are reported once
// through warnings.
func forEachSidecarRow(droot *os.Root, layer *format.DiscoveredLayer, defaultName string, warnings *[]string, fn func(line []byte) rowResult) {
	rel, f, size, err := openSidecar(droot, layer, defaultName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		*warnings = append(*warnings, fmt.Sprintf("%s layer sidecar unreadable: %v", layer.Envelope.Kind, err))
		return
	}
	defer f.Close()

	truncated := size > maxSidecarBytes
	if truncated {
		*warnings = append(*warnings, fmt.Sprintf("%s exceeds %d bytes; only the first %d bytes were read", rel, maxSidecarBytes, maxSidecarBytes))
	}
	sc := bufio.NewScanner(io.LimitReader(f, maxSidecarBytes))
	sc.Buffer(make([]byte, 0, 64<<10), maxSidecarLineSize)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && truncated {
			if i := bytes.IndexByte(data, '\n'); i < 0 {
				return len(data), nil, nil // the byte cap split this row: drop it
			}
		}
		return bufio.ScanLines(data, atEOF)
	})

	scanned, kept, malformed, noClock := 0, 0, 0, 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if scanned == maxSidecarScanRows {
			*warnings = append(*warnings, fmt.Sprintf("%s has more than %d rows; the rest were not read", rel, maxSidecarScanRows))
			break
		}
		if kept == maxSidecarRows {
			*warnings = append(*warnings, fmt.Sprintf("%s: more than %d usable rows; the rest were not read", rel, maxSidecarRows))
			break
		}
		scanned++
		switch fn(line) {
		case rowKept:
			kept++
		case rowMalformed:
			malformed++
		case rowNoClock:
			noClock++
		}
	}
	if err := sc.Err(); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s: reading stopped after %d rows: %v", rel, scanned, err))
	}
	if malformed > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: %d malformed rows skipped", rel, malformed))
	}
	if noClock > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: %d rows skipped: they carry t_utc but no t_ms, and no usable clock origin exists (neither the layer clock.t0 nor the conversation t0 parses)", rel, noClock))
	}
}

// openSidecar locates and opens a layer's JSONL sidecar: the envelope's
// .jsonl content refs first (a bare name resolves inside the layer
// directory, a slashed path from the discussion folder), then the default
// name in the layer directory. Every candidate must be a local path and a
// regular file (probed no-follow through the folder root); a candidate that
// is missing, a symlink, or otherwise unusable falls through to the next.
// When none works, the last real failure is returned (fs.ErrNotExist only
// when every candidate was simply absent).
func openSidecar(droot *os.Root, layer *format.DiscoveredLayer, defaultName string) (string, *os.File, int64, error) {
	layerDir := path.Dir(layer.Path)
	if layer.Layout != format.LayoutFolder {
		layerDir = format.LayersDirName
	}
	var candidates []string
	if c := layer.Envelope.Content; c != nil {
		for _, ref := range c.Refs {
			if !strings.HasSuffix(ref.Path, ".jsonl") {
				continue
			}
			if strings.Contains(ref.Path, "/") {
				candidates = append(candidates, ref.Path)
			} else {
				candidates = append(candidates, layerDir+"/"+ref.Path)
			}
		}
	}
	candidates = append(candidates, layerDir+"/"+defaultName)

	lastErr := fs.ErrNotExist
	tried := map[string]bool{}
	for _, rel := range candidates {
		if tried[rel] || strings.ContainsAny(rel, "\\\x00") || path.Clean(rel) != rel || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue // duplicate, or a hostile ref that is never joined
		}
		tried[rel] = true
		native := filepath.FromSlash(rel)
		info, err := droot.Lstat(native)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				lastErr = err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			lastErr = fmt.Errorf("%s is not a regular file (symlinks are never followed)", rel)
			continue
		}
		f, err := droot.Open(native)
		if err != nil {
			lastErr = err
			continue
		}
		return rel, f, info.Size(), nil
	}
	return "", nil, 0, lastErr
}

// layerT0 is the clock origin rows are measured against: the layer's own
// clock.t0 first, the conversation manifest's t0 second.
func layerT0(layer *format.DiscoveredLayer, manifest *format.Manifest) (time.Time, bool) {
	if c := layer.Envelope.Clock; c != nil && c.T0 != "" {
		if t, err := time.Parse(time.RFC3339Nano, c.T0); err == nil {
			return t, true
		}
	}
	return manifestT0(manifest)
}

// Bounds on untrusted row timestamps, checked before any conversion so a
// hostile value cannot overflow time.Duration or int64.
const (
	// maxEpochMS is year ~2286 in epoch milliseconds.
	maxEpochMS = 1e13
	// earlySlack tolerates a row stamped slightly before t0 (clock skew
	// between the capture threads); it clamps to zero. Anything earlier is
	// not part of this recording.
	earlySlack = 2 * time.Second
)

// rowMediaTime maps a row onto the media clock: t_utc − t0 when t_utc is
// present and a clock origin is known (t_utc as an RFC 3339 string or epoch
// milliseconds), else t_ms as a millisecond offset. The result must land in
// [−earlySlack, maxMediaOffset]; slightly-early rows clamp to zero, and
// everything else — non-finite, negative, absurd — is malformed. A row with
// only t_utc and no usable origin is rowNoClock, not malformed.
func rowMediaTime(tUTC json.RawMessage, tMS *float64, t0 time.Time, hasT0 bool) (time.Duration, rowResult) {
	hasUTC := len(tUTC) > 0 && string(tUTC) != "null"
	if hasUTC && hasT0 {
		var at time.Time
		var s string
		var ms float64
		switch {
		case json.Unmarshal(tUTC, &s) == nil:
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return 0, rowMalformed
			}
			at = t
		case json.Unmarshal(tUTC, &ms) == nil:
			if math.IsNaN(ms) || ms < 0 || ms > maxEpochMS {
				return 0, rowMalformed
			}
			at = time.UnixMilli(int64(ms))
		default:
			return 0, rowMalformed
		}
		return boundOffset(at.Sub(t0))
	}
	if tMS != nil {
		v := *tMS
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -float64(earlySlack.Milliseconds()) || v > float64(maxMediaOffset.Milliseconds()) {
			return 0, rowMalformed
		}
		return boundOffset(time.Duration(v * float64(time.Millisecond)))
	}
	if hasUTC {
		return 0, rowNoClock
	}
	return 0, rowMalformed
}

// boundOffset applies the media-offset range to an already-computed offset
// (time.Time.Sub saturates rather than overflowing, so the check is sound).
func boundOffset(d time.Duration) (time.Duration, rowResult) {
	if d < -earlySlack || d > maxMediaOffset {
		return 0, rowMalformed
	}
	return max(d, 0), rowKept
}
