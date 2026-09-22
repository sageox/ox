package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validBulletinInput is a baseline that passes every local check; each table
// below changes one thing so the failure it exercises is the only one present.
func validBulletinInput() bulletinInput {
	return bulletinInput{
		Source:  "release-notes.md",
		Content: []byte("# Release notes for 0.17\n\nShipped.\n"),
		TTL:     "14d",
	}
}

// requireBulletinInputError asserts the refusal names the expected wire field.
func requireBulletinInputError(t *testing.T, err error, field string) *bulletinInputError {
	t.Helper()
	require.Error(t, err)
	var inputErr *bulletinInputError
	require.True(t, errors.As(err, &inputErr), "want *bulletinInputError, got %T: %v", err, err)
	assert.Equal(t, field, inputErr.Field, "refusal must name the field to fix (got %q)", inputErr.Message)
	return inputErr
}

// TestPlanBulletinPost_SlugDerivation pins the bulletin slug rules, which are
// the server's and differ from the plan-store and repo slugifiers.
//
// Failure prevented: a derived slug the server rejects (uppercase, a leading
// hyphen, over 80 characters) costing a round-trip and a 400; or --slug being
// silently rewritten so the file name teammates see is not what was typed.
func TestPlanBulletinPost_SlugDerivation(t *testing.T) {
	longStem := strings.Repeat("x", 79) + "-" + strings.Repeat("y", 20) // 100 chars

	tests := []struct {
		name      string
		source    string
		slugFlag  string
		wantSlug  string
		wantError bool
	}{
		{name: "spaces, parens, and a dot collapse to single hyphens", source: "Release Notes (0.17).md", wantSlug: "release-notes-0-17"},
		{name: "non-ASCII letters become hyphens and edges are trimmed", source: "--Ünïcode--.md", wantSlug: "n-code"},
		{name: "nothing survives", source: "###.md", wantError: true},
		{name: "100-char name capped at 80 and trimmed of a trailing hyphen", source: longStem + ".md", wantSlug: strings.Repeat("x", 79)},
		{name: "directory and extension are dropped", source: "/tmp/notes/Weekly Update.MD", wantSlug: "weekly-update"},
		{name: "inner dots become hyphens", source: "notes.tar.md", wantSlug: "notes-tar"},
		{name: "digits may lead", source: "2026-09-21 standup.md", wantSlug: "2026-09-21-standup"},
		{name: "--slug is used as given", source: "Whatever.md", slugFlag: "my-slug", wantSlug: "my-slug"},
		{name: "--slug is never rewritten: uppercase refused", source: "Whatever.md", slugFlag: "My-Slug", wantError: true},
		{name: "--slug leading hyphen refused", source: "Whatever.md", slugFlag: "-x", wantError: true},
		{name: "--slug over 80 refused", source: "Whatever.md", slugFlag: strings.Repeat("a", 81), wantError: true},
		{name: "--slug exactly 80 accepted", source: "Whatever.md", slugFlag: strings.Repeat("a", 80), wantSlug: strings.Repeat("a", 80)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validBulletinInput()
			in.Source = tt.source
			in.Slug = tt.slugFlag
			plan, err := planBulletinPost(in)
			if tt.wantError {
				e := requireBulletinInputError(t, err, "slug")
				assert.Equal(t, "--slug", e.Fix)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSlug, plan.Request.Slug)
		})
	}
}

// TestPlanBulletinPost_TitleDerivation pins where the title comes from and
// the CLI's own single-line and 200-rune rules.
//
// Failure prevented: a Markdown post published under its file name when it
// has a heading; a title carrying a pasted paragraph; a 201-rune title
// costing a server round-trip.
func TestPlanBulletinPost_TitleDerivation(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		content   string
		titleFlag string
		wantTitle string
		wantError bool
	}{
		{name: "markdown first heading", source: "n.md", content: "# Release notes for 0.17\n\nbody\n", wantTitle: "Release notes for 0.17"},
		{name: "markdown heading after prose", source: "n.md", content: "intro\n\n# Later heading  \nbody", wantTitle: "Later heading"},
		{name: "markdown heading with CRLF", source: "n.md", content: "# Windows heading\r\nbody\r\n", wantTitle: "Windows heading"},
		{name: "markdown heading behind a BOM", source: "n.md", content: "\ufeff# Behind a BOM\n", wantTitle: "Behind a BOM"},
		{name: "markdown without an H1 falls back to the file name", source: "Weekly Update.md", content: "## only a subheading\n", wantTitle: "Weekly Update"},
		{name: "hash without a space is not a heading", source: "n.md", content: "#hashtag\n", wantTitle: "n"},
		{name: "html title, case-insensitive, whitespace collapsed", source: "n.html", content: "<html><head><TITLE>\n  Incident   42\n  timeline </TITLE></head></html>", wantTitle: "Incident 42 timeline"},
		{name: "html title entities are unescaped", source: "n.html", content: "<title>Q&amp;A</title>", wantTitle: "Q&A"},
		{name: "html first title wins", source: "n.html", content: "<title>first</title><title>second</title>", wantTitle: "first"},
		{name: "html without a title falls back to the file name", source: "runbook.htm", content: "<p>no title</p>", wantTitle: "runbook"},
		{name: "--title wins over the heading", source: "n.md", content: "# heading\n", titleFlag: "  Custom title  ", wantTitle: "Custom title"},
		{name: "--title must be a single line", source: "n.md", content: "body\n", titleFlag: "line one\nline two", wantError: true},
		{name: "--title of 200 runes is accepted", source: "n.md", content: "body\n", titleFlag: strings.Repeat("é", 200), wantTitle: strings.Repeat("é", 200)},
		{name: "--title of 201 runes is refused", source: "n.md", content: "body\n", titleFlag: strings.Repeat("é", 201), wantError: true},
		{name: "derived heading over 200 runes is refused", source: "n.md", content: "# " + strings.Repeat("x", 201) + "\n", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validBulletinInput()
			in.Source = tt.source
			in.Content = []byte(tt.content)
			in.Title = tt.titleFlag
			plan, err := planBulletinPost(in)
			if tt.wantError {
				requireBulletinInputError(t, err, "title")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantTitle, plan.Request.Title)
		})
	}
}

// TestPlanBulletinPost_FormatFromExtension pins the extension map and that an
// unknown extension is refused naming --format rather than guessed.
//
// Failure prevented: a .txt file published as Markdown by guesswork, or a
// .HTML file refused because the extension check was case-sensitive.
func TestPlanBulletinPost_FormatFromExtension(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		formatFlag string
		wantFormat string
		wantError  bool
	}{
		{name: ".md", source: "a.md", wantFormat: "markdown"},
		{name: ".markdown", source: "a.markdown", wantFormat: "markdown"},
		{name: ".MD uppercase", source: "a.MD", wantFormat: "markdown"},
		{name: ".html", source: "a.html", wantFormat: "html"},
		{name: ".htm", source: "a.htm", wantFormat: "html"},
		{name: ".txt refused", source: "a.txt", wantError: true},
		{name: "no extension refused", source: "README", wantError: true},
		{name: "--format overrides the extension", source: "a.md", formatFlag: "html", wantFormat: "html"},
		{name: "--format is case-insensitive", source: "a.txt", formatFlag: "Markdown", wantFormat: "markdown"},
		{name: "--format md is accepted as markdown", source: "a.txt", formatFlag: "md", wantFormat: "markdown"},
		{name: "--format pdf refused", source: "a.md", formatFlag: "pdf", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validBulletinInput()
			in.Source = tt.source
			in.Format = tt.formatFlag
			plan, err := planBulletinPost(in)
			if tt.wantError {
				e := requireBulletinInputError(t, err, "format")
				assert.Equal(t, "--format", e.Fix)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, plan.Request.Format)
		})
	}
}

// TestPlanBulletinPost_TTL pins the TTL grammar and the mirrored 1h..90d
// bounds.
//
// Failure prevented: "2w" or "120d" leaving the machine and coming back as a
// 400 that names a field the caller then has to map to a flag; or a valid
// boundary value (1h, 90d, 2160h) being refused locally.
func TestPlanBulletinPost_TTL(t *testing.T) {
	tests := []struct {
		ttl       string
		wantError bool
	}{
		{ttl: "14d"}, {ttl: "1h"}, {ttl: "24h"}, {ttl: "90d"}, {ttl: "2160h"},
		{ttl: "0h", wantError: true},
		{ttl: "91d", wantError: true},
		{ttl: "2161h", wantError: true},
		{ttl: "2w", wantError: true},
		{ttl: "14", wantError: true},
		{ttl: "14D", wantError: true},
		{ttl: "-1d", wantError: true},
		{ttl: "1.5d", wantError: true},
		{ttl: "99999999999999999999h", wantError: true},
		{ttl: "", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.ttl, func(t *testing.T) {
			in := validBulletinInput()
			in.TTL = tt.ttl
			plan, err := planBulletinPost(in)
			if tt.wantError {
				e := requireBulletinInputError(t, err, "ttl")
				assert.Equal(t, "--ttl", e.Fix)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.ttl, plan.Request.TTL)
		})
	}
}

// TestPlanBulletinPost_ContentChecks pins the server's content rules applied
// locally.
//
// Failure prevented: a 1 MiB + 1 byte file being sent and refused with a
// 413, a NUL byte or invalid UTF-8 reaching the server, or a blank file being
// published as an empty post.
func TestPlanBulletinPost_ContentChecks(t *testing.T) {
	tests := []struct {
		name      string
		content   []byte
		wantError bool
	}{
		{name: "exactly 1 MiB is accepted", content: []byte("# t\n" + strings.Repeat("a", bulletinMaxContentBytes-4))},
		{name: "1 MiB + 1 byte refused", content: []byte("# t\n" + strings.Repeat("a", bulletinMaxContentBytes-3)), wantError: true},
		{name: "NUL byte refused", content: []byte("# t\nbody\x00more\n"), wantError: true},
		{name: "invalid UTF-8 refused", content: []byte("# t\nbody \xff\xfe\n"), wantError: true},
		{name: "whitespace-only refused", content: []byte(" \n\t\r\n  "), wantError: true},
		{name: "empty refused", content: []byte{}, wantError: true},
		{name: "non-ASCII UTF-8 accepted", content: []byte("# Título\n\nCafé ☕\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validBulletinInput()
			in.Content = tt.content
			plan, err := planBulletinPost(in)
			if tt.wantError {
				requireBulletinInputError(t, err, "content")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, string(tt.content), plan.Request.Content)
		})
	}
}

// TestPlanBulletinPost_StdinRequirements pins that "-" needs --format,
// --title, and --slug, each refused by name, and publishes the piped bytes
// when all three are given.
//
// Failure prevented: stdin input deriving a slug from "-" (an empty slug the
// server rejects) or a title of "-", instead of telling the caller which flag
// is missing.
func TestPlanBulletinPost_StdinRequirements(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		title     string
		slug      string
		wantField string
	}{
		{name: "no format", wantField: "format"},
		{name: "no title", format: "markdown", wantField: "title"},
		{name: "no slug", format: "markdown", title: "Notes", wantField: "slug"},
		{name: "all three given", format: "markdown", title: "Notes", slug: "notes"},
	}
	piped := []byte("piped bytes\r\n")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planBulletinPost(bulletinInput{
				Source: "-", Content: piped, Format: tt.format, Title: tt.title, Slug: tt.slug, TTL: "1d",
			})
			if tt.wantField != "" {
				e := requireBulletinInputError(t, err, tt.wantField)
				assert.Equal(t, "--"+tt.wantField, e.Fix)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, string(piped), plan.Request.Content)
			assert.Equal(t, "notes", plan.Request.Slug)
			assert.Equal(t, "Notes", plan.Request.Title)
			assert.Equal(t, "markdown", plan.Request.Format)
		})
	}
}

// TestPlanBulletinPost_BoardShape pins the board shape check and the general
// default. Which boards exist is the server's decision.
//
// Failure prevented: an empty --board being sent as "" when the caller meant
// general, or "Ops" reaching the server to be refused there.
func TestPlanBulletinPost_BoardShape(t *testing.T) {
	tests := []struct {
		board     string
		wantBoard string
		wantError bool
	}{
		{board: "", wantBoard: "general"},
		{board: "general", wantBoard: "general"},
		{board: "ops-2", wantBoard: "ops-2"},
		{board: "Ops", wantError: true},
		{board: "-x", wantError: true},
		{board: strings.Repeat("a", 33), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.board, func(t *testing.T) {
			in := validBulletinInput()
			in.Board = tt.board
			plan, err := planBulletinPost(in)
			if tt.wantError {
				e := requireBulletinInputError(t, err, "board")
				assert.Equal(t, "--board", e.Fix)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantBoard, plan.Request.Board)
		})
	}
}

// TestPlanBulletinPost_ContentIsBytesAsIs pins that the request carries the
// file's bytes untouched — BOM, CRLF, trailing whitespace and all — and that
// the plan's hash is the hash of exactly those bytes.
//
// Failure prevented: a "helpful" trim or newline fix changing the bytes so
// the server's content_sha256 never matches the local hash and every publish
// is reported as hash_mismatch.
func TestPlanBulletinPost_ContentIsBytesAsIs(t *testing.T) {
	content := []byte("\ufeff# Title\r\n\r\n<b>&</b>   \r\n\t\n")
	in := validBulletinInput()
	in.Content = content

	plan, err := planBulletinPost(in)
	require.NoError(t, err)

	assert.Equal(t, string(content), plan.Request.Content)
	sum := sha256.Sum256(content)
	assert.Equal(t, hex.EncodeToString(sum[:]), plan.SHA256)
}

// TestBulletinShouldConfirm pins the confirmation decision.
//
// Failure prevented: an AI coworker running with --json being blocked on a
// prompt it can never answer, or --yes still asking.
func TestBulletinShouldConfirm(t *testing.T) {
	tests := []struct {
		name        string
		yes, json   bool
		interactive bool
		want        bool
	}{
		{name: "interactive human is asked", interactive: true, want: true},
		{name: "--yes skips", yes: true, interactive: true, want: false},
		{name: "--json never prompts", json: true, interactive: true, want: false},
		{name: "non-interactive never prompts", interactive: false, want: false},
		{name: "--yes and --json non-interactive", yes: true, json: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bulletinShouldConfirm(tt.yes, tt.json, tt.interactive))
		})
	}
}

// TestBulletinCommaInt pins the byte-count rendering on the receipt.
func TestBulletinCommaInt(t *testing.T) {
	tests := map[int]string{0: "0", 999: "999", 1000: "1,000", 1842: "1,842", 1048576: "1,048,576"}
	for n, want := range tests {
		assert.Equal(t, want, bulletinCommaInt(n), "n=%d", n)
	}
}
