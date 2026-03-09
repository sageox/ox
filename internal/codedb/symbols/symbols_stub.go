package symbols

// Extract is a no-op stub (tree-sitter requires CGO which is not available in ox).
func Extract(source, language string) ([]Symbol, []Ref) {
	return nil, nil
}

// SupportedLanguages returns nil (tree-sitter not available).
func SupportedLanguages() []string {
	return nil
}
