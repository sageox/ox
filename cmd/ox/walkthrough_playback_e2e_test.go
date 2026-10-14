//go:build slow

// walkthrough_playback_e2e_test.go — the executable proof for
// tests/acceptance/features/walkthrough/joined-moments.feature.
//
// Every scenario runs the real ox binary as a subprocess, behind the real
// access gate, against a real HTTP server that serves the playback document
// and its images over the network (playbackServer). Nothing about the answer
// is canned by the scenario: the moments, spans, image bytes and links come
// from the shared contract fixture; the assertions are on what the binary
// prints, what the server was asked, and what lands on disk.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/teamaccess"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

const walkthroughE2ETeam = "team_walkthrough"

// walkthroughPlaybackE2E is one hermetic ox process environment whose cloud is
// a playbackServer.
type walkthroughPlaybackE2E struct {
	oxBin     string
	workspace string
	teamPath  string
	srv       *playbackServer
	env       []string
}

// newWalkthroughPlaybackE2E builds a workspace whose project, login and team
// context all point at srv, with the walkthrough fixture synced locally.
func newWalkthroughPlaybackE2E(t *testing.T, oxBin string, srv *playbackServer) *walkthroughPlaybackE2E {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "xdg", "config")
	cacheHome := filepath.Join(root, "xdg", "cache")
	for _, d := range []string{workspace, home, configHome, cacheHome,
		filepath.Join(root, "xdg", "data"), filepath.Join(root, "xdg", "state"), filepath.Join(root, "xdg", "run")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	initConversationGitRepo(t, workspace, home)

	team := stageConversationTeamContext(t, filepath.Join(root, "teams"), conversationTeamContext{
		id: walkthroughE2ETeam, name: "Walkthrough E2E", slug: "walkthrough-e2e",
	})
	require.NoError(t, os.CopyFS(filepath.Join(team.path, "discussions"),
		os.DirFS(repoPath("..", "..", "internal", "conversation", "read", "testdata", "walkthrough", "discussions"))))
	transcript, err := os.ReadFile(playbackFixturePath("transcript.vtt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(team.path, "discussions", "2026-08-18-01-00-walkthrough", "transcript.vtt"), transcript, 0o644))

	cfg, err := json.Marshal(map[string]any{
		"config_version": "2", "repo_id": "repo_walkthrough_e2e",
		"team_id": team.id, "team_name": team.name, "endpoint": srv.URL,
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".sageox", "config.json"), cfg, 0o644))
	writeConversationLocalConfigTeams(t, workspace, []conversationTeamContext{team})

	// The login is stored under the endpoint the server listens on, and the
	// access gate's membership confirmation is seeded as a server would have
	// written it: the scenarios under test are the walkthrough's, not sign-in's.
	auth, err := json.Marshal(map[string]any{"tokens": map[string]any{
		endpoint.NormalizeEndpoint(srv.URL): map[string]any{
			"access_token": playbackTestToken, "token_type": "Bearer",
			"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "sageox"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "sageox", "auth.json"), auth, 0o600))
	require.NoError(t, teamaccess.SeedAllowed(filepath.Join(cacheHome, "sageox"), srv.URL, playbackTestToken, team.id, time.Now()))

	return &walkthroughPlaybackE2E{
		oxBin: oxBin, workspace: workspace, teamPath: team.path, srv: srv,
		env: []string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + configHome,
			"XDG_DATA_HOME=" + filepath.Join(root, "xdg", "data"),
			"XDG_STATE_HOME=" + filepath.Join(root, "xdg", "state"),
			"XDG_CACHE_HOME=" + cacheHome,
			"XDG_RUNTIME_DIR=" + filepath.Join(root, "xdg", "run"),
			"OX_XDG_ENABLE=1",
			"SAGEOX_ENDPOINT=" + srv.URL,
			// Anything that is not this server must fail loudly, not reach out.
			"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost",
		},
	}
}

func (e *walkthroughPlaybackE2E) run(t *testing.T, args ...string) string {
	t.Helper()
	out, exit, _ := testguard.RunOx(t, e.oxBin, e.workspace, e.env, append([]string{"walkthrough", shareTestWalkthroughRec}, args...)...)
	require.Equal(t, 0, exit, "ox walkthrough %v:\n%s", args, out)
	return out
}

// runJSON runs ox walkthrough and decodes the envelope's data payload.
func (e *walkthroughPlaybackE2E) runJSON(t *testing.T, args ...string) (string, joinedDataWire, *conversationE2EEnvelope) {
	t.Helper()
	out := e.run(t, args...)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, out)
	var data joinedDataWire
	decodeConversationData(t, env, &data)
	return out, data, env
}

func requireNoSignedLinks(t *testing.T, srv *playbackServer, out string) {
	t.Helper()
	for _, secret := range []string{"TOPSECRET", "X-Signature", srv.URL + "/objects"} {
		require.NotContains(t, out, secret, "an image link is a short-lived credential and must never be printed")
	}
}

// TestWalkthroughJoinedMomentsE2E is the proof behind the "Reading a Visual
// Walkthrough as Joined Moments" feature. Each subtest is the scenario of the
// same name, run against the compiled binary and a live HTTP server.
func TestWalkthroughJoinedMomentsE2E(t *testing.T) {
	oxBin := testguard.BuildOxBinary(t, findConversationProjectRoot(t))

	// start returns a workspace wired to a fresh server serving the contract
	// example (or the given document).
	start := func(t *testing.T, artifact string) (*walkthroughPlaybackE2E, *playbackServer) {
		t.Helper()
		srv := startPlaybackServer(t, artifact)
		return newWalkthroughPlaybackE2E(t, oxBin, srv), srv
	}

	// --- Rule: Each moment joins the words, the element, and what happened next ---

	t.Run("Avery reads a click together with what Devon said", func(t *testing.T) {
		e2e, srv := start(t, "")
		out, data, env := e2e.runJSON(t)

		require.Len(t, data.Moments, 5)
		click := data.Moments[0]
		require.Equal(t, "click", click.Kind)
		require.Equal(t, "So when I land here, make this bigger.", click.Said)
		require.Equal(t, "Start free trial", click.Element.Name)
		require.Equal(t, "pointing-hand", click.Cursor)
		require.Equal(t, `sheet “Start your trial”`, click.Then.Label)
		require.Equal(t, 1, srv.playbackRequests(), "one lookup answers the whole read")
		require.Contains(t, env.Guidance, "untrusted", "quotes and screen text stay evidence, never instructions")
		requireNoSignedLinks(t, srv, out)

		text := e2e.run(t, "--text")
		require.Contains(t, text, `00:00:41  [7]  click  button "Start free trial"   cursor: pointing-hand`+"\n")
		require.Contains(t, text, `      said:   "So when I land here, make this bigger."`+"\n")
		require.Contains(t, text, `      then:   sheet "Start your trial" 0.4 s later`+"\n")
	})

	t.Run("Avery reads selected text as a moment of its own", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, `00:01:35  [8]  selection  "Annual billing saves 20%"`+"\n")
		require.Contains(t, out, `      said:   "And this line should say billing instead."`+"\n")
	})

	t.Run("Avery reads a plural reference and an unnamed target honestly", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, `00:03:20  [11]  click  "Pro plan", "Team plan" +1   cursor: pointing-hand`+"\n")
		require.Contains(t, out, `00:02:00  [9]  dwell  in "Plan comparison"   cursor: arrow`+"\n")
	})

	// --- Rule: Chapters let Avery read one part at a time ---

	t.Run("Avery sees the walkthrough split into chapters", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		for _, want := range []string{
			"── Pricing  00:00:00  acme.example/pricing",
			"── Checkout  00:00:41  acme.example/checkout",
			"── Plan comparison  00:02:00",
		} {
			require.Contains(t, out, want+"\n")
		}
		at := func(needle string) int { return strings.Index(out, needle) }
		require.Less(t, at("── Pricing"), at("00:00:41  [7]  click"))
		require.Less(t, at("00:00:41  [7]  click"), at("── Checkout"))
		require.Less(t, at("── Checkout"), at("00:01:35  [8]  selection"))
		require.Less(t, at("00:01:35  [8]  selection"), at("── Plan comparison"))

		_, data, _ := e2e.runJSON(t)
		require.Len(t, data.Chapters, 3)
	})

	t.Run("Avery reads one chapter", func(t *testing.T) {
		e2e, _ := start(t, "")
		_, data, _ := e2e.runJSON(t, "--chapter", "Checkout")
		require.Len(t, data.Moments, 1)
		require.Equal(t, "selection", data.Moments[0].Kind)
		require.Len(t, data.Chapters, 1)
		require.Equal(t, "Checkout", data.Chapters[0].Title)
		require.Len(t, data.Spans, 1)
		require.Len(t, data.Shortcuts, 1)
	})

	t.Run("Avery reads a long chapter's words page by page", func(t *testing.T) {
		e2e, _ := start(t, "")
		var long strings.Builder
		long.WriteString("WEBVTT\n\n")
		clock := func(sec int) string { return fmt.Sprintf("%02d:%02d:%02d.000", sec/3600, sec/60%60, sec%60) }
		for i := 0; i < 250; i++ {
			fmt.Fprintf(&long, "%s --> %s\ncue number %d\n\n", clock(i), clock(i+1), i+1)
		}
		require.NoError(t, os.WriteFile(filepath.Join(e2e.teamPath, "discussions", "2026-08-18-01-00-walkthrough", "transcript.vtt"), []byte(long.String()), 0o644))

		seen := map[int]int{}
		cursor, pages := "", 0
		for {
			args := []string{"--chapter", "Plan comparison", "--transcript"}
			if cursor != "" {
				args = append(args, "--cursor", cursor)
			}
			env, _ := decodeConversationEnvelope(t, e2e.run(t, args...))
			require.True(t, env.Success)
			var page struct {
				Transcript struct {
					Cues       []struct{ N int } `json:"cues"`
					NextCursor string            `json:"next_cursor"`
				} `json:"transcript"`
			}
			decodeConversationData(t, env, &page)
			for _, c := range page.Transcript.Cues {
				seen[c.N]++
			}
			pages++
			cursor = page.Transcript.NextCursor
			if cursor == "" {
				break
			}
			require.Contains(t, env.Guidance, "--chapter 'Plan comparison' --transcript --cursor "+cursor, "the offered continuation stays in the chapter")
			require.Less(t, pages, 6)
		}
		require.Equal(t, 2, pages)
		require.Len(t, seen, 131, "cues 120-250 overlap the chapter")
		for n, count := range seen {
			require.Equal(t, 1, count, "cue %d served %d times", n, count)
		}
	})

	t.Run("Avery limits the read and is told chapters were left out", func(t *testing.T) {
		e2e, _ := start(t, "")
		_, data, _ := e2e.runJSON(t, "--limit", "2")
		require.Len(t, data.Chapters, 2)
		require.Contains(t, strings.Join(data.Notes, "\n"), "Chapters are capped at 2 of 3; read the rest with --chapter")
	})

	t.Run("Avery names a chapter that is not there", func(t *testing.T) {
		e2e, _ := start(t, "")
		out, exit, _ := testguard.RunOx(t, e2e.oxBin, e2e.workspace, e2e.env, "walkthrough", shareTestWalkthroughRec, "--chapter", "Settings")
		require.Equal(t, 2, exit, out)
		env, _ := decodeConversationEnvelope(t, out)
		require.False(t, env.Success)
		require.Contains(t, env.Error.Message, `no chapter is titled "Settings"`)
		for _, listed := range []string{`"Pricing" at 00:00:00`, `"Checkout" at 00:00:41.400`, `"Plan comparison" at 00:02:00`} {
			require.Contains(t, env.Error.Message, listed)
		}
		require.Empty(t, env.Data, "no walkthrough comes back in its place")
	})

	t.Run("Avery asks for a chapter of a recording that has none", func(t *testing.T) {
		srv := startPlaybackServer(t, "")
		srv.setPlaybackStatus(http.StatusNotFound)
		e2e := newWalkthroughPlaybackE2E(t, oxBin, srv)
		out, exit, _ := testguard.RunOx(t, e2e.oxBin, e2e.workspace, e2e.env, "walkthrough", shareTestWalkthroughRec, "--chapter", "Pricing")
		require.Equal(t, 2, exit, out)
		env, _ := decodeConversationEnvelope(t, out)
		require.False(t, env.Success)
		require.Contains(t, env.Error.Message, "does not have")
		require.Contains(t, env.Error.Message, "Playback data is not available for this recording")
	})

	// --- Rule: Shortcuts, screen changes and extra tracks are said plainly ---

	t.Run("Avery sees a keyboard shortcut and how often Devon pressed it", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, "00:01:01  shortcut  ⌘K ×2\n")
		require.Contains(t, out, "00:02:05  shortcut  Esc\n")
		_, data, _ := e2e.runJSON(t)
		require.Len(t, data.Shortcuts, 2)
		require.Equal(t, 2, data.Shortcuts[0].Count)
	})

	t.Run("Avery sees where the screen changed in words", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, "      then:   screen changed (top-right) 0.7 s later\n")
		require.NotContains(t, out, "0.55", "no coordinates in the text")
	})

	t.Run("Avery learns Devon had a camera and cleaned audio without being handed them", func(t *testing.T) {
		e2e, srv := start(t, "")
		out, data, _ := e2e.runJSON(t)
		require.True(t, data.Sources.Camera)
		require.True(t, data.Sources.Audio)
		require.Contains(t, e2e.run(t, "--text"), "camera track (not offered)")
		for _, leaked := range []string{"camera.v1", "narration.v1", "TOPSECRET-camera", "TOPSECRET-audio"} {
			require.NotContains(t, out, leaked)
		}
		e2e.run(t, "--overview")
		for _, p := range srv.images() {
			require.NotContains(t, p, "camera", "no camera frame is ever requested")
			require.NotContains(t, p, "narration")
		}
	})

	// --- Rule: Quiet stretches and typing collapse to one line ---

	t.Run("Avery sees a long pause as a single line", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, "00:00:52–00:01:30  quiet   38 s, nothing said, pointer still\n")
		require.Equal(t, 1, strings.Count(out, "nothing said, pointer still"), "a 38 second pause is one line, not 38 seconds of rows")
	})

	t.Run("Avery sees typing as a single line naming the field", func(t *testing.T) {
		e2e, _ := start(t, "")
		out := e2e.run(t, "--text")
		require.Contains(t, out, "00:02:10–00:02:24  typing  14 s, typing in text field \"Search plans\"\n")
	})

	// --- Rule: Pictures come only when Avery asks, and links never show ---

	t.Run("Avery asks for the pictures and gets files she can open", func(t *testing.T) {
		e2e, srv := start(t, "")
		out, data, _ := e2e.runJSON(t, "--fetch")
		requireNoSignedLinks(t, srv, out)

		require.Len(t, srv.images(), 4, "one focus picture per moment that has one")
		for _, auth := range srv.imageAuth {
			require.Empty(t, auth, "Devon's login is never sent to the picture store")
		}
		cache := filepath.Join(e2e.teamPath, ".sageox", "cache", "walkthrough-playback")
		for _, i := range []int{0, 1, 2, 4} {
			path := data.Moments[i].Focus.LocalImage
			require.True(t, strings.HasPrefix(path, cache), "pictures live in the team context cache: %s", path)
			f, err := os.Open(path)
			require.NoError(t, err)
			_, err = jpeg.DecodeConfig(f)
			require.NoError(t, err, "what lands on disk is a picture")
			require.NoError(t, f.Close())
		}
	})

	t.Run("Avery reads again and nothing is downloaded twice", func(t *testing.T) {
		e2e, srv := start(t, "")
		e2e.run(t, "--fetch")
		require.Len(t, srv.images(), 4)

		again := e2e.run(t, "--fetch")
		requireNoSignedLinks(t, srv, again)
		require.Len(t, srv.images(), 4, "the second run is served from disk")
		cache := filepath.Join(e2e.teamPath, ".sageox", "cache", "walkthrough-playback")
		require.Contains(t, e2e.run(t, "--text"), "      focus:  "+cache)
	})

	t.Run("A picture that was cut short is fetched again", func(t *testing.T) {
		e2e, srv := start(t, "")
		_, data, _ := e2e.runJSON(t, "--fetch")
		damaged := data.Moments[0].Focus.LocalImage
		require.NoError(t, os.WriteFile(damaged, truncatedPlaybackJPEG(t), 0o644))
		before := len(srv.images())

		_, data, _ = e2e.runJSON(t, "--fetch")
		require.Len(t, srv.images(), before+1, "only the damaged picture is fetched again")
		require.Equal(t, damaged, data.Moments[0].Focus.LocalImage)
		whole, err := os.ReadFile(damaged)
		require.NoError(t, err)
		_, err = jpeg.Decode(bytes.NewReader(whole))
		require.NoError(t, err, "the replacement is a whole picture")
	})

	t.Run("Avery asks for the whole-frame picture as well", func(t *testing.T) {
		e2e, srv := start(t, "")
		out, data, _ := e2e.runJSON(t, "--overview")
		requireNoSignedLinks(t, srv, out)
		require.Len(t, srv.images(), 8, "four zoomed pictures and four whole-frame pictures")
		require.FileExists(t, data.Moments[0].Overview.LocalImage)
		require.Contains(t, e2e.run(t, "--text"), "      overview: ")
	})

	t.Run("Avery asks for a very long stretch", func(t *testing.T) {
		var moments []string
		for i := 1; i <= 12; i++ {
			moments = append(moments, fmt.Sprintf(`{"id":"m%d","t_ms":%d,"kind":"click","cue":1,"said":"click %d","focus_image":{"width":100,"height":60,"url":"https://objects.example.test/focus/m%d.jpg?X-Signature=TOPSECRET-%d"}}`, i, i*1000, i, i, i))
		}
		e2e, srv := start(t, `{"spec":"walkthrough-playback/1","recording_id":"`+shareTestWalkthroughRec+`","generated_at":"2026-10-14T18:00:00Z","duration_ms":20000,"moments":[`+strings.Join(moments, ",")+`]}`)

		out, _, env := e2e.runJSON(t, "--fetch")
		requireNoSignedLinks(t, srv, out)
		require.Len(t, srv.images(), 8)
		require.Contains(t, strings.Join(env.Warnings, "\n"), "image fetch budget reached (8); narrow the cue/time window")
	})

	t.Run("One picture cannot be fetched", func(t *testing.T) {
		srv := startPlaybackServer(t, "")
		srv.setImageStatus("/objects/focus/m2.jpg", http.StatusForbidden)
		e2e := newWalkthroughPlaybackE2E(t, oxBin, srv)

		out, data, env := e2e.runJSON(t, "--fetch")
		requireNoSignedLinks(t, srv, out)
		require.Len(t, data.Moments, 5, "every moment is still there")
		require.Equal(t, "local", data.Moments[0].Focus.Availability)
		require.Equal(t, "unfetched", data.Moments[1].Focus.Availability)
		require.Contains(t, strings.Join(env.Warnings, "\n"), "focus image at 00:01:35.210 unavailable")
	})

	t.Run("Avery never sees a picture link", func(t *testing.T) {
		e2e, srv := start(t, "")
		for _, args := range [][]string{{}, {"--text"}, {"--fetch"}, {"--fetch", "--text"}, {"--overview"}, {"--cues", "7-8", "--fetch"}} {
			requireNoSignedLinks(t, srv, e2e.run(t, args...))
		}
		require.NotEmpty(t, srv.images(), "the pictures really were downloaded in these runs")
	})

	// --- Rule: A walkthrough without this data reads exactly as before ---

	t.Run("Avery reads a recording that has no joined moments yet", func(t *testing.T) {
		srv := startPlaybackServer(t, "")
		srv.setPlaybackStatus(http.StatusNotFound)
		e2e := newWalkthroughPlaybackE2E(t, oxBin, srv)

		out, data, _ := e2e.runJSON(t)
		require.True(t, data.ScreenRecording)
		kinds := map[string]bool{}
		for _, m := range data.Moments {
			kinds[m.Kind] = true
			require.Empty(t, m.Said, "no joined narration without playback data")
		}
		require.True(t, kinds["keyframe"] && kinds["click"], "the standard evidence index is intact: %v", kinds)
		require.Empty(t, data.Spans)
		require.Contains(t, strings.Join(data.Notes, "\n"), "Playback data is not available for this recording")
		requireNoSignedLinks(t, srv, out)
	})

	t.Run("The server cannot be reached", func(t *testing.T) {
		srv := startPlaybackServer(t, "")
		e2e := newWalkthroughPlaybackE2E(t, oxBin, srv)
		srv.setPlaybackStatus(http.StatusServiceUnavailable)

		_, data, _ := e2e.runJSON(t)
		require.NotEmpty(t, data.Moments)
		require.Contains(t, strings.Join(data.Notes, "\n"), "could not be fetched")
	})
}
