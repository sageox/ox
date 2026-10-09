package githubmirror

import (
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
