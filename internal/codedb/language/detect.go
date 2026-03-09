package language

import (
	"path/filepath"
	"strings"
)

// Detect returns the programming language name for a file path based on its extension.
// Returns empty string if the language is not recognized.
func Detect(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return ""
	}
	ext = strings.ToLower(ext[1:]) // strip leading dot

	switch ext {
	case "rs":
		return "rust"
	case "py":
		return "python"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "java":
		return "java"
	case "c", "h":
		return "c"
	case "cpp", "cc", "cxx":
		return "cpp"
	case "hpp", "hxx", "hh":
		return "cpp"
	case "go":
		return "go"
	case "rb":
		return "ruby"
	case "php":
		return "php"
	case "swift":
		return "swift"
	case "kt", "kts":
		return "kotlin"
	case "scala":
		return "scala"
	case "cs":
		return "csharp"
	case "sh", "bash":
		return "shell"
	case "sql":
		return "sql"
	case "html", "htm":
		return "html"
	case "css":
		return "css"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "xml":
		return "xml"
	case "md", "markdown":
		return "markdown"
	case "r":
		return "r"
	case "lua":
		return "lua"
	case "zig":
		return "zig"
	case "ex", "exs":
		return "elixir"
	case "erl", "hrl":
		return "erlang"
	case "hs":
		return "haskell"
	case "ml", "mli":
		return "ocaml"
	case "pl", "pm":
		return "perl"
	case "proto":
		return "protobuf"
	case "dart":
		return "dart"
	default:
		return ""
	}
}
