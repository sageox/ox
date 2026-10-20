package session

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// A filename grep missed Store/history encoders because their file paths were
// held in variables. Constructor ownership does not depend on a filename or
// variable name: every session JSON encoder belongs inside RawWriter.
func TestSessionRedactionArchitecture(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate session sources")
	}
	dir := filepath.Dir(here)
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "raw_writer.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		violations, err := sessionJSONEncoderViolations(name, body)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			t.Error(violation)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("architecture guard inspected no production sources")
	}
}

// This bounded source check follows raw-file names through local aliases in
// the two recording packages. Publication still validates exact bytes: static
// syntax checks are defense in depth, not a general Go data-flow proof.
func TestSessionRedactionArchitectureRawFileSinks(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate source tree")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	checked := 0
	for _, relative := range []string{"internal/session", "cmd/ox"} {
		directory := filepath.Join(root, relative)
		files, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") || relative == "internal/session" && file.Name() == "raw_writer.go" {
				continue
			}
			name := filepath.Join(directory, file.Name())
			body, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			violations, err := sessionRawFileSinkViolations(name, body)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range violations {
				t.Error(violation)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("raw-file ownership guard inspected no production sources")
	}
}

func sessionRawFileSinkViolations(name string, body []byte) ([]string, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, name, body, 0)
	if err != nil {
		return nil, err
	}
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		alias := filepath.Base(path)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		imports[alias] = path
	}
	// Object identity is the cheapest way to tell one function's local from an
	// unrelated same-named local; go/types would need whole-package loading.
	paths := map[*ast.Object]bool{}       //nolint:staticcheck // see above
	functions := map[*ast.Object]string{} //nolint:staticcheck // see above
	function := func(expression ast.Expr) string {
		switch x := expression.(type) {
		case *ast.SelectorExpr:
			if identifier, ok := x.X.(*ast.Ident); ok {
				if path, ok := imports[identifier.Name]; ok {
					return path + "." + x.Sel.Name
				}
			}
		case *ast.Ident:
			if target := functions[x.Obj]; target != "" {
				return target
			}
			for _, path := range []string{"os", "github.com/sageox/ox/internal/fileutil"} {
				if imports["."] == path {
					return path + "." + x.Name
				}
			}
		}
		return ""
	}
	var rawPath func(ast.Expr) bool
	rawPath = func(expression ast.Expr) bool {
		switch x := expression.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				value, err := strconv.Unquote(x.Value)
				return err == nil && filepath.Base(value) == "raw.jsonl"
			}
		case *ast.Ident:
			// These package-level constants are declared in sibling source files.
			return paths[x.Obj] || x.Obj == nil && (x.Name == "rawFilename" || x.Name == "ledgerFileRaw")
		case *ast.ParenExpr:
			return rawPath(x.X)
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				if suffix, ok := x.Y.(*ast.BasicLit); ok && suffix.Kind == token.STRING {
					text, err := strconv.Unquote(suffix.Value)
					if err == nil && text != "" {
						return filepath.Base(text) == "raw.jsonl"
					}
				}
				return rawPath(x.X) || rawPath(x.Y)
			}
		case *ast.CallExpr:
			switch function(x.Fun) {
			case "path/filepath.Join", "path.Join", "path/filepath.Clean", "path/filepath.FromSlash":
				for _, argument := range x.Args {
					if rawPath(argument) {
						return true
					}
				}
			}
		}
		return false
	}
	// A fixed point follows assignment chains even when declarations are not
	// textually ordered. Object identity keeps one function's path from tainting
	// an unrelated local variable with the same name in another function.
	for changed := true; changed; {
		changed = false
		bind := func(identifier *ast.Ident, expression ast.Expr) {
			if identifier.Obj == nil {
				return
			}
			if rawPath(expression) && !paths[identifier.Obj] {
				paths[identifier.Obj] = true
				changed = true
			}
			if target := function(expression); target != "" && functions[identifier.Obj] == "" {
				functions[identifier.Obj] = target
				changed = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.ValueSpec:
				for i, expression := range x.Values {
					if i < len(x.Names) {
						bind(x.Names[i], expression)
					}
				}
			case *ast.AssignStmt:
				for i, expression := range x.Rhs {
					if i < len(x.Lhs) {
						if identifier, ok := x.Lhs[i].(*ast.Ident); ok {
							bind(identifier, expression)
						}
					}
				}
			}
			return true
		})
	}
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 || !rawPath(call.Args[0]) {
			return true
		}
		switch function(call.Fun) {
		case "os.WriteFile", "os.OpenFile", "os.Create", "github.com/sageox/ox/internal/fileutil.AtomicWriteBytes", "github.com/sageox/ox/internal/fileutil.AtomicWriteJSON":
			violations = append(violations, fmt.Sprintf("%s: raw session file sink bypasses RawWriter", positions.Position(call.Pos())))
		}
		return true
	})
	return violations, nil
}

func TestSessionRedactionArchitectureRejectsMarshalFileSinkBypass(t *testing.T) {
	unsafe := map[string]string{
		"marshal_write":    `package fixture; import "encoding/json"; import "os"; import "path/filepath"; func write(dir string, entry any){destination:=filepath.Join(dir,"raw.jsonl"); body,_:=json.Marshal(entry); os.WriteFile(destination,body,0600)}`,
		"renamed_imports":  `package fixture; import disk "os"; import paths "path/filepath"; func write(dir string){destination:=paths.Join(dir,"raw.jsonl"); alias:=destination; disk.Create(alias)}`,
		"function_alias":   `package fixture; import disk "os"; func write(){const destination="raw.jsonl"; sink:=disk.WriteFile; sink(destination,nil,0600)}`,
		"package_constant": `package fixture; import "os"; import "path/filepath"; func write(dir string){destination:=filepath.Join(dir,rawFilename); os.WriteFile(destination,nil,0600)}`,
		"atomic_write":     `package fixture; import safe "github.com/sageox/ox/internal/fileutil"; func write(){destination:="raw.jsonl";safe.AtomicWriteBytes(destination,nil,0600)}`,
	}
	for name, source := range unsafe {
		t.Run(name, func(t *testing.T) {
			violations, err := sessionRawFileSinkViolations(name+".go", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) == 0 {
				t.Fatal("raw-file write bypass was accepted")
			}
		})
	}
	benign := map[string]string{
		"other_file":       `package fixture; import "encoding/json"; import "os"; import "path/filepath"; func write(dir string, entry any){destination:=filepath.Join(dir,"notes.json"); body,_:=json.Marshal(entry); os.WriteFile(destination,body,0600)}`,
		"read_only":        `package fixture; import "os"; func read(){destination:="raw.jsonl";os.ReadFile(destination)}`,
		"journal":          `package fixture; import "os"; func write(){destination:="raw.jsonl";os.WriteFile(destination+".lock",nil,0600)}`,
		"directory":        `package fixture; import "os"; import "path/filepath"; func write(){destination:="raw.jsonl";os.CreateTemp(filepath.Dir(destination),"safe-*")}`,
		"scoped_variables": `package fixture; import "os"; func read(){destination:="raw.jsonl";os.ReadFile(destination)}; func write(){destination:="notes.json";os.WriteFile(destination,nil,0600)}`,
	}
	for name, source := range benign {
		t.Run(name, func(t *testing.T) {
			violations, err := sessionRawFileSinkViolations(name+".go", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) != 0 {
				t.Fatal("non-session sink or read incorrectly rejected")
			}
		})
	}
}

func sessionJSONEncoderViolations(name string, body []byte) ([]string, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, name, body, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	dot := false
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		if path != "encoding/json" {
			continue
		}
		alias := "json"
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias == "." {
			dot = true
		} else if alias != "_" {
			aliases[alias] = true
		}
	}
	var violations []string
	add := func(pos token.Pos) {
		violations = append(violations, fmt.Sprintf("%s: session JSON encoder bypasses RawWriter", positions.Position(pos)))
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			identifier, ok := n.X.(*ast.Ident)
			if ok && aliases[identifier.Name] && n.Sel.Name == "NewEncoder" {
				add(n.Pos())
			}
		case *ast.Ident:
			if dot && n.Name == "NewEncoder" {
				add(n.Pos())
			}
		}
		return true
	})
	return violations, nil
}

// These intentionally unsafe source snippets never execute. The AST guard must
// catch imports and constructor aliases even when no raw.jsonl literal exists.
func TestSessionRedactionArchitectureRejectsIndirectEncoders(t *testing.T) {
	for name, body := range map[string]string{
		"variable_path":     `package fixture; import "encoding/json"; import "os"; func write(destination string){f,_:=os.Create(destination); json.NewEncoder(f).Encode("entry")}`,
		"renamed_import":    `package fixture; import wire "encoding/json"; func write(dst any){wire.NewEncoder(dst)}`,
		"constructor_alias": `package fixture; import wire "encoding/json"; var construct = wire.NewEncoder`,
		"dot_import":        `package fixture; import . "encoding/json"; var construct = NewEncoder`,
	} {
		t.Run(name, func(t *testing.T) {
			violations, err := sessionJSONEncoderViolations(name+".go", []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) == 0 {
				t.Error("unsafe session encoder was not rejected")
			}
		})
	}
	for name, body := range map[string]string{
		"read_only":         `package fixture; import "encoding/json"; func read(src any){json.NewDecoder(src)}`,
		"different_package": `package fixture; import "example.invalid/codec"; var constructor = codec.NewEncoder`,
	} {
		t.Run(name, func(t *testing.T) {
			violations, err := sessionJSONEncoderViolations(name+".go", []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) != 0 {
				t.Error("unrelated read/codec was incorrectly rejected")
			}
		})
	}
}
