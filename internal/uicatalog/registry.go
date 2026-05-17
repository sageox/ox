// Package uicatalog is a runnable registry of reusable UX components
// in the ox CLI. Every component implementation in internal/ui,
// internal/tui, and internal/cli registers an Entry here so that
// `ox dev catalog` (hidden cobra command) can render them all
// side-by-side and so the bundle exported to sageox-design.netlify.app
// stays in sync with the code that ships.
//
// See docs/design/README.md and .claude/rules/design.md.
package uicatalog

import (
	"sort"
	"sync"
)

// Family groups components by purpose. The catalog page renders families
// as sections; the JSON manifest serializes the field verbatim.
type Family string

const (
	FamilyLayout      Family = "layout"
	FamilyDataDisplay Family = "data-display"
	FamilyViz         Family = "viz"
	FamilyInput       Family = "input"
	FamilyFeedback    Family = "feedback"
	// FamilyScreen groups full composed screens — concrete snapshots of
	// real commands (`ox config`, `ox init`, `ox dashboard`) so designers
	// can see the assembled surface, not just the primitives that build it.
	FamilyScreen Family = "screen"
)

// Renderer picks the right tool for the visual fidelity a component needs.
// Components without motion get freeze (static, sharp, inline SVG, no JS).
// Components that ARE the motion (spinners, select navigation, streaming
// timelines) get asciinema (.cast) embedded with the player.
type Renderer string

const (
	// RendererFreeze captures the static Render() output as one inline SVG
	// frame using charmbracelet/freeze. Default — covers most components.
	RendererFreeze Renderer = "freeze"

	// RendererAsciinema records an animated demo via `ox dev catalog
	// --component=<name> --demo` into a .cast file, plus a freeze SVG
	// poster frame as fallback. Pick this when the visual interest is
	// in the motion, not the final frame.
	RendererAsciinema Renderer = "asciinema"
)

// Entry describes one reusable component. Render is the source of
// truth for what the catalog shows; everything else is metadata for
// docs and the portal export.
type Entry struct {
	Name      string   // kebab-case, e.g. "timeline"
	Family    Family   // grouping for catalog rendering
	Package   string   // e.g. "internal/ui"
	Exports   []string // public Go symbols, e.g. ["RenderTimeline", "TimelineNode"]
	Since     string   // semver where this component first shipped
	WhenToUse string   // one-paragraph guidance
	WhenNotTo string   // one-paragraph anti-pattern
	Renderer  Renderer // freeze (default) or asciinema
	Render    func() string

	// Asset paths relative to the catalog export root. Populated by
	// make catalog-export; empty in unit tests and in `ox dev catalog`
	// terminal rendering.
	SVGPath  string // inline-able freeze SVG (always present)
	CastPath string // asciinema v2 recording (only for RendererAsciinema)
}

var (
	mu      sync.RWMutex
	entries = map[string]Entry{}
)

// Register adds a component to the catalog. Call from init() in
// internal/uicatalog/entries/*.go. Panics on duplicate names — duplicates
// almost always mean a refactor missed a rename, and silent overwrite
// would hide the bug.
func Register(e Entry) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := entries[e.Name]; dup {
		panic("uicatalog: duplicate entry name: " + e.Name)
	}
	entries[e.Name] = e
}

// All returns every registered entry, sorted by Family then Name for
// stable rendering across runs.
func All() []Entry {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Get returns one entry by name; ok is false if not registered.
func Get(name string) (Entry, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := entries[name]
	return e, ok
}

// Names returns just the registered entry names, sorted alphabetically.
// Used by the drift-check test that pairs entries with docs/design/components/*.md.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(entries))
	for n := range entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
