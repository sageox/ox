package githubmirror

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

const changeHashPrefix = "sha256:"

// ChangeHash is "sha256:<hex>" over the canonical JSON of the item's material
// fields (see the spec). It ignores UpdatedAt, LastMaterialChangeAt, URL and
// Omitted.
//
// It works on sorted copies and never mutates it, so any daemon that sees the
// same GitHub state computes the same hash regardless of the order GitHub
// returned things in. The server treats a repeated hash as a no-op, so this
// function is the contract between every teammate's daemon and the server.
func ChangeHash(it Item) string {
	canonical, err := canonicalJSON(it)
	if err != nil {
		// unreachable: the payload holds only strings, integers, bools and
		// nil. An empty hash fails the server's format check loudly rather
		// than colliding with a real one.
		return ""
	}
	sum := sha256.Sum256(canonical)
	return changeHashPrefix + hex.EncodeToString(sum[:])
}

// canonicalJSON is the exact byte string ChangeHash digests: object keys
// sorted (encoding/json sorts map keys), no insignificant whitespace, no
// trailing newline, HTML escaping off so "<" and "&" stay literal.
//
// Every key is always present: "draft" is false rather than absent and a
// comment with no path has "path":"". A verifier in another language therefore
// never has to decide what "absent" means.
//
// A comment's Line is deliberately not hashed. GitHub nulls it when a later
// push outdates the inline comment, which changes nothing a reader sees but
// would otherwise re-relay the post.
func canonicalJSON(it Item) ([]byte, error) {
	reviews := slices.Clone(it.Reviews)
	slices.SortFunc(reviews, func(a, b Review) int {
		return cmp.Or(cmp.Compare(a.Author.ID, b.Author.ID), cmp.Compare(a.State, b.State))
	})
	hashedReviews := make([]any, 0, len(reviews))
	for _, r := range reviews {
		hashedReviews = append(hashedReviews, map[string]any{
			"author_id": r.Author.ID,
			"state":     r.State,
		})
	}

	comments := slices.Clone(it.Comments)
	slices.SortFunc(comments, compareCommentIdentity)
	hashedComments := make([]any, 0, len(comments))
	for _, c := range comments {
		hashedComments = append(hashedComments, map[string]any{
			"id":        c.ID,
			"author_id": c.Author.ID,
			"body":      c.Body,
			"path":      c.Path,
		})
	}

	payload := map[string]any{
		"kind":      it.Kind,
		"number":    it.Number,
		"state":     it.State,
		"draft":     it.Draft,
		"title":     it.Title,
		"body":      it.Body,
		"author_id": it.Author.ID,
		"labels":    sortedStrings(it.Labels),
		"reviews":   hashedReviews,
		"comments":  hashedComments,
		"files":     sortedStrings(it.Files),
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// sortedStrings returns a sorted, non-nil copy so an absent list encodes as
// [] and the caller's slice keeps its order.
func sortedStrings(in []string) []string {
	out := append(make([]string, 0, len(in)), in...)
	slices.Sort(out)
	return out
}

// compareCommentIdentity orders comments by id. Inline review comments and
// conversation comments come from different GitHub id sequences, so two
// comments on one PR can share an id; the remaining keys keep the order (and
// therefore the hash) independent of the order GitHub returned them in. Line
// is a tiebreaker for Build's ordering of Item.Comments only; the hash ignores
// it, so two comments that differ only by line hash identically in either order.
func compareCommentIdentity(a, b Comment) int {
	return cmp.Or(
		cmp.Compare(a.ID, b.ID),
		cmp.Compare(a.Author.ID, b.Author.ID),
		strings.Compare(a.Path, b.Path),
		compareLines(a.Line, b.Line),
		strings.Compare(a.Body, b.Body),
	)
}

// compareLines orders nil before any line number.
func compareLines(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	default:
		return cmp.Compare(*a, *b)
	}
}
