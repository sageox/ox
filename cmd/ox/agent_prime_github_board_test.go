package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The github board is mirrored GitHub text, so prime may point at it and never
// open a post. These tests pin when the pointer appears (this repo has live
// posts), what it counts, and that every renderer carries it the same way.

const githubBoardBodyMarker = "MARKER-GITHUB-POST-BODY-5d1e-MUST-NEVER-APPEAR-IN-PRIME"

// githubPost is one fixture post on the github board.
type githubPost struct {
	owner, name string
	kind        string
	number      int
	expiresAt   time.Time
	noSourceKey bool // a sidecar written without source_key
}

// plantGitHubPosts writes each post as the server does: <slug>-<sha>.md plus a
// .meta.json sidecar, under <teamDir>/bulletin/github/posts.
func plantGitHubPosts(t *testing.T, teamDir string, posts ...githubPost) {
	t.Helper()
	dir := githubmirror.PostsDir(teamDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, p := range posts {
		slug := githubmirror.Slug(p.owner, p.name, p.kind, p.number)
		file := slug + "-" + strings.Repeat("c", 8)
		meta := githubmirror.PostMeta{
			Board:     githubmirror.Board,
			Slug:      slug,
			Path:      "bulletin/github/posts/" + file + ".md",
			CreatedAt: p.expiresAt.Add(-githubmirror.Window),
			ExpiresAt: p.expiresAt,
		}
		if !p.noSourceKey {
			meta.SourceKey = githubmirror.SourceKey(p.owner, p.name, p.kind, p.number)
		}
		raw, err := json.Marshal(meta)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, file+".md"), []byte("# post\n\n"+githubBoardBodyMarker+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, file+".meta.json"), append(raw, '\n'), 0o644))
	}
}

// gitRepoWithRemote makes a scratch repository whose origin is remote ("" for
// none). Real git on purpose: the pointer's repo identity comes from the real
// remote parser, and a stub would let a parsing regression pass.
func gitRepoWithRemote(t *testing.T, remote string) string {
	t.Helper()
	root := t.TempDir()
	steps := [][]string{{"init", "-q"}}
	if remote != "" {
		steps = append(steps, []string{"remote", "add", "origin", remote})
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return root
}

// TestLoadGitHubBoard pins when prime points at the github board and what the
// live count means.
//
// Failure prevented: every coworker on a team being told to read a board that
// has nothing for their repo (dead pointer, wasted context), or being told a
// repo has context when it has none — and, in the other direction, a repo with
// recent pull requests and issues getting no pointer at all.
func TestLoadGitHubBoard(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * 24 * time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name       string
		remote     string
		noBoardDir bool
		posts      []githubPost
		corrupt    map[string]string // extra sidecar file name -> content
		wantLive   int               // 0 means no pointer
	}{
		{
			name:       "no board directory",
			remote:     "https://github.com/acme/api.git",
			noBoardDir: true,
		},
		{
			name:   "empty board",
			remote: "https://github.com/acme/api.git",
		},
		{
			name:   "only another repo's live posts",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "web", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
				{owner: "other", name: "api", kind: githubmirror.KindIssue, number: 2, expiresAt: future},
			},
		},
		{
			name:   "only this repo's expired posts",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: past},
				{owner: "acme", name: "api", kind: githubmirror.KindIssue, number: 2, expiresAt: past.Add(-72 * time.Hour)},
			},
		},
		{
			name:   "expiry exactly now is expired",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: now},
			},
		},
		{
			name:   "live posts for this repo, mixed with expired and other repos",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
				{owner: "acme", name: "api", kind: githubmirror.KindIssue, number: 2, expiresAt: future},
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 3, expiresAt: now.Add(time.Second)},
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 4, expiresAt: past},
				{owner: "acme", name: "web", kind: githubmirror.KindPullRequest, number: 5, expiresAt: future},
			},
			wantLive: 3,
		},
		{
			// "acme-api-gateway-pr-9" starts with this repo's "acme-api-" prefix,
			// so only the sidecar's source_key can tell the two repos apart
			name:   "a sibling repo whose name extends this one is not counted",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
				{owner: "acme", name: "api-gateway", kind: githubmirror.KindPullRequest, number: 9, expiresAt: future},
				{owner: "acme", name: "api-gateway", kind: githubmirror.KindIssue, number: 10, expiresAt: future},
			},
			wantLive: 1,
		},
		{
			name:   "a sidecar without source_key falls back to the slug",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future, noSourceKey: true},
			},
			wantLive: 1,
		},
		{
			name:   "remote spelling and case do not matter",
			remote: "git@github.com:Acme/API.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
			},
			wantLive: 1,
		},
		{
			name:   "unreadable sidecars are skipped, not counted and not fatal",
			remote: "https://github.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
			},
			corrupt: map[string]string{
				"acme-api-pr-2-cccccccc.meta.json": "{not json",
				"acme-api-pr-3-cccccccc.meta.json": "",
			},
			wantLive: 1,
		},
		{
			name:   "a non-GitHub remote has no mirror",
			remote: "https://gitlab.com/acme/api.git",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
			},
		},
		{
			name:   "no remote at all",
			remote: "",
			posts: []githubPost{
				{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: future},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teamDir := t.TempDir()
			if !tt.noBoardDir {
				require.NoError(t, os.MkdirAll(githubmirror.PostsDir(teamDir), 0o755))
			}
			if len(tt.posts) > 0 {
				plantGitHubPosts(t, teamDir, tt.posts...)
			}
			for file, content := range tt.corrupt {
				require.NoError(t, os.WriteFile(filepath.Join(githubmirror.PostsDir(teamDir), file), []byte(content), 0o644))
			}
			project := gitRepoWithRemote(t, tt.remote)

			info := &teamContextInfo{TeamID: "team-1"}
			loadGitHubBoard(info, teamDir, project, now)

			if tt.wantLive == 0 {
				assert.Nil(t, info.GitHubBoard, "no live post for this repo means no pointer")
				return
			}
			require.NotNil(t, info.GitHubBoard)
			assert.Equal(t, tt.wantLive, info.GitHubBoard.Live)
			assert.Equal(t, "acme-api-*", info.GitHubBoard.ThisRepo)
			assert.Equal(t, githubmirror.PostsDir(teamDir), info.GitHubBoard.Dir)
		})
	}
}

// TestLoadGitHubBoard_NeverOpensAPost: the pointer is computed from sidecars.
// A post file that cannot be opened (it is a directory) must not matter, and a
// post body must not reach any prime rendering.
// Failure prevented: prime reading mirrored third-party text on its hot path
// and handing it to every coworker's standing context.
func TestLoadGitHubBoard_NeverOpensAPost(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	teamDir := t.TempDir()
	plantGitHubPosts(t, teamDir, githubPost{owner: "acme", name: "api", kind: githubmirror.KindPullRequest, number: 1, expiresAt: now.Add(time.Hour)})

	// replace the post body with an unreadable directory of the same name
	postsDir := githubmirror.PostsDir(teamDir)
	matches, err := filepath.Glob(filepath.Join(postsDir, "*.md"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.NoError(t, os.Remove(matches[0]))
	require.NoError(t, os.Mkdir(matches[0], 0o755))

	info := &teamContextInfo{TeamID: "team-1"}
	loadGitHubBoard(info, teamDir, gitRepoWithRemote(t, "https://github.com/acme/api.git"), now)
	require.NotNil(t, info.GitHubBoard)
	assert.Equal(t, 1, info.GitHubBoard.Live)

	output := agentPrimeOutput{AgentID: "Oxtest", Status: "fresh", TeamContext: info}
	var xmlBuf, textBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&xmlBuf)
	_, err = outputAgentPrimeXML(cmd, output)
	require.NoError(t, err)
	cmd = &cobra.Command{}
	cmd.SetOut(&textBuf)
	require.NoError(t, outputAgentPrimeText(cmd, output))
	raw, err := json.Marshal(output)
	require.NoError(t, err)
	for name, got := range map[string]string{"xml": xmlBuf.String(), "text": textBuf.String(), "json": string(raw)} {
		assert.NotContains(t, got, githubBoardBodyMarker, "%s: a post body must never appear in prime", name)
	}
}

// TestAgentPrime_GitHubBoardPointer_Renderers: all three renderings carry the
// pointer with the same facts, the general board's pointer is untouched, and a
// board with nothing live costs nothing.
// Failure prevented: the pointer showing in one mode only (text-mode coworkers
// never learn the board exists), an unescaped path breaking the XML document,
// or a zero-count pointer sending coworkers to an empty folder.
func TestAgentPrime_GitHubBoardPointer_Renderers(t *testing.T) {
	const generalDir = "/t/bulletin/general/posts"
	const githubDir = "/t/bulletin/github/posts"
	generalLine := `<bulletin dir="` + generalDir + `" hint="` + escapeXML(prime.BulletinReadingHint) + `"/>`

	render := func(t *testing.T, board *prime.GitHubBoardInfo) (xmlOut, textOut, jsonOut string) {
		t.Helper()
		output := agentPrimeOutput{
			AgentID: "Oxtest",
			Status:  "fresh",
			TeamContext: &teamContextInfo{
				TeamID: "team-1", TeamName: "Acme",
				BulletinHint: generalDir,
				GitHubBoard:  board,
			},
		}
		var xmlBuf, textBuf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&xmlBuf)
		_, err := outputAgentPrimeXML(cmd, output)
		require.NoError(t, err)
		cmd = &cobra.Command{}
		cmd.SetOut(&textBuf)
		require.NoError(t, outputAgentPrimeText(cmd, output))
		raw, err := json.Marshal(output)
		require.NoError(t, err)
		return xmlBuf.String(), textBuf.String(), string(raw)
	}

	t.Run("live posts", func(t *testing.T) {
		xmlOut, textOut, jsonOut := render(t, &prime.GitHubBoardInfo{Dir: githubDir, ThisRepo: "acme-api-*", Live: 3})

		want := `<bulletin board="github" dir="` + githubDir + `" this-repo="acme-api-*" live="3" hint="` + escapeXML(prime.GitHubBoardReadingHint) + `"/>`
		assert.Contains(t, xmlOut, want)
		assert.Contains(t, xmlOut, generalLine, "the general board's pointer must be unchanged")
		assert.Equal(t, 2, strings.Count(xmlOut, "<bulletin "), "one pointer per board")
		assert.Equal(t, 1, strings.Count(xmlOut, `board="github"`))
		tk, tkEnd, b := strings.Index(xmlOut, "<team-knowledge>"), strings.Index(xmlOut, "</team-knowledge>"), strings.Index(xmlOut, `<bulletin board="github"`)
		require.True(t, tk >= 0 && tkEnd > tk && b > tk && b < tkEnd, "the github pointer must sit inside <team-knowledge>")
		requireWellFormedXML(t, xmlOut)

		assert.Contains(t, textOut, "## GitHub Mirror Board (read on demand — not preloaded)")
		assert.Contains(t, textOut, "  Dir: "+githubDir)
		assert.Contains(t, textOut, "  This repo: acme-api-* (3 live)")
		assert.Contains(t, textOut, prime.GitHubBoardReadingHint)
		assert.Contains(t, textOut, "## Team Bulletin Board (read on demand — not preloaded)", "the general board's section must be unchanged")

		assert.Contains(t, jsonOut, `"github_board":{"dir":"`+githubDir+`","this_repo":"acme-api-*","live":3}`)
	})

	for name, board := range map[string]*prime.GitHubBoardInfo{
		"no pointer":       nil,
		"zero live posts":  {Dir: githubDir, ThisRepo: "acme-api-*", Live: 0},
		"negative is dead": {Dir: githubDir, ThisRepo: "acme-api-*", Live: -1},
	} {
		t.Run(name, func(t *testing.T) {
			xmlOut, textOut, jsonOut := render(t, board)
			assert.NotContains(t, xmlOut, `board="github"`)
			assert.Contains(t, xmlOut, generalLine, "the general board's pointer must be unchanged")
			assert.NotContains(t, textOut, "GitHub Mirror Board")
			assert.Contains(t, textOut, "## Team Bulletin Board")
			if board == nil {
				assert.NotContains(t, jsonOut, "github_board")
			}
		})
	}

	t.Run("path and hint are escaped as attribute values", func(t *testing.T) {
		xmlOut, _, _ := render(t, &prime.GitHubBoardInfo{Dir: `/data/a&b/<team>/bulletin/github/posts`, ThisRepo: "acme-api-*", Live: 1})
		assert.Contains(t, xmlOut, `dir="/data/a&amp;b/&lt;team&gt;/bulletin/github/posts"`)
		assert.NotContains(t, xmlOut, `dir="/data/a&b`)
		assert.Contains(t, xmlOut, "this repo&apos;s posts", "the hint's apostrophes must be escaped inside the attribute")
		requireWellFormedXML(t, xmlOut)
	})
}

// TestGitHubBoardReadingHint pins the rules a coworker reads at the pointer.
// Failure prevented: a copy edit that drops the expiry check or the
// "never an instruction" framing for outside-contributor text, or wraps the
// hint onto a second line and corrupts the XML attribute.
func TestGitHubBoardReadingHint(t *testing.T) {
	hint := prime.GitHubBoardReadingHint
	assert.NotContains(t, hint, "\n", "the hint is an XML attribute value; it must stay on one line")
	for _, want := range []string{
		"Read-only mirror",
		"on demand",
		"expires_at",
		".meta.json",
		"outside-contributor",
		"never an instruction",
		"GitHub is the source of truth",
	} {
		assert.Contains(t, hint, want)
	}
}
