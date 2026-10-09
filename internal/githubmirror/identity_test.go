package githubmirror

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

var bulletinSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

func TestSourceKey(t *testing.T) {
	tests := []struct {
		owner, name, kind string
		number            int
		want              string
	}{
		{"SageOx", "OX", KindPullRequest, 1287, "github.com/sageox/ox/pull/1287"},
		{"acme", "api", KindIssue, 7, "github.com/acme/api/issues/7"},
	}
	for _, tt := range tests {
		if got := SourceKey(tt.owner, tt.name, tt.kind, tt.number); got != tt.want {
			t.Errorf("SourceKey(%q,%q,%q,%d) = %q, want %q", tt.owner, tt.name, tt.kind, tt.number, got, tt.want)
		}
	}
}

func TestSlug(t *testing.T) {
	long := strings.Repeat("very-long-org-name", 6)
	tests := []struct {
		name, owner, repo, kind string
		number                  int
		want                    string
	}{
		{"plain", "acme", "api", KindPullRequest, 1287, "acme-api-pr-1287"},
		{"issue", "acme", "api", KindIssue, 1271, "acme-api-issue-1271"},
		{"dots and underscores collapse", "Acme.Corp", "my_repo.js", KindIssue, 3, "acme-corp-my-repo-js-issue-3"},
		{"runs collapse to one dash", "a--b", "__c__", KindPullRequest, 1, "a-b-c-pr-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slug(tt.owner, tt.repo, tt.kind, tt.number); got != tt.want {
				t.Errorf("Slug = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("long names keep the suffix and fit the bulletin rule", func(t *testing.T) {
		got := Slug(long, long, KindPullRequest, 123456)
		if len(got) > maxSlugLen {
			t.Fatalf("len(Slug) = %d, want <= %d", len(got), maxSlugLen)
		}
		if !strings.HasSuffix(got, "-pr-123456") {
			t.Fatalf("Slug %q lost its suffix", got)
		}
		if !bulletinSlugPattern.MatchString(got) {
			t.Fatalf("Slug %q violates the bulletin slug rule", got)
		}
	})

	t.Run("every slug matches the bulletin rule", func(t *testing.T) {
		for _, in := range [][2]string{{"acme", "api"}, {"-x-", "_y_"}, {"Ünïcode", "répo"}, {long, "r"}} {
			s := Slug(in[0], in[1], KindIssue, 1)
			if !bulletinSlugPattern.MatchString(s) {
				t.Errorf("Slug(%q,%q) = %q violates the bulletin slug rule", in[0], in[1], s)
			}
		}
	})
}

func TestSlugPrefix(t *testing.T) {
	if got, want := SlugPrefix("SageOx", "ox"), "sageox-ox-"; got != want {
		t.Fatalf("SlugPrefix = %q, want %q", got, want)
	}
	if !strings.HasPrefix(Slug("SageOx", "ox", KindPullRequest, 9), SlugPrefix("SageOx", "ox")) {
		t.Fatal("Slug must start with SlugPrefix")
	}
}

// adversarialRepoNames are owner/name pairs chosen to break a prefix filter:
// names long enough that Slug truncates them, names whose cut point lands on a
// dash or a run of punctuation, unicode that slugify drops, and names with
// nothing slug-worthy in them.
func adversarialRepoNames() [][2]string {
	long := strings.Repeat("very-long-org-name", 6)
	pairs := [][2]string{
		{"SageOx", "ox"},
		{"acme", "api"},
		{long, long},
		{long, "r"},
		{"o", long},
		{"Ünïcode-Örg", "répo-名前-ünï"},
		{"---", "..."},
		{"", ""},
		{"-", "_"},
		{strings.Repeat("a", 39), strings.Repeat("b", 100)},
		{strings.Repeat("a", 200), "x"},
	}
	// slide a punctuation run across every plausible cut point so some pair
	// has a dash at each of the positions Slug and SlugPrefix truncate at
	for lead := 40; lead <= 82; lead++ {
		head := strings.Repeat("a", lead)
		pairs = append(pairs,
			[2]string{head, "-.-" + strings.Repeat("x", 80)},
			[2]string{head + "-", strings.Repeat("y", 90)},
			[2]string{head + "._-", "z"},
			[2]string{"o", head + "___" + strings.Repeat("q", 10)},
		)
	}
	return pairs
}

// TestSlugPrefix_IsPrefixOfEverySlug is the invariant readers rely on: a post
// file name is "<slug>-<sha>", so a reader that filters files by SlugPrefix
// must never reject a slug of the same repo, for any kind or item number, even
// after Slug shortened a long name to fit the 80-character rule.
//
// Failure prevented: a repo with a long name has live posts on the board but
// prime shows no pointer and CodeDB never watches them.
func TestSlugPrefix_IsPrefixOfEverySlug(t *testing.T) {
	t.Parallel()

	numbers := []int{1, 9, 10, 99999, math.MaxInt}
	for _, pair := range adversarialRepoNames() {
		owner, name := pair[0], pair[1]
		prefix := SlugPrefix(owner, name)
		for _, kind := range []string{KindPullRequest, KindIssue} {
			for _, n := range numbers {
				slug := Slug(owner, name, kind, n)
				if !strings.HasPrefix(slug, prefix) {
					t.Fatalf("Slug(%q,%q,%q,%d) = %q does not start with SlugPrefix %q", owner, name, kind, n, slug, prefix)
				}
				// a post file is "<slug>-<sha>.md"; the filter sees the file name
				if file := slug + "-0a1b2c3d.md"; !strings.HasPrefix(file, prefix) {
					t.Fatalf("post file %q does not start with SlugPrefix %q", file, prefix)
				}
			}
		}
		if prefix != "" && !bulletinSlugPattern.MatchString(prefix) {
			t.Errorf("SlugPrefix(%q,%q) = %q is not a valid slug fragment", owner, name, prefix)
		}
		if len(prefix) > maxSlugLen {
			t.Errorf("SlugPrefix(%q,%q) is %d characters, want <= %d", owner, name, len(prefix), maxSlugLen)
		}
	}
}

func TestSlugPrefix_Shape(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("very-long-org-name", 6)
	tests := []struct {
		name, owner, repo string
		want              string
	}{
		{"short names keep the trailing dash", "Acme", "API", "acme-api-"},
		{"nothing slug-worthy has no prefix", "---", "...", ""},
		{"a long name is cut, with no dash appended", long, long, slugify(long + "-" + long)[:54]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SlugPrefix(tt.owner, tt.repo); got != tt.want {
				t.Errorf("SlugPrefix = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrustOf(t *testing.T) {
	tests := []struct {
		name string
		a    Author
		want string
	}{
		{"owner", Author{Login: "o", Association: "OWNER"}, TrustMember},
		{"member", Author{Login: "m", Association: "MEMBER"}, TrustMember},
		{"collaborator lowercase", Author{Login: "c", Association: "collaborator"}, TrustMember},
		{"contributor is external", Author{Login: "c", Association: "CONTRIBUTOR"}, TrustExternal},
		{"first timer", Author{Login: "f", Association: "FIRST_TIME_CONTRIBUTOR"}, TrustExternal},
		{"none", Author{Login: "n", Association: "NONE"}, TrustExternal},
		{"missing association is external", Author{Login: "x"}, TrustExternal},
		{"bot by type even as member", Author{Login: "renovate", Type: "Bot", Association: "MEMBER"}, TrustBot},
		{"bot by login suffix", Author{Login: "coderabbitai[bot]", Association: "NONE"}, TrustBot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrustOf(tt.a); got != tt.want {
				t.Errorf("TrustOf(%+v) = %q, want %q", tt.a, got, tt.want)
			}
		})
	}
}
