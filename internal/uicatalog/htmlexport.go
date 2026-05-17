package uicatalog

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/theme"
)

// HTMLExportOpts controls how the catalog snapshot is built.
type HTMLExportOpts struct {
	// AssetsDir is where freeze SVGs and asciinema .cast files live on disk.
	// They get inlined into the HTML at export time, so the output is one
	// self-contained file per the sageox-design/catalog/ contract.
	AssetsDir string

	// PlayerJSPath / PlayerCSSPath point at vendored asciinema-player
	// runtime files. Both get inlined. If empty, animated components
	// degrade to their freeze SVG poster frame (still readable).
	PlayerJSPath  string
	PlayerCSSPath string

	// Version is rendered in the page header (e.g. ox "0.9.0").
	Version string
}

// ExportHTML writes a single self-contained index.html for the catalog
// to w. Matches the contract documented at sageox-design/catalog/README.md:
// inline CSS, inline JS limited to chrome toggles + asciinema-player
// runtime, fonts from CDN, no external asset dependencies.
func ExportHTML(w io.Writer, opts HTMLExportOpts) error {
	es := All()

	playerCSS := readOrEmpty(opts.PlayerCSSPath)
	playerJS := readOrEmpty(opts.PlayerJSPath)

	type clientEntry struct {
		Name     string `json:"name"`
		Family   Family `json:"family"`
		Renderer string `json:"renderer"`
		CastData string `json:"cast,omitempty"` // raw .cast contents (JSON-Lines)
	}

	clientEntries := make([]clientEntry, 0, len(es))
	bodyCards := &strings.Builder{}
	tocByFamily := map[Family][]Entry{}

	for _, e := range es {
		tocByFamily[e.Family] = append(tocByFamily[e.Family], e)

		// Two themed SVGs per component so the page Mode toggle is
		// visually consistent across the frame AND the snapshot.
		svgDark := readAsset(opts.AssetsDir, e.Name+".dark.svg")
		svgLight := readAsset(opts.AssetsDir, e.Name+".light.svg")
		if svgDark == "" {
			// fall back to legacy single-theme name
			svgDark = readAsset(opts.AssetsDir, e.Name+".svg")
		}

		castText := ""
		if e.Renderer == RendererAsciinema {
			castRel := e.CastPath
			if castRel == "" {
				castRel = e.Name + ".cast"
			}
			castText = readAsset(opts.AssetsDir, castRel)
		}

		clientEntries = append(clientEntries, clientEntry{
			Name:     e.Name,
			Family:   e.Family,
			Renderer: string(e.Renderer),
			CastData: castText,
		})

		writeCard(bodyCards, e, svgDark, svgLight, castText != "")
	}

	clientJSON, err := json.Marshal(clientEntries)
	if err != nil {
		return fmt.Errorf("marshal client entries: %w", err)
	}

	page := buildPage(pageInput{
		Version:    opts.Version,
		Cards:      bodyCards.String(),
		ThemeBlock: renderThemeBlock(),
		TOC:        renderTOC(tocByFamily),
		PlayerCSS:  playerCSS,
		PlayerJS:   playerJS,
		ClientJSON: string(clientJSON),
	})

	_, err = io.WriteString(w, page)
	return err
}

func writeCard(w *strings.Builder, e Entry, svgDark, svgLight string, animated bool) {
	fmt.Fprintf(w, `<article class="component" id="c-%s" data-family="%s">`+"\n",
		html.EscapeString(e.Name), html.EscapeString(string(e.Family)))
	fmt.Fprintf(w, `<header><h2>%s</h2><span class="family">%s</span></header>`+"\n",
		html.EscapeString(e.Name), html.EscapeString(string(e.Family)))

	if animated {
		fmt.Fprintf(w, `<div class="player" data-cast-for="%s"></div>`+"\n",
			html.EscapeString(e.Name))
		w.WriteString(`<noscript>` + "\n")
	}
	switch {
	case svgDark != "" && svgLight != "":
		w.WriteString(`<figure class="snapshot snapshot-dark">` + svgDark + `</figure>` + "\n")
		w.WriteString(`<figure class="snapshot snapshot-light">` + svgLight + `</figure>` + "\n")
	case svgDark != "":
		w.WriteString(`<figure class="snapshot">` + svgDark + `</figure>` + "\n")
	default:
		w.WriteString(`<figure class="snapshot snapshot-missing">no snapshot yet — run <code>make catalog-build</code></figure>` + "\n")
	}
	if animated {
		w.WriteString(`</noscript>` + "\n")
	}

	fmt.Fprintf(w, `<section class="meta"><dl>`+"\n")
	fmt.Fprintf(w, `<dt>Package</dt><dd><code>%s</code></dd>`+"\n", html.EscapeString(e.Package))
	fmt.Fprintf(w, `<dt>Exports</dt><dd>%s</dd>`+"\n", renderExports(e.Exports))
	fmt.Fprintf(w, `<dt>Since</dt><dd>%s</dd>`+"\n", html.EscapeString(e.Since))
	fmt.Fprintf(w, `<dt>Use when</dt><dd class="notes">%s</dd>`+"\n", html.EscapeString(e.WhenToUse))
	fmt.Fprintf(w, `<dt>Avoid when</dt><dd class="notes">%s</dd>`+"\n", html.EscapeString(e.WhenNotTo))
	w.WriteString(`</dl></section>` + "\n")
	w.WriteString(`</article>` + "\n\n")
}

func renderExports(exports []string) string {
	codes := make([]string, len(exports))
	for i, x := range exports {
		codes[i] = `<code>` + html.EscapeString(x) + `</code>`
	}
	return strings.Join(codes, " · ")
}

func renderTOC(byFamily map[Family][]Entry) string {
	families := []Family{
		FamilyScreen, FamilyLayout, FamilyDataDisplay, FamilyViz, FamilyInput, FamilyFeedback,
	}
	w := &strings.Builder{}
	for _, fam := range families {
		entries, ok := byFamily[fam]
		if !ok || len(entries) == 0 {
			continue
		}
		fmt.Fprintf(w, `<section><h3>%s</h3><ul>`+"\n", html.EscapeString(string(fam)))
		for _, e := range entries {
			fmt.Fprintf(w, `<li><a href="#c-%s">%s</a></li>`+"\n",
				html.EscapeString(e.Name), html.EscapeString(e.Name))
		}
		w.WriteString(`</ul></section>` + "\n")
	}
	return w.String()
}

func readAsset(dir, rel string) string {
	if rel == "" || dir == "" {
		return ""
	}
	path := filepath.Join(dir, rel)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readOrEmpty(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

type pageInput struct {
	Version    string
	Cards      string
	ThemeBlock string
	TOC        string
	PlayerCSS  string
	PlayerJS   string
	ClientJSON string
}

// renderThemeBlock emits the palette/tokens reference at the top of the
// catalog page so designers and AI coworkers can see every semantic
// color without reading Go source. Data comes from theme.Tokens, which
// mirrors internal/theme/generated.go (the npm-synced output of
// sageox-design).
func renderThemeBlock() string {
	byCategory := map[theme.TokenCategory][]theme.TokenInfo{}
	order := []theme.TokenCategory{
		theme.CategoryBrand, theme.CategorySemantic,
		theme.CategoryVisibility, theme.CategoryWordmark,
	}
	for _, t := range theme.Tokens {
		byCategory[t.Category] = append(byCategory[t.Category], t)
	}

	w := &strings.Builder{}
	w.WriteString(`<section id="theme" class="theme">` + "\n")
	w.WriteString(`<header class="theme-header">` +
		`<h2>Theme</h2>` +
		`<p>ox ships <strong>one palette in two variants</strong>. Every command — boxes, timelines, spinners, the doctor wordmark — uses the same semantic tokens; lipgloss picks the right hex at runtime based on your terminal's background.</p>` +
		`<div class="theme-howto">` +
		`<h3>How detection works</h3>` +
		`<ol>` +
		`<li><strong>OSC 11 query.</strong> At first render, lipgloss queries the terminal for its actual background color (escape sequence <code>\x1b]11;?\x07</code>). Modern terminals (iTerm2, Alacritty, Kitty, Wezterm, Windows Terminal, recent VS Code) answer back; lipgloss computes luminance and picks dark or light.</li>` +
		`<li><strong><code>COLORFGBG</code> fallback.</strong> Some emulators (rxvt-derived) export this env var instead. lipgloss reads it.</li>` +
		`<li><strong>Default dark.</strong> If nothing answers (CI, piped output, exotic terminal), ox assumes a dark background — the most common case among coworkers.</li>` +
		`<li><strong><code>NO_COLOR=1</code></strong> short-circuits everything: no ANSI emitted, terminal default colors only.</li>` +
		`</ol>` +
		`<p class="theme-howto-mech">Mechanism: <code>compat.AdaptiveColor{Light, Dark}</code> in <a href="https://github.com/sageox/ox/blob/main/internal/theme/generated.go"><code>internal/theme/generated.go</code></a> (npm-synced from <a href="https://github.com/sageox/sageox-design/blob/main/tokens/colors.yaml"><code>sageox-design/tokens/colors.yaml</code></a>). The pair below shows <em>both</em> variants; the one outlined is what your current Mode toggle would render.</p>` +
		`</div>` +
		`</header>` + "\n")

	for _, cat := range order {
		toks, ok := byCategory[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(w, `<div class="token-group"><h3>%s</h3><div class="swatches">`+"\n",
			html.EscapeString(string(cat)))
		for _, t := range toks {
			fmt.Fprintf(w, `<figure class="swatch">`+
				`<div class="swatch-pair">`+
				`<span class="chip chip-light" style="background:%s" title="light variant"></span>`+
				`<span class="chip chip-dark" style="background:%s" title="dark variant"></span>`+
				`</div>`+
				`<figcaption>`+
				`<code class="tname">%s</code>`+
				`<span class="thex thex-light"><span class="lbl">light</span>%s</span>`+
				`<span class="thex thex-dark"><span class="lbl">dark</span>%s</span>`+
				`<span class="tdesc">%s</span>`+
				`</figcaption>`+
				`</figure>`+"\n",
				html.EscapeString(t.LightHex), html.EscapeString(t.DarkHex),
				html.EscapeString(t.Name),
				html.EscapeString(t.LightHex), html.EscapeString(t.DarkHex),
				html.EscapeString(t.UseCase))
		}
		w.WriteString(`</div></div>` + "\n")
	}
	w.WriteString(`</section>` + "\n")
	return w.String()
}

// Tokens used here mirror sageox-design/tokens/colors.yaml. If you change
// a value, update sageox-design first and `npm run sync` afterwards.
const inlineCSS = `
:root {
  --bg: #f5f3ed;
  --bg-elev: #ffffff;
  --fg: #1a1d1a;
  --fg-dim: #6b7580;
  --border: #d9d3c5;
  --primary: #4F6A48;
  --secondary: #B87D3A;
  --accent: #3D5437;
  --success: #4F6A48;
  --warning: #B87D3A;
  --error: #A03030;
  --info: #5580A0;
  --mono: ui-monospace, "JetBrains Mono", SFMono-Regular, Menlo, Consolas, monospace;
  --sans: Inter, -apple-system, "Segoe UI", system-ui, sans-serif;
}
html[data-mode="dark"] {
  --bg: #111518;
  --bg-elev: #1a1f23;
  --fg: #e8e6df;
  --fg-dim: #8F99A3;
  --border: #2a2f33;
  --primary: #7A8F78;
  --secondary: #E0A56A;
  --accent: #8FA888;
  --success: #7A8F78;
  --warning: #E0A56A;
  --error: #E07070;
  --info: #7FA7C8;
}
html[data-calm="on"] .component { border-color: transparent; box-shadow: none; }
html[data-calm="on"] header.site { border-bottom-color: transparent; }
html[data-notes="off"] .notes { display: none; }
html[data-notes="off"] .meta dt:nth-of-type(4),
html[data-notes="off"] .meta dt:nth-of-type(5) { display: none; }

* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font-family: var(--sans); line-height: 1.5; }
header.site { position: sticky; top: 0; z-index: 10; display: flex; align-items: center; gap: 1.5rem; padding: 0.75rem 1.5rem; background: var(--bg); border-bottom: 1px solid var(--border); }
header.site h1 { margin: 0; font-size: 0.95rem; font-weight: 600; color: var(--primary); letter-spacing: 0.02em; }
header.site .version { font-family: var(--mono); font-size: 0.8rem; color: var(--fg-dim); }
header.site .toggles { margin-left: auto; display: flex; gap: 0.5rem; }
header.site button { background: transparent; border: 1px solid var(--border); color: var(--fg); padding: 0.3rem 0.6rem; border-radius: 4px; font-size: 0.8rem; font-family: var(--sans); cursor: pointer; }
header.site button:hover { border-color: var(--primary); }

main { display: grid; grid-template-columns: 220px 1fr; min-height: calc(100vh - 56px); }
nav.toc { position: sticky; top: 56px; align-self: start; height: calc(100vh - 56px); overflow-y: auto; padding: 1.5rem 1rem; border-right: 1px solid var(--border); font-size: 0.85rem; }
nav.toc section { margin-bottom: 1.25rem; }
nav.toc h3 { margin: 0 0 0.4rem; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--fg-dim); font-weight: 600; }
nav.toc ul { margin: 0; padding: 0; list-style: none; }
nav.toc li { padding: 0.15rem 0; }
nav.toc a { color: var(--fg); text-decoration: none; font-family: var(--mono); font-size: 0.85rem; }
nav.toc a:hover { color: var(--primary); }

section.content { padding: 2rem; max-width: 1100px; }
.component { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 1.5rem; margin-bottom: 1.75rem; transition: border-color 0.15s; }
.component header { display: flex; align-items: baseline; justify-content: space-between; margin: 0 0 1rem; }
.component header h2 { margin: 0; font-family: var(--mono); font-size: 1.1rem; color: var(--accent); }
.component .family { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--fg-dim); }
.snapshot { margin: 0 0 1rem; padding: 0; background: var(--bg); border-radius: 6px; overflow: hidden; }
.snapshot svg { display: block; max-width: 100%; height: auto; }
.snapshot-missing { padding: 1rem; color: var(--fg-dim); font-family: var(--mono); font-size: 0.85rem; }
/* Mode-aware snapshot swap: freeze bakes the theme into the SVG, so we
   ship both and toggle visibility on the html[data-mode] attribute. */
.snapshot-light { display: none; }
html[data-mode="light"] .snapshot-dark { display: none; }
html[data-mode="light"] .snapshot-light { display: block; }
.player { margin: 0 0 1rem; background: var(--bg); border-radius: 6px; overflow: hidden; min-height: 200px; }
.meta dl { display: grid; grid-template-columns: 110px 1fr; gap: 0.4rem 1rem; margin: 0; font-size: 0.9rem; }
.meta dt { color: var(--fg-dim); font-size: 0.8rem; }
.meta dd { margin: 0; }
.meta code { font-family: var(--mono); font-size: 0.85rem; background: var(--bg); padding: 0.05rem 0.35rem; border-radius: 3px; }
.notes { color: var(--fg); }

@media (max-width: 768px) {
  main { grid-template-columns: 1fr; }
  nav.toc { display: none; }
}

/* Theme block (palette reference) */
.theme { margin-bottom: 2.5rem; padding: 1.5rem; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; }
.theme-header h2 { margin: 0 0 0.25rem; font-family: var(--mono); font-size: 1.1rem; color: var(--accent); }
.theme-header > p { margin: 0 0 1.25rem; color: var(--fg); font-size: 0.95rem; max-width: 70ch; }
.theme-header strong { color: var(--accent); font-weight: 600; }
.theme-header code { font-family: var(--mono); font-size: 0.85rem; background: var(--bg); padding: 0.05rem 0.3rem; border-radius: 3px; }
.theme-howto { margin: 0 0 1.5rem; padding: 1rem 1.25rem; background: var(--bg); border-left: 3px solid var(--accent); border-radius: 0 6px 6px 0; }
.theme-howto h3 { margin: 0 0 0.5rem; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--accent); font-weight: 600; }
.theme-howto ol { margin: 0 0 0.75rem; padding-left: 1.25rem; color: var(--fg); font-size: 0.88rem; line-height: 1.6; }
.theme-howto li { margin-bottom: 0.3rem; }
.theme-howto-mech { margin: 0; color: var(--fg-dim); font-size: 0.82rem; line-height: 1.5; max-width: 70ch; }
.theme-howto-mech em { font-style: italic; color: var(--fg); }

.token-group { margin-bottom: 1.5rem; }
.token-group h3 { margin: 0 0 0.75rem; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--fg-dim); font-weight: 600; }
.swatches { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 0.75rem; }
.swatch { margin: 0; display: flex; flex-direction: column; gap: 0.45rem; padding: 0.65rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; }
.swatch-pair { display: flex; height: 36px; border-radius: 4px; overflow: hidden; border: 1px solid var(--border); }
.swatch-pair .chip { flex: 1; position: relative; transition: flex 0.2s ease; }
/* Highlight the variant ox would pick right now, based on the page Mode toggle.
   The "active" chip grows; the other dims behind a subtle veil. */
html[data-mode="dark"] .swatch-pair .chip-light { flex: 0.4; opacity: 0.55; }
html[data-mode="dark"] .swatch-pair .chip-dark { box-shadow: inset 0 0 0 2px var(--accent); }
html[data-mode="light"] .swatch-pair .chip-dark { flex: 0.4; opacity: 0.55; }
html[data-mode="light"] .swatch-pair .chip-light { box-shadow: inset 0 0 0 2px var(--accent); }
.swatch figcaption { display: grid; grid-template-columns: auto 1fr; gap: 0.2rem 0.5rem; font-size: 0.78rem; align-items: baseline; }
.swatch .tname { grid-column: 1 / -1; font-family: var(--mono); color: var(--fg); font-size: 0.88rem; font-weight: 500; }
.swatch .thex { font-family: var(--mono); color: var(--fg-dim); font-size: 0.74rem; transition: color 0.15s; }
.swatch .thex .lbl { display: inline-block; min-width: 2.5em; color: var(--fg-dim); opacity: 0.7; font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.06em; margin-right: 0.35rem; }
html[data-mode="dark"] .swatch .thex-dark { color: var(--fg); font-weight: 500; }
html[data-mode="light"] .swatch .thex-light { color: var(--fg); font-weight: 500; }
.swatch .tdesc { grid-column: 1 / -1; color: var(--fg-dim); font-size: 0.78rem; line-height: 1.4; margin-top: 0.15rem; }
`

const inlineJS = `
const html = document.documentElement;
function cycle(attr, values) {
  const cur = html.getAttribute(attr) || values[0];
  const idx = values.indexOf(cur);
  html.setAttribute(attr, values[(idx + 1) % values.length]);
  localStorage.setItem('catalog-' + attr, html.getAttribute(attr));
}
['mode','calm','notes'].forEach(a => {
  const saved = localStorage.getItem('catalog-' + a);
  if (saved) html.setAttribute('data-' + a, saved);
});
document.getElementById('toggle-mode').onclick = () => cycle('data-mode', ['dark','light']);
document.getElementById('toggle-calm').onclick = () => cycle('data-calm', ['off','on']);
document.getElementById('toggle-notes').onclick = () => cycle('data-notes', ['on','off']);

// Asciinema players — cast data is inlined as JSON, fed to the player
// through a Blob → object URL so we don't depend on the page being
// served over HTTP (works from file:// too). Each player picks its
// theme from the page mode and re-creates itself on Mode toggle.
const entries = JSON.parse(document.getElementById('catalog-data').textContent);
const playerInstances = new Map(); // name -> {player, url, opts}

function playerThemeForMode() {
  // asciinema-player accepts named themes; "asciinema" is its dark
  // default, "solarized-light" is the closest bundled light variant.
  return html.getAttribute('data-mode') === 'light' ? 'solarized-light' : 'asciinema';
}

function mountPlayers() {
  if (!window.AsciinemaPlayer) return;
  entries.forEach(e => {
    if (e.renderer !== 'asciinema' || !e.cast) return;
    const el = document.querySelector('.player[data-cast-for="' + e.name + '"]');
    if (!el) return;
    // tear down a previous instance so theme swaps don't stack
    const prev = playerInstances.get(e.name);
    if (prev) {
      try { prev.player.dispose(); } catch (_) {}
      URL.revokeObjectURL(prev.url);
      el.innerHTML = '';
    }
    const blob = new Blob([e.cast], { type: 'application/x-asciicast+json' });
    const url = URL.createObjectURL(blob);
    const player = AsciinemaPlayer.create(url, el, {
      theme: playerThemeForMode(),
      idleTimeLimit: 2,
      fit: 'width',
      autoPlay: false,
      controls: true,
      poster: 'npt:0:1',
    });
    playerInstances.set(e.name, { player, url });
  });
}

mountPlayers();
// Re-mount on mode change so the player picks up the new theme.
new MutationObserver(muts => {
  for (const m of muts) {
    if (m.attributeName === 'data-mode') { mountPlayers(); break; }
  }
}).observe(html, { attributes: true });
`

func buildPage(in pageInput) string {
	playerCSSBlock := ""
	if in.PlayerCSS != "" {
		playerCSSBlock = "<style>" + in.PlayerCSS + "</style>"
	}
	playerJSBlock := ""
	if in.PlayerJS != "" {
		playerJSBlock = "<script>" + in.PlayerJS + "</script>"
	}

	return `<!doctype html>
<html lang="en" data-mode="dark" data-calm="off" data-notes="on">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SageOx component catalog · cli</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>` + inlineCSS + `</style>
` + playerCSSBlock + `
</head>
<body>
<header class="site">
  <h1>SageOx · CLI catalog</h1>
  <span class="version">ox ` + html.EscapeString(in.Version) + `</span>
  <div class="toggles">
    <button id="toggle-mode">Mode</button>
    <button id="toggle-calm">Calm</button>
    <button id="toggle-notes">Notes</button>
  </div>
</header>
<main>
  <nav class="toc">
    <section><h3>reference</h3><ul><li><a href="#theme">Theme</a></li></ul></section>
` + in.TOC + `</nav>
  <section class="content">` + in.ThemeBlock + in.Cards + `</section>
</main>
<script id="catalog-data" type="application/json">` + in.ClientJSON + `</script>
` + playerJSBlock + `
<script>` + inlineJS + `</script>
</body>
</html>
`
}
