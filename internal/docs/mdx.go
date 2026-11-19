package docs

import (
	"bytes"
	"reflect"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mdxParser parses Markdown the way MDX does: MDX disables indented code,
// HTML and autolinks, so those constructs must not hide code spans.
var mdxParser = parser.NewParser(
	parser.WithBlockParsers(without(parser.DefaultBlockParsers(),
		parser.NewCodeBlockParser(), parser.NewHTMLBlockParser())...),
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

	parsed, origin := dedent(source)
	code := make([]bool, len(source))
	markCode := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			code[origin[i]] = true
		}
	}
	doc := mdxParser.Parse(text.NewReader(parsed))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
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

// dedent drops leading spaces and tabs from every line, since MDX reads an
// indented line as ordinary text. origin maps each kept byte to its source index.
func dedent(source []byte) (parsed []byte, origin []int) {
	parsed = make([]byte, 0, len(source))
	origin = make([]int, 0, len(source))
	lineStart := true
	for i, ch := range source {
		if lineStart && (ch == ' ' || ch == '\t') {
			continue
		}
		lineStart = ch == '\n'
		parsed = append(parsed, ch)
		origin = append(origin, i)
	}
	return parsed, origin
}
