package plan

import (
	"strings"
	"testing"
)

const sampleHead = `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>My Plan</title>
<style>body{}</style>
</head>
<body>hi</body>
</html>
`

// TestStampHTMLMeta_InsertsWhenMissing verifies all 3 tags land inside <head>
// (before </head>) with the expected content when the document carries none
// yet — the common case: a freshly-rendered plan.html.
func TestStampHTMLMeta_InsertsWhenMissing(t *testing.T) {
	got := string(StampHTMLMeta([]byte(sampleHead), "pln_abc123", "my-plan", "My Plan Title"))

	for _, want := range []string{
		`<meta name="sageox:plan" content="pln_abc123">`,
		`<meta name="sageox:plan-slug" content="my-plan">`,
		`<meta name="sageox:plan-title" content="My Plan Title">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stamped html missing %q\ngot: %s", want, got)
		}
	}

	headEnd := strings.Index(got, "</head>")
	if headEnd < 0 {
		t.Fatalf("stamped html lost its </head>: %s", got)
	}
	for _, name := range []string{"sageox:plan\"", "sageox:plan-slug\"", "sageox:plan-title\""} {
		if idx := strings.Index(got, name); idx < 0 || idx > headEnd {
			t.Errorf("tag %q not found before </head>", name)
		}
	}

	// Untouched content survives verbatim.
	for _, want := range []string{`<meta charset="utf-8"/>`, `<title>My Plan</title>`, `<body>hi</body>`} {
		if !strings.Contains(got, want) {
			t.Errorf("stamping must not disturb existing content; missing %q", want)
		}
	}
}

// TestStampHTMLMeta_ReplacesExisting verifies a re-stamp of html that already
// carries the 3 tags (the realistic case: a skill reads back a previously
// stored plan.html, edits it, and resubmits via `ox plan save --html`) updates
// the values in place rather than duplicating them.
func TestStampHTMLMeta_ReplacesExisting(t *testing.T) {
	first := string(StampHTMLMeta([]byte(sampleHead), "pln_old", "old-slug", "Old Title"))
	second := StampHTMLMeta([]byte(first), "pln_new", "new-slug", "New Title")
	got := string(second)

	if strings.Count(got, `name="sageox:plan"`) != 1 {
		t.Errorf("expected exactly one sageox:plan tag after re-stamp, got html: %s", got)
	}
	if !strings.Contains(got, `<meta name="sageox:plan" content="pln_new">`) {
		t.Errorf("expected updated plan id, got: %s", got)
	}
	if strings.Contains(got, "pln_old") || strings.Contains(got, "old-slug") || strings.Contains(got, "Old Title") {
		t.Errorf("stale values survived re-stamp: %s", got)
	}
	if !strings.Contains(got, `<meta name="sageox:plan-slug" content="new-slug">`) ||
		!strings.Contains(got, `<meta name="sageox:plan-title" content="New Title">`) {
		t.Errorf("slug/title not updated: %s", got)
	}
}

// TestStampHTMLMeta_EscapesValues verifies a title containing HTML-significant
// characters is escaped so it can't break out of the attribute or inject markup.
func TestStampHTMLMeta_EscapesValues(t *testing.T) {
	got := string(StampHTMLMeta([]byte(sampleHead), "pln_x", "slug-x", `<script>alert("x")</script> & "quoted"`))
	if strings.Contains(got, "<script>alert") {
		t.Errorf("title was not escaped, injected markup survived: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped title content, got: %s", got)
	}
}

// TestStampHTMLMeta_NilAndNoHeadAreNoop verifies the two fail-open cases: a nil
// input (nothing to stamp) and a document with no </head> (nothing safe to
// anchor on) both return unmodified.
func TestStampHTMLMeta_NilAndNoHeadAreNoop(t *testing.T) {
	if got := StampHTMLMeta(nil, "pln_x", "s", "t"); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}

	noHead := []byte("plain text, not html at all")
	got := StampHTMLMeta(noHead, "pln_x", "s", "t")
	if string(got) != string(noHead) {
		t.Errorf("no-</head> input must pass through unmodified, got: %s", got)
	}
}

// TestStampHTMLMeta_SizeGatingByteEquality is the load-bearing regression this
// test guards: TestSave_HTMLSizeGating in store_test.go asserts a small plain
// html payload survives Save() BYTE-FOR-BYTE. That payload
// (bytes.Repeat([]byte("a"), n)) has no <head>, so StampHTMLMeta must return it
// completely unchanged — this pins that contract directly against the exact
// shape store_test.go exercises.
func TestStampHTMLMeta_SizeGatingByteEquality(t *testing.T) {
	in := strings.Repeat("a", 1024)
	got := StampHTMLMeta([]byte(in), "pln_x", "s", "t")
	if string(got) != in {
		t.Errorf("html with no <head> must round-trip byte-for-byte, got %d bytes want %d", len(got), len(in))
	}
}
