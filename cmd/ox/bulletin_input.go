package main

// bulletin_input.go — pure input derivation and local checks for
// `ox bulletin post`. Nothing here touches the network, the terminal, or the
// Team Context checkout: it turns what the caller supplied (flags plus the
// bytes of one file) into the exact six-field request the server accepts, or
// refuses with the name of the field to fix. Every rule mirrors a server rule
// so that invalid input never leaves the machine; the server still re-checks
// everything.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sageox/ox/internal/api"
)

// Server-mirrored limits. These are the server's numbers restated so a post
// that cannot be accepted is refused here, before any bytes are sent.
const (
	// bulletinMaxContentBytes is the server's cap on the content field.
	bulletinMaxContentBytes = 1 << 20
	// bulletinMaxTitleRunes is the server's cap on the title.
	bulletinMaxTitleRunes = 200
	// bulletinMaxSlugLen is the server's cap on the slug.
	bulletinMaxSlugLen = 80
	// bulletinMinTTLHours / bulletinMaxTTLHours are the server's inclusive TTL
	// bounds (1h..90d). Mirrored locally so a typo like 120d is refused with
	// the flag name instead of costing a round-trip; the server's answer for
	// anything that slips through is the same validation_error.
	bulletinMinTTLHours = 1
	bulletinMaxTTLHours = 90 * 24
)

// bulletinStdinSource is the positional argument that means "read stdin".
const bulletinStdinSource = "-"

var (
	// bulletinSlugPattern is the server's slug grammar, applied to --slug as
	// given (never rewritten) and to the slug derived from a file name.
	bulletinSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	// bulletinBoardPattern is a shape check only; which boards exist is the
	// server's decision.
	bulletinBoardPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// bulletinTTLPattern is the server's TTL grammar: whole hours or days.
	bulletinTTLPattern = regexp.MustCompile(`^[0-9]+[hd]$`)
	// bulletinHTMLTitlePattern finds the first <title> element, case-insensitively,
	// across lines.
	bulletinHTMLTitlePattern = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
)

// bulletinInput is everything the caller supplied: the flags and the bytes of
// the one file (or stdin) to publish. Content is the file's bytes exactly as
// read — no trimming, no newline normalization, no BOM stripping — because
// the server hashes what it receives and the receipt is checked against a
// local hash of these same bytes.
type bulletinInput struct {
	// Source is the positional argument: a file path, or "-" for stdin.
	Source  string
	Content []byte
	// Flag overrides. Empty means "derive from the file".
	Title  string
	Slug   string
	Format string
	Board  string
	TTL    string
}

// bulletinPlan is a validated, ready-to-send post. Request carries the six
// wire fields; SHA256 is the lowercase hex digest of Request.Content, kept so
// the receipt's content_sha256 can be compared without re-hashing.
type bulletinPlan struct {
	Request api.BulletinPublishRequest
	SHA256  string
}

// bulletinInputError is a local refusal. Field is the wire field name (board,
// slug, title, format, content, ttl) so the --json details map uses the same
// keys the server's 400 would; Fix names the flag or input the caller should
// change.
type bulletinInputError struct {
	Field   string
	Fix     string
	Message string
}

func (e *bulletinInputError) Error() string {
	return e.Field + ": " + e.Message
}

// planBulletinPost derives the request from the caller's input and checks it
// against the server's rules. It is pure: same input, same answer, nothing
// touched. On error the returned *bulletinInputError names the field.
func planBulletinPost(in bulletinInput) (*bulletinPlan, error) {
	stdin := in.Source == bulletinStdinSource

	// Stdin has no file name to derive anything from, so every derived value
	// must be given explicitly. Refuse before looking at the bytes so the
	// message is about the missing flag, not about the content.
	if stdin {
		switch {
		case in.Format == "":
			return nil, &bulletinInputError{Field: "format", Fix: "--format",
				Message: "reading from stdin: pass --format markdown or --format html"}
		case in.Title == "":
			return nil, &bulletinInputError{Field: "title", Fix: "--title",
				Message: "reading from stdin: pass --title"}
		case in.Slug == "":
			return nil, &bulletinInputError{Field: "slug", Fix: "--slug",
				Message: "reading from stdin: pass --slug"}
		}
	}

	format, err := bulletinFormat(in.Format, in.Source)
	if err != nil {
		return nil, err
	}

	board := in.Board
	if board == "" {
		board = api.BulletinBoardGeneral
	}
	if !bulletinBoardPattern.MatchString(board) {
		return nil, &bulletinInputError{Field: "board", Fix: "--board",
			Message: fmt.Sprintf("board %q must be lowercase letters, digits, and hyphens, start with a letter or digit, and be at most 32 characters", board)}
	}

	if err := bulletinCheckTTL(in.TTL); err != nil {
		return nil, err
	}

	slug, err := bulletinSlug(in.Slug, in.Source)
	if err != nil {
		return nil, err
	}

	title, err := bulletinTitle(in.Title, in.Source, format, in.Content)
	if err != nil {
		return nil, err
	}

	if err := bulletinCheckContent(in.Content, in.Source); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(in.Content)
	return &bulletinPlan{
		Request: api.BulletinPublishRequest{
			Board:   board,
			Slug:    slug,
			Title:   title,
			Format:  format,
			Content: string(in.Content),
			TTL:     in.TTL,
		},
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

// bulletinFormat resolves the post format: --format if given, else the file
// extension. Anything else is refused naming --format, because guessing a
// format from content would store a post under the wrong renderer forever.
func bulletinFormat(flag, source string) (string, error) {
	if flag != "" {
		switch strings.ToLower(strings.TrimSpace(flag)) {
		case api.BulletinFormatMarkdown, "md":
			return api.BulletinFormatMarkdown, nil
		case api.BulletinFormatHTML:
			return api.BulletinFormatHTML, nil
		}
		return "", &bulletinInputError{Field: "format", Fix: "--format",
			Message: fmt.Sprintf("format %q is not one of markdown, html", flag)}
	}
	switch strings.ToLower(filepath.Ext(source)) {
	case ".md", ".markdown":
		return api.BulletinFormatMarkdown, nil
	case ".html", ".htm":
		return api.BulletinFormatHTML, nil
	}
	return "", &bulletinInputError{Field: "format", Fix: "--format",
		Message: fmt.Sprintf("cannot tell the format from %q (expected .md, .markdown, .html, or .htm); pass --format markdown or --format html", filepath.Base(source))}
}

// bulletinSlug resolves the slug: --slug as given (validated, never rewritten,
// so what the caller typed is what teammates will see in the file name), else
// derived from the file name.
func bulletinSlug(flag, source string) (string, error) {
	if flag != "" {
		if !bulletinSlugPattern.MatchString(flag) {
			return "", &bulletinInputError{Field: "slug", Fix: "--slug",
				Message: fmt.Sprintf("slug %q must be lowercase letters, digits, and hyphens, start with a letter or digit, and be at most %d characters", flag, bulletinMaxSlugLen)}
		}
		return flag, nil
	}
	slug := bulletinSlugify(bulletinStem(source))
	if slug == "" || !bulletinSlugPattern.MatchString(slug) {
		return "", &bulletinInputError{Field: "slug", Fix: "--slug",
			Message: fmt.Sprintf("no slug can be derived from the file name %q; pass --slug", filepath.Base(source))}
	}
	return slug, nil
}

// bulletinSlugify applies the bulletin slug rules to a file stem: ASCII
// lowercase, every run of characters outside [a-z0-9] becomes one hyphen,
// leading and trailing hyphens are trimmed, the result is capped at 80
// characters (and re-trimmed so the cap never leaves a trailing hyphen).
//
// Lowercasing is ASCII-only on purpose: a non-ASCII letter is outside
// [a-z0-9] whichever case it is in, so it becomes a hyphen either way, and
// Unicode case folding would only add surprises (a dotted capital I folds to
// two code points). The plan-store and repo slugifiers use different rules;
// this one is the server's.
func bulletinSlugify(stem string) string {
	var b strings.Builder
	b.Grow(len(stem))
	pendingHyphen := false
	for _, r := range stem {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
			fallthrough
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
		default:
			pendingHyphen = true
		}
	}
	slug := b.String()
	if len(slug) > bulletinMaxSlugLen {
		slug = strings.TrimRight(slug[:bulletinMaxSlugLen], "-")
	}
	return slug
}

// bulletinStem is the file name without its directory and extension.
func bulletinStem(source string) string {
	base := filepath.Base(source)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// bulletinTitle resolves the title: --title, else the first "# " heading for
// markdown or the first <title> for html, else the file stem. The result must
// be 1..200 runes after trimming and a single line. The single-line rule is
// the CLI's own (the server accepts any non-blank title) because a title that
// wraps is almost always a heading pasted with its body.
func bulletinTitle(flag, source, format string, content []byte) (string, error) {
	title := strings.TrimSpace(flag)
	fix := "--title"
	if title == "" {
		title = strings.TrimSpace(bulletinDeriveTitle(format, content))
		if title == "" {
			title = strings.TrimSpace(bulletinStem(source))
		}
		fix = "--title (nothing usable was derived from the file)"
	}
	if title == "" {
		return "", &bulletinInputError{Field: "title", Fix: "--title",
			Message: "title is blank; pass --title"}
	}
	if strings.ContainsAny(title, "\r\n") {
		return "", &bulletinInputError{Field: "title", Fix: fix,
			Message: "title must be a single line"}
	}
	if n := utf8.RuneCountInString(title); n > bulletinMaxTitleRunes {
		return "", &bulletinInputError{Field: "title", Fix: fix,
			Message: fmt.Sprintf("title is %d characters; the limit is %d", n, bulletinMaxTitleRunes)}
	}
	return title, nil
}

// bulletinDeriveTitle pulls a title out of the content for the given format,
// or returns "" when the content carries none. Detection only: the content
// itself is never altered. A leading UTF-8 BOM is skipped for the markdown
// heading scan so a file saved by a Windows editor still yields its heading.
func bulletinDeriveTitle(format string, content []byte) string {
	switch format {
	case api.BulletinFormatMarkdown:
		text := strings.TrimPrefix(string(content), "\ufeff")
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimRight(line, "\r")
			if rest, ok := strings.CutPrefix(line, "# "); ok {
				return strings.TrimSpace(rest)
			}
		}
	case api.BulletinFormatHTML:
		if m := bulletinHTMLTitlePattern.FindSubmatch(content); m != nil {
			return strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
		}
	}
	return ""
}

// bulletinCheckTTL applies the server's TTL grammar and inclusive bounds.
func bulletinCheckTTL(ttl string) error {
	if !bulletinTTLPattern.MatchString(ttl) {
		return &bulletinInputError{Field: "ttl", Fix: "--ttl",
			Message: fmt.Sprintf("ttl %q must be a whole number of hours or days, e.g. 6h or 14d", ttl)}
	}
	n, err := strconv.ParseUint(ttl[:len(ttl)-1], 10, 32)
	if err != nil {
		return &bulletinInputError{Field: "ttl", Fix: "--ttl",
			Message: fmt.Sprintf("ttl %q is longer than the maximum 90d", ttl)}
	}
	hours := n
	if ttl[len(ttl)-1] == 'd' {
		hours = n * 24
	}
	switch {
	case hours < bulletinMinTTLHours:
		return &bulletinInputError{Field: "ttl", Fix: "--ttl",
			Message: fmt.Sprintf("ttl %q is shorter than the minimum 1h", ttl)}
	case hours > bulletinMaxTTLHours:
		return &bulletinInputError{Field: "ttl", Fix: "--ttl",
			Message: fmt.Sprintf("ttl %q is longer than the maximum 90d", ttl)}
	}
	return nil
}

// bulletinCheckContent applies the server's content rules: at most 1 MiB,
// valid UTF-8, no NUL byte, not blank.
func bulletinCheckContent(content []byte, source string) error {
	what := filepath.Base(source)
	if source == bulletinStdinSource {
		what = "stdin"
	}
	if len(content) > bulletinMaxContentBytes {
		// The read is capped one byte past the limit, so len(content) is not
		// the input's real size — say "larger than", never a number that
		// would read as "one byte over" for a 40 MB file.
		return &bulletinInputError{Field: "content", Fix: what,
			Message: fmt.Sprintf("%s is larger than the %d-byte (1 MiB) limit", what, bulletinMaxContentBytes)}
	}
	if !utf8.Valid(content) {
		return &bulletinInputError{Field: "content", Fix: what,
			Message: fmt.Sprintf("%s is not valid UTF-8", what)}
	}
	if strings.IndexByte(string(content), 0) >= 0 {
		return &bulletinInputError{Field: "content", Fix: what,
			Message: fmt.Sprintf("%s contains a NUL byte", what)}
	}
	if len(strings.TrimFunc(string(content), unicode.IsSpace)) == 0 {
		return &bulletinInputError{Field: "content", Fix: what,
			Message: fmt.Sprintf("%s is empty or only whitespace", what)}
	}
	return nil
}

// bulletinShouldConfirm decides whether to show the preview and ask before
// publishing. --yes, --json, and a non-interactive terminal each skip it: AI
// coworkers always pass --json, so they are never prompted.
func bulletinShouldConfirm(yes, jsonOutput, interactive bool) bool {
	return !yes && !jsonOutput && interactive
}
