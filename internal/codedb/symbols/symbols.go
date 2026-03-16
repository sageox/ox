// Package symbols extracts symbol definitions and references from source code
// using tree-sitter for accurate AST-based parsing.
//
// When built with CGO enabled, full tree-sitter extraction is available.
// When CGO is disabled, Extract returns nil and SupportedLanguages returns nil.
package symbols

// Symbol represents a symbol definition extracted from source code.
type Symbol struct {
	Name       string
	Kind       string
	Line       int
	Col        int
	EndLine    int
	EndCol     int
	ParentIdx  int // -1 if no parent
	Signature  string
	ReturnType string
	Params     string
	startByte  uint32
	endByte    uint32
}

// Ref represents a reference (call site) extracted from source code.
type Ref struct {
	RefName          string
	Kind             string
	Line             int
	Col              int
	ContainingSymIdx int // -1 if not inside a symbol
}
