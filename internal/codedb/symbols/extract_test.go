package symbols

import (
	"fmt"
	"strings"
	"testing"
)

func TestExtract_Go_FunctionWithCallRefs(t *testing.T) {
	t.Parallel()

	src := `package main

import "fmt"

func greet(name string) string {
	return fmt.Sprintf("hello %s", name)
}

func main() {
	msg := greet("world")
	fmt.Println(msg)
}
`
	syms, refs := Extract(src, "go")

	// should find greet and main functions
	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	for _, want := range []string{"greet", "main"} {
		if !symNames[want] {
			t.Errorf("missing symbol %q in %v", want, symNames)
		}
	}

	// should find call references
	refNames := map[string]bool{}
	for _, r := range refs {
		refNames[r.RefName] = true
	}
	if !refNames["greet"] {
		t.Errorf("missing call ref to greet in %v", refNames)
	}

	// greet call should be contained in main function
	for _, r := range refs {
		if r.RefName == "greet" {
			if r.ContainingSymIdx < 0 {
				t.Error("greet call should have a containing symbol")
			} else if syms[r.ContainingSymIdx].Name != "main" {
				t.Errorf("greet call contained in %q, want main", syms[r.ContainingSymIdx].Name)
			}
		}
	}
}

func TestExtract_Go_TypeInfo(t *testing.T) {
	t.Parallel()

	src := `package main

func add(a int, b int) int {
	return a + b
}
`
	syms, _ := Extract(src, "go")
	var addSym *Symbol
	for i := range syms {
		if syms[i].Name == "add" {
			addSym = &syms[i]
			break
		}
	}
	if addSym == nil {
		t.Fatal("did not find add function")
	}
	if addSym.Params == "" {
		t.Error("expected non-empty Params for add function")
	}
	if addSym.Signature == "" {
		t.Error("expected non-empty Signature for add function")
	}
}

func TestExtract_Python(t *testing.T) {
	t.Parallel()

	src := `class Calculator:
    def add(self, a, b):
        return a + b

    def multiply(self, a, b):
        return a * b

def main():
    calc = Calculator()
    calc.add(1, 2)
`
	syms, refs := Extract(src, "python")
	if len(syms) == 0 {
		t.Fatal("expected symbols from Python source")
	}

	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	for _, want := range []string{"Calculator", "add", "multiply", "main"} {
		if !symNames[want] {
			t.Errorf("missing Python symbol %q", want)
		}
	}

	// add and multiply should be nested under Calculator
	for _, s := range syms {
		if s.Name == "add" || s.Name == "multiply" {
			if s.ParentIdx < 0 {
				t.Errorf("%s should have a parent (Calculator)", s.Name)
			} else if syms[s.ParentIdx].Name != "Calculator" {
				t.Errorf("%s parent = %q, want Calculator", s.Name, syms[s.ParentIdx].Name)
			}
		}
	}

	if len(refs) == 0 {
		t.Error("expected at least one call ref from Python source")
	}
}

func TestExtract_TypeScript(t *testing.T) {
	t.Parallel()

	src := `interface Logger {
    log(message: string): void;
}

function createLogger(name: string): Logger {
    return { log: (msg) => console.log(msg) };
}

class App {
    private logger: Logger;
    constructor(name: string) {
        this.logger = createLogger(name);
    }
}
`
	syms, refs := Extract(src, "typescript")
	if len(syms) == 0 {
		t.Fatal("expected symbols from TypeScript source")
	}

	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	for _, want := range []string{"Logger", "createLogger", "App"} {
		if !symNames[want] {
			t.Errorf("missing TypeScript symbol %q", want)
		}
	}

	// should have ref to createLogger
	refNames := map[string]bool{}
	for _, r := range refs {
		refNames[r.RefName] = true
	}
	if !refNames["createLogger"] {
		t.Errorf("missing call ref to createLogger")
	}
}

func TestExtract_Rust(t *testing.T) {
	t.Parallel()

	src := `struct Config {
    name: String,
}

fn create_config(name: &str) -> Config {
    Config { name: name.to_string() }
}

fn main() {
    let cfg = create_config("test");
}
`
	syms, refs := Extract(src, "rust")
	if len(syms) == 0 {
		t.Fatal("expected symbols from Rust source")
	}

	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	for _, want := range []string{"Config", "create_config", "main"} {
		if !symNames[want] {
			t.Errorf("missing Rust symbol %q", want)
		}
	}

	refNames := map[string]bool{}
	for _, r := range refs {
		refNames[r.RefName] = true
	}
	if !refNames["create_config"] {
		t.Errorf("missing Rust call ref to create_config")
	}
}

func TestExtract_C(t *testing.T) {
	t.Parallel()

	src := `#include <stdio.h>

struct Point {
    int x;
    int y;
};

int add(int a, int b) {
    return a + b;
}

int main() {
    printf("%d\n", add(1, 2));
    return 0;
}
`
	syms, refs := Extract(src, "c")
	if len(syms) == 0 {
		t.Fatal("expected symbols from C source")
	}

	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	if !symNames["add"] {
		t.Errorf("missing C symbol add")
	}
	if !symNames["main"] {
		t.Errorf("missing C symbol main")
	}

	refNames := map[string]bool{}
	for _, r := range refs {
		refNames[r.RefName] = true
	}
	if !refNames["add"] {
		t.Errorf("missing C call ref to add")
	}
}

func TestExtract_JavaScript(t *testing.T) {
	t.Parallel()

	src := `function fetchData(url) {
    return fetch(url).then(r => r.json());
}

class DataService {
    async getData() {
        return fetchData("/api/data");
    }
}
`
	syms, _ := Extract(src, "javascript")
	if len(syms) == 0 {
		t.Fatal("expected symbols from JavaScript source")
	}

	symNames := map[string]bool{}
	for _, s := range syms {
		symNames[s.Name] = true
	}
	if !symNames["fetchData"] {
		t.Errorf("missing JS symbol fetchData")
	}
	if !symNames["DataService"] {
		t.Errorf("missing JS symbol DataService")
	}
}

func TestExtract_AllLanguages(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"go":         "package main\nfunc main() {}",
		"python":     "def main():\n    pass",
		"javascript": "function main() {}",
		"typescript":  "function main(): void {}",
		"tsx":         "function App(): JSX.Element { return <div/>; }",
		"rust":        "fn main() {}",
		"c":           "int main() { return 0; }",
		"cpp":         "int main() { return 0; }",
	}

	for lang, src := range cases {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			syms, _ := Extract(src, lang)
			if len(syms) == 0 {
				t.Errorf("Extract(%q, %q) returned no symbols", src, lang)
			}
		})
	}
}

func TestAssignParents_Nesting(t *testing.T) {
	t.Parallel()

	syms := []Symbol{
		{Name: "outer", startByte: 0, endByte: 100, ParentIdx: -1},
		{Name: "inner", startByte: 10, endByte: 50, ParentIdx: -1},
		{Name: "sibling", startByte: 60, endByte: 90, ParentIdx: -1},
	}

	assignParents(syms)

	// inner should be child of outer
	if syms[1].ParentIdx != 0 {
		t.Errorf("inner ParentIdx = %d, want 0 (outer)", syms[1].ParentIdx)
	}
	// sibling should be child of outer
	if syms[2].ParentIdx != 0 {
		t.Errorf("sibling ParentIdx = %d, want 0 (outer)", syms[2].ParentIdx)
	}
	// outer should have no parent
	if syms[0].ParentIdx != -1 {
		t.Errorf("outer ParentIdx = %d, want -1", syms[0].ParentIdx)
	}
}

func TestAssignContainingSymbols(t *testing.T) {
	t.Parallel()

	syms := []Symbol{
		{Name: "outer", startByte: 0, endByte: 100},
		{Name: "inner", startByte: 20, endByte: 60},
	}
	refs := []Ref{
		{RefName: "a", ContainingSymIdx: -1},
		{RefName: "b", ContainingSymIdx: -1},
		{RefName: "c", ContainingSymIdx: -1},
	}
	startBytes := []uint32{30, 70, 200}

	assignContainingSymbols(syms, refs, startBytes)

	// ref at byte 30 is inside both outer and inner; inner is narrower
	if refs[0].ContainingSymIdx != 1 {
		t.Errorf("ref a ContainingSymIdx = %d, want 1 (inner)", refs[0].ContainingSymIdx)
	}
	// ref at byte 70 is inside outer only
	if refs[1].ContainingSymIdx != 0 {
		t.Errorf("ref b ContainingSymIdx = %d, want 0 (outer)", refs[1].ContainingSymIdx)
	}
	// ref at byte 200 is outside all symbols
	if refs[2].ContainingSymIdx != -1 {
		t.Errorf("ref c ContainingSymIdx = %d, want -1", refs[2].ContainingSymIdx)
	}
}

func TestExtract_Go_ReturnType(t *testing.T) {
	t.Parallel()
	src := `package main

func add(a, b int) int {
	return a + b
}
`
	syms, _ := Extract(src, "go")
	for _, s := range syms {
		if s.Name == "add" {
			if !strings.Contains(s.ReturnType, "int") {
				t.Errorf("add ReturnType = %q, want contains 'int'", s.ReturnType)
			}
			return
		}
	}
	t.Fatal("did not find add function")
}

func TestExtract_Rust_ReturnType(t *testing.T) {
	t.Parallel()
	src := `struct Config { name: String }

fn create() -> Config {
    Config { name: String::new() }
}
`
	syms, _ := Extract(src, "rust")
	for _, s := range syms {
		if s.Name == "create" {
			if !strings.Contains(s.ReturnType, "Config") {
				t.Errorf("create ReturnType = %q, want contains 'Config'", s.ReturnType)
			}
			return
		}
	}
	t.Fatal("did not find create function")
}

func TestExtract_TypeScript_ReturnType(t *testing.T) {
	t.Parallel()
	src := `function greet(name: string): string {
    return name;
}
`
	syms, _ := Extract(src, "typescript")
	for _, s := range syms {
		if s.Name == "greet" {
			if !strings.Contains(s.ReturnType, "string") {
				t.Errorf("greet ReturnType = %q, want contains 'string'", s.ReturnType)
			}
			return
		}
	}
	t.Fatal("did not find greet function")
}

func TestExtract_C_ReturnType(t *testing.T) {
	t.Parallel()
	src := `int add(int a, int b) {
    return a + b;
}
`
	syms, _ := Extract(src, "c")
	for _, s := range syms {
		if s.Name == "add" {
			if !strings.Contains(s.ReturnType, "int") {
				t.Errorf("add ReturnType = %q, want contains 'int'", s.ReturnType)
			}
			return
		}
	}
	t.Fatal("did not find add function")
}

func TestExtract_LargeFile(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("package main\n\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "func f%d() {}\n", i)
	}
	syms, _ := Extract(b.String(), "go")
	if len(syms) < 500 {
		t.Errorf("expected >= 500 symbols from large file, got %d", len(syms))
	}
}

func TestExtract_MalformedSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		lang string
	}{
		{"unclosed_brace", "package main\nfunc main() {", "go"},
		{"truncated_function", "package main\nfunc ma", "go"},
		{"random_garbage", "asd;flkj @#$ }{[]", "go"},
		{"empty_class", "class {", "javascript"},
		{"partial_struct", "struct Foo {", "rust"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// must not panic — result can be nil or non-nil
			syms, refs := Extract(tc.src, tc.lang)
			_ = syms
			_ = refs
		})
	}
}
