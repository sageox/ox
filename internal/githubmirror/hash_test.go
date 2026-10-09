package githubmirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var changeHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// goldenItem is the cross-language test vector published in the spec. A server
// in another language that canonicalizes this item as goldenCanonicalJSON and
// digests it with SHA-256 must arrive at goldenHash. Fields the hash ignores
// (urls, times, logins, omitted) are filled in on purpose to show they do not
// matter (including an inline comment's Line); labels, files, comments and
// reviews are deliberately out of order.
var goldenItem = Item{
	Kind:   KindPullRequest,
	Number: 42,
	State:  StateMerged,
	Title:  "Add retry <backoff> & jitter",
	Body:   "Line one.\n\"Quoted\" text \u2014 \u00e9\n",
	Author: Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"},
	Labels: []string{"zeta", "alpha"},
	URL:    "https://github.com/acme/api/pull/42",
	Reviews: []Review{
		{Author: Author{Login: "avery-dev", ID: 5550102}, State: "APPROVED", SubmittedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)},
	},
	Comments: []Comment{
		{ID: 9002, Author: Author{Login: "avery-dev", ID: 5550102}, Body: "inline note", Path: "a.go", Line: ptr(7)},
		{ID: 9001, Author: Author{Login: "devon-dev", ID: 5550101}, Body: "first"},
	},
	Files:                []string{"b.go", "a.go"},
	CreatedAt:            time.Date(2026, 9, 28, 17, 2, 11, 0, time.UTC),
	UpdatedAt:            time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC),
	LastMaterialChangeAt: time.Date(2026, 10, 8, 16, 59, 40, 0, time.UTC),
	Omitted:              Omitted{BotComments: 9, HiddenSpans: 1, FilesTruncated: true},
}

// goldenCanonicalJSON was written by hand, then confirmed against Python's
// json.dumps(sort_keys=True, separators=(",", ":"), ensure_ascii=False) and its
// SHA-256 computed outside Go, so the pinned hash is independent of this code.
const goldenCanonicalJSON = `{"author_id":5550101,"body":"Line one.\n\"Quoted\" text — é\n","comments":[{"author_id":5550101,"body":"first","id":9001,"path":""},{"author_id":5550102,"body":"inline note","id":9002,"path":"a.go"}],"draft":false,"files":["a.go","b.go"],"kind":"pull_request","labels":["alpha","zeta"],"number":42,"reviews":[{"author_id":5550102,"state":"APPROVED"}],"state":"merged","title":"Add retry <backoff> & jitter"}`

const goldenHash = "sha256:3ccf2971a6745c951555501faec741a1c72fa6d618d4f6749f12a3303c84a595"

// Failure prevented: the Go daemon and the server (another language) hashing
// the same item differently, so every relay looks like a change, or a real
// change looks like a repeat.
func TestChangeHash_Golden(t *testing.T) {
	t.Parallel()
	canonical, err := canonicalJSON(goldenItem)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if string(canonical) != goldenCanonicalJSON {
		t.Errorf("canonical JSON drifted\n got  %s\n want %s", canonical, goldenCanonicalJSON)
	}
	if got := ChangeHash(goldenItem); got != goldenHash {
		t.Errorf("ChangeHash = %s, want %s", got, goldenHash)
	}
}

// goldenEscapingItem pins how strings are escaped. encoding/json differs from
// other languages' defaults on two points a verifier must copy: it always
// escapes U+2028 and U+2029, and it does not escape "<", ">" or "&". Its
// expected bytes were derived from Python's encoder (which leaves U+2028/9 raw)
// with those two code points escaped by hand.
var goldenEscapingItem = Item{
	Kind:   KindIssue,
	Number: 1,
	State:  StateOpen,
	Body:   "tab\t nl\n quote\" back\\ ctl\u0001 del\u007f ls\u2028 ps\u2029 <&> emoji \U0001F600 \u00e9",
	Author: Author{ID: 1},
}

const goldenEscapingCanonicalPrefix = `{"author_id":1,"body":"tab\t nl\n quote\" back\\ ctl\u0001 del`
const goldenEscapingCanonicalSuffix = ` ls\u2028 ps\u2029 <&> emoji 😀 é","comments":[],"draft":false,"files":[],"kind":"issue","labels":[],"number":1,"reviews":[],"state":"open","title":""}`

const goldenEscapingHash = "sha256:10576b69f0eaddacb96b50c2510180f6fb9d4f627566518fbe0b4c517aa68584"

func TestChangeHash_GoldenStringEscaping(t *testing.T) {
	t.Parallel()
	canonical, err := canonicalJSON(goldenEscapingItem)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	// DEL (0x7f) is written as a byte escape so it is visible in the source
	want := goldenEscapingCanonicalPrefix + "\x7f" + goldenEscapingCanonicalSuffix
	if string(canonical) != want {
		t.Errorf("canonical JSON drifted\n got  %q\n want %q", canonical, want)
	}
	if got := ChangeHash(goldenEscapingItem); got != goldenEscapingHash {
		t.Errorf("ChangeHash = %s, want %s", got, goldenEscapingHash)
	}
}

// Failure prevented: the change hash noticing things it must ignore (relaying
// the same post again and again) or missing things it must notice (a changed
// PR never refreshed). This is the stability matrix, driven through Build so
// it covers the bot filter and cleanup too.
func TestChangeHash_StabilityMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*prInputs)
		same   bool
	}{
		// --- must NOT change the hash ---
		{"bot comment added", func(in *prInputs) {
			in.conversation = append(in.conversation, SourceComment{ID: 1300, Author: botByType, Body: "build green", CreatedAt: at(9), UpdatedAt: at(9)})
		}, true},
		{"bot inline comment added", func(in *prInputs) {
			in.inline = append(in.inline, SourceComment{ID: 2300, Author: botBySuffix, Body: "lint", CreatedAt: at(9), UpdatedAt: at(9), Path: "a.go", Line: ptr(5)})
		}, true},
		{"bot comment edited", func(in *prInputs) { in.conversation[1].Body = "Coverage is 86%." }, true},
		{"bot comment removed", func(in *prInputs) {
			in.conversation = slices.DeleteFunc(in.conversation, func(c SourceComment) bool { return IsBot(c.Author) })
		}, true},
		{"github updated_at bump", func(in *prInputs) { in.pr.UpdatedAt = at(900) }, true},
		{"reaction-like bump on a human comment's updated_at", func(in *prInputs) { in.conversation[0].UpdatedAt = at(800) }, true},
		{"last material change moves", func(in *prInputs) { in.reviews[0].SubmittedAt = at(700) }, true},
		// GitHub nulls an inline comment's line when a push outdates it; that
		// must not re-relay a post whose text did not change.
		{"inline comment line changes", func(in *prInputs) { in.inline[0].Line = ptr(43) }, true},
		{"inline comment line becomes null (outdated)", func(in *prInputs) { in.inline[0].Line = nil }, true},
		{"inline comment line 0", func(in *prInputs) { in.inline[0].Line = ptr(0) }, true},
		{"same verdict re-submitted later", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3005, Author: authorAvery, State: "APPROVED", SubmittedAt: at(30)})
		}, true},
		{"url changes", func(in *prInputs) { in.pr.HTMLURL = "https://github.com/acme/renamed/pull/1287" }, true},
		{"created_at differs", func(in *prInputs) { in.pr.CreatedAt = at(-100) }, true},
		{"logins renamed", func(in *prInputs) {
			in.pr.Author.Login = "devon-renamed"
			in.conversation[0].Author.Login = "avery-renamed"
			in.reviews[0].Author.Login = "avery-renamed"
		}, true},
		{"conversation comment order shuffled", func(in *prInputs) { slices.Reverse(in.conversation) }, true},
		{"label order shuffled", func(in *prInputs) { slices.Reverse(in.pr.Labels) }, true},
		{"file order shuffled", func(in *prInputs) { slices.Reverse(in.files) }, true},
		{"review order shuffled", func(in *prInputs) { slices.Reverse(in.reviews) }, true},
		{"commented-only review from a new reviewer", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3006, Author: authorRiley, State: "COMMENTED", SubmittedAt: at(9)})
		}, true},
		{"pending review", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3007, Author: authorRiley, State: "PENDING", SubmittedAt: at(9)})
		}, true},
		{"bot review", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3008, Author: botByType, State: "CHANGES_REQUESTED", SubmittedAt: at(9)})
		}, true},
		{"superseded earlier review of the same reviewer", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3009, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(-5)})
		}, true},
		{"fetcher truncation flag flips (omitted is not hashed)", func(in *prInputs) { in.filesTruncated = true }, true},

		// --- MUST change the hash ---
		{"human conversation comment added", func(in *prInputs) {
			in.conversation = append(in.conversation, SourceComment{ID: 1400, Author: authorRiley, Body: "any update?", CreatedAt: at(9), UpdatedAt: at(9)})
		}, false},
		{"human inline comment added", func(in *prInputs) {
			in.inline = append(in.inline, SourceComment{ID: 2400, Author: authorRiley, Body: "why?", CreatedAt: at(9), UpdatedAt: at(9), Path: "a.go", Line: ptr(5)})
		}, false},
		{"human comment edited", func(in *prInputs) { in.conversation[0].Body = "Looks great overall." }, false},
		{"human comment deleted", func(in *prInputs) { in.conversation = in.conversation[1:] }, false},
		{"inline comment moves to another file", func(in *prInputs) { in.inline[0].Path = "other.go" }, false},
		{"comment authored by a different account", func(in *prInputs) { in.conversation[0].Author.ID = 99 }, false},
		{"review flips to changes requested", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3010, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(9)})
		}, false},
		{"another reviewer approves", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3011, Author: authorCarol, State: "APPROVED", SubmittedAt: at(9)})
		}, false},
		{"approval is dismissed", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3012, Author: authorAvery, State: "DISMISSED", SubmittedAt: at(9)})
		}, false},
		{"approval disappears", func(in *prInputs) { in.reviews = nil }, false},
		{"closed", func(in *prInputs) { in.pr.State, in.pr.ClosedAt = "closed", ptr(at(9)) }, false},
		{"merged", func(in *prInputs) {
			in.pr.State, in.pr.ClosedAt, in.pr.MergedAt = "closed", ptr(at(9)), ptr(at(9))
		}, false},
		{"title edited", func(in *prInputs) { in.pr.Title += "!" }, false},
		{"body edited", func(in *prInputs) { in.pr.Body += " More." }, false},
		{"hidden comment appears in the body", func(in *prInputs) { in.pr.Body += "<!-- x -->" }, false},
		{"label added", func(in *prInputs) { in.pr.Labels = append(in.pr.Labels, "needs-review") }, false},
		{"label removed", func(in *prInputs) { in.pr.Labels = in.pr.Labels[:1] }, false},
		{"label renamed", func(in *prInputs) { in.pr.Labels[0] = "github-sync" }, false},
		{"file added", func(in *prInputs) { in.files = append(in.files, "README.md") }, false},
		{"file removed", func(in *prInputs) { in.files = in.files[:2] }, false},
		{"file renamed", func(in *prInputs) { in.files[0] = "internal/daemon/renamed.go" }, false},
		{"draft toggled", func(in *prInputs) { in.pr.Draft = true }, false},
		{"author account differs", func(in *prInputs) { in.pr.Author.ID = 12345 }, false},
		{"number differs", func(in *prInputs) { in.pr.Number++ }, false},
	}
	base := newPRInputs().build().ChangeHash
	if !changeHashPattern.MatchString(base) {
		t.Fatalf("base hash %q is not sha256:<64 hex>", base)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newPRInputs()
			tt.mutate(&in)
			got := in.build().ChangeHash
			if same := got == base; same != tt.same {
				t.Errorf("hash same-as-base = %v, want %v (base %s, got %s)", same, tt.same, base, got)
			}
		})
	}
}

// Failure prevented: an edit made only inside a hidden HTML comment (invisible
// to people, and already stripped from the post) re-publishing the post.
func TestChangeHash_HiddenTextEditsAreInvisible(t *testing.T) {
	t.Parallel()
	build := func(body string) string {
		in := newPRInputs()
		in.pr.Body = body
		return in.build().ChangeHash
	}
	if a, b := build("Fix.<!-- v1 -->"), build("Fix.<!-- v2, entirely different text -->"); a != b {
		t.Errorf("an edit inside a hidden comment changed the hash: %s vs %s", a, b)
	}
	if a, b := build("Fix.\u200b"), build("Fix.\u200b\u200b\u200b"); a != b {
		t.Errorf("a longer invisible run changed the hash: %s vs %s", a, b)
	}
	if a, b := build("Fix."), build("Fix.<!-- v1 -->"); a == b {
		t.Errorf("hidden text appearing must change the hash (the post gains a marker)")
	}
}

// Failure prevented: an edit made only inside a hidden span of an inline
// comment's file path (invisible to people, and already stripped from the
// post) re-publishing the post. The path is hashed, so it has to be hashed
// after cleanup like every other text field.
func TestChangeHash_HiddenPathEditsAreInvisible(t *testing.T) {
	t.Parallel()
	build := func(path string) string {
		in := newPRInputs()
		in.inline[0].Path = path
		return in.build().ChangeHash
	}
	if a, b := build("docs/a.md<!-- v1 -->"), build("docs/a.md<!-- v2, entirely different text -->"); a != b {
		t.Errorf("an edit inside a hidden comment in the path changed the hash: %s vs %s", a, b)
	}
	if a, b := build("docs/a\u200b.md"), build("docs/a\u200b\u200b\u200b.md"); a != b {
		t.Errorf("a longer invisible run in the path changed the hash: %s vs %s", a, b)
	}
	if a, b := build("docs/a.md"), build("docs/a.md<!-- v1 -->"); a == b {
		t.Errorf("hidden text appearing in the path must change the hash (the post gains a marker)")
	}
}

// Failure prevented: a hash built by concatenation, where moving a character
// between adjacent fields collides.
func TestChangeHash_FieldBoundariesCannotShift(t *testing.T) {
	t.Parallel()
	base := Item{Kind: KindIssue, Number: 1, State: StateOpen}
	with := func(edit func(*Item)) string {
		it := base
		edit(&it)
		return ChangeHash(it)
	}
	tests := []struct {
		name string
		a, b func(*Item)
	}{
		{"title/body", func(it *Item) { it.Title, it.Body = "ab", "c" }, func(it *Item) { it.Title, it.Body = "a", "bc" }},
		{"labels", func(it *Item) { it.Labels = []string{"a", "b"} }, func(it *Item) { it.Labels = []string{"a,b"} }},
		{"labels vs one label", func(it *Item) { it.Labels = []string{"ab"} }, func(it *Item) { it.Labels = []string{"a", "b"} }},
		{"files", func(it *Item) { it.Files = []string{"a/b"} }, func(it *Item) { it.Files = []string{"a", "b"} }},
		{"body vs comment body", func(it *Item) { it.Body = "x" }, func(it *Item) { it.Comments = []Comment{{ID: 1, Body: "x"}} }},
		{"comment body/path", func(it *Item) { it.Comments = []Comment{{ID: 1, Body: "ab", Path: "c"}} }, func(it *Item) { it.Comments = []Comment{{ID: 1, Body: "a", Path: "bc"}} }},
		{"kind", func(it *Item) { it.Kind = KindIssue }, func(it *Item) { it.Kind = KindPullRequest }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if a, b := with(tt.a), with(tt.b); a == b {
				t.Errorf("distinct items hashed identically: %s", a)
			}
		})
	}
}

// Failure prevented: ChangeHash depending on anything outside the spec's field
// list. Each mutation touches a field the spec excludes on purpose.
func TestChangeHash_IgnoresExcludedFields(t *testing.T) {
	t.Parallel()
	built := newPRInputs().build()
	base := built.ChangeHash
	tests := []struct {
		name   string
		mutate func(*Item)
	}{
		{"UpdatedAt", func(it *Item) { it.UpdatedAt = it.UpdatedAt.Add(time.Hour) }},
		{"LastMaterialChangeAt", func(it *Item) { it.LastMaterialChangeAt = it.LastMaterialChangeAt.Add(240 * time.Hour) }},
		{"URL", func(it *Item) { it.URL = "https://example.invalid/x" }},
		{"Omitted", func(it *Item) { it.Omitted = Omitted{BotComments: 99, HiddenSpans: 99, FilesTruncated: true} }},
		{"CreatedAt", func(it *Item) { it.CreatedAt = it.CreatedAt.Add(-time.Hour) }},
		{"ClosedAt", func(it *Item) { it.ClosedAt = ptr(at(1)) }},
		{"MergedAt", func(it *Item) { it.MergedAt = ptr(at(1)) }},
		{"ChangeHash itself", func(it *Item) { it.ChangeHash = "sha256:stale" }},
		{"author login", func(it *Item) { it.Author.Login = "someone-else" }},
		{"comment login", func(it *Item) { it.Comments[0].Author.Login = "someone-else" }},
		{"comment CreatedAt", func(it *Item) { it.Comments[0].CreatedAt = it.Comments[0].CreatedAt.Add(time.Hour) }},
		{"comment UpdatedAt", func(it *Item) { it.Comments[0].UpdatedAt = it.Comments[0].UpdatedAt.Add(time.Hour) }},
		{"comment Line changed", func(it *Item) { it.Comments[0].Line = ptr(999) }},
		{"comment Line nulled", func(it *Item) { it.Comments[0].Line = nil }},
		{"review login", func(it *Item) { it.Reviews[0].Author.Login = "someone-else" }},
		{"review SubmittedAt", func(it *Item) { it.Reviews[0].SubmittedAt = it.Reviews[0].SubmittedAt.Add(time.Hour) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			it := cloneItem(built)
			tt.mutate(&it)
			if got := ChangeHash(it); got != base {
				t.Errorf("hash moved when %s changed: %s -> %s", tt.name, base, got)
			}
		})
	}
}

// Failure prevented: ChangeHash relying on Build's ordering. The server (or any
// other caller) may hash an item whose slices arrive in any order, and the
// caller's item must come back untouched.
func TestChangeHash_SortsCopiesAndNeverMutates(t *testing.T) {
	t.Parallel()
	built := newPRInputs().build()
	want := built.ChangeHash

	scrambled := cloneItem(built)
	slices.Reverse(scrambled.Labels)
	slices.Reverse(scrambled.Files)
	slices.Reverse(scrambled.Comments)
	slices.Reverse(scrambled.Reviews)
	snapshot := cloneItem(scrambled)

	if got := ChangeHash(scrambled); got != want {
		t.Errorf("reordered slices changed the hash: %s vs %s", got, want)
	}
	if !reflect.DeepEqual(scrambled, snapshot) {
		t.Errorf("ChangeHash mutated its argument:\n got  %+v\n want %+v", scrambled, snapshot)
	}
}

// Failure prevented: a nil list hashing differently from an empty one, so the
// same item hashes differently depending on how the fetcher built its slices.
func TestChangeHash_NilAndEmptyListsAgree(t *testing.T) {
	t.Parallel()
	nilLists := Item{Kind: KindIssue, Number: 3, State: StateOpen, Title: "t"}
	emptyLists := nilLists
	emptyLists.Labels, emptyLists.Reviews, emptyLists.Comments, emptyLists.Files = []string{}, []Review{}, []Comment{}, []string{}
	if a, b := ChangeHash(nilLists), ChangeHash(emptyLists); a != b {
		t.Errorf("nil vs empty lists: %s vs %s", a, b)
	}
}

// Failure prevented: a canonical form that is not canonical, e.g. keys in
// struct order, indentation, or a trailing newline, which a verifier in
// another language would not reproduce.
func TestCanonicalJSON_Shape(t *testing.T) {
	t.Parallel()
	built := newPRInputs().build()
	built.Body = "a < b && c > d"
	raw, err := canonicalJSON(built)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}

	if bytes.HasSuffix(raw, []byte("\n")) {
		t.Error("canonical JSON must not end in a newline")
	}
	if !bytes.Contains(raw, []byte(`"body":"a < b && c > d"`)) {
		t.Errorf("HTML characters must stay literal: %s", raw)
	}
	if bytes.ContainsAny(raw, "\n\t") || outsideStringsContainsSpace(raw) {
		t.Errorf("insignificant whitespace present: %s", raw)
	}
	assertKeysSorted(t, raw)

	wantKeys := []string{"author_id", "body", "comments", "draft", "files", "kind", "labels", "number", "reviews", "state", "title"}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	gotKeys := make([]string, 0, len(top))
	for k := range top {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("hashed fields = %v, want exactly %v", gotKeys, wantKeys)
	}

	var comments []map[string]json.RawMessage
	if err := json.Unmarshal(top["comments"], &comments); err != nil || len(comments) == 0 {
		t.Fatalf("comments = %s (err %v), want a non-empty array", top["comments"], err)
	}
	wantCommentKeys := []string{"author_id", "body", "id", "path"}
	for _, c := range comments {
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, wantCommentKeys) {
			t.Errorf("hashed comment fields = %v, want exactly %v (line is not hashed)", keys, wantCommentKeys)
		}
	}
}

// outsideStringsContainsSpace reports a space byte outside any JSON string.
func outsideStringsContainsSpace(raw []byte) bool {
	inString, escaped := false, false
	for _, b := range raw {
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		case !inString && b == ' ':
			return true
		}
	}
	return false
}

// assertKeysSorted walks the token stream and fails if any object's keys are
// not in ascending byte order.
func assertKeysSorted(t *testing.T, raw []byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		isObject bool
		last     string
		wantKey  bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, &frame{isObject: true, wantKey: true})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].isObject {
					stack[n-1].wantKey = true
				}
				continue
			}
		}
		if n := len(stack); n > 0 && stack[n-1].isObject {
			f := stack[n-1]
			if f.wantKey {
				key := tok.(string)
				if key < f.last {
					t.Errorf("object keys out of order: %q after %q", key, f.last)
				}
				f.last, f.wantKey = key, false
			} else {
				f.wantKey = true
			}
		}
	}
}

func cloneItem(it Item) Item {
	out := it
	out.Labels = slices.Clone(it.Labels)
	out.Files = slices.Clone(it.Files)
	out.Reviews = slices.Clone(it.Reviews)
	out.Comments = slices.Clone(it.Comments)
	for i, c := range out.Comments {
		if c.Line != nil {
			out.Comments[i].Line = ptr(*c.Line)
		}
	}
	return out
}

// Failure prevented: Go's default HTML escaping rewriting "<", ">" and "&" as
// hex escapes, which no other language's JSON encoder does by default, so the
// server could never reproduce the hash of an ordinary markdown body.
func TestChangeHash_HTMLEscapingIsOff(t *testing.T) {
	t.Parallel()
	it := Item{Kind: KindIssue, Number: 1, State: StateOpen, Body: "<script>&</script>"}
	raw, err := canonicalJSON(it)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if !strings.Contains(string(raw), `"body":"<script>&</script>"`) {
		t.Errorf("HTML characters must stay literal: %s", raw)
	}
}
