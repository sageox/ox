package plan

import "github.com/sageox/ox/internal/viz"

// VizPattern is retained as an alias for plan consumers that predate the
// top-level, artifact-neutral ox viz surface.
type VizPattern = viz.Pattern

func VizCatalog() []VizPattern { return viz.Catalog() }

func VizPatternByID(id string) (VizPattern, bool) { return viz.PatternByID(id) }

func RenderViz(pattern string, data []byte) (string, error) { return viz.Render(pattern, data) }

// These mirrors keep the historical package-local drift tests useful while the
// implementation lives in internal/viz.
var vizRenderers = viz.RendererIDs()
var vizColors = viz.ColorCSSVars()
