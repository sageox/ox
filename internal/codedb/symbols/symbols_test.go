package symbols

import (
	"testing"
)

func TestSymbol_Fields(t *testing.T) {
	t.Parallel()

	sym := Symbol{
		Name:       "MyFunc",
		Kind:       "function",
		Line:       10,
		Col:        1,
		EndLine:    20,
		EndCol:     1,
		ParentIdx:  -1,
		Signature:  "func MyFunc(x int) string",
		ReturnType: "string",
		Params:     "x int",
		startByte:  100,
		endByte:    500,
	}

	if sym.Name != "MyFunc" {
		t.Errorf("Name = %q, want %q", sym.Name, "MyFunc")
	}
	if sym.Kind != "function" {
		t.Errorf("Kind = %q, want %q", sym.Kind, "function")
	}
	if sym.ParentIdx != -1 {
		t.Errorf("ParentIdx = %d, want -1 for top-level symbol", sym.ParentIdx)
	}
	if sym.Line != 10 || sym.EndLine != 20 {
		t.Errorf("Line range = [%d, %d], want [10, 20]", sym.Line, sym.EndLine)
	}
}

func TestSymbol_ZeroValue(t *testing.T) {
	t.Parallel()

	var sym Symbol

	if sym.Name != "" {
		t.Errorf("zero-value Name = %q, want empty", sym.Name)
	}
	if sym.ParentIdx != 0 {
		t.Errorf("zero-value ParentIdx = %d, want 0", sym.ParentIdx)
	}
	// zero ParentIdx is ambiguous with "index 0" -- callers should use -1 for no parent
}

func TestRef_Fields(t *testing.T) {
	t.Parallel()

	ref := Ref{
		RefName:          "fmt.Println",
		Kind:             "call",
		Line:             42,
		Col:              5,
		ContainingSymIdx: 3,
	}

	if ref.RefName != "fmt.Println" {
		t.Errorf("RefName = %q, want %q", ref.RefName, "fmt.Println")
	}
	if ref.Kind != "call" {
		t.Errorf("Kind = %q, want %q", ref.Kind, "call")
	}
	if ref.ContainingSymIdx != 3 {
		t.Errorf("ContainingSymIdx = %d, want 3", ref.ContainingSymIdx)
	}
}

func TestRef_NoContainingSymbol(t *testing.T) {
	t.Parallel()

	ref := Ref{
		RefName:          "globalVar",
		Kind:             "read",
		ContainingSymIdx: -1,
	}

	if ref.ContainingSymIdx != -1 {
		t.Errorf("ContainingSymIdx = %d, want -1 for ref outside any symbol", ref.ContainingSymIdx)
	}
}

func TestExtract_Go_BasicFunction(t *testing.T) {
	t.Parallel()

	syms, _ := Extract("package main\nfunc main() {}", "go")
	if len(syms) == 0 {
		t.Fatal("expected at least one symbol from Go source")
	}
	found := false
	for _, s := range syms {
		if s.Name == "main" && s.Kind == "function" {
			found = true
			if s.Line != 2 {
				t.Errorf("main function Line = %d, want 2", s.Line)
			}
			if s.ParentIdx != -1 {
				t.Errorf("main function ParentIdx = %d, want -1", s.ParentIdx)
			}
		}
	}
	if !found {
		t.Errorf("did not find main function in symbols: %v", syms)
	}
}

func TestExtract_Stub_EmptySource(t *testing.T) {
	t.Parallel()

	syms, refs := Extract("", "go")
	if syms != nil {
		t.Errorf("stub Extract empty source symbols = %v, want nil", syms)
	}
	if refs != nil {
		t.Errorf("stub Extract empty source refs = %v, want nil", refs)
	}
}

func TestExtract_Stub_UnknownLanguage(t *testing.T) {
	t.Parallel()

	syms, refs := Extract("some code", "brainfuck")
	if syms != nil {
		t.Errorf("stub Extract unknown language symbols = %v, want nil", syms)
	}
	if refs != nil {
		t.Errorf("stub Extract unknown language refs = %v, want nil", refs)
	}
}

func TestSupportedLanguages_ReturnsLanguages(t *testing.T) {
	t.Parallel()

	langs := SupportedLanguages()
	if len(langs) == 0 {
		t.Fatal("SupportedLanguages returned empty slice")
	}
	expected := map[string]bool{"go": true, "python": true, "javascript": true, "typescript": true, "tsx": true, "rust": true, "c": true, "cpp": true}
	actual := map[string]bool{}
	for _, l := range langs {
		if !expected[l] {
			t.Errorf("unexpected language %q in SupportedLanguages", l)
		}
		actual[l] = true
	}
	if len(actual) != len(langs) {
		t.Errorf("SupportedLanguages contains duplicates: %d unique vs %d total", len(actual), len(langs))
	}
	for want := range expected {
		if !actual[want] {
			t.Errorf("missing expected language %q", want)
		}
	}
}

// TestSymbol_NestedHierarchy verifies parent-child relationships are representable.
func TestSymbol_NestedHierarchy(t *testing.T) {
	t.Parallel()

	symbols := []Symbol{
		{Name: "MyClass", Kind: "class", ParentIdx: -1, Line: 1, EndLine: 50},
		{Name: "MyMethod", Kind: "method", ParentIdx: 0, Line: 5, EndLine: 15},
		{Name: "innerVar", Kind: "variable", ParentIdx: 1, Line: 8, EndLine: 8},
	}

	// verify class has no parent
	if symbols[0].ParentIdx != -1 {
		t.Errorf("class ParentIdx = %d, want -1", symbols[0].ParentIdx)
	}

	// verify method's parent is the class
	if symbols[1].ParentIdx != 0 {
		t.Errorf("method ParentIdx = %d, want 0 (class)", symbols[1].ParentIdx)
	}

	// verify variable's parent is the method
	if symbols[2].ParentIdx != 1 {
		t.Errorf("variable ParentIdx = %d, want 1 (method)", symbols[2].ParentIdx)
	}
}

// TestRef_MultipleKinds verifies different reference kinds are representable.
func TestRef_MultipleKinds(t *testing.T) {
	t.Parallel()

	kinds := []string{"call", "read", "write", "import", "type_reference"}
	for _, kind := range kinds {
		ref := Ref{RefName: "SomeSymbol", Kind: kind, Line: 1, Col: 1}
		if ref.Kind != kind {
			t.Errorf("Kind = %q, want %q", ref.Kind, kind)
		}
	}
}
