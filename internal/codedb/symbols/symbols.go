// Package symbols extracts symbol definitions and references from source code
// using tree-sitter via gotreesitter (pure Go, no CGO required).
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
