package docs

import (
	"bytes"
	"reflect"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mdxParser parses Markdown the way MDX does: MDX disables HTML and
// autolinks, so those constructs must not hide code spans.
var mdxParser = parser.NewParser(
	parser.WithBlockParsers(without(parser.DefaultBlockParsers(), parser.NewHTMLBlockParser())...),
	parser.WithInlineParsers(without(parser.DefaultInlineParsers(),
		parser.NewRawHTMLParser(), parser.NewAutoLinkParser())...),
	parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
)

func without(values []util.PrioritizedValue, drop ...any) []util.PrioritizedValue {
	kept := values[:0]
	for _, v := range values {
		dropped := false
		for _, d := range drop {
			dropped = dropped || reflect.TypeOf(v.Value) == reflect.TypeOf(d)
		}
		if !dropped {
			kept = append(kept, v)
		}
	}
	return kept
}

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
	origin := make([]int, len(source))
	for i := range origin {
		origin[i] = i
	}
	markCode(source, origin, code)

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

// markCode flags the bytes of src that MDX reads as code; origin maps each
// byte of src to its index in code.
func markCode(src []byte, origin []int, code []bool) {
	mark := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			code[origin[i]] = true
		}
	}
	doc := mdxParser.Parse(text.NewReader(src))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.FencedCodeBlock:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				mark(lines.At(i))
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			// MDX disables indented code, so these lines are ordinary
			// Markdown: parse them again without the indentation.
			var sub []byte
			var subOrigin []int
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				line := lines.At(i)
				for j := line.Start; j < line.Stop; j++ {
					sub = append(sub, src[j])
					subOrigin = append(subOrigin, origin[j])
				}
			}
			markCode(sub, subOrigin, code)
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				mark(child.(*ast.Text).Segment)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
}
