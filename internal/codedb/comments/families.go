// Package comments extracts comments from source code using a character-level
// scanner configured by language-specific syntax families.
package comments

// Family describes the comment syntax for a group of languages.
type Family struct {
	LinePrefix string // e.g. "//" or "#"; empty if no line comments
	BlockOpen  string // e.g. "/*"; empty if no block comments
	BlockClose string // e.g. "*/"; empty if no block comments
}

var (
	cFamily       = Family{LinePrefix: "//", BlockOpen: "/*", BlockClose: "*/"}
	hashFamily    = Family{LinePrefix: "#"}
	htmlFamily    = Family{BlockOpen: "<!--", BlockClose: "-->"}
	sqlFamily     = Family{LinePrefix: "--", BlockOpen: "/*", BlockClose: "*/"}
	luaFamily     = Family{LinePrefix: "--", BlockOpen: "--[[", BlockClose: "]]"}
	haskellFamily = Family{LinePrefix: "--", BlockOpen: "{-", BlockClose: "-}"}
	ocamlFamily   = Family{BlockOpen: "(*", BlockClose: "*)"}
	erlangFamily  = Family{LinePrefix: "%"}
)

// familyMap maps canonical language names (from language.Detect) to their
// comment syntax family. Languages without comments (e.g. json) are absent.
var familyMap = map[string]*Family{
	// C-family
	"go":         &cFamily,
	"c":          &cFamily,
	"cpp":        &cFamily,
	"java":       &cFamily,
	"javascript": &cFamily,
	"typescript":  &cFamily,
	"tsx":         &cFamily,
	"jsx":         &cFamily,
	"rust":        &cFamily,
	"swift":       &cFamily,
	"kotlin":      &cFamily,
	"scala":       &cFamily,
	"csharp":      &cFamily,
	"dart":        &cFamily,
	"php":         &cFamily,
	"zig":         &cFamily,
	"protobuf":    &cFamily,
	"css":         &cFamily,

	// Hash-family
	"python": &hashFamily,
	"ruby":   &hashFamily,
	"shell":  &hashFamily,
	"r":      &hashFamily,
	"perl":   &hashFamily,
	"elixir": &hashFamily,
	"yaml":   &hashFamily,
	"toml":   &hashFamily,

	// HTML-family
	"html":     &htmlFamily,
	"xml":      &htmlFamily,
	"markdown": &htmlFamily,

	// Others
	"sql":     &sqlFamily,
	"lua":     &luaFamily,
	"haskell": &haskellFamily,
	"ocaml":   &ocamlFamily,
	"erlang":  &erlangFamily,
}

// FamilyForLanguage returns the comment syntax family for a language, or nil
// if the language has no comment syntax (e.g. json).
func FamilyForLanguage(lang string) *Family {
	return familyMap[lang]
}

// SupportedLanguages returns all language names that have comment extraction support.
func SupportedLanguages() []string {
	langs := make([]string, 0, len(familyMap))
	for lang := range familyMap {
		langs = append(langs, lang)
	}
	return langs
}
