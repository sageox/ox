package githubmirror

import (
	"cmp"
	"slices"
	"time"
)

// cleaner runs Cleanup and totals the spans it removes across every text field
// of one item, for Omitted.HiddenSpans.
type cleaner struct{ spans int }

func (c *cleaner) clean(s string) string {
	cleaned, removed := Cleanup(s)
	c.spans += removed
	return cleaned
}

// cleanAll cleans every string in ss. Labels and file paths are author
// controlled too and land in the post header, so they get the same treatment
// as the title.
func (c *cleaner) cleanAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = c.clean(s)
	}
	return out
}

// BuildPR turns fetched PR data into a relay Item: bot comments dropped and
// counted, title/body/comments/labels/file paths cleaned, reviews reduced to each reviewer's
// latest APPROVED / CHANGES_REQUESTED / DISMISSED decision, files capped at
// MaxFilesPerPR, LastMaterialChangeAt and ChangeHash computed.
//
// The repo is not used yet. It is in the signature so call sites do not change
// when it is, but nothing derived from it may enter the change hash: every
// teammate's daemon must hash the same GitHub state identically.
func BuildPR(_ Repo, pr SourcePR, conversation, inline []SourceComment, reviews []SourceReview, files []string, filesTruncated bool) Item {
	var c cleaner
	it := Item{
		Kind:      KindPullRequest,
		Number:    pr.Number,
		State:     pr.State,
		Draft:     pr.Draft,
		Title:     c.clean(pr.Title),
		Body:      c.clean(pr.Body),
		Author:    pr.Author,
		Labels:    sortedStrings(c.cleanAll(pr.Labels)),
		URL:       pr.HTMLURL,
		CreatedAt: pr.CreatedAt,
		ClosedAt:  cloneTime(pr.ClosedAt),
		MergedAt:  cloneTime(pr.MergedAt),
		UpdatedAt: pr.UpdatedAt,
	}
	if pr.MergedAt != nil {
		it.State = StateMerged
	}

	var botComments int
	it.Comments, botComments = humanComments(&c, conversation, inline)
	it.Reviews = latestReviews(reviews)

	// sort before capping so which 50 paths survive does not depend on the
	// order GitHub happened to return them in.
	it.Files = sortedStrings(c.cleanAll(files))
	if len(it.Files) > MaxFilesPerPR {
		it.Files = it.Files[:MaxFilesPerPR]
		filesTruncated = true
	}

	it.Omitted = Omitted{BotComments: botComments, HiddenSpans: c.spans, FilesTruncated: filesTruncated}
	return finishItem(it)
}

// BuildIssue is BuildPR for an issue.
func BuildIssue(_ Repo, issue SourceIssue, comments []SourceComment) Item {
	var c cleaner
	it := Item{
		Kind:      KindIssue,
		Number:    issue.Number,
		State:     issue.State,
		Title:     c.clean(issue.Title),
		Body:      c.clean(issue.Body),
		Author:    issue.Author,
		Labels:    sortedStrings(c.cleanAll(issue.Labels)),
		URL:       issue.HTMLURL,
		CreatedAt: issue.CreatedAt,
		ClosedAt:  cloneTime(issue.ClosedAt),
		UpdatedAt: issue.UpdatedAt,
		Reviews:   []Review{},
		Files:     []string{},
	}

	var botComments int
	it.Comments, botComments = humanComments(&c, comments)

	it.Omitted = Omitted{BotComments: botComments, HiddenSpans: c.spans}
	return finishItem(it)
}

// finishItem stamps the two derived fields every item needs once its content
// is final. The hash goes last because it covers the cleaned content.
func finishItem(it Item) Item {
	it.LastMaterialChangeAt = lastMaterialChange(it)
	it.ChangeHash = ChangeHash(it)
	return it
}

// humanComments merges the groups, drops bot comments (returning how many),
// cleans each kept body and orders the result by (CreatedAt, ID). The result is
// never nil so it encodes as [].
func humanComments(c *cleaner, groups ...[]SourceComment) (kept []Comment, bots int) {
	kept = []Comment{}
	for _, group := range groups {
		for _, sc := range group {
			if IsBot(sc.Author) {
				bots++
				continue
			}
			var line *int
			if sc.Line != nil {
				n := *sc.Line
				line = &n
			}
			kept = append(kept, Comment{
				ID:        sc.ID,
				Author:    sc.Author,
				Body:      c.clean(sc.Body),
				CreatedAt: sc.CreatedAt,
				UpdatedAt: sc.UpdatedAt,
				Path:      sc.Path,
				Line:      line,
			})
		}
	}
	slices.SortFunc(kept, func(a, b Comment) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), compareCommentIdentity(a, b))
	})
	return kept, bots
}

// isDecision reports whether a review state is a verdict. COMMENTED and PENDING
// are not: a drive-by remark must not erase an earlier approval, and an
// unsubmitted draft is not something the reviewer has said yet.
func isDecision(state string) bool {
	switch state {
	case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		return true
	}
	return false
}

// latestReviews keeps each human reviewer's most recent verdict, ordered by
// reviewer id. Reviewers are told apart by numeric id, never by login.
func latestReviews(src []SourceReview) []Review {
	latest := make(map[int64]SourceReview, len(src))
	for _, r := range src {
		if IsBot(r.Author) || !isDecision(r.State) {
			continue
		}
		cur, seen := latest[r.Author.ID]
		// equal timestamps fall back to the review id so the winner does not
		// depend on input order.
		if !seen || r.SubmittedAt.After(cur.SubmittedAt) || (r.SubmittedAt.Equal(cur.SubmittedAt) && r.ID > cur.ID) {
			latest[r.Author.ID] = r
		}
	}

	out := make([]Review, 0, len(latest))
	for _, r := range latest {
		out = append(out, Review{Author: r.Author, State: r.State, SubmittedAt: r.SubmittedAt})
	}
	slices.SortFunc(out, func(a, b Review) int { return cmp.Compare(a.Author.ID, b.Author.ID) })
	return out
}

// lastMaterialChange is the latest of the creation, close and merge times, each
// kept human comment's created/updated times and each kept review's submit
// time. GitHub's own updated_at is deliberately absent: bots, reactions and
// label churn move it without changing anything a reader would care about.
// Title and body edits carry no timestamp, so they cannot advance this.
func lastMaterialChange(it Item) time.Time {
	latest := it.CreatedAt
	consider := func(t time.Time) {
		if t.After(latest) {
			latest = t
		}
	}
	if it.ClosedAt != nil {
		consider(*it.ClosedAt)
	}
	if it.MergedAt != nil {
		consider(*it.MergedAt)
	}
	for _, c := range it.Comments {
		consider(c.CreatedAt)
		consider(c.UpdatedAt)
	}
	for _, r := range it.Reviews {
		consider(r.SubmittedAt)
	}
	return latest
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// ExpiresAt is LastMaterialChangeAt + Window.
func ExpiresAt(it Item) time.Time { return it.LastMaterialChangeAt.Add(Window) }

// Expired reports whether the item's post would already have expired at now.
// The instant of expiry itself counts as expired.
func Expired(it Item, now time.Time) bool { return !now.Before(ExpiresAt(it)) }
