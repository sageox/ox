package read

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
// Missing layers mean no pointing and no error; unreadable or oversized
// layers are reported through warnings.
// served and out are parallel: out[i] is the rendering of served[i].
func attachPointing(droot *os.Root, manifest *format.Manifest, served []vtt.Cue, out []TranscriptCue, warnings *[]string) {
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

	// Pick events per served cue first, so the AX pass only keeps the nodes
	// actually referenced — the AX sidecar is the large one.
	type pick struct {
		cue    int
		sample pointerSample
	}
	var picks []pick
	wanted := map[string]bool{}
	for i, c := range served {
		for _, s := range pickPointerSamples(samples, c.Start, c.End) {
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

// pickPointerSamples chooses at most maxPointingPerCue visible samples inside
// [start, end): mouse-down clicks first (in time order), then the longest
// dwells, one event per element; when neither exists, the first visible
// sample over a known element stands in as a hover.
func pickPointerSamples(samples []pointerSample, start, end time.Duration) []pointerSample {
	var clicks, dwells []pointerSample
	var hover *pointerSample
	for i := range samples {
		s := samples[i]
		if s.at < start || s.at >= end {
			continue
		}
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
	forEachSidecarRow(droot, layer, defaultPointerSidecar, warnings, func(line []byte) bool {
		var row pointerRow
		if json.Unmarshal(line, &row) != nil {
			return false
		}
		if len(row.Target) > 0 && string(row.Target) != "null" {
			return true // geometry transition, not a sample
		}
		if !row.Vis {
			return true
		}
		at, ok := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if !ok {
			return false
		}
		out = append(out, pointerSample{
			at:    at,
			click: row.Btn != nil && row.Btn.Phase == "down",
			dwell: row.DwellMS,
			axRef: row.AXRef,
		})
		return true
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// loadAXNodes parses the ax-tree sidecar, keeping only node rows whose
// ax_ref is wanted, grouped by ref and sorted by media time.
func loadAXNodes(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, wanted map[string]bool, warnings *[]string) map[string][]axNode {
	t0, hasT0 := layerT0(layer, manifest)
	nodes := map[string][]axNode{}
	forEachSidecarRow(droot, layer, defaultAXSidecar, warnings, func(line []byte) bool {
		var row axRow
		if json.Unmarshal(line, &row) != nil {
			return false
		}
		if row.Role == "" || !wanted[row.AXRef] {
			return true // marker row, or a node nobody pointed at
		}
		at, ok := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if !ok {
			return false
		}
		nodes[row.AXRef] = append(nodes[row.AXRef], axNode{at: at, role: row.Role, title: row.Title, desc: row.Desc, domID: row.DOMID})
		return true
	})
	for ref := range nodes {
		sort.SliceStable(nodes[ref], func(i, j int) bool { return nodes[ref][i].at < nodes[ref][j].at })
	}
	return nodes
}

// forEachSidecarRow reads a layer's JSONL sidecar through the folder root
// (bounded bytes and rows, symlinks refused) and calls fn per non-empty
// line. fn returns false for a malformed row; malformed rows and any bound
// that cut the read are reported once through warnings.
func forEachSidecarRow(droot *os.Root, layer *format.DiscoveredLayer, defaultName string, warnings *[]string, fn func(line []byte) bool) {
	rel, data, truncated, err := readSidecar(droot, layer, defaultName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		*warnings = append(*warnings, fmt.Sprintf("%s layer sidecar unreadable: %v", layer.Envelope.Kind, err))
		return
	}
	if truncated {
		*warnings = append(*warnings, fmt.Sprintf("%s exceeds %d bytes; only the first %d bytes were read", rel, maxScreenFileBytes, maxScreenFileBytes))
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i] // drop the partial final row
		}
	}
	rows, malformed := 0, 0
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if rows == maxSidecarRows {
			*warnings = append(*warnings, fmt.Sprintf("%s has more than %d rows; the rest were not read", rel, maxSidecarRows))
			break
		}
		rows++
		if !fn(line) {
			malformed++
		}
	}
	if malformed > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: %d malformed rows skipped", rel, malformed))
	}
}

// readSidecar locates and reads a layer's JSONL sidecar: the envelope's
// .jsonl content ref first (a bare name resolves inside the layer
// directory, a slashed path from the discussion folder), else the default
// name in the layer directory. Every candidate must be a local path, and is
// read through the folder root with symlinks refused.
func readSidecar(droot *os.Root, layer *format.DiscoveredLayer, defaultName string) (string, []byte, bool, error) {
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
	for _, rel := range candidates {
		if strings.ContainsAny(rel, "\\\x00") || path.Clean(rel) != rel || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue // hostile ref: never joined
		}
		data, truncated, err := readBoundedFile(droot, filepath.FromSlash(rel), maxScreenFileBytes)
		if err == nil {
			return rel, data, truncated, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return rel, nil, false, err
		}
		lastErr = err
	}
	return "", nil, false, lastErr
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

// rowMediaTime maps a row onto the media clock: t_utc − t0 when both are
// known (t_utc as an RFC 3339 string or epoch milliseconds), else t_ms as a
// millisecond offset. Negative offsets clamp to zero.
func rowMediaTime(tUTC json.RawMessage, tMS *float64, t0 time.Time, hasT0 bool) (time.Duration, bool) {
	if hasT0 && len(tUTC) > 0 && string(tUTC) != "null" {
		var s string
		if json.Unmarshal(tUTC, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return max(t.Sub(t0), 0), true
			}
		} else {
			var ms float64
			if json.Unmarshal(tUTC, &ms) == nil {
				return max(time.UnixMilli(int64(ms)).Sub(t0), 0), true
			}
		}
	}
	if tMS != nil {
		return max(time.Duration(*tMS*float64(time.Millisecond)), 0), true
	}
	return 0, false
}
