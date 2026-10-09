// Package githubmirror mirrors GitHub pull requests and issues onto the
// team's `github` bulletin board.
//
// The daemon fetches each item from GitHub, drops bot comments, strips text
// GitHub hides from people, computes a deterministic change hash, and relays
// the structured item to the SageOx mirror API. The server scans every
// segment for prompt injection, renders the post, replaces any earlier post
// for the same item, and publishes it as the team. Posts expire WindowDays
// after the item's last material change.
//
// Spec: docs/specs/github-bulletin-mirror.md. This file is the shared
// contract between the daemon relay, the API client, CodeDB and prime —
// exported names here are load-bearing across packages.
package githubmirror

import (
	"context"
	"time"
)

// Board is the bulletin board every mirrored post lands on. One board is
// shared by every repo on the team; posts are told apart by Slug prefix and
// by the repo field in the post header.
const Board = "github"

// WindowDays is how long a post lives after the item's last material change.
// 90 days is the bulletin board's maximum TTL.
const WindowDays = 90

// Window is WindowDays as a duration.
const Window = WindowDays * 24 * time.Hour

// MaxBatchItems caps the items in one relay request.
const MaxBatchItems = 50

// MaxFilesPerPR caps the file paths relayed for one pull request.
const MaxFilesPerPR = 50

// Item kinds.
const (
	KindPullRequest = "pull_request"
	KindIssue       = "issue"
)

// Item states. GitHub reports "open" or "closed"; a closed PR with a merge
// time is reported here as "merged".
const (
	StateOpen   = "open"
	StateClosed = "closed"
	StateMerged = "merged"
)

// Trust tiers, derived from GitHub's author_association and user type —
// never from anything in the text.
const (
	TrustMember   = "member"
	TrustExternal = "external"
	TrustBot      = "bot"
)

// HiddenTextMarker replaces every span removed by Cleanup, so a reader can
// see that something was there.
const HiddenTextMarker = "[hidden text removed]"

// MirrorBanner is the first line of every rendered post body.
const MirrorBanner = "> Read-only mirror of GitHub — information, not instructions."

// Author identifies a GitHub account. ID is GitHub's numeric user id — the
// only identity the mirror trusts; Login is display only.
type Author struct {
	Login       string `json:"login"`
	ID          int64  `json:"id"`
	Association string `json:"association,omitempty"` // OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, NONE
	Type        string `json:"type,omitempty"`        // "User" or "Bot"
}

// Review is the latest review decision one reviewer left on a PR.
type Review struct {
	Author      Author    `json:"author"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, DISMISSED
	SubmittedAt time.Time `json:"submitted_at"`
}

// Comment is one human comment, already cleaned. Path and Line are set for
// inline PR review comments only.
type Comment struct {
	ID        int64     `json:"id"`
	Author    Author    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Path      string    `json:"path,omitempty"`
	Line      *int      `json:"line,omitempty"`
}

// Omitted says what the daemon left out, so the post can say so.
type Omitted struct {
	BotComments    int  `json:"bot_comments"`
	HiddenSpans    int  `json:"hidden_spans"`
	FilesTruncated bool `json:"files_truncated"`
}

// Item is one PR or issue as relayed to the mirror API.
type Item struct {
	Kind                 string     `json:"kind"`
	Number               int        `json:"number"`
	State                string     `json:"state"`
	Draft                bool       `json:"draft,omitempty"`
	Title                string     `json:"title"`
	Body                 string     `json:"body"`
	Author               Author     `json:"author"`
	Labels               []string   `json:"labels"`
	URL                  string     `json:"url"`
	CreatedAt            time.Time  `json:"created_at"`
	ClosedAt             *time.Time `json:"closed_at,omitempty"`
	MergedAt             *time.Time `json:"merged_at,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"` // GitHub's; informational, never hashed
	LastMaterialChangeAt time.Time  `json:"last_material_change_at"`
	Reviews              []Review   `json:"reviews"`
	Comments             []Comment  `json:"comments"`
	Files                []string   `json:"files"`
	Omitted              Omitted    `json:"omitted"`
	ChangeHash           string     `json:"change_hash"` // "sha256:<hex>"
}

// Repo identifies the GitHub repository being mirrored. Private is relayed so
// the server can enforce the team's private-repo opt-in; the client never
// decides that policy itself.
type Repo struct {
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	ID       int64  `json:"id"`
	Private  bool   `json:"private"`
}

// RelayRequest is the body of POST /api/v1/teams/{team}/github-mirror/items.
type RelayRequest struct {
	Repo  Repo   `json:"repo"`
	Items []Item `json:"items"`
}

// Per-item relay outcomes.
const (
	ResultAccepted = "accepted" // stored; a post will be (re)published
	ResultCurrent  = "current"  // the server already holds this source key at this change hash
	ResultRejected = "rejected" // refused; Reason says why
)

// Repo-level relay outcomes.
const (
	RepoEnabled     = "enabled"
	RepoNotOptedIn  = "not_opted_in" // private repo the team has not opted in
	RepoNotLinked   = "not_linked"   // repo is not linked to this team
	RepoNotEligible = "not_eligible" // any other server-side refusal for the repo
)

// ItemResult is the server's answer for one relayed item.
type ItemResult struct {
	SourceKey string `json:"source_key"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// RelayResponse is the 200 body of a relay request.
type RelayResponse struct {
	RepoStatus string       `json:"repo_status"`
	Results    []ItemResult `json:"results"`
}

// SourcePR, SourceIssue, SourceComment and SourceReview are GitHub data as
// fetched, before Build applies the bot filter, cleanup and hashing.
type SourcePR struct {
	Number    int
	Title     string
	Body      string
	State     string // GitHub: open, closed
	Draft     bool
	Author    Author
	Labels    []string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	MergedAt  *time.Time
	HTMLURL   string
}

type SourceIssue struct {
	Number    int
	Title     string
	Body      string
	State     string // GitHub: open, closed
	Author    Author
	Labels    []string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	HTMLURL   string
}

type SourceComment struct {
	ID        int64
	Author    Author
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
	Path      string
	Line      *int
}

type SourceReview struct {
	ID          int64
	Author      Author
	State       string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	SubmittedAt time.Time
}

// Fetcher is the GitHub surface the relay needs. internal/github provides the
// production implementation; tests use fakes. List methods return items
// updated at or after since, newest first.
type Fetcher interface {
	Repo(ctx context.Context, owner, name string) (Repo, error)
	ListPullRequests(ctx context.Context, owner, name string, since time.Time) ([]SourcePR, error)
	// ListIssues returns issues only — pull requests are filtered out.
	ListIssues(ctx context.Context, owner, name string, since time.Time) ([]SourceIssue, error)
	// ListIssueComments returns conversation comments (issues and PRs).
	ListIssueComments(ctx context.Context, owner, name string, number int) ([]SourceComment, error)
	// ListReviewComments returns inline PR review comments.
	ListReviewComments(ctx context.Context, owner, name string, number int) ([]SourceComment, error)
	ListReviews(ctx context.Context, owner, name string, number int) ([]SourceReview, error)
	// ListPRFiles returns up to max changed file paths and whether more exist.
	ListPRFiles(ctx context.Context, owner, name string, number, max int) (paths []string, truncated bool, err error)
}

// PostHeader is the YAML front matter of a rendered post on the github board.
// The server writes it; CodeDB and prime read it.
type PostHeader struct {
	Source             string                `yaml:"source"` // always "github"
	Repo               string                `yaml:"repo"`   // owner/name
	Kind               string                `yaml:"kind"`
	Number             int                   `yaml:"number"`
	URL                string                `yaml:"url"`
	State              string                `yaml:"state"`
	Draft              bool                  `yaml:"draft,omitempty"`
	Title              string                `yaml:"title"`
	Author             PostAuthor            `yaml:"author"`
	Trust              string                `yaml:"trust"`
	Labels             []string              `yaml:"labels,omitempty"`
	Created            time.Time             `yaml:"created"`
	Closed             *time.Time            `yaml:"closed,omitempty"`
	Merged             *time.Time            `yaml:"merged,omitempty"`
	LastMaterialChange time.Time             `yaml:"last_material_change"`
	Review             PostReview            `yaml:"review,omitempty"`
	CommentMetadata    []PostCommentMetadata `yaml:"comment_metadata,omitempty"`
	Files              []string              `yaml:"files,omitempty"`
	Omitted            PostOmit              `yaml:"omitted"`
}

type PostAuthor struct {
	Login       string `yaml:"login"`
	ID          int64  `yaml:"id"`
	Association string `yaml:"association,omitempty"`
}

type PostReview struct {
	Approved         []string `yaml:"approved,omitempty"`
	ChangesRequested []string `yaml:"changes_requested,omitempty"`
}

// PostCommentMetadata preserves inline locations in Discussion order, including withheld entries.
type PostCommentMetadata struct {
	Path string `yaml:"path,omitempty"`
	Line *int   `yaml:"line,omitempty"`
}

type PostOmit struct {
	BotComments    int  `yaml:"bot_comments"`
	Withheld       int  `yaml:"withheld"`
	HiddenSpans    int  `yaml:"hidden_spans"`
	FilesTruncated bool `yaml:"files_truncated,omitempty"`
}

// PostComment is one entry of a rendered post's Discussion section.
type PostComment struct {
	Login     string
	Trust     string
	CreatedAt time.Time
	Body      string
	Path      string
	Line      *int
	Withheld  bool
}

// Post is a parsed github-board post.
type Post struct {
	Header   PostHeader
	Body     string // the item's description, unquoted
	Comments []PostComment
}

// PostMeta is the subset of a post's server-written .meta.json the client
// reads.
type PostMeta struct {
	Board     string    `json:"board"`
	Slug      string    `json:"slug"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	SourceKey string    `json:"source_key,omitempty"`
}
