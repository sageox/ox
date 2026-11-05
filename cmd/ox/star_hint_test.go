package main

// star_hint_test.go covers the one-line GitHub star ask (star_hint.go).
//
// Every test drives the real begin/show pair against an httptest server and
// asserts EFFECTS: the bytes written to stderr, how many requests reached the
// server, and what the state file says afterwards. The eligible run is the
// positive control for every suppression row: a row that only checks "nothing
// printed" would also pass if the hint were broken for an unrelated reason.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	starHintTestUserID = "usr_star_test"
	starHintTestAccess = "access-token-star-test"
	starHintExpectLine = "★ Enjoying ox? If ox has been useful, a star on GitHub helps other teams find it. %s/cli\n"
)

// starHintTestToken is what the real server would mint for this card: opaque to
// the client, so any URL-safe string works, but a realistic one catches path
// escaping mistakes.
var starHintTestToken = base64.RawURLEncoding.EncodeToString([]byte(`{"o":"star-ox-github","s":"user"}`))

// starHintServer is a fake api-go: it serves the cli.hint card and records every
// request so tests can count them.
type starHintServer struct {
	*httptest.Server

	gets  atomic.Int32
	seens atomic.Int32

	mu         sync.Mutex
	getStatus  int
	getBody    string
	getDelay   time.Duration
	seenPaths  []string
	seenAuth   []string
	seenQuery  []string
	getAuths   []string
	getQueries []string
}

// starHintCardJSON marshals a server answer. json.Marshal, not a format string:
// the hostile rows carry control characters, and a body that is not valid JSON
// would be rejected for the wrong reason.
func starHintCardJSON(nudgeID, title, body, href, token string) string {
	raw, err := json.Marshal(map[string]any{
		"placement": "cli.hint",
		"card": map[string]any{
			"nudge_id":      nudgeID,
			"kind":          "ask",
			"subject":       "user",
			"title":         title,
			"body":          body,
			"cta":           map[string]any{"label": "Star on GitHub", "href": href},
			"dismiss_label": "Dismiss",
			"art":           map[string]any{"asset": "brand/oxy-cta", "fit": "contain"},
			"first_view":    true,
			"token":         token,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func newStarHintServer(t *testing.T) *starHintServer {
	t.Helper()
	s := &starHintServer{
		getStatus: http.StatusOK,
		getBody: starHintCardJSON("star-ox-github", "Enjoying ox?",
			"If ox has been useful, a star on GitHub helps other teams find it.", "/cli", starHintTestToken),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status, body, delay := s.getStatus, s.getBody, s.getDelay
		s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/me/nudges":
			s.gets.Add(1)
			s.mu.Lock()
			s.getAuths = append(s.getAuths, r.Header.Get("Authorization"))
			s.getQueries = append(s.getQueries, r.URL.RawQuery)
			s.mu.Unlock()
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodPost:
			s.seens.Add(1)
			s.mu.Lock()
			s.seenPaths = append(s.seenPaths, r.URL.EscapedPath())
			s.seenAuth = append(s.seenAuth, r.Header.Get("Authorization"))
			s.seenQuery = append(s.seenQuery, r.URL.RawQuery)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *starHintServer) answer(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getStatus, s.getBody = status, body
}

func (s *starHintServer) delayGets(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getDelay = d
}

// starHintEnv is one signed-in person at a terminal, running `ox status`
// against the fake server, with every process-global the hint reads pinned.
type starHintEnv struct {
	t   *testing.T
	srv *starHintServer
}

// starHintClosedStderr stands in for a stderr that cannot be written.
type starHintClosedStderr struct{}

func (starHintClosedStderr) Write([]byte) (int, error) { return 0, errors.New("stderr closed") }

func newStarHintEnv(t *testing.T) *starHintEnv {
	t.Helper()
	srv := newStarHintServer(t)

	useTestCacheDir(t)
	atTerminal(t)
	t.Cleanup(func() { updatenotice.SetMachineOutput(false) })

	oldPath, oldAutomated, oldStarted, oldPending, oldCtx := starHintStatePath, starHintAutomated, startedByOx, starHintPending, cliCtx
	starHintStatePath = filepath.Join(t.TempDir(), "star-hint.json")
	starHintAutomated = func() bool { return false } // a person, whatever runs `go test`
	startedByOx = false
	starHintPending = nil
	t.Cleanup(func() {
		starHintStatePath, starHintAutomated, startedByOx, starHintPending, cliCtx = oldPath, oldAutomated, oldStarted, oldPending, oldCtx
	})

	t.Setenv(endpoint.EnvVar, srv.URL)
	t.Setenv(auth.EnvVarToken, "")
	require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
		AccessToken: starHintTestAccess,
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    auth.UserInfo{UserID: starHintTestUserID},
	}))
	t.Cleanup(func() { _ = auth.RemoveTokenForEndpoint(srv.URL) })

	e := &starHintEnv{t: t, srv: srv}
	e.useCommand("status")
	return e
}

// useCommand makes the current invocation `ox <path...>`.
func (e *starHintEnv) useCommand(path ...string) {
	e.t.Helper()
	root := &cobra.Command{Use: "ox"}
	parent := root
	for _, name := range path {
		child := &cobra.Command{Use: name}
		parent.AddCommand(child)
		parent = child
	}
	cliCtx = &cli.Context{Cmd: parent, Config: &config.Config{}}
}

// run is one whole command lifecycle: pre-run, then exit. It returns what the
// person would see on stderr.
func (e *starHintEnv) run(exitCode int) string {
	e.t.Helper()
	beginStarHint()
	var stderr bytes.Buffer
	showStarHint(exitCode, &stderr)
	return ansi.Strip(stderr.String())
}

func (e *starHintEnv) key() string {
	return endpoint.NormalizeEndpoint(e.srv.URL) + "|" + starHintTestUserID
}

func (e *starHintEnv) entry() starHintEntry {
	e.t.Helper()
	state, err := readStarHintState()
	require.NoError(e.t, err)
	return state.entry(e.key())
}

func (e *starHintEnv) seed(mutate func(*starHintEntry)) {
	e.t.Helper()
	require.NoError(e.t, updateStarHintEntry(e.key(), mutate))
}

func (e *starHintEnv) wantLine() string {
	return ansi.Strip(fmt.Sprintf(starHintExpectLine, endpoint.NormalizeEndpoint(e.srv.URL)))
}

// --- the eligible run: everything else is judged against this ---

// Failure prevented: the ask never reaches an eligible person, or reaches them
// without telling the server (so the server never rests the card).
func TestStarHint_PrintsOnceWhenEligible(t *testing.T) {
	e := newStarHintEnv(t)

	out := e.run(0)

	assert.Equal(t, e.wantLine(), out, "the exact one-line contract format")
	assert.EqualValues(t, 1, e.srv.gets.Load())
	assert.EqualValues(t, 1, e.srv.seens.Load(), "the server is told the card was shown")

	entry := e.entry()
	assert.Equal(t, 1, entry.Shown)
	assert.WithinDuration(t, time.Now(), entry.LastShown, 5*time.Second)

	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	assert.Equal(t, []string{"placement=cli.hint"}, e.srv.getQueries)
	assert.Equal(t, []string{"Bearer " + starHintTestAccess}, e.srv.getAuths)
	assert.Equal(t, []string{"/api/v1/users/me/nudges/" + starHintTestToken + "/seen"}, e.srv.seenPaths, "seen carries the card's own token")
	assert.Equal(t, []string{"Bearer " + starHintTestAccess}, e.srv.seenAuth)
	assert.Equal(t, []string{"placement=cli.hint"}, e.srv.seenQuery)
}

// --- who never sees it ---

// Failure prevented: the ask lands in machine output, an AI coworker's
// transcript, automation, or a credential that has no person behind it.
func TestStarHint_SilentWhenNobodyIsReading(t *testing.T) {
	tests := []struct {
		name string
		arm  func(t *testing.T, e *starHintEnv)
	}{
		{name: "--json or --text", arm: func(t *testing.T, _ *starHintEnv) { updatenotice.SetMachineOutput(true) }},
		{name: "stderr is not a terminal", arm: func(t *testing.T, _ *starHintEnv) {
			updatenotice.StderrIsTTY = func() bool { return false }
		}},
		{name: "ox agent prime", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("agent") }},
		{name: "ox agent hook (agent hooks)", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("agent", "hook") }},
		{name: "git hook", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("hooks", "pre-push") }},
		{name: "git credential helper", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("git-credential-helper") }},
		{name: "daemon", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("daemon", "start") }},
		{name: "shell completion", arm: func(t *testing.T, e *starHintEnv) { e.useCommand("__complete") }},
		{name: "started by another ox (OX_STARTED_BY_OX)", arm: func(t *testing.T, _ *starHintEnv) { startedByOx = true }},
		{name: "CI or an agent context", arm: func(t *testing.T, _ *starHintEnv) {
			starHintAutomated = func() bool { return true }
		}},
		{name: "a service process", arm: func(t *testing.T, _ *starHintEnv) { cliCtx.LongRunning = true }},
		{name: "--quiet", arm: func(t *testing.T, _ *starHintEnv) { cliCtx.Config.Quiet = true }},
		{name: "tips are disabled", arm: func(t *testing.T, _ *starHintEnv) {
			userCfg, err := config.LoadUserConfig()
			require.NoError(t, err)
			off := false
			userCfg.TipsEnabled = &off
			require.NoError(t, config.SaveUserConfig(userCfg))
			t.Cleanup(func() {
				userCfg.TipsEnabled = nil
				require.NoError(t, config.SaveUserConfig(userCfg))
			})
		}},
		{name: "team (oxt_) token", arm: func(t *testing.T, _ *starHintEnv) { t.Setenv(auth.EnvVarToken, validTeamToken) }},
		{name: "env-supplied personal token (automation has no local person)", arm: func(t *testing.T, _ *starHintEnv) {
			t.Setenv(auth.EnvVarToken, validPersonalToken)
		}},
		{name: "signed out", arm: func(t *testing.T, e *starHintEnv) {
			require.NoError(t, auth.RemoveTokenForEndpoint(e.srv.URL))
		}},
		{name: "expired login", arm: func(t *testing.T, e *starHintEnv) {
			require.NoError(t, auth.SaveTokenForEndpoint(e.srv.URL, &auth.StoredToken{
				AccessToken: starHintTestAccess,
				ExpiresAt:   time.Now().Add(-time.Hour),
				UserInfo:    auth.UserInfo{UserID: starHintTestUserID},
			}))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newStarHintEnv(t)
			tt.arm(t, e)

			out := e.run(0)

			assert.Empty(t, out)
			assert.EqualValues(t, 0, e.srv.seens.Load())
			assert.Equal(t, 0, e.entry().Shown)
			assert.EqualValues(t, 0, e.srv.gets.Load(), "a gated invocation must not touch the network")
		})
	}
}

// The control for the table above: the same environment with nothing armed.
func TestStarHint_SuppressionTableControlPrints(t *testing.T) {
	e := newStarHintEnv(t)
	assert.Equal(t, e.wantLine(), e.run(0))
}

// Failure prevented: a cheerful ask under an error message. The refresh may
// have started; what must not happen is a line, a seen report, or a stamp.
func TestStarHint_SilentAfterAFailedCommand(t *testing.T) {
	e := newStarHintEnv(t)

	beginStarHint()
	run := starHintPending
	require.NotNil(t, run, "the refresh starts before the exit code is known")
	var stderr bytes.Buffer
	showStarHint(1, &stderr)
	<-run.refreshed // production lets exit kill it; the test lets it finish so it cannot outlive the sandbox

	assert.Empty(t, stderr.String())
	assert.EqualValues(t, 0, e.srv.seens.Load())
	assert.Equal(t, 0, e.entry().Shown)
}

// Failure prevented: `ox logout` (or a login as someone else) is followed by a
// "Enjoying ox?" addressed to the person who just left.
func TestStarHint_SilentWhenTheCommandChangedWhoIsSignedIn(t *testing.T) {
	e := newStarHintEnv(t)

	beginStarHint()
	require.NoError(t, auth.RemoveTokenForEndpoint(e.srv.URL))
	var stderr bytes.Buffer
	showStarHint(0, &stderr)

	assert.Empty(t, stderr.String())
	assert.EqualValues(t, 0, e.srv.seens.Load())
	assert.Equal(t, 0, e.entry().Shown)
}

// --- what the server may say ---

// Failure prevented: a newer server's card, a malformed card, or hostile text
// is printed. Nothing prints, nothing is reported seen, nothing is stamped.
func TestStarHint_OnlyTheStarCardPrints(t *testing.T) {
	const title, body = "Enjoying ox?", "If ox has been useful, a star on GitHub helps other teams find it."
	tests := []struct {
		name string
		body string
	}{
		{name: "another nudge_id", body: starHintCardJSON("some-other-card", title, body, "/cli", starHintTestToken)},
		{name: "card: null", body: `{"placement":"cli.hint","card":null}`},
		{name: "no card key", body: `{"placement":"cli.hint"}`},
		{name: "off-site href", body: starHintCardJSON("star-ox-github", title, body, "https://evil.example/cli", starHintTestToken)},
		{name: "protocol-relative href", body: starHintCardJSON("star-ox-github", title, body, "//evil.example/cli", starHintTestToken)},
		{name: "backslash href", body: starHintCardJSON("star-ox-github", title, body, `/\evil.example`, starHintTestToken)},
		{name: "escape in href", body: starHintCardJSON("star-ox-github", title, body, "/cli\x1b[2J", starHintTestToken)},
		{name: "empty title", body: starHintCardJSON("star-ox-github", "", body, "/cli", starHintTestToken)},
		{name: "title that is only escapes", body: starHintCardJSON("star-ox-github", "\x1b]8;;https://evil.example\x07", body, "/cli", starHintTestToken)},
		{name: "no token to report with", body: starHintCardJSON("star-ox-github", title, body, "/cli", "")},
		{name: "garbage", body: `not json at all`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newStarHintEnv(t)
			e.srv.answer(http.StatusOK, tt.body)

			assert.Empty(t, e.run(0))
			assert.EqualValues(t, 0, e.srv.seens.Load())
			assert.Equal(t, 0, e.entry().Shown, "a line nobody saw does not spend the budget")
		})
	}
}

// Failure prevented: a hostile server smuggles a hyperlink or clipboard write
// to the terminal through the card text (the repo treats OSC 8 and OSC 52 as
// smuggling vectors). The words survive, the escapes do not.
func TestStarHint_StripsTerminalEscapesFromCardText(t *testing.T) {
	e := newStarHintEnv(t)
	e.srv.answer(http.StatusOK, starHintCardJSON("star-ox-github",
		"Enjoying \x1b]8;;https://evil.example\x07ox?\x1b]8;;\x07",
		"A star\x1b]52;c;ZXZpbA==\x07 helps.\r\nForged second line.",
		"/cli", starHintTestToken))

	var stderr bytes.Buffer
	beginStarHint()
	showStarHint(0, &stderr) // raw: NOT passed through ansi.Strip like e.run

	out := stderr.String()
	assert.NotContains(t, out, "\x1b]", "no OSC sequence reaches the terminal")
	assert.NotContains(t, out, "\x07")
	assert.NotContains(t, out[:len(out)-1], "\n", "exactly one line")
	assert.Contains(t, out, "Enjoying ox?")
	assert.NotContains(t, out, "evil.example", "the hyperlink target went with its escape")
	assert.Contains(t, out, "A star helps.", "the words around a stripped escape survive")
}

// --- the caps ---

// Failure prevented: the ask comes back the next day.
func TestStarHint_SecondShowNeedsAWeek(t *testing.T) {
	tests := []struct {
		name      string
		lastShown time.Duration // how long ago
		wantShow  bool
	}{
		{name: "an hour ago", lastShown: time.Hour, wantShow: false},
		{name: "three days ago", lastShown: 3 * 24 * time.Hour, wantShow: false},
		{name: "a minute short of a week", lastShown: 7*24*time.Hour - time.Minute, wantShow: false},
		{name: "a week and a minute", lastShown: 7*24*time.Hour + time.Minute, wantShow: true},
		{name: "a month", lastShown: 30 * 24 * time.Hour, wantShow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newStarHintEnv(t)
			e.seed(func(en *starHintEntry) {
				en.Shown = 1
				en.LastShown = time.Now().Add(-tt.lastShown)
			})

			out := e.run(0)

			if tt.wantShow {
				assert.Equal(t, e.wantLine(), out)
				assert.Equal(t, 2, e.entry().Shown)
				return
			}
			assert.Empty(t, out)
			assert.Equal(t, 1, e.entry().Shown)
			assert.EqualValues(t, 0, e.srv.gets.Load(), "too soon means no network call at all")
		})
	}
}

// Failure prevented: the hard cap leaks and the ask becomes a nag. Two shows
// ever, however much time passes, and a third never even asks the server.
func TestStarHint_TwoShowsEverThenNeverAgain(t *testing.T) {
	e := newStarHintEnv(t)
	longAgo := func(en *starHintEntry) {
		en.LastShown = time.Now().Add(-30 * 24 * time.Hour)
		en.CheckAgainAt = time.Time{} // also expire the cached answer
	}

	assert.Equal(t, e.wantLine(), e.run(0), "first show")
	e.seed(longAgo)
	assert.Equal(t, e.wantLine(), e.run(0), "second show, a month later")
	assert.Equal(t, 2, e.entry().Shown)

	e.seed(longAgo)
	getsBefore, seensBefore := e.srv.gets.Load(), e.srv.seens.Load()
	assert.Empty(t, e.run(0), "no third show, however long ago the second was")
	assert.Equal(t, getsBefore, e.srv.gets.Load(), "a capped person never costs a request")
	assert.Equal(t, seensBefore, e.srv.seens.Load())
	assert.Equal(t, 2, e.entry().Shown)
}

// Failure prevented: one person's shows use up another's, or a test endpoint's
// shows use up production's.
func TestStarHint_CapIsPerEndpointAndPerson(t *testing.T) {
	e := newStarHintEnv(t)
	cappedElsewhere := func(en *starHintEntry) { en.Shown = starHintMaxShows; en.LastShown = time.Now() }
	require.NoError(t, updateStarHintEntry("https://sageox.ai|"+starHintTestUserID, cappedElsewhere))
	require.NoError(t, updateStarHintEntry(endpoint.NormalizeEndpoint(e.srv.URL)+"|usr_someone_else", cappedElsewhere))

	assert.Equal(t, e.wantLine(), e.run(0))
}

// --- the answer cache ---

// Failure prevented: every command of the day asks the server again.
func TestStarHint_AnswerIsReusedForADay(t *testing.T) {
	t.Run("a null card is cached", func(t *testing.T) {
		e := newStarHintEnv(t)
		e.srv.answer(http.StatusOK, `{"placement":"cli.hint","card":null}`)

		assert.Empty(t, e.run(0))
		assert.Empty(t, e.run(0))
		assert.Empty(t, e.run(0))
		assert.EqualValues(t, 1, e.srv.gets.Load(), "one GET for three commands")
	})

	t.Run("a card is shown from the cache with no second GET", func(t *testing.T) {
		e := newStarHintEnv(t)
		assert.Equal(t, e.wantLine(), e.run(0))
		require.EqualValues(t, 1, e.srv.gets.Load())

		// a week later the spacing allows it again, but the answer is still fresh
		e.seed(func(en *starHintEntry) { en.LastShown = time.Now().Add(-8 * 24 * time.Hour) })
		assert.Equal(t, e.wantLine(), e.run(0))
		assert.EqualValues(t, 1, e.srv.gets.Load(), "reused, not refetched")
		assert.EqualValues(t, 2, e.srv.seens.Load(), "but each show is still reported")
	})

	t.Run("an expired answer is fetched again", func(t *testing.T) {
		e := newStarHintEnv(t)
		e.srv.answer(http.StatusOK, `{"placement":"cli.hint","card":null}`)
		assert.Empty(t, e.run(0))

		e.seed(func(en *starHintEntry) { en.CheckAgainAt = time.Now().Add(-time.Minute) })
		e.srv.answer(http.StatusOK, starHintCardJSON("star-ox-github", "Enjoying ox?",
			"If ox has been useful, a star on GitHub helps other teams find it.", "/cli", starHintTestToken))

		assert.Equal(t, e.wantLine(), e.run(0), "the new answer is used by the very command that fetched it")
		assert.EqualValues(t, 2, e.srv.gets.Load())
	})

	t.Run("the answer is trusted for about a day, not longer", func(t *testing.T) {
		e := newStarHintEnv(t)
		e.run(0)
		fresh := e.entry().CheckAgainAt
		assert.WithinDuration(t, time.Now().Add(starHintAnswerTTL), fresh, time.Minute)
	})
}

// --- stamping ---

// Failure prevented: a line that never reached a terminal spends one of the two
// shows, so the person never sees it.
func TestStarHint_NotStampedWhenNothingWasPrinted(t *testing.T) {
	e := newStarHintEnv(t)

	beginStarHint()
	showStarHint(0, starHintClosedStderr{})

	assert.Equal(t, 0, e.entry().Shown)
	assert.EqualValues(t, 0, e.srv.seens.Load(), "not reported as seen either")

	// and the next command still has its full budget
	assert.Equal(t, e.wantLine(), e.run(0))
	assert.Equal(t, 1, e.entry().Shown)
}

// --- an older or unwell server ---

// Failure prevented: an older server's 404 (or its unknown_placement answer) is
// retried by every command, or surfaced to the person.
func TestStarHint_OlderServerIsQuietForADay(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "404", status: http.StatusNotFound, body: `{"error":"not found"}`},
		{name: "unknown_placement", status: http.StatusOK, body: `{"placement":"cli.hint","card":null,"unknown_placement":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newStarHintEnv(t)
			e.srv.answer(tt.status, tt.body)

			assert.Empty(t, e.run(0))
			assert.Empty(t, e.run(0))
			assert.EqualValues(t, 1, e.srv.gets.Load(), "asked once, then quiet")
			assert.EqualValues(t, 0, e.srv.seens.Load())
			assert.Equal(t, 0, e.entry().Shown)
			assert.WithinDuration(t, time.Now().Add(starHintAnswerTTL), e.entry().CheckAgainAt, time.Minute)
		})
	}
}

// Failure prevented: a server that is down costs every command a request and a
// wait. A failure is silent and backs off for an hour, then tries again.
func TestStarHint_FailureIsSilentAndBacksOff(t *testing.T) {
	e := newStarHintEnv(t)
	e.srv.answer(http.StatusInternalServerError, `boom`)

	assert.Empty(t, e.run(0))
	assert.Empty(t, e.run(0))
	assert.EqualValues(t, 1, e.srv.gets.Load(), "no hammering during the backoff")
	assert.WithinDuration(t, time.Now().Add(starHintRetryAfterFailure), e.entry().CheckAgainAt, time.Minute)

	// the hour passes and the server recovers
	e.seed(func(en *starHintEntry) { en.CheckAgainAt = time.Now().Add(-time.Minute) })
	e.srv.answer(http.StatusOK, starHintCardJSON("star-ox-github", "Enjoying ox?",
		"If ox has been useful, a star on GitHub helps other teams find it.", "/cli", starHintTestToken))
	assert.Equal(t, e.wantLine(), e.run(0))
	assert.EqualValues(t, 2, e.srv.gets.Load())
}

// Failure prevented: a hung server holds every command open. The request has a
// hard budget, so the command ends in a fraction of the server's delay, prints
// nothing, and does not retry for an hour.
func TestStarHint_SlowServerCannotHoldACommandOpen(t *testing.T) {
	e := newStarHintEnv(t)
	e.srv.delayGets(10 * time.Second)

	start := time.Now()
	out := e.run(0)
	elapsed := time.Since(start)

	assert.Empty(t, out)
	assert.Less(t, elapsed, 3*time.Second, "budget is %s; the server would have taken 10s", starHintBudget)
	assert.EqualValues(t, 0, e.srv.seens.Load())
	assert.WithinDuration(t, time.Now().Add(starHintRetryAfterFailure), e.entry().CheckAgainAt, time.Minute, "backed off")
}

// --- the state file ---

// Failure prevented: a state file from a newer ox is overwritten by this one,
// erasing its caps, or a damaged file silences the hint forever.
func TestStarHint_StateFileEdgeCases(t *testing.T) {
	t.Run("a newer schema is left alone and the hint stays quiet", func(t *testing.T) {
		e := newStarHintEnv(t)
		raw := []byte(`{"version": 99, "people": {}}`)
		require.NoError(t, os.WriteFile(starHintStatePath, raw, 0o600))

		assert.Empty(t, e.run(0))
		assert.EqualValues(t, 0, e.srv.gets.Load())
		got, err := os.ReadFile(starHintStatePath)
		require.NoError(t, err)
		assert.Equal(t, raw, got, "untouched")
	})

	t.Run("a corrupt file is treated as empty and rewritten", func(t *testing.T) {
		e := newStarHintEnv(t)
		require.NoError(t, os.WriteFile(starHintStatePath, []byte(`{{{ not json`), 0o600))

		assert.Equal(t, e.wantLine(), e.run(0))
		assert.Equal(t, 1, e.entry().Shown)
	})

	t.Run("fields a newer ox added are ignored", func(t *testing.T) {
		e := newStarHintEnv(t)
		doc := `{"version":1,"future":true,"people":{` + strconv.Quote(e.key()) +
			`:{"shown":1,"last_shown":"2020-01-01T00:00:00Z","future_field":[1,2],` +
			`"check_again_at":"2999-01-01T00:00:00Z","card":null}}}`
		require.NoError(t, os.WriteFile(starHintStatePath, []byte(doc), 0o600))

		// shown=1 a long time ago: eligible again; the far-future expiry is a
		// clock that moved back, so the answer is refreshed rather than trusted
		assert.Equal(t, e.wantLine(), e.run(0))
		assert.EqualValues(t, 1, e.srv.gets.Load())
	})

	t.Run("a machine that cannot remember never prints", func(t *testing.T) {
		e := newStarHintEnv(t)
		// a regular file where the cache directory must be: MkdirAll fails
		blocker := filepath.Join(t.TempDir(), "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		starHintStatePath = filepath.Join(blocker, "star-hint.json")

		assert.Empty(t, e.run(0))
		assert.EqualValues(t, 0, e.srv.seens.Load())
	})
}

// --- the production predicates (the tests above stub starHintAutomated) ---

// Failure prevented: CI or an agent session reads as a person. Set values
// rather than clearing them, so the result does not depend on where `go test`
// runs.
func TestDetectAutomatedRun(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "CI=true", env: map[string]string{"CI": "true"}},
		{name: "GitHub Actions without CI", env: map[string]string{"GITHUB_ACTIONS": "true"}},
		{name: "an explicit agent", env: map[string]string{"AGENT_ENV": "codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			assert.True(t, detectAutomatedRun())
		})
	}
}

func TestStarHintSkipsCommand(t *testing.T) {
	for path, want := range map[string]bool{
		"status":                false,
		"doctor":                false,
		"session list":          false,
		"login":                 false,
		"agent":                 true,
		"agent hook":            true,
		"agent hook PreCompact": true,
		"hooks pre-push":        true,
		"hooks install":         true,
		"daemon start":          true,
		"mcp serve":             true,
		"statusline":            true,
		"git-credential-helper": true,
		"completion zsh":        true,
		"__complete":            true,
		"__completeNoDesc":      true,
	} {
		assert.Equal(t, want, starHintSkipsCommand(path), path)
	}
}

// Respect NO_COLOR: only the star is styled and it goes through the theme's
// adaptive color, so a no-color profile leaves exactly the contract line.
func TestStarHintLine_PlainTextIsTheContractLine(t *testing.T) {
	line, ok := starHintLine(&api.NudgeCard{
		NudgeID: "star-ox-github",
		Title:   "Enjoying ox?",
		Body:    "If ox has been useful, a star on GitHub helps other teams find it.",
		CTA:     api.NudgeCTA{Label: "Star on GitHub", Href: "/cli"},
		Token:   "tok",
	}, "https://sageox.ai/")
	require.True(t, ok)
	assert.Equal(t,
		"★ Enjoying ox? If ox has been useful, a star on GitHub helps other teams find it. https://sageox.ai/cli",
		ansi.Strip(line), "a trailing slash on the endpoint does not double up")
}
