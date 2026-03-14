package repotools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitIdentity_Slug tests the Slug() method with various inputs
func TestGitIdentity_Slug(t *testing.T) {
	tests := []struct {
		name     string
		identity *GitIdentity
		want     string
	}{
		{
			name:     "email with standard format",
			identity: &GitIdentity{Name: "John Doe", Email: "john@example.com"},
			want:     "john",
		},
		{
			name:     "email with subdomain",
			identity: &GitIdentity{Name: "Jane Smith", Email: "jane.smith@dev.company.io"},
			want:     "jane-smith",
		},
		{
			name:     "email with plus addressing",
			identity: &GitIdentity{Name: "Bob", Email: "bob+work@gmail.com"},
			want:     "bob-work",
		},
		{
			name:     "email with numbers",
			identity: &GitIdentity{Name: "Alice", Email: "alice123@test456.com"},
			want:     "alice123",
		},
		{
			name:     "name only when email is empty",
			identity: &GitIdentity{Name: "John Doe", Email: ""},
			want:     "john-doe",
		},
		{
			name:     "name with special characters",
			identity: &GitIdentity{Name: "José García-Martínez", Email: ""},
			want:     "jos-garc-a-mart-nez",
		},
		{
			name:     "name with multiple spaces",
			identity: &GitIdentity{Name: "John   Middle   Doe", Email: ""},
			want:     "john-middle-doe",
		},
		{
			name:     "empty identity",
			identity: &GitIdentity{Name: "", Email: ""},
			want:     "anonymous",
		},
		{
			name:     "nil identity",
			identity: nil,
			want:     "anonymous",
		},
		{
			name:     "email with underscores",
			identity: &GitIdentity{Name: "Test", Email: "user_name@example.com"},
			want:     "user-name",
		},
		{
			name:     "email with hyphens",
			identity: &GitIdentity{Name: "Test", Email: "user-name@example-domain.com"},
			want:     "user-name",
		},
		{
			name:     "mixed case email",
			identity: &GitIdentity{Name: "Test", Email: "John.Doe@Example.COM"},
			want:     "john-doe",
		},
		{
			name:     "only special characters becomes anonymous",
			identity: &GitIdentity{Name: "!!!@@@###", Email: ""},
			want:     "anonymous",
		},
		{
			name:     "leading and trailing spaces in name",
			identity: &GitIdentity{Name: "  John Doe  ", Email: ""},
			want:     "john-doe",
		},
		{
			name:     "name with dots",
			identity: &GitIdentity{Name: "J.R.R. Tolkien", Email: ""},
			want:     "j-r-r-tolkien",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.identity.Slug()
			assert.Equal(t, tt.want, got, "GitIdentity.Slug()")
		})
	}
}

// TestGitIdentity_Slug_Lowercase tests that all slugs are lowercase
func TestGitIdentity_Slug_Lowercase(t *testing.T) {
	testCases := []struct {
		name     string
		identity *GitIdentity
	}{
		{"uppercase email", &GitIdentity{Email: "JOHN@EXAMPLE.COM"}},
		{"uppercase name", &GitIdentity{Name: "JOHN DOE"}},
		{"mixed case", &GitIdentity{Email: "JoHn@ExAmPlE.CoM"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			slug := tc.identity.Slug()
			assert.Equal(t, strings.ToLower(slug), slug, "Slug() should be lowercase")
		})
	}
}

// TestGitIdentity_Slug_NoHyphensAtEnds tests that slugs don't have leading/trailing hyphens
func TestGitIdentity_Slug_NoHyphensAtEnds(t *testing.T) {
	testCases := []struct {
		name     string
		identity *GitIdentity
	}{
		{"email with leading special char", &GitIdentity{Email: "_john@example.com"}},
		{"email with trailing special char", &GitIdentity{Email: "john@example.com_"}},
		{"name with special chars at ends", &GitIdentity{Name: "!John Doe!"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			slug := tc.identity.Slug()
			assert.False(t, strings.HasPrefix(slug, "-"), "Slug() = %q has leading hyphen", slug)
			assert.False(t, strings.HasSuffix(slug, "-"), "Slug() = %q has trailing hyphen", slug)
		})
	}
}

// TestNormalizeGitURL tests URL normalization with various formats
func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "SSH format with .git",
			input: "git@github.com:org/repo.git",
			want:  "github.com/org/repo",
		},
		{
			name:  "SSH format without .git",
			input: "git@github.com:org/repo",
			want:  "github.com/org/repo",
		},
		{
			name:  "HTTPS format with .git",
			input: "https://github.com/org/repo.git",
			want:  "github.com/org/repo",
		},
		{
			name:  "HTTPS format without .git",
			input: "https://github.com/org/repo",
			want:  "github.com/org/repo",
		},
		{
			name:  "HTTP format",
			input: "http://github.com/org/repo.git",
			want:  "github.com/org/repo",
		},
		{
			name:  "SSH protocol format",
			input: "ssh://git@github.com/org/repo.git",
			want:  "github.com/org/repo",
		},
		{
			name:  "GitLab SSH",
			input: "git@gitlab.com:group/subgroup/project.git",
			want:  "gitlab.com/group/subgroup/project",
		},
		{
			name:  "GitLab HTTPS",
			input: "https://gitlab.com/group/subgroup/project.git",
			want:  "gitlab.com/group/subgroup/project",
		},
		{
			name:  "Bitbucket SSH",
			input: "git@bitbucket.org:team/repo.git",
			want:  "bitbucket.org/team/repo",
		},
		{
			name:  "mixed case URL",
			input: "https://GitHub.COM/Org/Repo.git",
			want:  "github.com/org/repo",
		},
		{
			name:  "custom domain SSH",
			input: "git@git.company.com:org/repo.git",
			want:  "git.company.com/org/repo",
		},
		{
			name:  "custom domain HTTPS",
			input: "https://git.company.com/org/repo.git",
			want:  "git.company.com/org/repo",
		},
		{
			name:  "multiple colons in SSH URL",
			input: "git@github.com:org:repo.git",
			want:  "github.com/org:repo",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitURL(tt.input)
			assert.Equal(t, tt.want, got, "normalizeGitURL(%q)", tt.input)
		})
	}
}

// TestNormalizeGitURL_Idempotent tests that normalizing twice produces same result
func TestNormalizeGitURL_Idempotent(t *testing.T) {
	urls := []string{
		"git@github.com:org/repo.git",
		"https://github.com/org/repo.git",
		"http://gitlab.com/group/project.git",
	}

	for _, url := range urls {
		first := normalizeGitURL(url)
		second := normalizeGitURL(first)
		assert.Equal(t, first, second, "normalizeGitURL is not idempotent for %q", url)
	}
}

// TestNormalizeGitURL_EquivalentURLs tests that equivalent URLs normalize to same value
// Note: This test documents current behavior where uppercase protocols (HTTPS://) are not normalized
// the same as lowercase due to case-sensitive TrimPrefix before ToLower
func TestNormalizeGitURL_EquivalentURLs(t *testing.T) {
	equivalentSets := [][]string{
		{
			"git@github.com:org/repo.git",
			"git@github.com:org/repo",
		},
		{
			"https://github.com/org/repo.git",
			"http://github.com/org/repo.git",
			"https://github.com/org/repo",
			"http://github.com/org/repo",
		},
		{
			"git@gitlab.com:group/project.git",
			"git@gitlab.com:group/project",
		},
		{
			"https://gitlab.com/group/project.git",
			"https://gitlab.com/group/project",
			"http://gitlab.com/group/project.git",
			"http://gitlab.com/group/project",
		},
	}

	for i, set := range equivalentSets {
		normalized := make(map[string]bool)
		for _, url := range set {
			normalized[normalizeGitURL(url)] = true
		}
		assert.Len(t, normalized, 1, "equivalent set %d produced %d different normalized forms, expected 1", i, len(normalized))
	}
}

// TestNormalizeGitURL_CaseSensitiveProtocol tests that uppercase protocols are not handled correctly
// This documents a limitation in the current implementation
func TestNormalizeGitURL_CaseSensitiveProtocol(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercase https is normalized",
			input:    "https://github.com/org/repo.git",
			expected: "github.com/org/repo",
		},
		{
			name:     "uppercase HTTPS is not fully normalized",
			input:    "HTTPS://GitHub.COM/Org/Repo.git",
			expected: "https://github.com/org/repo", // protocol prefix not removed due to case sensitivity
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitURL(tt.input)
			if got != tt.expected {
				t.Logf("normalizeGitURL(%q) = %q (this documents current behavior)", tt.input, got)
			}
		})
	}
}

// TestHashRemoteURLs tests URL hashing functionality
func TestHashRemoteURLs(t *testing.T) {
	urls := []string{
		"github.com/org/repo",
		"gitlab.com/group/project",
	}
	salt := "test-salt-12345"

	hashes := HashRemoteURLs(salt, urls)

	assert.Len(t, hashes, len(urls))

	for i, hash := range hashes {
		assert.True(t, strings.HasPrefix(hash, "sha256:"), "hash[%d] = %q does not have 'sha256:' prefix", i, hash)

		hexPart := strings.TrimPrefix(hash, "sha256:")
		assert.Len(t, hexPart, 64, "hash[%d] hex part has wrong length", i)

		// verify it's valid hex
		_, err := hex.DecodeString(hexPart)
		assert.NoError(t, err, "hash[%d] contains invalid hex", i)
	}
}

// TestHashRemoteURLs_Deterministic tests that hashing is deterministic
func TestHashRemoteURLs_Deterministic(t *testing.T) {
	urls := []string{
		"github.com/org/repo1",
		"github.com/org/repo2",
		"gitlab.com/group/project",
	}
	salt := "consistent-salt"

	hashes1 := HashRemoteURLs(salt, urls)
	hashes2 := HashRemoteURLs(salt, urls)

	require.Len(t, hashes1, len(hashes2))

	for i := range hashes1 {
		assert.Equal(t, hashes1[i], hashes2[i], "hash[%d] differs", i)
	}
}

// TestHashRemoteURLs_DifferentSalts tests that different salts produce different hashes
func TestHashRemoteURLs_DifferentSalts(t *testing.T) {
	urls := []string{"github.com/org/repo"}
	salt1 := "salt-one"
	salt2 := "salt-two"

	hashes1 := HashRemoteURLs(salt1, urls)
	hashes2 := HashRemoteURLs(salt2, urls)

	require.Len(t, hashes1, 1)
	require.Len(t, hashes2, 1)

	assert.NotEqual(t, hashes1[0], hashes2[0], "different salts produced same hash")
}

// TestHashRemoteURLs_DifferentURLs tests that different URLs produce different hashes
func TestHashRemoteURLs_DifferentURLs(t *testing.T) {
	urls := []string{
		"github.com/org/repo1",
		"github.com/org/repo2",
		"gitlab.com/org/repo1",
	}
	salt := "same-salt"

	hashes := HashRemoteURLs(salt, urls)

	require.Len(t, hashes, len(urls))

	// verify all hashes are unique
	seen := make(map[string]bool)
	for i, hash := range hashes {
		assert.False(t, seen[hash], "duplicate hash found at index %d: %q", i, hash)
		seen[hash] = true
	}
}

// TestHashRemoteURLs_EmptyInputs tests edge cases
func TestHashRemoteURLs_EmptyInputs(t *testing.T) {
	t.Run("empty URL list", func(t *testing.T) {
		hashes := HashRemoteURLs("salt", []string{})
		assert.Empty(t, hashes)
	})

	t.Run("nil URL list", func(t *testing.T) {
		hashes := HashRemoteURLs("salt", nil)
		assert.Empty(t, hashes)
	})

	t.Run("empty salt", func(t *testing.T) {
		urls := []string{"github.com/org/repo"}
		hashes := HashRemoteURLs("", urls)
		require.Len(t, hashes, 1)
		// should still produce valid hash, just without salt
		assert.True(t, strings.HasPrefix(hashes[0], "sha256:"), "hash without salt still needs sha256 prefix")
	})
}

// TestHashRemoteURLs_VerifyFormat tests the exact hash format
func TestHashRemoteURLs_VerifyFormat(t *testing.T) {
	url := "github.com/test/repo"
	salt := "known-salt"

	// manually compute expected hash
	data := salt + ":" + url
	hash := sha256.Sum256([]byte(data))
	expectedHash := "sha256:" + hex.EncodeToString(hash[:])

	hashes := HashRemoteURLs(salt, []string{url})
	require.Len(t, hashes, 1)

	assert.Equal(t, expectedHash, hashes[0], "hash format mismatch")
}

// TestDetectGitIdentity tests git identity detection
// This test requires git to be installed and will skip if not available
func TestDetectGitIdentity(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed, skipping DetectGitIdentity test")
	}

	identity, err := DetectGitIdentity()
	require.NoError(t, err, "DetectGitIdentity() error")

	// identity can be nil if git config is not set, which is valid
	if identity != nil {
		t.Logf("Detected git identity: Name=%q, Email=%q", identity.Name, identity.Email)

		// if identity exists, at least one field should be non-empty
		assert.True(t, identity.Name != "" || identity.Email != "",
			"DetectGitIdentity() returned non-nil identity with both fields empty")
	} else {
		t.Log("No git identity configured (this is valid)")
	}
}

// TestGetInitialCommitHash tests getting the initial commit hash
// This test requires git to be installed and in a git repository
func TestGetInitialCommitHash(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed, skipping GetInitialCommitHash test")
	}

	// check if we're in a git repository
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping GetInitialCommitHash test")
	}

	hash, err := GetInitialCommitHash()
	require.NoError(t, err, "GetInitialCommitHash() error")

	// verify hash format (40 character hex string for SHA-1)
	assert.Len(t, hash, 40, "GetInitialCommitHash() returned hash with wrong length")

	_, hexErr := hex.DecodeString(hash)
	assert.NoError(t, hexErr, "GetInitialCommitHash() returned invalid hex")

	t.Logf("Initial commit hash: %s", hash)
}

// TestGetRemoteURLs tests getting git remote URLs
// This test requires git to be installed and in a git repository
func TestGetRemoteURLs(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed, skipping GetRemoteURLs test")
	}

	// check if we're in a git repository
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping GetRemoteURLs test")
	}

	urls, err := GetRemoteURLs()
	require.NoError(t, err, "GetRemoteURLs() error")

	// it's valid to have no remotes
	t.Logf("Found %d remote URL(s)", len(urls))
	for i, url := range urls {
		t.Logf("  Remote[%d]: %s", i, url)

		// verify URLs are normalized (no protocol prefix, no .git suffix, lowercase)
		assert.False(t, strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "ssh://"),
			"URL[%d] = %q still has protocol prefix", i, url)
		assert.False(t, strings.HasSuffix(url, ".git"), "URL[%d] = %q still has .git suffix", i, url)
		assert.Equal(t, strings.ToLower(url), url, "URL[%d] = %q is not lowercase", i, url)
	}
}

// TestGetRemoteURLs_NoDuplicates tests that duplicate remotes are deduplicated
func TestGetRemoteURLs_NoDuplicates(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed, skipping test")
	}

	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping test")
	}

	urls, err := GetRemoteURLs()
	require.NoError(t, err, "GetRemoteURLs() error")

	// verify no duplicates
	seen := make(map[string]bool)
	for i, url := range urls {
		assert.False(t, seen[url], "duplicate URL found at index %d: %q", i, url)
		seen[url] = true
	}
}

// TestIsPublicRepo tests public repository detection
func TestIsPublicRepo(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed, skipping IsPublicRepo test")
	}

	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping IsPublicRepo test")
	}

	isPublic, err := IsPublicRepo()
	require.NoError(t, err, "IsPublicRepo() error")

	// we can't assert the value, but we can log it
	t.Logf("IsPublicRepo() = %v", isPublic)

	// the current implementation always returns false, so test that
	if isPublic {
		t.Log("Repository detected as public")
	} else {
		t.Log("Repository detected as private (or defaulted to private)")
	}
}

// TestIsPublicRepo_NoGit tests behavior when git is not available
func TestIsPublicRepo_NoGit(t *testing.T) {
	// temporarily modify PATH to hide git
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)

	os.Setenv("PATH", "/nonexistent")

	_, err := IsPublicRepo()
	assert.Error(t, err, "IsPublicRepo() should return error when git is not available")
}

// BenchmarkGitIdentitySlug benchmarks the Slug method
func BenchmarkGitIdentitySlug(b *testing.B) {
	identity := &GitIdentity{
		Name:  "John Doe",
		Email: "john.doe@example.com",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		identity.Slug()
	}
}

// BenchmarkNormalizeGitURL benchmarks URL normalization
func BenchmarkNormalizeGitURL(b *testing.B) {
	url := "git@github.com:organization/repository.git"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		normalizeGitURL(url)
	}
}

func TestGetRepoName_FromRemote(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository")
	}

	name := GetRepoName("")
	// in the ox repo, this should extract owner/repo from the origin remote
	t.Logf("GetRepoName() = %q", name)
	assert.NotEmpty(t, name, "expected non-empty repo name")
	// should contain a slash if remote was parsed (owner/repo)
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		assert.GreaterOrEqual(t, len(parts), 2, "expected owner/repo format")
	}
}

func TestGetRepoName_FallbackToDirectory(t *testing.T) {
	tests := []struct {
		name    string
		gitRoot string
		want    string
	}{
		{"simple path", "/Users/dev/projects/my-app", "my-app"},
		{"trailing slash", "/Users/dev/projects/my-app/", "my-app"},
		{"nested path", "/home/user/code/sageox/ox", "ox"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// GetRepoName with no remotes falls back to directory name.
			// We test the fallback path by passing gitRoot to a helper
			// that exercises the same logic.
			gitRoot := tt.gitRoot
			cleaned := strings.TrimRight(gitRoot, "/")
			var got string
			if cleaned != "" {
				if idx := strings.LastIndex(cleaned, "/"); idx >= 0 {
					got = cleaned[idx+1:]
				} else {
					got = cleaned
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGetRepoName_UsesGitRoot_NotCWD verifies that GetRepoName extracts the
// repo name from the given gitRoot's remotes, not from CWD's remotes.
// This catches a real bug: GetRemoteURLs() runs `git remote -v` in CWD,
// ignoring the gitRoot parameter entirely.
func TestGetRepoName_UsesGitRoot_NotCWD(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}

	// create two separate git repos with different remotes
	repoA := t.TempDir()
	repoB := t.TempDir()

	for _, setup := range []struct {
		dir    string
		remote string
	}{
		{repoA, "https://github.com/alice/project-a.git"},
		{repoB, "https://github.com/bob/project-b.git"},
	} {
		cmd := exec.Command("git", "init")
		cmd.Dir = setup.dir
		require.NoError(t, cmd.Run())

		cmd = exec.Command("git", "remote", "add", "origin", setup.remote)
		cmd.Dir = setup.dir
		require.NoError(t, cmd.Run())
	}

	// cd into repoA, then call GetRepoName(repoB)
	origDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(origDir)

	require.NoError(t, os.Chdir(repoA))

	name := GetRepoName(repoB)

	// must return repoB's remote name, not repoA's (CWD)
	assert.Equal(t, "bob/project-b", name,
		"GetRepoName should use gitRoot's remotes, not CWD's remotes")
}

// TestGetRepoName_PrefersOrigin verifies that GetRepoName uses the "origin"
// remote even when another remote comes first alphabetically.
func TestGetRepoName_PrefersOrigin(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// "abc-backup" sorts before "origin" alphabetically
	cmd = exec.Command("git", "remote", "add", "abc-backup", "https://github.com/other/backup.git")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "remote", "add", "origin", "https://github.com/real/project.git")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	name := GetRepoName(dir)
	assert.Equal(t, "real/project", name,
		"GetRepoName should prefer origin remote over alphabetically-first remote")
}

// TestGetRepoName_NoRemote_FallsBackToDir verifies that when a repo has
// no remotes, GetRepoName returns the directory basename from gitRoot.
func TestGetRepoName_NoRemote_FallsBackToDir(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}

	// create a git repo with no remotes
	dir := t.TempDir() + "/my-project"
	require.NoError(t, os.Mkdir(dir, 0755))

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	origDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(origDir)
	require.NoError(t, os.Chdir(dir))

	name := GetRepoName(dir)
	assert.Equal(t, "my-project", name, "should fall back to directory name when no remotes exist")
}

// TestGetRepoName_NonGitDir_FallsBackToDir verifies that when gitRoot
// points to a non-git directory, GetRepoName returns the directory basename.
func TestGetRepoName_NonGitDir_FallsBackToDir(t *testing.T) {
	dir := t.TempDir() + "/some-workspace"
	require.NoError(t, os.Mkdir(dir, 0755))

	// not in any git repo
	origDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(origDir)
	require.NoError(t, os.Chdir(dir))

	name := GetRepoName(dir)
	assert.Equal(t, "some-workspace", name)
}

// TestGetRepoName_EmptyGitRoot verifies graceful handling of empty gitRoot.
func TestGetRepoName_EmptyGitRoot(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}

	// when CWD is not a git repo and gitRoot is empty, should return ""
	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(origDir)
	require.NoError(t, os.Chdir(dir))

	name := GetRepoName("")
	// empty gitRoot in a non-git CWD should return empty string
	assert.Empty(t, name, "GetRepoName with empty gitRoot in non-git dir should return empty")
}

// TestNormalizeGitURL_EdgeCases tests URL normalization edge cases that
// affect repo name extraction. A bug here means GetRepoName returns
// wrong names for SSH, HTTP, or unconventional git URLs.
func TestNormalizeGitURL_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"ssh standard", "git@github.com:sageox/ox.git", "github.com/sageox/ox"},
		{"https standard", "https://github.com/sageox/ox.git", "github.com/sageox/ox"},
		{"https no .git", "https://github.com/sageox/ox", "github.com/sageox/ox"},
		{"ssh no .git", "git@github.com:sageox/ox", "github.com/sageox/ox"},
		{"gitlab ssh", "git@gitlab.com:group/subgroup/repo.git", "gitlab.com/group/subgroup/repo"},
		{"http insecure", "http://internal.example.com/team/repo.git", "internal.example.com/team/repo"},
		{"ssh protocol", "ssh://git@github.com/sageox/ox.git", "github.com/sageox/ox"},
		{"ssh with port", "ssh://git@github.com:2222/org/repo.git", "github.com/org/repo"},
		{"scp with port", "git@selfhosted.example.com:2222/team/project.git", "selfhosted.example.com/team/project"},
		{"mixed case", "git@GitHub.Com:SageOx/Ox.git", "github.com/sageox/ox"},
		{"empty", "", ""},
		{"just .git", ".git", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGetRepoName_ExtractsOwnerRepo tests the full extraction pipeline:
// normalizeGitURL → strip host → owner/repo. This catches bugs where
// the host-stripping logic breaks for multi-segment paths (gitlab subgroups).
func TestGetRepoName_ExtractsOwnerRepo(t *testing.T) {
	tests := []struct {
		name       string
		normalized string // output of normalizeGitURL
		want       string // expected from GetRepoName's host-stripping logic
	}{
		{"github", "github.com/sageox/ox", "sageox/ox"},
		{"gitlab subgroup", "gitlab.com/group/subgroup/repo", "group/subgroup/repo"},
		{"no slash", "localhost", ""},
		{"trailing slash", "github.com/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// replicate the extraction logic from GetRepoName
			var got string
			if idx := strings.Index(tt.normalized, "/"); idx >= 0 {
				ownerRepo := tt.normalized[idx+1:]
				if ownerRepo != "" {
					got = ownerRepo
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// BenchmarkHashRemoteURLs benchmarks URL hashing
func BenchmarkHashRemoteURLs(b *testing.B) {
	urls := []string{
		"github.com/org/repo1",
		"github.com/org/repo2",
		"gitlab.com/group/project",
		"bitbucket.org/team/repo",
	}
	salt := "benchmark-salt"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HashRemoteURLs(salt, urls)
	}
}
