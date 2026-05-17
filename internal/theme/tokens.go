package theme

// TokenCategory groups semantic tokens for catalog rendering.
type TokenCategory string

const (
	CategoryBrand      TokenCategory = "brand"
	CategorySemantic   TokenCategory = "semantic"
	CategoryVisibility TokenCategory = "visibility"
	CategoryWordmark   TokenCategory = "wordmark"
)

// TokenInfo is hand-curated metadata about each adaptive color in
// generated.go: name, intended use, and the hex values for both
// light and dark mode (the same values lipgloss picks at runtime).
//
// Keep this in sync with internal/theme/generated.go. A drift test
// could be added that parses generated.go and asserts these hex
// values match; for now it's a manual contract.
type TokenInfo struct {
	Name     string        // semantic name, matches the Go identifier
	Category TokenCategory // grouping for catalog display
	LightHex string        // hex used in light terminals
	DarkHex  string        // hex used in dark terminals
	UseCase  string        // one-line description
}

// Tokens is the authoritative ordered list of every semantic color
// the CLI exposes. The component catalog uses this to render swatches;
// docs/design/tokens.md references it as the canonical reference.
var Tokens = []TokenInfo{
	{"Primary", CategoryBrand, "#4F6A48", "#7A8F78", "App name, headers, spinners — the calm sage anchor"},
	{"Secondary", CategoryBrand, "#B87D3A", "#E0A56A", "Commands, interactive elements — copper highlight"},
	{"Accent", CategoryBrand, "#3D5437", "#8FA888", "File paths, callouts, \"look here\""},

	{"Success", CategorySemantic, "#4F6A48", "#7A8F78", "Passed checks, confirmations"},
	{"Warning", CategorySemantic, "#B87D3A", "#E0A56A", "Cautions, dirty builds, soft warnings"},
	{"Error", CategorySemantic, "#A03030", "#E07070", "Failures, blocked items, hard stops"},
	{"Info", CategorySemantic, "#5580A0", "#7FA7C8", "Flags, links, informational notes"},
	{"Dim", CategorySemantic, "#6B7580", "#8F99A3", "Descriptions, secondary text"},

	{"Public", CategoryVisibility, "#0f766e", "#2dd4bf", "Visible-to-team — teal"},
	{"Private", CategoryVisibility, "#b45309", "#fbbf24", "Personal / not shared — amber"},

	{"WordmarkSage", CategoryWordmark, "#7a8f78", "#c4d1c0", "\"Sage\" half of the ASCII wordmark"},
	{"WordmarkOx", CategoryWordmark, "#546a54", "#7a8f78", "\"Ox\" half of the ASCII wordmark"},
}
