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

// Tokens is the authoritative ordered list of every semantic color the CLI
// exposes. Values mirror sageox-design tokens/colors.yaml's CLI hex and
// hex-light mappings. Secondary is a stronger sage emphasis step, not a second
// brand hue; gold is reserved exclusively for Warning.
var Tokens = []TokenInfo{
	{"Primary", CategoryBrand, "#3D643B", "#7AAA77", "App name, headers, spinners — the calm sage anchor"},
	{"Secondary", CategoryBrand, "#324F31", "#ADCFA3", "Commands and selected elements — strongest sage emphasis"},
	{"Accent", CategoryBrand, "#4E7D4C", "#99C693", "File paths, callouts, \"look here\""},

	{"Success", CategorySemantic, "#3D643B", "#7AAA77", "Passed checks, confirmations"},
	{"Warning", CategorySemantic, "#A5842F", "#D9B654", "Cautions, dirty builds, soft warnings"},
	{"Error", CategorySemantic, "#9F4838", "#D77E6C", "Failures, blocked items, hard stops"},
	{"Info", CategorySemantic, "#5580A0", "#7FA7C8", "Flags, links, informational notes"},
	{"Dim", CategorySemantic, "#6B7580", "#8F99A3", "Descriptions, secondary text"},

	{"Public", CategoryVisibility, "#0f766e", "#2dd4bf", "Visible-to-team — teal"},
	{"Private", CategoryVisibility, "#b45309", "#fbbf24", "Personal / not shared — amber"},

	{"WordmarkSage", CategoryWordmark, "#7a8f78", "#c4d1c0", "\"Sage\" half of the ASCII wordmark"},
	{"WordmarkOx", CategoryWordmark, "#546a54", "#7a8f78", "\"Ox\" half of the ASCII wordmark"},
}
