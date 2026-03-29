package language

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeOrCompareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	data := []byte(got + "\n")

	if os.Getenv("REGENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n  → run: REGENERATE=1 go test ./internal/codedb/language/", path, err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("golden divergence: detect/%s\ngot:  %q\nwant: %q", name, got, string(bytes.TrimSpace(want)))
	}
}

func TestGoldenDetect(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		// No extension
		{"no_extension_makefile", "Makefile"},
		{"no_extension_bare", "README"},
		{"empty_path", ""},
		// Go
		{"ext_go", "main.go"},
		{"ext_go_nested", "internal/auth/handler.go"},
		// Rust
		{"ext_rs", "lib.rs"},
		// Python
		{"ext_py", "script.py"},
		// JavaScript
		{"ext_js", "index.js"},
		// TypeScript
		{"ext_ts", "app.ts"},
		{"ext_tsx", "component.tsx"},
		{"ext_jsx", "widget.jsx"},
		// Java
		{"ext_java", "Main.java"},
		// C/C++
		{"ext_c", "parser.c"},
		{"ext_h", "parser.h"},
		{"ext_cpp", "engine.cpp"},
		{"ext_cc", "engine.cc"},
		{"ext_cxx", "engine.cxx"},
		{"ext_hpp", "engine.hpp"},
		{"ext_hxx", "engine.hxx"},
		{"ext_hh", "engine.hh"},
		// Ruby
		{"ext_rb", "model.rb"},
		// PHP
		{"ext_php", "index.php"},
		// Swift
		{"ext_swift", "ContentView.swift"},
		// Kotlin
		{"ext_kt", "Activity.kt"},
		{"ext_kts", "build.kts"},
		// Scala
		{"ext_scala", "App.scala"},
		// C#
		{"ext_cs", "Program.cs"},
		// Shell
		{"ext_sh", "deploy.sh"},
		{"ext_bash", "setup.bash"},
		// SQL
		{"ext_sql", "schema.sql"},
		// Web
		{"ext_html", "index.html"},
		{"ext_htm", "page.htm"},
		{"ext_css", "styles.css"},
		// Data
		{"ext_json", "config.json"},
		{"ext_yaml", "config.yaml"},
		{"ext_yml", "docker-compose.yml"},
		{"ext_toml", "Cargo.toml"},
		{"ext_xml", "pom.xml"},
		// Docs
		{"ext_md", "README.md"},
		{"ext_markdown", "guide.markdown"},
		// Other languages
		{"ext_r", "analysis.r"},
		{"ext_lua", "config.lua"},
		{"ext_zig", "main.zig"},
		{"ext_ex", "router.ex"},
		{"ext_exs", "config.exs"},
		{"ext_erl", "server.erl"},
		{"ext_hrl", "records.hrl"},
		{"ext_hs", "Main.hs"},
		{"ext_ml", "parser.ml"},
		{"ext_mli", "parser.mli"},
		{"ext_pl", "script.pl"},
		{"ext_pm", "Module.pm"},
		{"ext_proto", "api.proto"},
		{"ext_dart", "main.dart"},
		// Uppercase extensions (normalized to lower)
		{"ext_uppercase_go", "MAIN.GO"},
		{"ext_uppercase_py", "SCRIPT.PY"},
		{"ext_uppercase_rs", "LIB.RS"},
		// Unknown extensions
		{"ext_unknown_wasm", "module.wasm"},
		{"ext_unknown_bin", "data.bin"},
		{"ext_unknown_txt", "notes.txt"},
		// Multiple dots
		{"multi_dot_min_js", "app.min.js"},
		{"multi_dot_test_go", "server_test.go"},
		{"multi_dot_d_ts", "types.d.ts"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.path)
			writeOrCompareGolden(t, tc.name, got)
		})
	}
}
