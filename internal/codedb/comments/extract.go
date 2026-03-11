package comments

import "strings"

// Comment represents a single comment extracted from source code.
type Comment struct {
	Text    string // comment text without delimiters, trimmed
	Kind    string // "line", "block", or "doc"
	Line    int    // 1-based start line
	EndLine int    // 1-based end line
	Col     int    // 1-based start column
	EndCol  int    // 1-based end column
}

// scanner state
type state int

const (
	sNormal state = iota
	sLineComment
	sBlockComment
	sString       // double-quoted
	sChar         // single-quoted
	sRawString    // backtick (Go) or triple-quoted (Python)
	sTripleDouble // """..."""
	sTripleSingle // '''...'''
)

// Extract returns all comments found in source code for the given language.
// Returns nil if the language has no comment syntax or is unsupported.
func Extract(source, language string) []Comment {
	fam := FamilyForLanguage(language)
	if fam == nil {
		return nil
	}

	var comments []Comment
	runes := []rune(source)
	n := len(runes)

	st := sNormal
	line := 1
	col := 1

	// comment accumulation
	var commentBuf strings.Builder
	commentStartLine := 0
	commentStartCol := 0
	blockNest := 0 // for nestable block comments (haskell {- -})
	nestable := language == "haskell"

	i := 0
	for i < n {
		ch := runes[i]

		switch st {
		case sNormal:
			if matchAt(runes, i, fam.BlockOpen) && fam.BlockOpen != "" {
				st = sBlockComment
				blockNest = 1
				commentBuf.Reset()
				commentStartLine = line
				commentStartCol = col
				i += len([]rune(fam.BlockOpen))
				col += len([]rune(fam.BlockOpen))
				continue
			}
			if matchAt(runes, i, fam.LinePrefix) && fam.LinePrefix != "" {
				st = sLineComment
				commentBuf.Reset()
				commentStartLine = line
				commentStartCol = col
				i += len([]rune(fam.LinePrefix))
				col += len([]rune(fam.LinePrefix))
				continue
			}
			// string literal detection — skip to avoid false positives
			switch {
			case ch == '"':
				// check for triple-double-quote (Python)
				if (language == "python" || language == "elixir") && i+2 < n && runes[i+1] == '"' && runes[i+2] == '"' {
					st = sTripleDouble
					i += 3
					col += 3
					continue
				}
				st = sString
			case ch == '\'':
				// check for triple-single-quote (Python)
				if language == "python" && i+2 < n && runes[i+1] == '\'' && runes[i+2] == '\'' {
					st = sTripleSingle
					i += 3
					col += 3
					continue
				}
				st = sChar
			case ch == '`' && (language == "go" || language == "javascript" || language == "typescript" || language == "tsx" || language == "jsx"):
				st = sRawString
			}

		case sLineComment:
			if ch == '\n' {
				text := strings.TrimSpace(commentBuf.String())
				if text != "" {
					kind := classifyComment(fam, "line", text)
					comments = append(comments, Comment{
						Text:    text,
						Kind:    kind,
						Line:    commentStartLine,
						EndLine: line,
						Col:     commentStartCol,
						EndCol:  col,
					})
				}
				st = sNormal
			} else {
				commentBuf.WriteRune(ch)
			}

		case sBlockComment:
			// check for nested open (haskell)
			if nestable && matchAt(runes, i, fam.BlockOpen) {
				blockNest++
				s := fam.BlockOpen
				commentBuf.WriteString(s)
				i += len([]rune(s))
				col += len([]rune(s))
				continue
			}
			if matchAt(runes, i, fam.BlockClose) {
				blockNest--
				if blockNest <= 0 {
					text := strings.TrimSpace(commentBuf.String())
					endCol := col + len([]rune(fam.BlockClose))
					if text != "" {
						kind := classifyComment(fam, "block", text)
						comments = append(comments, Comment{
							Text:    text,
							Kind:    kind,
							Line:    commentStartLine,
							EndLine: line,
							Col:     commentStartCol,
							EndCol:  endCol,
						})
					}
					st = sNormal
					i += len([]rune(fam.BlockClose))
					col += len([]rune(fam.BlockClose))
					continue
				}
				s := fam.BlockClose
				commentBuf.WriteString(s)
				i += len([]rune(s))
				col += len([]rune(s))
				continue
			}
			commentBuf.WriteRune(ch)
			if ch == '\n' {
				line++
				col = 1
				i++
				continue
			}

		case sString:
			if ch == '\\' && i+1 < n {
				// skip escaped character
				i += 2
				col += 2
				continue
			}
			if ch == '"' {
				st = sNormal
			}

		case sChar:
			if ch == '\\' && i+1 < n {
				i += 2
				col += 2
				continue
			}
			if ch == '\'' {
				st = sNormal
			}

		case sRawString:
			if ch == '`' {
				st = sNormal
			}
			if ch == '\n' {
				line++
				col = 1
				i++
				continue
			}

		case sTripleDouble:
			if ch == '"' && i+2 < n && runes[i+1] == '"' && runes[i+2] == '"' {
				st = sNormal
				i += 3
				col += 3
				continue
			}
			if ch == '\n' {
				line++
				col = 1
				i++
				continue
			}

		case sTripleSingle:
			if ch == '\'' && i+2 < n && runes[i+1] == '\'' && runes[i+2] == '\'' {
				st = sNormal
				i += 3
				col += 3
				continue
			}
			if ch == '\n' {
				line++
				col = 1
				i++
				continue
			}
		}

		if ch == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i++
	}

	// handle unterminated line comment at EOF (no trailing newline)
	if st == sLineComment {
		text := strings.TrimSpace(commentBuf.String())
		if text != "" {
			kind := classifyComment(fam, "line", text)
			comments = append(comments, Comment{
				Text:    text,
				Kind:    kind,
				Line:    commentStartLine,
				EndLine: line,
				Col:     commentStartCol,
				EndCol:  col,
			})
		}
	}

	return comments
}

// matchAt checks if s appears at position i in runes.
func matchAt(runes []rune, i int, s string) bool {
	if s == "" {
		return false
	}
	sr := []rune(s)
	if i+len(sr) > len(runes) {
		return false
	}
	for j, r := range sr {
		if runes[i+j] != r {
			return false
		}
	}
	return true
}

// classifyComment decides if a comment is a "doc" comment based on heuristics.
// Doc comment patterns: /** (C-family), /// (C-family), ## (hash-family).
func classifyComment(fam *Family, baseKind, text string) string {
	// block doc comments: /** ... */ (JavaDoc/JSDoc style)
	if baseKind == "block" && fam.BlockOpen == "/*" && len(text) > 0 && text[0] == '*' {
		return "doc"
	}
	// line doc comments: /// (C-family)
	if baseKind == "line" && fam.LinePrefix == "//" && len(text) > 0 && text[0] == '/' {
		return "doc"
	}
	// hash doc comments: ## (Python, Ruby, etc.)
	if baseKind == "line" && fam.LinePrefix == "#" && len(text) > 0 && text[0] == '#' {
		return "doc"
	}
	return baseKind
}
