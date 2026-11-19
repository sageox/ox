package docs

import (
	"bytes"
	"reflect"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MDX disables HTML, autolinks, and indented code blocks. Its fences
// accept indentation that CommonMark treats as indented code.
var mdxParser = parser.NewParser(
	parser.WithBlockParsers(mdxBlockParsers()...),
	parser.WithInlineParsers(without(parser.DefaultInlineParsers(),
		parser.NewRawHTMLParser(), parser.NewAutoLinkParser())...),
	parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
)

func mdxBlockParsers() []util.PrioritizedValue {
	fenced := parser.NewFencedCodeBlockParser()
	blocks := without(parser.DefaultBlockParsers(), parser.NewHTMLBlockParser(), parser.NewCodeBlockParser())
	for i, block := range blocks {
		switch reflect.TypeOf(block.Value) {
		case reflect.TypeOf(fenced):
			blocks[i].Value = mdxFenceParser{indentedBlockParser{fenced}}
		case reflect.TypeOf(parser.NewThematicBreakParser()), reflect.TypeOf(parser.NewSetextHeadingParser()):
			blocks[i].Value = mdxDedentBlockParser{block.Value.(parser.BlockParser)}
		case reflect.TypeOf(parser.NewParagraphParser()), reflect.TypeOf(parser.NewATXHeadingParser()):
			blocks[i].Value = indentedBlockParser{block.Value.(parser.BlockParser)}
		}
	}
	return blocks
}

// mdxFenceParser retains Goldmark's list, quote, and fence rules with
// MDX's relaxed indentation for the opening and closing markers.
type mdxFenceParser struct{ indentedBlockParser }

type mdxDedentBlockParser struct{ parser.BlockParser }

func (mdxDedentBlockParser) CanAcceptIndentedLine() bool { return true }

func (p mdxDedentBlockParser) Open(parent ast.Node, reader text.Reader, ctx parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	_, start := util.IndentWidth(line, reader.LineOffset())
	if start == 0 {
		return p.BlockParser.Open(parent, reader, ctx)
	}
	// These parsers also check indentation internally. Present the marker
	// first and restore the line if it is ordinary prose.
	lineNo, segment := reader.Position()
	reader.Advance(start)
	node, state := p.BlockParser.Open(parent, reader, ctx)
	if node == nil {
		reader.SetPosition(lineNo, segment)
	} else {
		ctx.SetBlockOffset(start)
	}
	return node, state
}

type indentedBlockParser struct{ parser.BlockParser }

func (indentedBlockParser) CanAcceptIndentedLine() bool { return true }

func (p indentedBlockParser) Open(parent ast.Node, reader text.Reader, ctx parser.Context) (ast.Node, parser.State) {
	if ctx.BlockOffset() < 0 {
		// Goldmark assumes an indent at least as wide as the byte length
		// of the line means it is blank. Leading tabs can break that assumption.
		line, _ := reader.PeekLine()
		if !util.IsBlank(line) {
			width, offset := util.IndentWidth(line, reader.LineOffset())
			ctx.SetBlockIndent(width)
			ctx.SetBlockOffset(offset)
		}
	}
	return p.BlockParser.Open(parent, reader, ctx)
}

type mdxFence struct {
	marker byte
	length int
}

var mdxFenceKey = parser.NewContextKey()

func (p mdxFenceParser) Open(parent ast.Node, reader text.Reader, ctx parser.Context) (ast.Node, parser.State) {
	node, state := p.indentedBlockParser.Open(parent, reader, ctx)
	if node != nil {
		line, _ := reader.PeekLine()
		start := ctx.BlockOffset()
		ctx.Set(mdxFenceKey, mdxFence{line[start], fenceLength(line[start:], line[start])})
	}
	return node, state
}

func (p mdxFenceParser) Continue(node ast.Node, reader text.Reader, ctx parser.Context) parser.State {
	line, _ := reader.PeekLine()
	width, start := util.IndentWidth(line, reader.LineOffset())
	if width >= 4 {
		fence := ctx.Get(mdxFenceKey).(mdxFence)
		n := fenceLength(line[start:], fence.marker)
		if n >= fence.length && util.IsBlank(line[start+n:]) {
			reader.AdvanceToEOL()
			return parser.Close
		}
	}
	return p.BlockParser.Continue(node, reader, ctx)
}

func fenceLength(line []byte, marker byte) int {
	for i, ch := range line {
		if ch != marker {
			return i
		}
	}
	return len(line)
}

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
	markCode(source, code)

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

// markCode flags the bytes of src that MDX reads as code.
func markCode(src []byte, code []bool) {
	mark := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			code[i] = true
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
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				mark(child.(*ast.Text).Segment)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
}
