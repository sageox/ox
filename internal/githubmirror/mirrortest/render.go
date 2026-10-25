// Package mirrortest is a test double of the SageOx GitHub mirror API.
//
// It exists ONLY so tests in other packages (the daemon relay, the API client,
// CodeDB, prime) can drive the real client code against something that behaves
// like the server: RenderPost is the reference renderer for the post layout in
// docs/specs/github-bulletin-mirror.md, and Server is an in-process relay
// endpoint that writes those posts into a Team Context directory.
//
// Nothing in this package ships in the ox binary. The real renderer and the
// real injection scan live in the SageOx cloud; the scan here is a deliberately
// dumb stand-in (see WithScan). Do not import this package from non-test code.
package mirrortest

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sageox/ox/internal/githubmirror"
)

// RenderPost renders one item as a github-board post, byte-for-byte the layout
// the spec defines. It is deterministic: the same arguments give the same bytes,
// so a post's content hash is stable across runs.
//
// withheld lists the IDs of comments whose body the safety scan refused; their
// bodies are replaced by githubmirror.WithheldNotice and counted in the header.
// Bot-authored comments are never rendered (the daemon drops them before
// relaying, so one arriving here is a bug upstream, not content to publish).
func RenderPost(repo githubmirror.Repo, it githubmirror.Item, withheld map[int64]bool) []byte {
	trust := githubmirror.TrustOf(it.Author)
	comments := renderableComments(it.Comments)

	header := postHeader(repo, it, trust, countWithheld(comments, withheld))
	for _, c := range comments {
		var metadata githubmirror.PostCommentMetadata
		if !withheld[c.ID] {
			var removed int
			metadata.Path, removed = githubmirror.Cleanup(c.Path)
			metadata.Line = c.Line
			header.Omitted.HiddenSpans += removed
		}
		header.CommentMetadata = append(header.CommentMetadata, metadata)
	}
	front, err := marshalHeader(header)
	if err != nil {
		// every field is a plain string, number, bool, time or slice of
		// those; there is no input that makes this fail
		panic(fmt.Sprintf("mirrortest: encode post header: %v", err))
	}

	blocks := []string{githubmirror.MirrorBanner, titleLine(it)}
	if description := renderDescription(it, trust); description != "" {
		blocks = append(blocks, description)
	}
	if len(comments) > 0 {
		blocks = append(blocks, "## Discussion")
		for _, c := range comments {
			blocks = append(blocks, renderComment(c, withheld[c.ID])...)
		}
	}
	if len(it.Files) > 0 {
		blocks = append(blocks, "## Files touched", renderFiles(it.Files))
	}

	return []byte("---\n" + front + "---\n" + strings.Join(blocks, "\n\n") + "\n")
}

func postHeader(repo githubmirror.Repo, it githubmirror.Item, trust string, withheld int) githubmirror.PostHeader {
	var review githubmirror.PostReview
	for _, r := range it.Reviews {
		switch r.State {
		case "APPROVED":
			review.Approved = append(review.Approved, renderLogin(r.Author.Login))
		case "CHANGES_REQUESTED":
			review.ChangesRequested = append(review.ChangesRequested, renderLogin(r.Author.Login))
		}
	}

	return githubmirror.PostHeader{
		Source: "github",
		Repo:   repo.Owner + "/" + repo.Name,
		Kind:   it.Kind,
		Number: it.Number,
		URL:    it.URL,
		State:  it.State,
		Draft:  it.Draft,
		Title:  it.Title,
		Author: githubmirror.PostAuthor{
			Login:       renderLogin(it.Author.Login),
			ID:          it.Author.ID,
			Association: it.Author.Association,
		},
		Trust:              trust,
		Labels:             it.Labels,
		Created:            it.CreatedAt.UTC(),
		Closed:             utcPtr(it.ClosedAt),
		Merged:             utcPtr(it.MergedAt),
		LastMaterialChange: it.LastMaterialChangeAt.UTC(),
		Review:             review,
		Files:              it.Files,
		Omitted: githubmirror.PostOmit{
			BotComments:    it.Omitted.BotComments,
			Withheld:       withheld,
			HiddenSpans:    it.Omitted.HiddenSpans,
			FilesTruncated: it.Omitted.FilesTruncated,
		},
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// marshalHeader writes the header from the same struct tags the parser reads,
// so the renderer cannot drift from the parser's field names. The spec lays
// the small nested values out inline ("author: {login: ..., id: ...}"), which
// block-style marshaling would not, so those nodes are switched to flow style.
func marshalHeader(header githubmirror.PostHeader) (string, error) {
	var node yaml.Node
	if err := node.Encode(header); err != nil {
		return "", err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "author", "review", "omitted", "labels", "files":
			node.Content[i+1].Style |= yaml.FlowStyle
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func renderableComments(all []githubmirror.Comment) []githubmirror.Comment {
	out := make([]githubmirror.Comment, 0, len(all))
	for _, c := range all {
		if githubmirror.TrustOf(c.Author) != githubmirror.TrustBot {
			out = append(out, c)
		}
	}
	return out
}

func countWithheld(comments []githubmirror.Comment, withheld map[int64]bool) int {
	n := 0
	for _, c := range comments {
		if withheld[c.ID] {
			n++
		}
	}
	return n
}

// titleLine is the "# PR #N — title" line. The title is flattened to one line
// first: a line break in it would let the rest of the title pose as a
// "## Discussion" heading.
func titleLine(it githubmirror.Item) string {
	label := "Issue"
	if it.Kind == githubmirror.KindPullRequest {
		label = "PR"
	}
	return fmt.Sprintf("# %s #%d — %s", label, it.Number, oneLine(it.Title))
}

// renderDescription returns the description block. Author text is first
// escaped (see escapeStructure), then, for an external author, quoted line by
// line, so nothing in it can pass for post structure. A bot's body is not
// published at all: the post carries a one-line summary instead.
func renderDescription(it githubmirror.Item, trust string) string {
	switch trust {
	case githubmirror.TrustBot:
		return fmt.Sprintf("Opened by the bot @%s. Its text is not mirrored.", renderLogin(it.Author.Login))
	case githubmirror.TrustExternal:
		return quote(escapeStructure(normalize(it.Body)))
	default:
		return escapeStructure(normalize(it.Body))
	}
}

// renderComment returns the heading block and, when there is one, the body block.
func renderComment(c githubmirror.Comment, withheld bool) []string {
	tier := githubmirror.TrustOf(c.Author)
	heading := fmt.Sprintf("### @%s · %s · %s", renderLogin(c.Author.Login), tier, c.CreatedAt.UTC().Format(time.RFC3339))

	var body string
	switch {
	case withheld:
		body = githubmirror.WithheldNotice
	case tier == githubmirror.TrustExternal:
		body = quote(escapeStructure(normalize(c.Body)))
	default:
		body = escapeStructure(normalize(c.Body))
	}
	if body == "" {
		return []string{heading}
	}
	return []string{heading, body}
}

func renderFiles(files []string) string {
	lines := make([]string, len(files))
	for i, f := range files {
		// a path may legally contain a newline; flattened, it cannot start a
		// heading line of its own
		lines[i] = "- " + oneLine(f)
	}
	return strings.Join(lines, "\n")
}

// escapeStructure puts one backslash in front of every line of author text that
// would otherwise read as post structure: the two section headings, and
// anything shaped like a comment heading. Readers strip that one backslash.
//
// Lines that already start with backslashes get one more, so an author's own
// "\## Discussion" survives a round trip instead of being mistaken for an
// escaped heading. Other markdown headings, such as a member's "## Summary",
// are left alone: they are not structure.
func escapeStructure(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if isStructure(strings.TrimLeft(line, `\`)) {
			lines[i] = `\` + line
		}
	}
	return strings.Join(lines, "\n")
}

// isStructure is the spec's definition of a line a reader would take for post
// structure. It deliberately matches the SHAPE of a comment heading and not
// only valid ones, so the rule needs no knowledge of tiers or timestamps.
func isStructure(line string) bool {
	return line == "## Discussion" || line == "## Files touched" ||
		(strings.HasPrefix(line, "### @") && len(strings.Split(line, " · ")) == 3)
}

// quote prefixes every line with "> ". An empty line becomes a bare ">" so the
// output carries no trailing whitespace.
func quote(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + line
		}
	}
	return strings.Join(lines, "\n")
}

// normalize converts CRLF (GitHub bodies are usually CRLF) to LF and drops the
// blank lines around the text.
func normalize(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// renderLogin is a login as the post shows it. A comment heading is
// "### @login · tier · time" and a reader rejects one with an empty login, so a
// missing login (the fetcher already maps a deleted account to UnknownLogin; this
// is the backstop) is shown as UnknownLogin rather than as "### @ ·".
func renderLogin(login string) string {
	if flat := oneLine(login); flat != "" {
		return flat
	}
	return githubmirror.UnknownLogin
}

// oneLine collapses every line break (and the Unicode line/paragraph
// separators) into a single space.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ' ' || r == ' '
	}), " ")
}
