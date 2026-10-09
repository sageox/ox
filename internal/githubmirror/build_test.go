package githubmirror

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

var (
	testRepo = Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 4242}

	baseTime = time.Date(2026, 9, 28, 17, 2, 11, 0, time.UTC)

	// anonymized accounts; ids are what the mirror trusts, logins are display.
	authorDevon = Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"}
	authorAvery = Author{Login: "avery-dev", ID: 5550102, Association: "COLLABORATOR", Type: "User"}
	authorRiley = Author{Login: "drive-by-user", ID: 5550103, Association: "NONE", Type: "User"}
	authorCarol = Author{Login: "carol-dev", ID: 5550104, Association: "OWNER", Type: "User"}
	botByType   = Author{Login: "renovate", ID: 5550900, Association: "NONE", Type: "Bot"}
	botBySuffix = Author{Login: "ci-helper[bot]", ID: 5550901, Association: "NONE", Type: "User"}
)

// at is baseTime plus h hours, so fixtures read as a timeline.
func at(h int) time.Time { return baseTime.Add(time.Duration(h) * time.Hour) }

func ptr[T any](v T) *T { return &v }

// prInputs is everything BuildPR takes, so a test changes one thing and
// rebuilds.
type prInputs struct {
	pr             SourcePR
	conversation   []SourceComment
	inline         []SourceComment
	reviews        []SourceReview
	files          []string
	filesTruncated bool
}

func (in prInputs) build() Item {
	return BuildPR(testRepo, in.pr, in.conversation, in.inline, in.reviews, in.files, in.filesTruncated)
}

// newPRInputs returns a fresh, fully populated open PR. Every call builds new
// slices, so a test may mutate its copy freely.
func newPRInputs() prInputs {
	return prInputs{
		pr: SourcePR{
			Number:    1287,
			Title:     "Mirror GitHub activity onto the bulletin board",
			Body:      "Relays PRs and issues.\n\nSee the spec.",
			State:     "open",
			Author:    authorDevon,
			Labels:    []string{"github", "daemon"},
			CreatedAt: at(0),
			UpdatedAt: at(50),
			HTMLURL:   "https://github.com/acme/api/pull/1287",
		},
		conversation: []SourceComment{
			{ID: 1001, Author: authorAvery, Body: "Looks good overall.", CreatedAt: at(2), UpdatedAt: at(2)},
			{ID: 1002, Author: botByType, Body: "Coverage is 85%.", CreatedAt: at(3), UpdatedAt: at(3)},
			{ID: 1003, Author: authorRiley, Body: "Can this support GitLab?", CreatedAt: at(4), UpdatedAt: at(4)},
		},
		inline: []SourceComment{
			{ID: 2001, Author: authorAvery, Body: "nit: rename this", CreatedAt: at(1), UpdatedAt: at(1), Path: "internal/daemon/github_sync.go", Line: ptr(42)},
		},
		reviews: []SourceReview{
			{ID: 3001, Author: authorAvery, State: "APPROVED", SubmittedAt: at(5)},
			{ID: 3002, Author: authorCarol, State: "COMMENTED", SubmittedAt: at(6)},
		},
		files: []string{"internal/daemon/github_sync.go", "internal/githubmirror/build.go", "docs/specs/github-bulletin-mirror.md"},
	}
}

// Failure prevented: the end-to-end Build contract drifting piecemeal, e.g.
// bot noise or hidden text reaching the relay, or reviews reported stale.
func TestBuildPR_EndToEnd(t *testing.T) {
	t.Parallel()
	closedAt, mergedAt := at(30), at(30)
	in := prInputs{
		pr: SourcePR{
			Number:    7,
			Title:     "Fix\u200b retry",
			Body:      "Body<!-- hidden -->text",
			State:     "closed",
			Draft:     false,
			Author:    authorDevon,
			Labels:    []string{"b", "a"},
			CreatedAt: at(0),
			UpdatedAt: at(99),
			ClosedAt:  &closedAt,
			MergedAt:  &mergedAt,
			HTMLURL:   "https://github.com/acme/api/pull/7",
		},
		conversation: []SourceComment{
			{ID: 12, Author: authorAvery, Body: "ok\u200b", CreatedAt: at(10), UpdatedAt: at(12)},
			{ID: 13, Author: botByType, Body: "<!-- tracking -->coverage", CreatedAt: at(40), UpdatedAt: at(40)},
		},
		inline: []SourceComment{
			{ID: 11, Author: authorRiley, Body: "why?", CreatedAt: at(5), UpdatedAt: at(5), Path: "a.go", Line: ptr(17)},
			{ID: 14, Author: botBySuffix, Body: "lint", CreatedAt: at(41), UpdatedAt: at(41), Path: "a.go", Line: ptr(1)},
		},
		reviews: []SourceReview{
			{ID: 1, Author: authorAvery, State: "APPROVED", SubmittedAt: at(20)},
			{ID: 2, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(25)},
			{ID: 3, Author: authorAvery, State: "COMMENTED", SubmittedAt: at(35)},
			{ID: 4, Author: authorCarol, State: "DISMISSED", SubmittedAt: at(22)},
			{ID: 5, Author: authorRiley, State: "COMMENTED", SubmittedAt: at(23)},
			{ID: 6, Author: authorDevon, State: "PENDING", SubmittedAt: at(24)},
			{ID: 7, Author: botByType, State: "APPROVED", SubmittedAt: at(45)},
		},
		files: []string{"b.go", "a.go"},
	}

	it := in.build()

	if it.Kind != KindPullRequest || it.Number != 7 {
		t.Errorf("identity = (%q, %d), want (%q, 7)", it.Kind, it.Number, KindPullRequest)
	}
	if it.State != StateMerged {
		t.Errorf("State = %q, want %q (closed PR with a merge time)", it.State, StateMerged)
	}
	if it.URL != "https://github.com/acme/api/pull/7" {
		t.Errorf("URL = %q", it.URL)
	}
	if it.Title != "Fix"+marker+" retry" || it.Body != "Body"+marker+"text" {
		t.Errorf("title/body not cleaned: %q / %q", it.Title, it.Body)
	}
	if want := []string{"a", "b"}; !slices.Equal(it.Labels, want) {
		t.Errorf("Labels = %v, want %v", it.Labels, want)
	}
	if want := []string{"a.go", "b.go"}; !slices.Equal(it.Files, want) {
		t.Errorf("Files = %v, want %v", it.Files, want)
	}

	// only the two human comments survive, in (created_at, id) order, cleaned
	if ids := commentIDs(it.Comments); !slices.Equal(ids, []int64{11, 12}) {
		t.Fatalf("comment ids = %v, want [11 12] (bots dropped, inline merged, time order)", ids)
	}
	if got := it.Comments[1].Body; got != "ok"+marker {
		t.Errorf("comment body not cleaned: %q", got)
	}
	if c := it.Comments[0]; c.Path != "a.go" || c.Line == nil || *c.Line != 17 {
		t.Errorf("inline comment lost its location: %+v", c)
	}

	// reviews: avery's latest verdict (CHANGES_REQUESTED, not the later
	// COMMENTED), carol's DISMISSED; riley (only COMMENTED), devon (PENDING)
	// and the bot are absent; sorted by reviewer id.
	wantReviews := []Review{
		{Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(25)},
		{Author: authorCarol, State: "DISMISSED", SubmittedAt: at(22)},
	}
	if !reflect.DeepEqual(it.Reviews, wantReviews) {
		t.Errorf("Reviews = %+v\nwant     %+v", it.Reviews, wantReviews)
	}

	// 3 hidden spans: title, body, avery's comment. The bot comment's own
	// hidden span is not counted because that comment never ships.
	wantOmitted := Omitted{BotComments: 2, HiddenSpans: 3, FilesTruncated: false}
	if it.Omitted != wantOmitted {
		t.Errorf("Omitted = %+v, want %+v", it.Omitted, wantOmitted)
	}

	// latest of created/closed/merged/comment times/review times; GitHub's
	// updated_at (99h) and the bot activity (40h, 41h, 45h) must not count.
	if !it.LastMaterialChangeAt.Equal(at(30)) {
		t.Errorf("LastMaterialChangeAt = %v, want %v", it.LastMaterialChangeAt, at(30))
	}
	if !it.UpdatedAt.Equal(at(99)) {
		t.Errorf("UpdatedAt = %v, want GitHub's %v passed through", it.UpdatedAt, at(99))
	}
	if it.ChangeHash != ChangeHash(it) || !changeHashPattern.MatchString(it.ChangeHash) {
		t.Errorf("ChangeHash = %q, want the hash of the built item", it.ChangeHash)
	}
}

func commentIDs(cs []Comment) []int64 {
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}

// Failure prevented: a PR with no labels/comments/reviews/files relaying JSON
// null instead of [], which the server's schema rejects.
func TestBuild_NilSlicesEncodeAsEmptyArrays(t *testing.T) {
	t.Parallel()
	items := map[string]Item{
		"pr":    BuildPR(testRepo, SourcePR{Number: 1, State: "open", CreatedAt: at(0)}, nil, nil, nil, nil, false),
		"issue": BuildIssue(testRepo, SourceIssue{Number: 2, State: "open", CreatedAt: at(0)}, nil),
	}
	for name, it := range items {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(it)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, key := range []string{"labels", "reviews", "comments", "files"} {
				if got := string(fields[key]); got != "[]" {
					t.Errorf("%s = %s, want []", key, got)
				}
			}
		})
	}
}

// Failure prevented: the wrong cap, or a truncated PR that does not say so
// (the post would present a partial file list as complete).
func TestBuildPR_Files(t *testing.T) {
	t.Parallel()
	numbered := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("pkg/file%03d.go", i)
		}
		return out
	}
	tests := []struct {
		name          string
		files         []string
		flag          bool
		wantLen       int
		wantTruncated bool
	}{
		{"none", nil, false, 0, false},
		{"under the cap", numbered(3), false, 3, false},
		{"exactly the cap", numbered(MaxFilesPerPR), false, MaxFilesPerPR, false},
		{"one over the cap", numbered(MaxFilesPerPR + 1), false, MaxFilesPerPR, true},
		{"far over the cap", numbered(500), false, MaxFilesPerPR, true},
		{"fetcher already truncated", numbered(MaxFilesPerPR), true, MaxFilesPerPR, true},
		{"fetcher flag with few files", numbered(2), true, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newPRInputs()
			in.files, in.filesTruncated = tt.files, tt.flag
			it := in.build()
			if len(it.Files) != tt.wantLen || it.Omitted.FilesTruncated != tt.wantTruncated {
				t.Errorf("len(Files)=%d truncated=%v, want %d %v", len(it.Files), it.Omitted.FilesTruncated, tt.wantLen, tt.wantTruncated)
			}
		})
	}

	t.Run("cap keeps the alphabetically first paths whatever order they arrive in", func(t *testing.T) {
		t.Parallel()
		files := numbered(80)
		slices.Reverse(files)
		in := newPRInputs()
		in.files = files
		it := in.build()
		if it.Files[0] != "pkg/file000.go" || it.Files[MaxFilesPerPR-1] != "pkg/file049.go" {
			t.Errorf("kept %q..%q, want file000..file049", it.Files[0], it.Files[MaxFilesPerPR-1])
		}
	})
}

// Failure prevented: a merged PR shown as merely closed (or an open PR shown
// as merged), which changes how an AI coworker reads its relevance.
func TestBuildPR_State(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		state    string
		closedAt *time.Time
		mergedAt *time.Time
		draft    bool
		want     string
	}{
		{"open", "open", nil, nil, false, StateOpen},
		{"open draft", "open", nil, nil, true, StateOpen},
		{"closed without merge", "closed", ptr(at(3)), nil, false, StateClosed},
		{"closed with merge time", "closed", ptr(at(3)), ptr(at(3)), false, StateMerged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newPRInputs()
			in.pr.State, in.pr.ClosedAt, in.pr.MergedAt, in.pr.Draft = tt.state, tt.closedAt, tt.mergedAt, tt.draft
			it := in.build()
			if it.State != tt.want || it.Draft != tt.draft {
				t.Errorf("State=%q Draft=%v, want %q %v", it.State, it.Draft, tt.want, tt.draft)
			}
		})
	}
}

// Failure prevented: a post that expires too early (a recent human comment
// not counted) or never (bot churn or GitHub's updated_at keeping it alive).
func TestBuildPR_LastMaterialChange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*prInputs)
		want   time.Time
	}{
		{"baseline is the latest human activity in the fixture", func(*prInputs) {}, at(5)},
		{"creation only", func(in *prInputs) {
			in.conversation, in.inline, in.reviews = nil, nil, nil
		}, at(0)},
		{"closed later", func(in *prInputs) { in.pr.ClosedAt = ptr(at(10)) }, at(10)},
		{"merged after closed", func(in *prInputs) {
			in.pr.ClosedAt, in.pr.MergedAt = ptr(at(10)), ptr(at(12))
		}, at(12)},
		{"new human comment", func(in *prInputs) {
			in.conversation = append(in.conversation, SourceComment{ID: 1100, Author: authorAvery, Body: "late", CreatedAt: at(20), UpdatedAt: at(20)})
		}, at(20)},
		{"human comment edited later", func(in *prInputs) {
			in.conversation[0].UpdatedAt = at(30)
		}, at(30)},
		{"new inline comment", func(in *prInputs) {
			in.inline = append(in.inline, SourceComment{ID: 2100, Author: authorRiley, Body: "x", CreatedAt: at(25), UpdatedAt: at(25), Path: "a.go", Line: ptr(1)})
		}, at(25)},
		{"new review decision", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3100, Author: authorCarol, State: "APPROVED", SubmittedAt: at(40)})
		}, at(40)},
		{"commented-only review does not count", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3101, Author: authorRiley, State: "COMMENTED", SubmittedAt: at(60)})
		}, at(5)},
		{"pending review does not count", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3102, Author: authorRiley, State: "PENDING", SubmittedAt: at(61)})
		}, at(5)},
		{"bot comment does not count", func(in *prInputs) {
			in.conversation = append(in.conversation, SourceComment{ID: 1101, Author: botByType, Body: "x", CreatedAt: at(70), UpdatedAt: at(70)})
		}, at(5)},
		{"bot review does not count", func(in *prInputs) {
			in.reviews = append(in.reviews, SourceReview{ID: 3103, Author: botBySuffix, State: "APPROVED", SubmittedAt: at(71)})
		}, at(5)},
		{"github updated_at does not count", func(in *prInputs) { in.pr.UpdatedAt = at(500) }, at(5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newPRInputs()
			tt.mutate(&in)
			if got := in.build().LastMaterialChangeAt; !got.Equal(tt.want) {
				t.Errorf("LastMaterialChangeAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// Failure prevented: a reviewer's old verdict or a bot's verdict shown as the
// PR's review state.
func TestBuildPR_ReviewReduction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reviews []SourceReview
		want    map[int64]string // reviewer id -> state, expected in ascending id order
	}{
		{"none", nil, map[int64]string{}},
		{"latest verdict wins over earlier", []SourceReview{
			{ID: 1, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(1)},
			{ID: 2, Author: authorAvery, State: "APPROVED", SubmittedAt: at(2)},
		}, map[int64]string{authorAvery.ID: "APPROVED"}},
		{"order of arrival does not matter", []SourceReview{
			{ID: 2, Author: authorAvery, State: "APPROVED", SubmittedAt: at(2)},
			{ID: 1, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(1)},
		}, map[int64]string{authorAvery.ID: "APPROVED"}},
		{"later COMMENTED does not erase an approval", []SourceReview{
			{ID: 1, Author: authorAvery, State: "APPROVED", SubmittedAt: at(1)},
			{ID: 2, Author: authorAvery, State: "COMMENTED", SubmittedAt: at(9)},
		}, map[int64]string{authorAvery.ID: "APPROVED"}},
		{"later PENDING does not erase a request for changes", []SourceReview{
			{ID: 1, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(1)},
			{ID: 2, Author: authorAvery, State: "PENDING", SubmittedAt: at(9)},
		}, map[int64]string{authorAvery.ID: "CHANGES_REQUESTED"}},
		{"dismissed is a verdict", []SourceReview{
			{ID: 1, Author: authorAvery, State: "APPROVED", SubmittedAt: at(1)},
			{ID: 2, Author: authorAvery, State: "DISMISSED", SubmittedAt: at(2)},
		}, map[int64]string{authorAvery.ID: "DISMISSED"}},
		{"identical timestamps resolve by review id", []SourceReview{
			{ID: 9, Author: authorAvery, State: "CHANGES_REQUESTED", SubmittedAt: at(1)},
			{ID: 8, Author: authorAvery, State: "APPROVED", SubmittedAt: at(1)},
		}, map[int64]string{authorAvery.ID: "CHANGES_REQUESTED"}},
		{"reviewers are told apart by id, not login", []SourceReview{
			{ID: 1, Author: Author{Login: "same-name", ID: 11, Association: "MEMBER"}, State: "APPROVED", SubmittedAt: at(1)},
			{ID: 2, Author: Author{Login: "same-name", ID: 12, Association: "MEMBER"}, State: "CHANGES_REQUESTED", SubmittedAt: at(2)},
		}, map[int64]string{11: "APPROVED", 12: "CHANGES_REQUESTED"}},
		{"bot reviewers are dropped", []SourceReview{
			{ID: 1, Author: botByType, State: "APPROVED", SubmittedAt: at(1)},
			{ID: 2, Author: botBySuffix, State: "CHANGES_REQUESTED", SubmittedAt: at(2)},
		}, map[int64]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := newPRInputs()
			in.reviews = tt.reviews
			got := map[int64]string{}
			var prev int64
			for _, r := range in.build().Reviews {
				if r.Author.ID < prev {
					t.Errorf("reviews not sorted by reviewer id: %d after %d", r.Author.ID, prev)
				}
				prev = r.Author.ID
				got[r.Author.ID] = r.State
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reviews = %v, want %v", got, tt.want)
			}
		})
	}
}

// Failure prevented: bot comments leaking into the relay, or being dropped
// without being counted (the post could not say how much was left out).
func TestBuild_BotComments(t *testing.T) {
	t.Parallel()
	in := newPRInputs()
	in.conversation = append(in.conversation,
		SourceComment{ID: 1200, Author: botBySuffix, Body: "suffix bot", CreatedAt: at(8), UpdatedAt: at(8)},
		SourceComment{ID: 1201, Author: Author{Login: "x", ID: 77, Type: "bot", Association: "MEMBER"}, Body: "type bot, member association", CreatedAt: at(9), UpdatedAt: at(9)},
	)
	in.inline = append(in.inline,
		SourceComment{ID: 2200, Author: botByType, Body: "inline bot", CreatedAt: at(8), UpdatedAt: at(8), Path: "a.go", Line: ptr(3)})
	it := in.build()

	if it.Omitted.BotComments != 4 {
		t.Errorf("BotComments = %d, want 4 (1 baseline + 2 conversation + 1 inline)", it.Omitted.BotComments)
	}
	for _, c := range it.Comments {
		if IsBot(c.Author) {
			t.Errorf("bot comment %d reached the relay", c.ID)
		}
	}
	if len(it.Comments) != 3 {
		t.Errorf("kept %d comments, want the 3 human ones", len(it.Comments))
	}
}

// Failure prevented: hidden text in an item the bot (or a human) authored
// reaching the relay because only one of title/body/comments was cleaned.
func TestBuild_HiddenSpansCoverEveryTextField(t *testing.T) {
	t.Parallel()
	in := newPRInputs()
	in.pr.Title = "t<!--1-->"
	in.pr.Body = "b<!--2-->mid\u200b"
	in.conversation = []SourceComment{{ID: 1, Author: authorAvery, Body: "c<!--3-->", CreatedAt: at(1), UpdatedAt: at(1)}}
	in.inline = []SourceComment{{ID: 2, Author: authorRiley, Body: "\u202ei", CreatedAt: at(2), UpdatedAt: at(2), Path: "a.go", Line: ptr(1)}}
	it := in.build()
	if it.Omitted.HiddenSpans != 5 {
		t.Errorf("HiddenSpans = %d, want 5 (title 1, body 2, conversation 1, inline 1)", it.Omitted.HiddenSpans)
	}
	for _, text := range append([]string{it.Title, it.Body}, it.Comments[0].Body, it.Comments[1].Body) {
		if cleaned, n := Cleanup(text); cleaned != text || n != 0 {
			t.Errorf("relayed text still has hidden content: %q", text)
		}
	}
}

// Failure prevented: Build depending on the order GitHub returned things in,
// so two teammates' daemons relay different bytes for the same PR.
func TestBuildPR_IndependentOfInputOrder(t *testing.T) {
	t.Parallel()
	in := newPRInputs()
	// two comments sharing an id and a timestamp: inline review comments and
	// conversation comments come from different id sequences.
	in.conversation = append(in.conversation, SourceComment{ID: 5000, Author: authorAvery, Body: "conversation", CreatedAt: at(7), UpdatedAt: at(7)})
	in.inline = append(in.inline, SourceComment{ID: 5000, Author: authorAvery, Body: "inline", CreatedAt: at(7), UpdatedAt: at(7), Path: "a.go", Line: ptr(9)})
	in.reviews = append(in.reviews,
		SourceReview{ID: 3200, Author: authorCarol, State: "APPROVED", SubmittedAt: at(8)},
		SourceReview{ID: 3201, Author: authorRiley, State: "CHANGES_REQUESTED", SubmittedAt: at(8)})
	want := in.build()

	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 100 {
		shuffled := in
		shuffled.conversation = shuffleCopy(rng, in.conversation)
		shuffled.inline = shuffleCopy(rng, in.inline)
		shuffled.reviews = shuffleCopy(rng, in.reviews)
		shuffled.files = shuffleCopy(rng, in.files)
		shuffled.pr.Labels = shuffleCopy(rng, in.pr.Labels)
		if got := shuffled.build(); !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d changed the built item:\n got  %+v\n want %+v", i, got, want)
		}
	}
}

func shuffleCopy[T any](rng *rand.Rand, in []T) []T {
	out := slices.Clone(in)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Failure prevented: Build sorting the caller's slices in place or handing out
// pointers into its input, so a later mutation of fetched data silently
// rewrites an already-built (and already-hashed) item.
func TestBuildPR_DoesNotAliasOrMutateInput(t *testing.T) {
	t.Parallel()
	in := newPRInputs()
	in.pr.ClosedAt = ptr(at(10))
	pristine := newPRInputs()
	pristine.pr.ClosedAt = ptr(at(10))

	it := in.build()

	if !reflect.DeepEqual(in, pristine) {
		t.Fatalf("BuildPR mutated its inputs:\n got  %+v\n want %+v", in, pristine)
	}

	// mutate the source after building; the item must not move
	hash := it.ChangeHash
	*in.inline[0].Line = 9999
	*in.pr.ClosedAt = at(900)
	in.pr.Labels[0] = "mutated"
	in.files[0] = "mutated"
	if got := ChangeHash(it); got != hash {
		t.Errorf("item changed after the source was mutated: hash %s -> %s", hash, got)
	}
	if it.Comments[0].Line == nil || *it.Comments[0].Line != 42 {
		t.Errorf("item's comment line aliases the source: %v", it.Comments[0].Line)
	}
	if !it.ClosedAt.Equal(at(10)) {
		t.Errorf("item's ClosedAt aliases the source: %v", it.ClosedAt)
	}
}

func TestBuildIssue(t *testing.T) {
	t.Parallel()
	issue := SourceIssue{
		Number:    1271,
		Title:     "Daemon <!-- x -->stalls",
		Body:      "It hangs.\u200b",
		State:     "closed",
		Author:    authorRiley,
		Labels:    []string{"bug", "daemon"},
		CreatedAt: at(0),
		UpdatedAt: at(80),
		ClosedAt:  ptr(at(20)),
		HTMLURL:   "https://github.com/acme/api/issues/1271",
	}
	comments := []SourceComment{
		{ID: 22, Author: authorAvery, Body: "fixed in a patch", CreatedAt: at(15), UpdatedAt: at(18)},
		{ID: 21, Author: botByType, Body: "stale bot", CreatedAt: at(60), UpdatedAt: at(60)},
		{ID: 20, Author: authorDevon, Body: "repro\u200b", CreatedAt: at(2), UpdatedAt: at(2)},
	}
	it := BuildIssue(testRepo, issue, comments)

	if it.Kind != KindIssue || it.Number != 1271 || it.State != StateClosed {
		t.Errorf("identity/state = (%q, %d, %q)", it.Kind, it.Number, it.State)
	}
	if it.URL != "https://github.com/acme/api/issues/1271" || !slices.Equal(it.Labels, []string{"bug", "daemon"}) {
		t.Errorf("URL/Labels = %q %v", it.URL, it.Labels)
	}
	if it.Title != "Daemon "+marker+"stalls" || it.Body != "It hangs."+marker {
		t.Errorf("title/body not cleaned: %q / %q", it.Title, it.Body)
	}
	if ids := commentIDs(it.Comments); !slices.Equal(ids, []int64{20, 22}) {
		t.Errorf("comment ids = %v, want [20 22]", ids)
	}
	if it.Comments[0].Body != "repro"+marker {
		t.Errorf("comment not cleaned: %q", it.Comments[0].Body)
	}
	if want := (Omitted{BotComments: 1, HiddenSpans: 3}); it.Omitted != want {
		t.Errorf("Omitted = %+v, want %+v", it.Omitted, want)
	}
	if len(it.Reviews) != 0 || len(it.Files) != 0 || it.Draft || it.MergedAt != nil {
		t.Errorf("issue must carry no reviews/files/draft/merge: %+v", it)
	}
	if !it.LastMaterialChangeAt.Equal(at(20)) {
		t.Errorf("LastMaterialChangeAt = %v, want the close time %v (not updated_at, not the bot)", it.LastMaterialChangeAt, at(20))
	}
	if it.ChangeHash != ChangeHash(it) {
		t.Errorf("ChangeHash not stamped")
	}
}

func TestBuildIssue_OpenStateAndEditedComment(t *testing.T) {
	t.Parallel()
	issue := SourceIssue{Number: 5, State: "open", Author: authorRiley, CreatedAt: at(0), UpdatedAt: at(1)}
	it := BuildIssue(testRepo, issue, []SourceComment{
		{ID: 1, Author: authorAvery, Body: "x", CreatedAt: at(3), UpdatedAt: at(9)},
	})
	if it.State != StateOpen {
		t.Errorf("State = %q, want open", it.State)
	}
	if !it.LastMaterialChangeAt.Equal(at(9)) {
		t.Errorf("LastMaterialChangeAt = %v, want the comment's edit time %v", it.LastMaterialChangeAt, at(9))
	}
}

// Failure prevented: an off-by-one at the 90-day boundary either republishes a
// post the server already expired or drops one with a day left.
func TestExpiry(t *testing.T) {
	t.Parallel()
	it := Item{LastMaterialChangeAt: at(0)}
	expiry := at(0).Add(90 * 24 * time.Hour)

	if got := ExpiresAt(it); !got.Equal(expiry) {
		t.Fatalf("ExpiresAt = %v, want %v", got, expiry)
	}
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"right after the change", at(0), false},
		{"one day in", at(24), false},
		{"one nanosecond before expiry", expiry.Add(-time.Nanosecond), false},
		{"the instant of expiry", expiry, true},
		{"one nanosecond after expiry", expiry.Add(time.Nanosecond), true},
		{"long after", expiry.Add(400 * 24 * time.Hour), true},
		{"before the change itself", at(-48), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Expired(it, tt.now); got != tt.want {
				t.Errorf("Expired(now=%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}
