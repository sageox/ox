package docs

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// escapeMDX keeps Cobra's Markdown help text from becoming JSX or JavaScript.
// Use the Markdown parser to identify code ranges so examples keep their
// literal bytes, including multiline spans and fences with extra backticks.
func escapeMDX(source []byte) []byte {
	var frontmatter []byte
	if rest, ok := bytes.CutPrefix(source, []byte("---\n")); ok {
		if _, body, found := bytes.Cut(rest, []byte("\n---\n")); found {
			frontmatter, source = source[:len(source)-len(body)], body
		}
	}

	code := make([]bool, len(source))
	markCode := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			code[i] = true
		}
	}
	doc := goldmark.New().Parser().Parse(text.NewReader(source))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		// MDX disables Markdown's indented code blocks.
		case *ast.FencedCodeBlock:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				markCode(lines.At(i))
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				markCode(child.(*ast.Text).Segment)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})

	var out bytes.Buffer
	out.Grow(len(frontmatter) + len(source))
	out.Write(frontmatter)
	escaped := false
	for i, ch := range source {
		if !code[i] && !escaped && (ch == '<' || ch == '{') {
			out.WriteByte('\\')
		}
		out.WriteByte(ch)
		escaped = ch == '\\' && !escaped
	}
	return out.Bytes()
}
