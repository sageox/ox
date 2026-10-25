package viz

// render_waterfall.go — the TIME waterfall: one row per unit of work, placed on
// one shared clock, so "what ran when, what overlapped, and what the long pole
// was" is a glance instead of a spreadsheet.
//
// This is the browser-devtools/WebPageTest chart shape applied to any timed
// pipeline (a request cascade, a Temporal workflow, a build, a recorded
// discussion's derived artifacts). It is DISTINCT from `cost-waterfall`, which
// is the financial bridge chart — accumulating magnitudes, not elapsed time.
//
// Two things it does that the browser waterfalls deliberately do not, and which
// are the entire reason to reach for this instead of a Gantt:
//
//   - CRITICAL PATH. Given `after` edges, the chain that actually set the total
//     is computed and everything off it is dimmed. On a wide fan-out, the eye
//     otherwise picks the widest bar, which is frequently NOT the bar that made
//     the pipeline slow.
//   - WAIT GAPS. The interval between a dependency finishing and its dependent
//     starting is dead time — queueing, polling, a scheduler tick. It is drawn
//     explicitly, because it is invisible in every bar-only chart and is
//     routinely the cheapest latency to delete.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// waterfallData is the `ox viz render waterfall --data` payload.
type waterfallData struct {
	Title string `json:"title"`
	// Unit labels durations after Scale is applied ("s", "ms", "min").
	Unit string `json:"unit"`
	// Scale divides every raw start/duration for DISPLAY only; geometry uses the
	// raw values. Feeding milliseconds with scale=1000 and unit="s" is the
	// common case and avoids callers hand-converting (and rounding) upstream.
	Scale      float64              `json:"scale"`
	Phases     []waterfallPhase     `json:"phases"`
	Milestones []waterfallMilestone `json:"milestones"`
	Rows       []waterfallRow       `json:"rows"`
	// HideOffPath drops non-critical rows entirely instead of dimming them.
	// Off by default: the rows that did NOT matter are themselves evidence.
	HideOffPath bool `json:"hide_off_path"`
}

// waterfallPhase declares one segment class and its color, e.g. queued vs running.
type waterfallPhase struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Color string `json:"color"`
}

// waterfallMilestone is a labeled vertical line across every row — the
// equivalent of DOMContentLoaded / onLoad in a network waterfall.
type waterfallMilestone struct {
	At    float64 `json:"at"`
	Label string  `json:"label"`
	Color string  `json:"color"`
}

type waterfallRow struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Start float64 `json:"start"`
	// Dur is the shorthand for a single unsegmented bar. Ignored when Segments
	// is non-empty.
	Dur      float64            `json:"dur"`
	Segments []waterfallSegment `json:"segments"`
	// After names the row this one waited on. It drives BOTH the critical-path
	// walk and the wait-gap bracket; without it the chart degrades gracefully to
	// a plain timeline.
	After string `json:"after"`
	// Depth indents the label to show nesting (parent workflow → child → activity).
	Depth int `json:"depth"`
	// Critical forces on-path highlighting when the caller already knows the
	// answer and has no `after` edges to derive it from.
	Critical bool   `json:"critical"`
	Status   string `json:"status"` // ""|ok|warn|error — error/warn tint the bar
	Note     string `json:"note"`
}

type waterfallSegment struct {
	Phase string  `json:"phase"`
	Dur   float64 `json:"dur"`
}

// waterfall geometry. The viewBox is wide because a waterfall's whole value is
// horizontal resolution; CSS scales it down to the column width.
const (
	wfW        = 760.0 // viewBox width
	wfGutter   = 214.0 // label column
	wfRight    = 66.0  // duration labels live here, outside the plot
	wfTop      = 30.0  // top axis
	wfRowH     = 15.0
	wfBarH     = 8.0
	wfBottomAx = 30.0 // bottom axis + its tick labels
	// wfMonoCh is the advance width of one Spline Sans Mono glyph at the 8.5px
	// used for duration and note labels. Used only to budget horizontal room so
	// two right-hand labels cannot overlap; a few tenths off is harmless.
	wfMonoCh = 5.2
)

func (r waterfallRow) total() float64 {
	if len(r.Segments) == 0 {
		return r.Dur
	}
	var t float64
	for _, s := range r.Segments {
		t += s.Dur
	}
	return t
}

func (r waterfallRow) end() float64 { return r.Start + r.total() }

// wfDur formats a duration label. It deliberately does NOT use fmtUnit: that
// helper prefixes any single-character unit ("$12"), which is right for currency
// and wrong for every time unit — "s75.8" instead of "75.8 s". Durations always
// read value-then-unit.
func wfDur(v float64, unit string) string {
	n := fmtNum(waterfallRound(v))
	u := strings.TrimSpace(unit)
	if u == "" {
		return n
	}
	return n + " " + u
}

func renderWaterfall(data []byte) (string, error) {
	var d waterfallData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("waterfall data: %w", err)
	}
	if len(d.Rows) == 0 {
		return "", fmt.Errorf("waterfall: no rows")
	}
	if len(d.Rows) > 120 {
		return "", fmt.Errorf("waterfall: %d rows exceeds the 120-row legibility cap — aggregate, or split by lane", len(d.Rows))
	}
	if d.Scale <= 0 {
		d.Scale = 1
	}
	for i, r := range d.Rows {
		if r.Start < 0 {
			return "", fmt.Errorf("waterfall: row %d (%q) has a negative start", i, r.Label)
		}
		if r.total() < 0 {
			return "", fmt.Errorf("waterfall: row %d (%q) has a negative duration", i, r.Label)
		}
	}

	// Phase colors. An undeclared phase key still renders — it just falls to the
	// rotating palette rather than erroring, so a caller can add a phase without
	// also editing a legend.
	phaseColor := map[string]string{}
	phaseOrder := make([]waterfallPhase, 0, len(d.Phases))
	for i, p := range d.Phases {
		phaseColor[p.Key] = paletteColor(p.Color, i)
		if p.Label == "" {
			p.Label = p.Key
		}
		phaseOrder = append(phaseOrder, p)
	}

	// Sort by start so the cascade reads top-left to bottom-right. Ties keep
	// input order (stable) so a caller's deliberate grouping survives.
	rows := make([]waterfallRow, len(d.Rows))
	copy(rows, d.Rows)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Start < rows[j].Start })

	span := 0.0
	for _, r := range rows {
		if e := r.end(); e > span {
			span = e
		}
	}
	if span <= 0 {
		span = 1
	}

	// A critical path is only worth ASSERTING when the caller supplied the
	// information to derive one: a chain of `after` edges, or explicit
	// `critical` flags. Without either, the backwards walk halts at the
	// last-finishing row — lighting one arbitrary row (often the shortest) and
	// dimming every other, which is a confident wrong answer to the only
	// question this chart is asked. Degrade to a plain timeline instead: every
	// row at full opacity, no long pole, no off-path legend. Rows carrying no
	// `id` at all cannot be path members, so this is also what keeps the
	// simplest possible input from rendering as an all-dimmed ghost.
	onPath, chained := waterfallCriticalPath(rows)
	derived := chained
	for _, r := range rows {
		if r.Critical {
			derived = true
			break
		}
	}
	if !derived {
		onPath = nil
	}
	onCritical := func(r waterfallRow) bool { return !derived || onPath[r.ID] || r.Critical }

	// The long pole is the longest row in the ASSERTED set, not the longest row
	// in the chart: on a wide fan-out the widest bar is frequently off the path
	// and is precisely the row the reader must not attack.
	longPole, poleDur := "", -1.0
	if derived {
		for _, r := range rows {
			if onCritical(r) && r.total() > poleDur {
				longPole, poleDur = r.ID, r.total()
			}
		}
	}

	visible := rows
	if d.HideOffPath && derived {
		visible = visible[:0]
		for _, r := range rows {
			if onCritical(r) {
				visible = append(visible, r)
			}
		}
		if len(visible) == 0 {
			return "", fmt.Errorf("waterfall: hide_off_path left no rows — no row is on the critical path")
		}
	}

	// Notes get their OWN reserved column, but only when some row has one. The
	// last row of a waterfall always ends at the span, so its duration label is
	// always jammed against the right edge — sharing that space with notes meant
	// the final row (often the interesting one) silently lost its note. Charts
	// without notes pay nothing and keep the full plot width.
	noteW := 0.0
	for _, r := range d.Rows {
		if r.Note != "" {
			noteW = 96
			break
		}
	}
	plotW := wfW - wfGutter - wfRight - noteW
	height := wfTop + float64(len(visible))*wfRowH + wfBottomAx
	px := func(v float64) float64 { return wfGutter + (v/span)*plotW }
	// Row index → the y of that row's bar top.
	rowY := func(i int) float64 { return wfTop + float64(i)*wfRowH + (wfRowH-wfBarH)/2 }

	var b strings.Builder
	b.WriteString(`<figure class="wfall">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	fmt.Fprintf(&b, `<svg class="wfall-svg" viewBox="0 0 %s %s" role="img" aria-label="%s">`,
		co(wfW), co(height), esc(waterfallAria(d.Title, visible, span, d.Unit, d.Scale, longPole)))

	// Zebra striping, drawn first so everything sits on top of it. Row banding is
	// what lets the eye track a label 700px to its bar without a leader line.
	for i := range visible {
		if i%2 == 1 {
			fmt.Fprintf(&b, `<rect class="wfall-zebra" x="0" y="%s" width="%s" height="%s"/>`,
				co(wfTop+float64(i)*wfRowH), co(wfW), co(wfRowH))
		}
	}

	// Time grid + the axis repeated top and bottom (the reference waterfalls do
	// this; on a 40-row chart the bottom axis is the only one still on screen).
	ticks := waterfallTicks(span)
	axisY := wfTop + float64(len(visible))*wfRowH
	for _, t := range ticks {
		x := px(t)
		fmt.Fprintf(&b, `<line class="wfall-grid" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x), co(wfTop), co(x), co(axisY))
		lab := esc(wfDur(t/d.Scale, d.Unit))
		fmt.Fprintf(&b, `<text class="wfall-tick" x="%s" y="%s" text-anchor="middle">%s</text>`, co(x), co(wfTop-8), lab)
		fmt.Fprintf(&b, `<text class="wfall-tick" x="%s" y="%s" text-anchor="middle">%s</text>`, co(x), co(axisY+14), lab)
	}

	// Milestones — vertical rules across every row, labeled at the top.
	for _, m := range d.Milestones {
		if m.At < 0 || m.At > span {
			continue
		}
		x := px(m.At)
		col := colorVar(m.Color)
		fmt.Fprintf(&b, `<line class="wfall-ms" x1="%s" y1="%s" x2="%s" y2="%s" style="stroke:%s"/>`,
			co(x), co(wfTop-3), co(x), co(axisY), col)
		if m.Label != "" {
			fmt.Fprintf(&b, `<text class="wfall-mslab" x="%s" y="%s" text-anchor="start" style="fill:%s">%s</text>`,
				co(x+3), co(wfTop-18), col, esc(m.Label))
		}
	}

	endByID := map[string]float64{}
	yByID := map[string]float64{}
	for i, r := range visible {
		if r.ID != "" {
			endByID[r.ID] = r.end()
			yByID[r.ID] = rowY(i) + wfBarH/2
		}
	}

	// Legend entries are gated on what the chart actually drew: naming an
	// encoding the reader cannot see is as confusing as leaving one unnamed.
	var drewGap, dimmedAny bool

	for i, r := range visible {
		y := rowY(i)
		crit := onCritical(r)
		cls := "wfall-bar"
		if !crit {
			cls += " wfall-off"
			dimmedAny = true
		}

		// Label: index, indent by depth, then the name. The index is what a
		// reviewer says out loud ("row 14 is the long pole").
		indent := 8 + float64(min(r.Depth, 6))*7
		fmt.Fprintf(&b, `<text class="wfall-idx" x="12" y="%s">%d</text>`, co(y+wfBarH-1), i+1)
		labCls := "wfall-lab"
		if !crit {
			labCls += " wfall-off"
		} else if r.ID != "" && r.ID == longPole {
			labCls += " wfall-lab-pole"
		}
		fmt.Fprintf(&b, `<text class="%s" x="%s" y="%s">%s</text>`,
			labCls, co(indent+18), co(y+wfBarH-1), esc(waterfallTrim(r.Label, 34-min(r.Depth, 6))))

		// Wait gap: dependency finished here, this row started there. Drawn as a
		// hairline with end caps so it reads as "nothing happened", not as work.
		if dep := r.After; dep != "" {
			if de, ok := endByID[dep]; ok && r.Start-de > span*0.004 {
				gx0, gx1 := px(de), px(r.Start)
				gy := y + wfBarH/2
				drewGap = true
				fmt.Fprintf(&b, `<line class="wfall-gap" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(gx0), co(gy), co(gx1), co(gy))
				fmt.Fprintf(&b, `<line class="wfall-gapcap" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(gx0), co(y+1), co(gx0), co(y+wfBarH-1))
				if crit && r.Start-de > span*0.03 {
					fmt.Fprintf(&b, `<text class="wfall-gaplab" x="%s" y="%s" text-anchor="middle">wait %s</text>`,
						co((gx0+gx1)/2), co(y-1), esc(wfDur((r.Start-de)/d.Scale, d.Unit)))
				}
				// Dependency thread from the parent row's bar down to this one.
				if py, ok := yByID[dep]; ok && crit {
					fmt.Fprintf(&b, `<path class="wfall-dep" d="M%s,%s L%s,%s L%s,%s" fill="none"/>`,
						co(gx0), co(py), co(gx0), co(gy), co(gx1), co(gy))
				}
			}
		}

		// The bar itself: stacked phase segments left to right.
		x := px(r.Start)
		segs := r.Segments
		if len(segs) == 0 {
			segs = []waterfallSegment{{Dur: r.Dur}}
		}
		for si, s := range segs {
			w := (s.Dur / span) * plotW
			// Sub-pixel work still has to be visible or a fast stage silently
			// vanishes and the row looks like it started late.
			if w < 0.6 {
				w = 0.6
			}
			col, ok := phaseColor[s.Phase]
			if !ok {
				col = paletteColor("", si)
			}
			switch r.Status {
			case "error":
				col = colorVar("red")
			case "warn":
				col = colorVar("amber")
			}
			// The tooltip carries the UNTRIMMED label and note. Every visible
			// string in this chart is width-budgeted and may be elided, so the
			// hover has to be the place where nothing is lost.
			tip := esc(r.Label) + " · " + esc(wfDur(s.Dur/d.Scale, d.Unit))
			if r.Note != "" {
				tip += " · " + esc(r.Note)
			}
			fmt.Fprintf(&b, `<rect class="%s" x="%s" y="%s" width="%s" height="%s" style="fill:%s"><title>%s</title></rect>`,
				cls, co(x), co(y), co(w), co(wfBarH), col, tip)
			x += w
		}

		// Duration label, right of the bar, clamped into the reserved gutter so a
		// full-width bar cannot push its own label off the canvas.
		lx := x + 5
		if max := wfW - wfRight - noteW + 2; lx > max {
			lx = max
		}
		durCls := "wfall-dur"
		if !crit {
			durCls += " wfall-off"
		}
		dur := wfDur(r.total()/d.Scale, d.Unit)
		fmt.Fprintf(&b, `<text class="%s" x="%s" y="%s">%s</text>`,
			durCls, co(lx), co(y+wfBarH-1), esc(dur))

		// The long pole is marked with a rail in the far-left margin, spanning the
		// full row. An earlier marker sat just left of the plot origin, which put
		// a floating gold tick at t=0 on a row whose bar started much later — it
		// read as a stray data mark rather than as "this is the row to attack".
		// At x=0, aligned to the zebra band, it is unambiguously chrome.
		if r.ID != "" && r.ID == longPole {
			fmt.Fprintf(&b, `<rect class="wfall-pole" x="0" y="%s" width="2.5" height="%s"/>`,
				co(wfTop+float64(i)*wfRowH), co(wfRowH))
		}
		// Notes ride the row's OWN baseline, right-aligned, with the width left
		// over after the duration label. They used to hang one line under the bar
		// — but that y is exactly the next row's baseline (note offset 15 == the
		// row pitch), so every note collided with the row beneath it, and the last
		// row's note collided with the bottom axis. The full text is on the hover.
		if r.Note != "" && crit {
			fmt.Fprintf(&b, `<text class="wfall-note" x="%s" y="%s" text-anchor="end">%s</text>`,
				co(wfW-2), co(y+wfBarH-1), esc(waterfallTrim(r.Note, int(noteW/wfMonoCh)-1)))
		}
	}

	// Bottom axis rule.
	fmt.Fprintf(&b, `<line class="wfall-axis" x1="%s" y1="%s" x2="%s" y2="%s"/>`,
		co(wfGutter), co(axisY), co(wfGutter+plotW), co(axisY))
	b.WriteString(`</svg>`)

	// Legend. Every encoding in the chart must be nameable here, or a reader has
	// to guess what the dimming means.
	b.WriteString(`<ul class="wfall-leg">`)
	for i, p := range phaseOrder {
		fmt.Fprintf(&b, `<li><span class="vsw" style="background:%s"></span>%s</li>`, paletteColor(p.Color, i), esc(p.Label))
	}
	if longPole != "" {
		b.WriteString(`<li><span class="vsw wfall-sw-pole"></span>long pole</li>`)
	}
	if drewGap {
		b.WriteString(`<li><span class="vsw wfall-sw-gap"></span>wait (idle between stages)</li>`)
	}
	if dimmedAny {
		b.WriteString(`<li><span class="vsw wfall-sw-off"></span>off critical path</li>`)
	}
	fmt.Fprintf(&b, `<li class="wfall-total">total %s</li>`, esc(wfDur(span/d.Scale, d.Unit)))
	b.WriteString(`</ul></figure>`)
	return b.String(), nil
}

// waterfallCriticalPath walks `after` edges backwards from the last-finishing
// row and returns the set of rows on that chain, plus whether the walk actually
// followed an edge. A one-row "chain" is the walk finding nothing: the caller
// must not present it as a derived critical path.
//
// Deliberately NOT a general longest-path solve: the input is a real observed
// execution, so the chain that ends last IS the chain that set the total. A
// cycle (bad input) is broken by a visit set rather than rejected — a malformed
// edge should degrade the highlight, never fail the render.
func waterfallCriticalPath(rows []waterfallRow) (map[string]bool, bool) {
	byID := make(map[string]waterfallRow, len(rows))
	for _, r := range rows {
		if r.ID != "" {
			byID[r.ID] = r
		}
	}
	var last waterfallRow
	var found bool
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		if !found || r.end() > last.end() {
			last, found = r, true
		}
	}
	on := map[string]bool{}
	if !found {
		return on, false
	}
	// The loop condition IS the cycle guard: a malformed `after` edge that loops
	// must degrade the highlight, never hang or fail the render.
	for cur := last; !on[cur.ID]; {
		on[cur.ID] = true
		next, ok := byID[cur.After]
		if !ok {
			break
		}
		cur = next
	}
	return on, len(on) > 1
}

// waterfallTicks picks ~6 gridlines on a 1/2/5×10^k ladder so tick labels are
// numbers a human reads without decoding ("2 s", not "1.87 s").
func waterfallTicks(span float64) []float64 {
	raw := span / 6
	if raw <= 0 {
		return []float64{0}
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 5, 10} {
		if mag*m >= raw {
			step = mag * m
			break
		}
	}
	// Stop AT the span, never past it. Overshooting placed a gridline and its
	// label beyond the plot's right edge (px(t) > wfW), where the viewBox clips
	// them — the chart lost its last tick to a half-drawn one outside the canvas.
	var out []float64
	for k := 0; float64(k)*step <= span+1e-9; k++ {
		out = append(out, float64(k)*step)
	}
	return out
}

// waterfallRound keeps tick and duration labels to 3 significant figures. A
// waterfall's job is proportion; trailing digits are noise that costs width.
func waterfallRound(v float64) float64 {
	if v == 0 {
		return 0
	}
	mag := math.Pow(10, 2-math.Floor(math.Log10(math.Abs(v))))
	if mag <= 0 || math.IsInf(mag, 0) {
		return v
	}
	return math.Round(v*mag) / mag
}

func waterfallTrim(s string, n int) string {
	if n < 6 {
		n = 6
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// waterfallAria states the conclusion in words, because a screen reader gets
// nothing from bar geometry. It names the total and the long pole — the two
// facts a sighted reader takes from the chart in the first second.
//
// poleID is the row the chart VISUALLY marks. It is passed in rather than
// recomputed because the two answers diverge on exactly the fan-out this chart
// exists to expose: the longest bar overall is often off the critical path, and
// naming it here would tell a blind reader to attack the row the chart dims.
// Falling back to the longest row is correct only when no path was derived, in
// which case the chart makes no claim about which row set the total.
func waterfallAria(title string, rows []waterfallRow, span float64, unit string, scale float64, poleID string) string {
	var pole waterfallRow
	for _, r := range rows {
		if poleID != "" {
			if r.ID == poleID {
				pole = r
				break
			}
			continue
		}
		if r.total() > pole.total() {
			pole = r
		}
	}
	s := fmt.Sprintf("waterfall of %d stages, total %s", len(rows), wfDur(span/scale, unit))
	if pole.Label != "" {
		s += fmt.Sprintf("; longest stage %s at %s", pole.Label, wfDur(pole.total()/scale, unit))
	}
	if title != "" {
		s = title + " — " + s
	}
	return s
}
