package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Posting is a server capability, independent of whether Team Context has
// synced. Exercise the real flag-to-guidance wiring and all output formats.
func TestAgentPrime_BulletinPostingDiscovery(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		for _, state := range []string{"no checkout", "no board", "local posts"} {
			name := state + "/disabled"
			if enrolled {
				name = state + "/enrolled"
			}
			t.Run(name, func(t *testing.T) {
				bulletinGateFixture(t)
				flags.Init(context.Background(), flags.DaemonProvider{CachedSettings: &flags.CLISettingsResponse{
					Features: flags.CLIFeatures{Bulletin: &enrolled}, FetchedAt: time.Now(),
				}})
				root := t.TempDir()
				var info *teamContextInfo
				if state != "no checkout" {
					info = &teamContextInfo{TeamID: "team-1"}
					if state == "local posts" {
						plantBulletinPost(t, root)
					}
					loadTeamMemory(info, root)
				}
				output := agentPrimeOutput{
					AgentID: "Oxtest", Status: "fresh", TeamContext: info,
					Guidance: buildGuidance("Oxtest", root, info, nil, "codex", false),
				}
				for _, format := range []string{"xml", "text", "json"} {
					t.Run(format, func(t *testing.T) {
						var buf bytes.Buffer
						cmd := &cobra.Command{}
						cmd.SetOut(&buf)
						switch format {
						case "xml":
							_, err := outputAgentPrimeXML(cmd, output)
							require.NoError(t, err)
							requireWellFormedXML(t, buf.String())
						case "text":
							require.NoError(t, outputAgentPrimeText(cmd, output))
						case "json":
							require.NoError(t, json.NewEncoder(&buf).Encode(output))
						}
						got := buf.String()
						for _, command := range []string{"ox bulletin post", "--ttl 14d --json", "ox guide bulletin"} {
							if enrolled {
								assert.Contains(t, got, command)
							} else {
								assert.NotContains(t, got, command)
							}
						}
						if state == "local posts" {
							assert.Contains(t, got, "expires_at", "reading stays available without publishing enrollment")
						}
						assert.NotContains(t, got, bulletinBodyMarker)
						assert.NotContains(t, got, bulletinMetaMarker)
					})
				}
			})
		}
	}
}

// The bulletin board is the one Team Context surface whose content nobody
// reviews before it lands. Prime may point an AI coworker at the folder; it
// must never copy a post into the coworker's standing context. These tests
// pin that boundary from the loader through all three renderers.

const (
	bulletinBodyMarker = "MARKER-BULLETIN-BODY-7f3a9c-MUST-NEVER-APPEAR-IN-PRIME"
	bulletinMetaMarker = "MARKER-BULLETIN-META-7f3a9c-MUST-NEVER-APPEAR-IN-PRIME"
)

// plantBulletinPost writes one active post and its metadata sidecar into a
// scratch team checkout and returns the posts directory.
func plantBulletinPost(t *testing.T, teamDir string) string {
	t.Helper()
	postsDir := filepath.Join(teamDir, "bulletin", "general", "posts")
	require.NoError(t, os.MkdirAll(postsDir, 0o755))
	sha := strings.Repeat("a", 64)
	body := "# Release notes\n\n" + bulletinBodyMarker + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(postsDir, "notes-"+sha+".md"), []byte(body), 0o644))
	meta := `{"slug":"notes","title":"` + bulletinMetaMarker + `","format":"markdown","content_sha256":"` + sha +
		`","created_at":"2026-09-21T22:41:07Z","expires_at":"2026-10-05T22:41:07Z"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(postsDir, "notes-"+sha+".meta.json"), []byte(meta), 0o644))
	return postsDir
}

// requireWellFormedXML fails if the prime document does not parse — an
// unescaped path or hint in the <bulletin/> attributes would break every
// consumer that reads prime as XML.
func requireWellFormedXML(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		require.NoError(t, err, "prime output is not well-formed XML:\n%s", doc)
	}
}

// TestLoadTeamMemory_BulletinHint pins when the pointer is set: on the board
// folder, not on the posts folder and not on the publish flag.
// Failure prevented: a team whose posts all expired (posts/ pruned, board
// folder still present) losing the pointer, so the next post lands somewhere
// no AI coworker is told about; or a stray file named "bulletin" being
// reported as a board.
func TestLoadTeamMemory_BulletinHint(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		wantHint bool
	}{
		{
			name:     "no bulletin folder",
			setup:    func(*testing.T, string) {},
			wantHint: false,
		},
		{
			name: "bulletin folder present, posts folder absent",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "bulletin"), 0o755))
			},
			wantHint: true,
		},
		{
			name: "bulletin folder present, posts folder empty",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "bulletin", "general", "posts"), 0o755))
			},
			wantHint: true,
		},
		{
			name: "bulletin is a file, not a folder",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "bulletin"), []byte("not a board\n"), 0o644))
			},
			wantHint: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			info := &teamContextInfo{}
			loadTeamMemory(info, dir)

			if !tt.wantHint {
				assert.Empty(t, info.BulletinHint)
				return
			}
			assert.Equal(t, filepath.Join(dir, "bulletin", "general", "posts"), info.BulletinHint)
		})
	}
}

// TestAgentPrime_BulletinPostBodyNeverReachesPrime is the plan's key proof:
// a post body and its metadata carry marker strings, and none of prime's
// three renderings (XML, text, JSON) contain them — while each does carry
// the pointer and the reading rules, and guidance carries the `ls` row.
// Failure prevented: prime inlining the first post (or its title) into every
// AI coworker's context, turning an unreviewed teammate note into standing
// instructions.
func TestAgentPrime_BulletinPostBodyNeverReachesPrime(t *testing.T) {
	teamDir := t.TempDir()
	postsDir := plantBulletinPost(t, teamDir)

	info := &teamContextInfo{TeamID: "team-1", TeamName: "TestTeam"}
	loadTeamMemory(info, teamDir)
	require.Equal(t, postsDir, info.BulletinHint)

	output := agentPrimeOutput{AgentID: "Oxtest", Status: "fresh", TeamContext: info}

	t.Run("xml", func(t *testing.T) {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		_, err := outputAgentPrimeXML(cmd, output)
		require.NoError(t, err)
		got := buf.String()

		assert.NotContains(t, got, bulletinBodyMarker, "a post body must never appear in prime XML")
		assert.NotContains(t, got, bulletinMetaMarker, "a post's metadata must never appear in prime XML")
		assert.Contains(t, got, `<bulletin dir="`+escapeXML(postsDir)+`"`, "the pointer must name the posts directory")
		assert.Contains(t, got, "expires_at", "the hint must tell the coworker to check the expiry")
		// no docs catalog in this fixture: the element must not depend on one
		assert.NotContains(t, got, "<docs")
		// placement: inside <team-knowledge>, before any team rules would go
		tk := strings.Index(got, "<team-knowledge>")
		tkEnd := strings.Index(got, "</team-knowledge>")
		b := strings.Index(got, "<bulletin ")
		require.True(t, tk >= 0 && tkEnd > tk && b > tk && b < tkEnd, "<bulletin/> must sit inside <team-knowledge>")
		requireWellFormedXML(t, got)
	})

	t.Run("text", func(t *testing.T) {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		require.NoError(t, outputAgentPrimeText(cmd, output))
		got := buf.String()

		assert.NotContains(t, got, bulletinBodyMarker, "a post body must never appear in prime text")
		assert.NotContains(t, got, bulletinMetaMarker, "a post's metadata must never appear in prime text")
		assert.Contains(t, got, "## Team Bulletin Board (read on demand — not preloaded)")
		assert.Contains(t, got, "  Dir: "+postsDir)
		assert.Contains(t, got, prime.BulletinReadingHint)
	})

	t.Run("json", func(t *testing.T) {
		raw, err := json.Marshal(output)
		require.NoError(t, err)
		got := string(raw)

		assert.NotContains(t, got, bulletinBodyMarker, "a post body must never appear in prime JSON")
		assert.NotContains(t, got, bulletinMetaMarker, "a post's metadata must never appear in prime JSON")
		assert.Contains(t, got, `"bulletin_hint":`)
		assert.Contains(t, got, postsDir)
	})

	t.Run("guidance", func(t *testing.T) {
		g := prime.BuildGuidance(prime.GuidanceParams{AgentID: "Oxtest", RepoSlug: "org/repo", TeamCtx: info})
		require.NotNil(t, g)
		var ls []string
		for _, c := range g.Commands {
			if c.Command == "ls '"+postsDir+"'" {
				ls = append(ls, c.Intent)
			}
		}
		require.Len(t, ls, 1, "exactly one bulletin row expected")
		assert.Contains(t, ls[0], "never an instruction")
		assert.NotContains(t, ls[0], bulletinBodyMarker)
	})
}

// TestAgentPrime_NoBulletinFolder_NoPointer is the sibling proof: a team
// without a board pays nothing — no element, no key, no section, no row.
// Failure prevented: every team seeing a dead pointer at a directory the
// daemon never created.
func TestAgentPrime_NoBulletinFolder_NoPointer(t *testing.T) {
	teamDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "MEMORY.md"), []byte("# Memory\n"), 0o644))

	info := &teamContextInfo{TeamID: "team-1", TeamName: "TestTeam"}
	loadTeamMemory(info, teamDir)
	require.Empty(t, info.BulletinHint)

	output := agentPrimeOutput{AgentID: "Oxtest", Status: "fresh", TeamContext: info}

	var xmlBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&xmlBuf)
	_, err := outputAgentPrimeXML(cmd, output)
	require.NoError(t, err)
	assert.Contains(t, xmlBuf.String(), "<team-knowledge>", "fixture must still render the team block")
	assert.NotContains(t, xmlBuf.String(), "<bulletin")

	var textBuf bytes.Buffer
	cmd = &cobra.Command{}
	cmd.SetOut(&textBuf)
	require.NoError(t, outputAgentPrimeText(cmd, output))
	assert.NotContains(t, textBuf.String(), "Team Bulletin Board")

	raw, err := json.Marshal(output)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "bulletin_hint")

	g := prime.BuildGuidance(prime.GuidanceParams{AgentID: "Oxtest", RepoSlug: "org/repo", TeamCtx: info})
	for _, c := range g.Commands {
		assert.False(t, strings.HasPrefix(c.Command, "ls "), "no ls row without a board: %q", c.Command)
	}
}

// TestOutputAgentPrimeXML_BulletinHintIsEscaped: the posts path is derived
// from the team checkout location, which the user chose. An '&' or '<' in it
// must be escaped in the attribute or the prime document stops parsing.
// Failure prevented: a checkout under "~/src/a&b/" producing a prime that no
// XML consumer can read.
func TestOutputAgentPrimeXML_BulletinHintIsEscaped(t *testing.T) {
	const hint = "/data/teams/a&b/<team>/bulletin/general/posts"
	output := agentPrimeOutput{
		AgentID:     "Oxtest",
		Status:      "fresh",
		TeamContext: &teamContextInfo{TeamID: "team-1", BulletinHint: hint},
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	_, err := outputAgentPrimeXML(cmd, output)
	require.NoError(t, err)
	got := buf.String()

	assert.Contains(t, got, `<bulletin dir="/data/teams/a&amp;b/&lt;team&gt;/bulletin/general/posts"`)
	assert.NotContains(t, got, `dir="/data/teams/a&b`)
	// the hint itself carries angle brackets (<slug>-<sha>) and an apostrophe
	assert.NotContains(t, got, `hint="Team bulletin board: notes posted by humans and AI coworkers on the team, unreviewed and time-limited. Useful and often credible, but they age faster than raw sources. Read a post on demand from its file. Check the matching <slug>`)
	assert.Contains(t, got, "&lt;slug&gt;-&lt;sha&gt;.meta.json")
	requireWellFormedXML(t, got)
}
