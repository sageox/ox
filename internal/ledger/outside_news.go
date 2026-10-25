package ledger

import (
	"regexp"
	"strings"
)

// Outside news is a murmur about activity outside the team — today, a pull
// request opened on GitHub. SageOx writes it into the Ledger, but its text
// (a PR title, file paths, a login) comes from people who are not coworkers.
// Every reader must keep it out of the teammate frame and treat its text as
// untrusted data, never as instructions.
const (
	// PrincipalTypeExternal marks a murmur whose principal is not a coworker.
	PrincipalTypeExternal = "external"

	// OutsideNewsSourceGitHub is metadata["source"] on outside news about GitHub.
	OutsideNewsSourceGitHub = "github"

	// Metadata keys outside news may carry. Readers use these and nothing else.
	// The PR title travels in metadata, not content, so ox releases that
	// read murmur content directly never show it.
	OutsideNewsKeySource      = "source"
	OutsideNewsKeyNumber      = "number"
	OutsideNewsKeyURL         = "url"
	OutsideNewsKeyAuthorLogin = "author_login"
	OutsideNewsKeyTitle       = "title"
	OutsideNewsKeyFiles       = "files"
)

var outsideNewsKeys = []string{
	OutsideNewsKeySource,
	OutsideNewsKeyNumber,
	OutsideNewsKeyURL,
	OutsideNewsKeyAuthorLogin,
	OutsideNewsKeyTitle,
	OutsideNewsKeyFiles,
}

var (
	prNumberPattern = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
	// https://github.com/<owner>/<repo>/pull/<number>, nothing before or after.
	githubPRURLPattern = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]{0,38}/([A-Za-z0-9._-]{1,100})/pull/([1-9][0-9]{0,9})$`)
	// https://github.com/<owner>/<repo>/pulls: the hourly digest's link.
	githubPullsURLPattern = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]{0,38}/([A-Za-z0-9._-]{1,100})/pulls$`)
	// GitHub login characters, plus the "[bot]" suffix GitHub gives app accounts.
	githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}(\[bot\])?$`)
)

// IsOutsideNews reports whether a murmur describes activity from outside the
// team. Either marker is enough, so a murmur that carries only one of them
// still lands in the untrusted frame.
func IsOutsideNews(principalType string, metadata map[string]string) bool {
	if strings.EqualFold(strings.TrimSpace(principalType), PrincipalTypeExternal) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(metadata[OutsideNewsKeySource]), OutsideNewsSourceGitHub)
}

// IsOutsideNews reports whether m describes activity from outside the team.
func (m MurmurFile) IsOutsideNews() bool {
	return IsOutsideNews(m.PrincipalType, m.Metadata)
}

// OutsideNewsMetadata returns only the metadata keys outside news may carry.
// Anything else a writer added is dropped, so it never reaches a reader.
func OutsideNewsMetadata(metadata map[string]string) map[string]string {
	kept := make(map[string]string, len(outsideNewsKeys))
	for _, key := range outsideNewsKeys {
		if value, ok := metadata[key]; ok {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// OutsideNewsPR holds the pull request fields of outside news that passed
// validation. A field is empty when it was missing or malformed, so a reader
// can only show values that cannot carry markup or instructions.
type OutsideNewsPR struct {
	Number string // decimal digits
	// URL is https://github.com/<owner>/<repo>/pull/<Number>, or, for a
	// digest that names no PR, the repository's https://…/pulls list.
	URL         string
	AuthorLogin string // GitHub login characters only
}

// ParseOutsideNewsPR validates the pull request fields in outside news
// metadata. A link must point at github.com and at the same PR number as the
// number field; a link that disagrees with it is dropped. Only news that
// names no PR may link a repository's pull request list.
func ParseOutsideNewsPR(metadata map[string]string) OutsideNewsPR {
	var pr OutsideNewsPR
	if number := strings.TrimSpace(metadata[OutsideNewsKeyNumber]); prNumberPattern.MatchString(number) {
		pr.Number = number
	}
	if url := strings.TrimSpace(metadata[OutsideNewsKeyURL]); url != "" {
		if match := githubPRURLPattern.FindStringSubmatch(url); match != nil {
			repo, urlNumber := match[1], match[2]
			if validRepoName(repo) && (pr.Number == "" || pr.Number == urlNumber) {
				pr.URL = url
				pr.Number = urlNumber
			}
		} else if match := githubPullsURLPattern.FindStringSubmatch(url); match != nil && pr.Number == "" && validRepoName(match[1]) {
			pr.URL = url
		}
	}
	if login := strings.TrimSpace(metadata[OutsideNewsKeyAuthorLogin]); githubLoginPattern.MatchString(login) {
		pr.AuthorLogin = login
	}
	return pr
}

// validRepoName rejects the two names the URL patterns admit but GitHub
// does not.
func validRepoName(repo string) bool {
	return repo != "." && repo != ".."
}
