package read

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// The screen-side readers behind `ox conversation walkthrough`: they turn a
// walkthrough's client layers (pointer, ax-tree) into the few moments worth
// an AI coworker's attention — clicks, deliberate dwells, and page changes —
// each naming the element or page involved. The producer is SageOx Desktop
// (src/shared/layers/take.ts documents the row forms); every value is
// screen-derived, untrusted, and cleaned before it leaves this file.

// minDwell is how long the pointer has to rest on one element before the
// rest counts as pointing at it. Desktop's deixis resolver uses the same
// number for "pointing-and-holding is deliberate reference"
// (src/shared/layers/deixis.ts DWELL_INTENT_MS).
const minDwell = 2 * time.Second

// maxAncestorHops bounds the parent walk that names an unnamed element by
// the nearest named element around it.
const maxAncestorHops = 8

// ScreenElement is an accessibility element the narrator clicked or rested
// the pointer on. Only role, name, and DOM id are surfaced — never an AX
// value, which can carry text the user typed.
type ScreenElement struct {
	Role  string `json:"role,omitempty"`
	Title string `json:"title,omitempty"`
	DOMID string `json:"dom_id,omitempty"`
	// Unnamed marks an element with no title, description, or DOM id, or one
	// the accessibility layer could not resolve.
	Unnamed bool `json:"unnamed,omitempty"`
	// Within names the nearest named element around an unnamed one (a
	// generic group inside a link, a list row, a dialog), so the moment
	// still says where the pointer was.
	Within *ScreenElement `json:"within,omitempty"`
}

// ScreenPage is what the captured window was showing: the web page title
// and address (scheme, host, and path only), or the window title for a
// native app.
type ScreenPage struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// pointerMoment is a click or a dwell on the media clock, before naming.
// at is where the moment sits on the timeline (a dwell's is where the rest
// began, back-dated by the first row's dwell_ms); seen is the first row that
// observed it, the instant the element is named at. The producer writes an
// element's node row when a pointer row first references it, so a rest that
// starts on a never-hovered element has no node row until seen, and naming
// it at the back-dated start would find nothing.
type pointerMoment struct {
	at    time.Duration
	seen  time.Duration
	click bool
	dwell time.Duration
	axRef string
}

// pointerSeqRow is the subset of a pointer.jsonl row the moment pass needs.
// Hidden and transition rows are kept as breaks: a dwell never spans one.
type pointerSeqRow struct {
	TUTC    json.RawMessage `json:"t_utc"`
	TMS     *float64        `json:"t_ms"`
	Vis     bool            `json:"vis"`
	DwellMS float64         `json:"dwell_ms"`
	AXRef   string          `json:"ax_ref"`
	Btn     *struct {
		Phase string `json:"phase"`
	} `json:"btn"`
	Target json.RawMessage `json:"target"`
}

// loadPointerMoments reads the pointer sidecar into clicks (mouse-down
// events inside the window) and dwells (one per uninterrupted rest of at
// least minDwell on one element, placed where the rest began).
func loadPointerMoments(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, warnings *[]string) []pointerMoment {
	t0, hasT0 := layerT0(layer, manifest)
	type seq struct {
		at    time.Duration
		brk   bool
		click bool
		dwell time.Duration
		axRef string
	}
	var rows []seq
	forEachSidecarRow(droot, layer, defaultPointerSidecar, warnings, func(line []byte) rowResult {
		var row pointerSeqRow
		if json.Unmarshal(line, &row) != nil {
			return rowMalformed
		}
		at, res := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if res != rowKept {
			return res
		}
		transition := len(row.Target) > 0 && string(row.Target) != "null"
		s := seq{at: at, brk: transition || !row.Vis, axRef: row.AXRef}
		if !s.brk {
			s.click = row.Btn != nil && row.Btn.Phase == "down"
			if row.DwellMS > 0 && row.DwellMS < float64(maxMediaOffset.Milliseconds()) {
				s.dwell = time.Duration(row.DwellMS * float64(time.Millisecond))
			}
		}
		rows = append(rows, s)
		return rowKept
	})
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at < rows[j].at })

	var out []pointerMoment
	var open *pointerMoment // the rest in progress; dwell holds its peak
	closeRest := func() {
		if open != nil && open.dwell >= minDwell {
			out = append(out, *open)
		}
		open = nil
	}
	for _, s := range rows {
		if s.click {
			out = append(out, pointerMoment{at: s.at, seen: s.at, click: true, axRef: s.axRef})
		}
		switch {
		case s.brk || s.dwell <= 0:
			closeRest()
		case open != nil && s.axRef == open.axRef && s.dwell >= open.dwell:
			open.dwell = s.dwell
		default:
			closeRest()
			open = &pointerMoment{at: max(s.at-s.dwell, 0), seen: s.at, dwell: s.dwell, axRef: s.axRef}
		}
	}
	closeRest()
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// defaultHintsSidecar is the keyframe-hints layer's row file.
const defaultHintsSidecar = "keyframe-hints.jsonl"

// hintReasonMark is the keyframe-hints reason SageOx Desktop writes when the
// presenter presses "Mark this moment" (detail {source:"user", seq}).
const hintReasonMark = "mark"

// markMoment is one presenter mark on the media clock.
type markMoment struct {
	at  time.Duration
	seq int
}

// hintRow is the subset of a keyframe-hints.jsonl row the mark pass needs.
type hintRow struct {
	TUTC   json.RawMessage `json:"t_utc"`
	TMS    *float64        `json:"t_ms"`
	Reason string          `json:"reason"`
	Detail struct {
		Seq float64 `json:"seq"`
	} `json:"detail"`
}

// loadMarks reads the presenter marks out of the keyframe-hints layer, time
// sorted. Inferred rows (click, dwell, focus-change) are skipped without
// counting against the kept-row cap; the moments they stand for come from
// the pointer and ax-tree layers.
func loadMarks(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, warnings *[]string) []markMoment {
	t0, hasT0 := layerT0(layer, manifest)
	var out []markMoment
	forEachSidecarRow(droot, layer, defaultHintsSidecar, warnings, func(line []byte) rowResult {
		var row hintRow
		if json.Unmarshal(line, &row) != nil {
			return rowMalformed
		}
		if row.Reason != hintReasonMark {
			return rowSkipped
		}
		at, res := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if res != rowKept {
			return res
		}
		mk := markMoment{at: at}
		if row.Detail.Seq >= 1 && row.Detail.Seq < 1e6 {
			mk.seq = int(row.Detail.Seq)
		}
		out = append(out, mk)
		return rowKept
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// axTreeRow is the subset of an ax.jsonl row the walkthrough reads: marker
// rows (snapshot + reason, no role) and node rows. `value` is deliberately
// never decoded; `url` is decoded only to reduce a web area's address to
// scheme, host, and path (pageURL) — never a query or fragment.
type axTreeRow struct {
	TUTC     json.RawMessage `json:"t_utc"`
	TMS      *float64        `json:"t_ms"`
	Snapshot json.RawMessage `json:"snapshot"`
	Reason   string          `json:"reason"`
	AXRef    string          `json:"ax_ref"`
	Parent   string          `json:"parent"`
	Depth    *int            `json:"depth"`
	Role     string          `json:"role"`
	Title    string          `json:"title"`
	Desc     string          `json:"desc"`
	DOMID    string          `json:"dom_id"`
	URL      string          `json:"url"`
}

// axTreeNode is one node row on the media clock.
type axTreeNode struct {
	at                       time.Duration
	parent                   string
	depth                    int
	role, title, desc, domID string
	url                      string
}

// sameState reports whether two rows describe the same element state, so a
// snapshot that re-walks an unchanged element costs nothing.
func (n axTreeNode) sameState(o axTreeNode) bool {
	return n.parent == o.parent && n.role == o.role && n.title == o.title &&
		n.desc == o.desc && n.domID == o.domID && n.url == o.url
}

// axTree is the walkthrough's accessibility layer, indexed for naming
// (each element's distinct states by ref, time-sorted) and for the page
// timeline (per snapshot: why it was taken and its outermost web area and
// window).
type axTree struct {
	byRef     map[string][]axTreeNode
	snapshots []axSnapshot
}

type axSnapshot struct {
	key         string
	at          time.Duration
	reason      string
	web, window *axTreeNode
}

// loadAXTree streams the ax-tree sidecar once. Interval snapshots re-walk
// the same elements every few seconds, so only a node row that changes its
// element's state is kept (a repeat is skipped and does not count against
// forEachSidecarRow's kept-row cap), and a snapshot keeps just the two rows
// the page timeline reads. That keeps a long walkthrough's late snapshots
// within reach.
func loadAXTree(droot *os.Root, layer *format.DiscoveredLayer, manifest *format.Manifest, warnings *[]string) *axTree {
	t0, hasT0 := layerT0(layer, manifest)
	tree := &axTree{byRef: map[string][]axTreeNode{}}
	index := map[string]int{} // snapshot key -> position in tree.snapshots
	snapshot := func(key string, at time.Duration) (*axSnapshot, bool) {
		i, ok := index[key]
		if !ok {
			i = len(tree.snapshots)
			index[key] = i
			tree.snapshots = append(tree.snapshots, axSnapshot{key: key, at: at})
		}
		return &tree.snapshots[i], !ok
	}
	forEachSidecarRow(droot, layer, defaultAXSidecar, warnings, func(line []byte) rowResult {
		var row axTreeRow
		if json.Unmarshal(line, &row) != nil {
			return rowMalformed
		}
		at, res := rowMediaTime(row.TUTC, row.TMS, t0, hasT0)
		if res != rowKept {
			return res
		}
		key := strings.Trim(string(row.Snapshot), `"`)
		if row.Role == "" {
			if key == "" {
				return rowSkipped
			}
			snap, created := snapshot(key, at)
			snap.reason = row.Reason
			if created {
				return rowKept
			}
			return rowSkipped
		}
		depth := -1
		if row.Depth != nil {
			depth = *row.Depth
		}
		n := axTreeNode{
			at: at, parent: row.Parent, depth: depth,
			role: row.Role, title: row.Title, desc: row.Desc, domID: row.DOMID,
		}
		if row.Role == "AXWebArea" {
			n.url = pageURL(row.URL)
		}
		result := rowSkipped
		if key != "" && (n.role == "AXWebArea" || n.role == "AXWindow") {
			snap, created := snapshot(key, at)
			if created {
				result = rowKept
			}
			slot := &snap.window
			if n.role == "AXWebArea" {
				slot = &snap.web
			}
			if named := cleanScreenText(n.title) != "" || n.url != ""; named && shallower(n, *slot) {
				kept := n
				*slot = &kept
			}
		}
		if row.AXRef != "" {
			states := tree.byRef[row.AXRef]
			if len(states) == 0 || !states[len(states)-1].sameState(n) {
				tree.byRef[row.AXRef] = append(states, n)
				result = rowKept
			}
		}
		return result
	})
	for ref := range tree.byRef {
		nodes := tree.byRef[ref]
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].at < nodes[j].at })
	}
	return tree
}

// shallower reports whether n is a better page candidate than best: any row
// beats none, and a known smaller depth beats a larger or unknown one.
func shallower(n axTreeNode, best *axTreeNode) bool {
	return best == nil || (n.depth >= 0 && (best.depth < 0 || n.depth < best.depth))
}

// nodeAt returns the latest node row for ref at or before at. The producer
// writes a node row before the first pointer row that references it, so a
// lookup never needs to look forward.
func (t *axTree) nodeAt(ref string, at time.Duration) *axTreeNode {
	if t == nil || ref == "" {
		return nil
	}
	var best *axTreeNode
	for i, n := range t.byRef[ref] {
		if n.at > at {
			break
		}
		best = &t.byRef[ref][i]
	}
	return best
}

// element names ref at instant at. An unnamed element gets the nearest
// named ancestor as Within. The window and the web page are skipped (the
// page moments already name them), and so is a container titled with the
// window's own title — browsers wrap their whole content in one.
func (t *axTree) element(ref string, at time.Duration) *ScreenElement {
	n := t.nodeAt(ref, at)
	if n == nil {
		return &ScreenElement{Unnamed: true}
	}
	el := describeNode(n)
	if !el.Unnamed {
		return el
	}
	var chain []*axTreeNode
	windowTitle := ""
	for cur := n; len(chain) < maxAncestorHops && cur.parent != ""; {
		cur = t.nodeAt(cur.parent, at)
		if cur == nil {
			break
		}
		if cur.role == "AXWindow" {
			windowTitle = cleanScreenText(cur.title)
			break
		}
		chain = append(chain, cur)
	}
	for _, up := range chain {
		if up.role == "AXWebArea" {
			break
		}
		if d := describeNode(up); !d.Unnamed && (d.Title == "" || d.Title != windowTitle) {
			el.Within = &ScreenElement{Role: d.Role, Title: d.Title, DOMID: d.DOMID}
			break
		}
	}
	return el
}

// describeNode cleans one node into its output form. "Named" follows the
// producer's own definition: a title, description, or DOM id.
func describeNode(n *axTreeNode) *ScreenElement {
	el := &ScreenElement{
		Role:  cleanScreenText(n.role),
		Title: cleanScreenText(n.title),
		DOMID: cleanScreenText(n.domID),
	}
	if el.Title == "" {
		el.Title = cleanScreenText(n.desc)
	}
	el.Unnamed = el.Title == "" && el.DOMID == ""
	return el
}

// pageMoment is a change of what the captured window showed.
type pageMoment struct {
	at   time.Duration
	page ScreenPage
}

// pages derives the page timeline from the snapshots that walked up to the
// window (every reason but hover, which writes only nodes not yet written).
// A web page is named by the snapshot's outermost web area (the top
// document, not an embedded frame); a moment is emitted when it differs from
// the last one. A snapshot with no web area (the browser's own toolbar, or a
// native app) names the page by the window title, but only when that title
// changed — so clicking the address bar does not read as leaving the page.
func (t *axTree) pages() []pageMoment {
	if t == nil {
		return nil
	}
	snaps := append([]axSnapshot(nil), t.snapshots...)
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].at < snaps[j].at })
	var out []pageMoment
	var lastWeb ScreenPage
	lastWindow := ""
	for _, s := range snaps {
		if s.reason == "hover" {
			continue
		}
		web, window := s.web, s.window
		windowTitle := ""
		if window != nil {
			windowTitle = cleanScreenText(window.title)
		}
		switch {
		case web != nil:
			page := ScreenPage{Title: cleanScreenText(web.title), URL: web.url}
			n := len(out)
			switch {
			case page == lastWeb:
			case n > 0 && lastWeb.Title == "" && page.URL != "" && page.URL == lastWeb.URL && out[n-1].page == lastWeb:
				// The page was announced before its title loaded (a
				// client-side navigation in flight): the title completes
				// that moment rather than repeating it.
				out[n-1].page.Title = page.Title
			default:
				out = append(out, pageMoment{at: s.at, page: page})
			}
			lastWeb = page
		case windowTitle != "" && windowTitle != lastWindow:
			out = append(out, pageMoment{at: s.at, page: ScreenPage{Title: windowTitle}})
			lastWeb = ScreenPage{} // a new window: the next web page is news
		}
		if windowTitle != "" {
			lastWindow = windowTitle
		}
	}
	return out
}

// pageURL reduces a web area's address to scheme, host, and path. The query
// and fragment are dropped (they carry tokens, search text, and session
// ids), as is any userinfo; anything but http(s) yields nothing.
func pageURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	clean := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return cleanScreenText(clean.String())
}

// screenLayerMeta is the part of a client layer's envelope the walkthrough
// reads beyond format.LayerEnvelope: the captured target and the spans where
// the pointer was outside it.
type screenLayerMeta struct {
	Content struct {
		TargetInitial *struct {
			Kind  string `json:"kind"`
			App   string `json:"app"`
			Title string `json:"title"`
			Size  *struct {
				W float64 `json:"w"`
				H float64 `json:"h"`
			} `json:"size"`
		} `json:"target_initial"`
	} `json:"content"`
	Coverage *struct {
		Gaps []struct {
			StartUTC string `json:"start_utc"`
			EndUTC   string `json:"end_utc"`
			Reason   string `json:"reason"`
		} `json:"gaps"`
	} `json:"coverage"`
}

// loadScreenLayerMeta re-reads a layer envelope (bounded, no-follow) for the
// fields format.LayerEnvelope does not model. Failure reads as "no extras":
// discovery already parsed this file once and reported anything wrong.
func loadScreenLayerMeta(droot *os.Root, layer *format.DiscoveredLayer) *screenLayerMeta {
	raw, truncated, err := readBoundedFile(droot, filepath.FromSlash(layer.Path), maxScreenFileBytes)
	if err != nil || truncated {
		return nil
	}
	var meta screenLayerMeta
	if json.Unmarshal(raw, &meta) != nil {
		return nil
	}
	return &meta
}
