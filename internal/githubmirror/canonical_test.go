package githubmirror

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalRepo pins which spelling the readers use for "this repo".
//
// Failure prevented: after a GitHub rename or transfer the remote keeps
// working under its old name while the relay publishes under the new one, so
// CodeDB and prime look for posts that do not exist and the repo's mirrored
// context silently disappears.
func TestCanonicalRepo(t *testing.T) {
	t.Parallel()

	renamed := &Repo{Owner: "acme", Name: "api-v2", FullName: "acme/api-v2"}
	tests := []struct {
		name       string
		state      *State  // nil: no state file
		raw        *string // written verbatim instead of state
		emptyPath  bool
		wantOwner  string
		wantName   string
		remoteOwn  string
		remoteName string
	}{
		{
			name:       "a renamed repo resolves to the relay's recorded name",
			state:      &State{Repo: "acme/api", RepoMeta: renamed},
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api-v2",
		},
		{
			name:       "a transferred repo resolves to the new owner",
			state:      &State{Repo: "old-org/api", RepoMeta: &Repo{Owner: "new-org", Name: "api"}},
			remoteOwn:  "old-org",
			remoteName: "api",
			wantOwner:  "new-org",
			wantName:   "api",
		},
		{
			name:       "the remote's case does not matter for recognizing its own state",
			state:      &State{Repo: "acme/api", RepoMeta: renamed},
			remoteOwn:  "ACME",
			remoteName: "Api",
			wantOwner:  "acme",
			wantName:   "api-v2",
		},
		{
			name:       "an unrenamed repo gets GitHub's spelling of the same name",
			state:      &State{Repo: "acme/api", RepoMeta: &Repo{Owner: "Acme", Name: "API"}},
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "Acme",
			wantName:   "API",
		},
		{
			name:       "no state file keeps the remote's spelling",
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
		{
			name:       "state written for another repo is not trusted",
			state:      &State{Repo: "acme/web", RepoMeta: &Repo{Owner: "acme", Name: "web-v2"}},
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
		{
			name:       "state with no repo metadata yet keeps the remote's spelling",
			state:      &State{Repo: "acme/api"},
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
		{
			name:       "a metadata part left blank keeps the remote's spelling",
			state:      &State{Repo: "acme/api", RepoMeta: &Repo{Owner: "acme"}},
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
		{
			name:       "a corrupt state file keeps the remote's spelling",
			raw:        strPtr("{not json"),
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
		{
			name:       "an empty ledger path never reads the working directory",
			emptyPath:  true,
			remoteOwn:  "acme",
			remoteName: "api",
			wantOwner:  "acme",
			wantName:   "api",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := t.TempDir()
			switch {
			case tt.raw != nil:
				path := StatePath(ledger)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tt.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			case tt.state != nil:
				if err := SaveState(ledger, tt.state); err != nil {
					t.Fatal(err)
				}
			}
			if tt.emptyPath {
				ledger = ""
			}

			owner, name := CanonicalRepo(ledger, tt.remoteOwn, tt.remoteName)
			if owner != tt.wantOwner || name != tt.wantName {
				t.Errorf("CanonicalRepo = %q/%q, want %q/%q", owner, name, tt.wantOwner, tt.wantName)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
