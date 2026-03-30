package symbols

import (
	"strings"
	"unicode"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// function-like symbol kinds eligible for type info enrichment
var functionKinds = map[string]bool{
	"function":    true,
	"method":      true,
	"constructor": true,
}


// extractTypeInfo enriches function-like symbols with signature, return type, and params.
func extractTypeInfo(lang *gotreesitter.Language, language string, source []byte, syms []Symbol) {
	if lang == nil || len(source) == 0 || len(syms) == 0 {
		return
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		return
	}

	for i := range syms {
		if !functionKinds[syms[i].Kind] {
			continue
		}

		node := root.DescendantForByteRange(syms[i].startByte, syms[i].endByte)
		if node == nil {
			continue
		}

		syms[i].Params = extractParams(node, lang, source)
		syms[i].Signature = extractSignature(node, lang, source)
		syms[i].ReturnType = extractReturnType(node, lang, language, source)
	}
}

// extractParams finds the parameter list child and returns its text without outer parens.
func extractParams(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	paramNode := findChild(node, lang, "parameter_list", "parameters", "formal_parameters")
	if paramNode == nil {
		return ""
	}
	text := string(source[paramNode.StartByte():paramNode.EndByte()])
	text = strings.TrimPrefix(text, "(")
	text = strings.TrimSuffix(text, ")")
	return collapseWhitespace(text)
}

// extractSignature extracts text from the node start up to (but not including) the body.
func extractSignature(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	bodyNode := findChild(node, lang,
		"block", "statement_block", "compound_statement",
		"field_declaration_list", "declaration_list", "class_body",
	)
	if bodyNode == nil {
		// no body found; use the entire node text as signature
		return collapseWhitespace(string(source[node.StartByte():node.EndByte()]))
	}
	return textBetween(source, node.StartByte(), bodyNode.StartByte())
}

// extractReturnType extracts the return type using language-specific heuristics.
func extractReturnType(node *gotreesitter.Node, lang *gotreesitter.Language, language string, source []byte) string {
	switch language {
	case "go":
		return extractReturnTypeGo(node, lang, source)
	case "python", "rust":
		return extractReturnTypeArrow(node, lang, source)
	case "typescript", "tsx":
		return extractReturnTypeAnnotation(node, lang, source)
	case "c", "cpp":
		return extractReturnTypeCCpp(node, lang, source)
	default:
		// javascript and others: no return type syntax
		return ""
	}
}

// extractReturnTypeGo extracts text between parameter_list end and block start.
func extractReturnTypeGo(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	paramNode := findChild(node, lang, "parameter_list", "parameters")
	if paramNode == nil {
		return ""
	}
	bodyNode := findChild(node, lang, "block")
	if bodyNode == nil {
		return ""
	}
	return textBetween(source, paramNode.EndByte(), bodyNode.StartByte())
}

// extractReturnTypeArrow finds a "->" token and captures the next named node's text.
func extractReturnTypeArrow(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		childType := child.Type(lang)
		childText := string(source[child.StartByte():child.EndByte()])
		// some grammars expose "->" as a node type, others only as raw text
		if childType == "->" || childText == "->" {
			// capture the next named node
			for j := i + 1; j < node.ChildCount(); j++ {
				next := node.Child(j)
				if next == nil {
					continue
				}
				if next.IsNamed() {
					return collapseWhitespace(string(source[next.StartByte():next.EndByte()]))
				}
			}
		}
	}
	return ""
}

// extractReturnTypeAnnotation finds a type_annotation child and strips the leading colon.
func extractReturnTypeAnnotation(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	typeNode := findChild(node, lang, "type_annotation")
	if typeNode == nil {
		return ""
	}
	text := string(source[typeNode.StartByte():typeNode.EndByte()])
	text = strings.TrimLeft(text, ":")
	return collapseWhitespace(text)
}

// extractReturnTypeCCpp extracts text before function_declarator (the return type precedes the declarator).
func extractReturnTypeCCpp(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	declNode := findChild(node, lang, "function_declarator")
	if declNode == nil {
		return ""
	}
	return textBetween(source, node.StartByte(), declNode.StartByte())
}

// findChild returns the first direct child of node whose type matches any of the given types.
func findChild(node *gotreesitter.Node, lang *gotreesitter.Language, types ...string) *gotreesitter.Node {
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if typeSet[child.Type(lang)] {
			return child
		}
	}
	return nil
}

// textBetween extracts text between two byte positions, collapsing whitespace.
func textBetween(source []byte, start, end uint32) string {
	if start >= end || int(end) > len(source) {
		return ""
	}
	return collapseWhitespace(string(source[start:end]))
}

// collapseWhitespace replaces runs of whitespace with a single space and trims.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}
