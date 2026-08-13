# Diagram Design upstream

The SageOx editorial SVG subset adapts eight visual types from
[cathrynlavery/diagram-design](https://github.com/cathrynlavery/diagram-design):
architecture, flowchart, data flow, layer stack, sequence, state machine,
timeline, and loop.

- Upstream revision: `f3622cf66a3c557cb2ead57b687a3c1ff63f5a2b`
- Upstream release at review: `2.3.2`
- License: MIT, Copyright (c) 2025 Cathryn Lavery
- Local form: dependency-free, static inline SVG recipes in
  `assets/viz-catalog.md`

The adaptations preserve Diagram Design's editorial principles—low density,
orthogonal connectors, one or two focal accents, readable type, and useful
descriptions—while replacing upstream branding and layout tokens with SageOx
CSS custom properties. No upstream fonts, scripts, images, or runtime code are
vendored.

## Refresh checklist

1. Review upstream changes between the pinned revision and the candidate SHA.
2. Re-evaluate only these eight types; do not import the full catalog by
   default.
3. Preserve the SageOx token fallbacks, unique IDs, `role="img"`, `<title>`,
   `<desc>`, `aria-labelledby`, and static/file-safe contract.
4. Run the catalog drift, lint, light/dark render, and embedding tests.
5. Update the pinned SHA here, catalog `origin` metadata, notices, and release
   notes in the same change.
