package search

import "strings"

// SearchType determined by the type: filter.
type SearchType int

const (
	SearchTypeCode SearchType = iota
	SearchTypeDiff
	SearchTypeCommit
	SearchTypeSymbol
	SearchTypeComment
)

func (st SearchType) String() string {
	switch st {
	case SearchTypeDiff:
		return "diff"
	case SearchTypeCommit:
		return "commit"
	case SearchTypeSymbol:
		return "symbol"
	case SearchTypeComment:
		return "comment"
	default:
		return "code"
	}
}

// SelectType determined by the select: filter.
type SelectType int

const (
	SelectNone SelectType = iota
	SelectRepo
	SelectFile
	SelectSymbol
	SelectSymbolKind
)

// Filters parsed from the query string.
type Filters struct {
	Repo        string
	NegRepo     string
	File        string
	NegFile     string
	Lang        string
	NegLang     string
	Rev         string
	Count       int // 0 means default (20)
	Case        bool
	Author      string
	NegAuthor   string
	Before      string
	After       string
	Message     string
	NegMessage  string
	Select      SelectType
	SelectKind  string // for SelectSymbolKind
	Calls       string
	CalledBy    string
	Returns     string
	CommentKind string
}

// ParsedQuery is the result of parsing a query string.
type ParsedQuery struct {
	// SearchTerms grouped by OR. Each group is space-joined AND terms.
	SearchTerms []string
	Type        SearchType
	IsRegex     bool
	Filters     Filters
}

// SearchPattern returns all OR groups joined with " OR ".
func (q *ParsedQuery) SearchPattern() string {
	return strings.Join(q.SearchTerms, " OR ")
}

// HasEmptyPattern returns true if there are no search terms.
func (q *ParsedQuery) HasEmptyPattern() bool {
	if len(q.SearchTerms) == 0 {
		return true
	}
	for _, t := range q.SearchTerms {
		if t != "" {
			return false
		}
	}
	return true
}

// TranslatedQuery is SQL ready for execution with bound parameters.
type TranslatedQuery struct {
	SQL        string
	Params     []string
	SearchType SearchType
}
