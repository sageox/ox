# Visualization catalog

`ox viz` gives every AI coworker a shared visual vocabulary for plans,
documentation, pull requests, reports, and design notes. Selection and
rendering are deterministic and local; the AI coworker still decides what the
artifact needs to explain.

## Choose a pattern

Start with the intent, not a chart type:

```bash
ox viz suggest "request flow between the CLI, daemon, and Ledger"
ox viz architecture
```

Suggestions use reviewed tags rather than model inference or keywords scraped
from editorial prose. `ox viz` lists the full catalog and `ox viz <id>` returns
one authoring recipe with its category, method, tags, and provenance.

## Author or render

Patterns use one of four authoring methods:

| Method | Contract |
|---|---|
| `inline-svg` | Adapt the accessible, SageOx-themed SVG recipe directly. |
| `ox-render` | Fill the documented JSON shape and run `ox viz render <id> --data <file>`. |
| `mermaid` | Author the catalog's Mermaid form in the target artifact. |
| `html-snippet` | Adapt the HTML/CSS primitive to the surrounding design. |

The eight editorial SVG recipes—architecture, flowchart, data flow, layer
stack, sequence, state machine, timeline, and loop—are static, file-safe, and
theme through CSS custom properties with SageOx fallbacks.

## Check a diagram

```bash
ox viz lint diagram.svg
ox viz lint diagram.svg --strict
```

Accessibility and self-containment failures return a non-zero exit. Editorial
findings such as excess density, too many focal accents, diagonal connectors,
small type, and hard-coded colors are advisory by default; `--strict` makes
them fail automation.

`ox plan viz` remains as a hidden compatibility alias, but new guidance and
automation should use the top-level command.
