package githubmirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// WithheldNotice is the exact line a withheld comment's body consists of. The
// server writes it when its safety scan refuses a comment; the text itself
// never reaches the board. It already starts with "> ", so it reads as a quote
// whatever the author's trust tier.
const WithheldNotice = "> ⚠ Withheld by the SageOx safety scan. Read it on GitHub."

// ErrInvalidPost is the class of every ParsePost failure. A caller that is
// indexing a folder of posts uses it to skip one bad file without aborting the
// rest.
var ErrInvalidPost = errors.New("invalid github post")

const (
	frontMatterDelimiter = "---"
	titlePrefix          = "# "
	discussionHeading    = "## Discussion"
	filesHeading         = "## Files touched"
	commentPrefix        = "### @"
	commentHeadingSep    = " · " // U+00B7 with a space on each side
	quotePrefix          = "> "
	quoteBare            = ">"
	escapePrefix         = `\`
)

// PostsDir is <teamContextPath>/bulletin/github/posts.
func PostsDir(teamContextPath string) string {
	return filepath.Join(teamContextPath, "bulletin", Board, "posts")
}

// ParsePost parses a rendered github-board post: YAML front matter followed by
// the markdown layout in docs/specs/github-bulletin-mirror.md.
//
// The front matter is parsed strictly (delimiters, source, and the identity
// fields must be present); the body is parsed leniently (extra blank lines are
// fine, and the "## Files touched" section is skipped because the header
// already carries the file list). Unknown front-matter keys are tolerated so a
// newer server can add a field without hiding every post from an older client.
//
// Only a line that is exactly "## Discussion" or "## Files touched" ends the
// description, so a member's own "## Summary" stays in it. Text the author
// wrote is returned as they wrote it: ParsePost removes the "> " prefix an
// external author's lines carry, and the one backslash the renderer put in
// front of any line that would otherwise read as post structure (see
// unescape). A comment whose body is WithheldNotice comes back with Withheld
// set and an empty Body.
func ParsePost(data []byte) (*Post, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")

	front, body, err := splitFrontMatter(text)
	if err != nil {
		return nil, err
	}

	var header PostHeader
	if err := yaml.Unmarshal([]byte(front), &header); err != nil {
		return nil, fmt.Errorf("%w: front matter: %w", ErrInvalidPost, err)
	}
	if err := validateHeader(header); err != nil {
		return nil, err
	}

	post := &Post{Header: header}
	if err := parseBody(post, body); err != nil {
		return nil, err
	}
	if err := attachCommentMetadata(post); err != nil {
		return nil, err
	}
	return post, nil
}

// attachCommentMetadata keeps old posts readable while rejecting positional metadata that cannot match.
func attachCommentMetadata(post *Post) error {
	metadata := post.Header.CommentMetadata
	if metadata == nil {
		return nil
	}
	if len(metadata) != len(post.Comments) {
		return fmt.Errorf("%w: comment metadata count does not match Discussion", ErrInvalidPost)
	}
	for i, entry := range metadata {
		if entry.Line != nil && (*entry.Line <= 0 || entry.Path == "") {
			return fmt.Errorf("%w: comment metadata %d line requires a positive value and a path", ErrInvalidPost, i)
		}
		post.Comments[i].Path = entry.Path
		post.Comments[i].Line = entry.Line
	}
	return nil
}

// splitFrontMatter returns the YAML between the first line "---" and the next
// line "---", and the lines after the closing delimiter.
func splitFrontMatter(text string) (front string, body []string, err error) {
	lines := strings.Split(text, "\n")
	if lines[0] != frontMatterDelimiter {
		return "", nil, fmt.Errorf("%w: first line must be %q", ErrInvalidPost, frontMatterDelimiter)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == frontMatterDelimiter {
			return strings.Join(lines[1:i], "\n"), lines[i+1:], nil
		}
	}
	return "", nil, fmt.Errorf("%w: front matter is not closed by a %q line", ErrInvalidPost, frontMatterDelimiter)
}

func validateHeader(h PostHeader) error {
	if h.Source != "github" {
		return fmt.Errorf("%w: source is %q, want %q", ErrInvalidPost, h.Source, "github")
	}
	if h.Kind == "" {
		return fmt.Errorf("%w: kind is missing", ErrInvalidPost)
	}
	if h.Number <= 0 {
		return fmt.Errorf("%w: number is missing", ErrInvalidPost)
	}
	if h.Repo == "" {
		return fmt.Errorf("%w: repo is missing", ErrInvalidPost)
	}
	return nil
}

// commentBuilder collects one Discussion entry until the next heading.
type commentBuilder struct {
	comment PostComment
	lines   []string
}

func parseBody(post *Post, lines []string) error {
	titleAt := -1
	for i, line := range lines {
		if strings.HasPrefix(line, titlePrefix) {
			titleAt = i
			break
		}
	}
	if titleAt < 0 {
		return fmt.Errorf("%w: missing %q title line", ErrInvalidPost, strings.TrimSpace(titlePrefix))
	}

	const (
		inDescription = iota
		inDiscussion
		inOther
	)
	section := inDescription

	var description []string
	var current *commentBuilder
	flush := func() {
		if current != nil {
			post.Comments = append(post.Comments, finishComment(current))
			current = nil
		}
	}

	for _, line := range lines[titleAt+1:] {
		// Only the two sections the renderer writes count. Any other "## "
		// line is the author's own markdown.
		if line == discussionHeading || line == filesHeading {
			flush()
			if line == discussionHeading {
				section = inDiscussion
			} else {
				section = inOther
			}
			continue
		}

		// A "### @" line only starts a comment when it is a well-formed comment
		// heading. A member's own "### Details" inside a comment body is
		// content, not structure.
		if section == inDiscussion && strings.HasPrefix(line, commentPrefix) {
			if comment, ok := parseCommentHeading(line); ok {
				flush()
				current = &commentBuilder{comment: comment}
				continue
			}
		}

		switch section {
		case inDescription:
			description = append(description, line)
		case inDiscussion:
			// lines between "## Discussion" and the first comment heading are
			// not part of any comment
			if current != nil {
				current.lines = append(current.lines, line)
			}
		}
	}
	flush()

	if post.Header.Trust == TrustExternal {
		description = unquote(description)
	}
	post.Body = joinTrimmed(unescape(description))
	return nil
}

// parseCommentHeading reads "### @login · tier · RFC3339".
func parseCommentHeading(line string) (PostComment, bool) {
	if !hasCommentHeadingShape(line) {
		return PostComment{}, false
	}
	parts := strings.Split(strings.TrimPrefix(line, commentPrefix), commentHeadingSep)

	login := parts[0]
	if login == "" {
		return PostComment{}, false
	}
	switch parts[1] {
	case TrustMember, TrustExternal, TrustBot:
	default:
		return PostComment{}, false
	}
	at, err := time.Parse(time.RFC3339, parts[2])
	if err != nil {
		return PostComment{}, false
	}
	return PostComment{Login: login, Trust: parts[1], CreatedAt: at.UTC()}, true
}

func finishComment(b *commentBuilder) PostComment {
	comment := b.comment

	// The withheld line is compared before unquoting: it is itself a "> "
	// line, and unquoting it would stop it matching.
	raw := joinTrimmed(b.lines)
	if strings.TrimSpace(raw) == WithheldNotice {
		comment.Withheld = true
		return comment
	}

	lines := b.lines
	if comment.Trust == TrustExternal {
		lines = unquote(lines)
	}
	comment.Body = joinTrimmed(unescape(lines))
	return comment
}

// unquote removes one leading "> " (or a lone ">") from every line. A line
// without the prefix is left alone: the parser reads what the server wrote, it
// does not police it.
func unquote(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, quotePrefix):
			out[i] = line[len(quotePrefix):]
		case line == quoteBare:
			out[i] = ""
		default:
			out[i] = line
		}
	}
	return out
}

// hasCommentHeadingShape reports whether a line looks like a comment heading,
// "### @login · tier · time", without checking that the tier and time are
// valid. The renderer escapes every line of this shape, valid or not, so a
// reader never has to guess which ones count.
func hasCommentHeadingShape(line string) bool {
	return strings.HasPrefix(line, commentPrefix) && len(strings.Split(line, commentHeadingSep)) == 3
}

// isStructureLine reports whether a line, left as it was, would read as post
// structure: a section heading or something shaped like a comment heading.
func isStructureLine(line string) bool {
	return line == discussionHeading || line == filesHeading || hasCommentHeadingShape(line)
}

// unescape undoes the renderer's escaping: a line that is a structure line
// behind one or more backslashes loses exactly one. A line the renderer would
// not have escaped, such as "\## Summary", keeps its backslash. Looking past the
// whole run of backslashes, not just the first, is what keeps this exact: an
// author's own line "\## Discussion" is written with two backslashes and read
// back with one.
func unescape(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line
		if rest, ok := strings.CutPrefix(line, escapePrefix); ok && isStructureLine(strings.TrimLeft(rest, escapePrefix)) {
			out[i] = rest
		}
	}
	return out
}

// joinTrimmed joins lines with "\n" after dropping the blank lines at both
// ends. Only blank lines go: indentation on the first content line is part of
// the text (a description may open with a code block).
func joinTrimmed(lines []string) string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// ReadPostMeta reads a post's .meta.json.
func ReadPostMeta(path string) (*PostMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read post meta %s: %w", path, err)
	}
	var meta PostMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("decode post meta %s: %w", path, err)
	}
	return &meta, nil
}
