package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// TestIsReturningLogin pins what "Welcome back" means: this account already
// held a stored login on this endpoint.
// Failure prevented: greeting a first-ever login with "Welcome back" (the
// previous text said it unconditionally), or greeting a different account that
// happens to share the machine as though it had returned.
func TestIsReturningLogin(t *testing.T) {
	prior := func(userID, email string) *auth.StoredToken {
		return &auth.StoredToken{UserInfo: auth.UserInfo{UserID: userID, Email: email}}
	}
	tests := []struct {
		name  string
		prior *auth.StoredToken
		now   auth.UserInfo
		want  bool
	}{
		{"no stored login: a first login", nil, auth.UserInfo{UserID: "u1", Email: "a@x.test"}, false},
		{"same account by id", prior("u1", "old@x.test"), auth.UserInfo{UserID: "u1", Email: "new@x.test"}, true},
		{"different account by id, same email", prior("u1", "a@x.test"), auth.UserInfo{UserID: "u2", Email: "a@x.test"}, false},
		{"same account by email when an id is missing", prior("", "A@X.test"), auth.UserInfo{UserID: "u1", Email: "a@x.test"}, true},
		{"different account by email", prior("", "a@x.test"), auth.UserInfo{Email: "b@x.test"}, false},
		{"a stored record that names nobody", prior("", ""), auth.UserInfo{UserID: "u1", Email: "a@x.test"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isReturningLogin(tt.prior, tt.now))
		})
	}
}

// TestLoginFailure verifies a denied or expired request ends with one line
// saying what to do, and that nothing else is mislabeled as expiry.
// Failure prevented: a bare "authorization was denied" with no way forward, and
// a flaky-network HTTP timeout reported as "the code expired".
func TestLoginFailure(t *testing.T) {
	open := context.Background()
	closedWindow, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-closedWindow.Done()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
		is   error
	}{
		{
			name: "denied",
			ctx:  open,
			err:  auth.ErrDeviceAuthorizationDenied,
			want: "authentication failed: authorization was denied\n  Nothing was shared. Run `ox login` to try again",
			is:   auth.ErrDeviceAuthorizationDenied,
		},
		{
			name: "expired, as the server says it",
			ctx:  open,
			err:  auth.ErrDeviceCodeExpired,
			want: "authentication failed: device code expired\n  The code expired before it was approved. Run `ox login` for a new one",
			is:   auth.ErrDeviceCodeExpired,
		},
		{
			name: "expired, as the poll window says it",
			ctx:  closedWindow,
			err:  context.DeadlineExceeded,
			want: "authentication failed: device code expired\n  The code expired before it was approved. Run `ox login` for a new one",
			is:   auth.ErrDeviceCodeExpired,
		},
		{
			name: "a deadline error while the window is open is a network problem, not expiry",
			ctx:  open,
			err:  context.DeadlineExceeded,
			want: "authentication failed: context deadline exceeded",
			is:   context.DeadlineExceeded,
		},
		{
			name: "any other failure keeps its message and gets no retry advice",
			ctx:  open,
			err:  errors.New("failed to exchange token for JWT"),
			want: "authentication failed: failed to exchange token for JWT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loginFailure(tt.ctx, tt.err)
			require.Error(t, got)
			assert.Equal(t, tt.want, got.Error())
			if tt.is != nil {
				assert.ErrorIs(t, got, tt.is)
			}
		})
	}
}

// TestLoginArtEnv_Allowed verifies Oxy appears for exactly one audience and
// each condition alone keeps him away.
// Failure prevented: art in a pipe, CI log, --json or --quiet run, or an AI
// coworker's captured output, where DESIGN.md promises calm, precise text.
func TestLoginArtEnv_Allowed(t *testing.T) {
	person := loginArtEnv{StdoutTerminal: true, Interactive: true, Profile: colorprofile.TrueColor}
	require.True(t, person.allowed(), "a person at a truecolor terminal sees Oxy")
	assert.True(t, loginArtEnv{StdoutTerminal: true, Interactive: true, Profile: colorprofile.ANSI256}.allowed(),
		"256 colors is enough for the half-block picture")

	tests := []struct {
		name   string
		mutate func(*loginArtEnv)
	}{
		{"stdout is a pipe or file", func(e *loginArtEnv) { e.StdoutTerminal = false }},
		{"not interactive (CI, --no-input, --no-interactive, no TTY on stdin)", func(e *loginArtEnv) { e.Interactive = false }},
		{"--quiet", func(e *loginArtEnv) { e.Quiet = true }},
		{"--json", func(e *loginArtEnv) { e.JSON = true }},
		{"run by an AI coworker", func(e *loginArtEnv) { e.AgentContext = true }},
		{"NO_COLOR", func(e *loginArtEnv) { e.Profile = colorprofile.ASCII }},
		{"16 colors cannot carry the picture", func(e *loginArtEnv) { e.Profile = colorprofile.ANSI }},
		{"no TTY profile", func(e *loginArtEnv) { e.Profile = colorprofile.NoTTY }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := person
			tt.mutate(&env)
			assert.False(t, env.allowed())
		})
	}
}

// TestLoginArt_NeverDrawsToAWriterThatIsNotATerminal is the guard on the real
// wiring: a buffer is not a terminal, so no art, whatever else is true.
func TestLoginArt_NeverDrawsToAWriterThatIsNotATerminal(t *testing.T) {
	assert.Nil(t, loginArt(&bytes.Buffer{}))
	assert.Nil(t, loginArt(io.Discard))
}

// TestLoginArtFor verifies Oxy's still reaches the success block for a person
// at a color terminal, and that a missing animation costs the art, not the login.
// Failure prevented: a failure to draw the art being returned as a failed login
// after the credential was already stored.
func TestLoginArtFor(t *testing.T) {
	person := loginArtEnv{StdoutTerminal: true, Interactive: true, Profile: colorprofile.TrueColor}

	lines := loginArtFor(person, loginArtAnimation)
	require.Len(t, lines, loginArtRows, "one string per terminal row")
	assert.Contains(t, lines[len(lines)/2], "▀", "something is drawn")

	assert.Nil(t, loginArtFor(person, "no-such-animation"), "art that cannot be drawn is left out")
	assert.Nil(t, loginArtFor(loginArtEnv{StdoutTerminal: true, Interactive: true, Quiet: true, Profile: colorprofile.TrueColor}, loginArtAnimation),
		"--quiet gets the text alone")
}

// TestCurrentLoginArtEnv_FollowsTheOutputFlags verifies --quiet and --json,
// which arrive through the global config, reach the art decision.
// Failure prevented: Oxy appearing in a --json or --quiet run because the flags
// were read from somewhere the command never set.
func TestCurrentLoginArtEnv_FollowsTheOutputFlags(t *testing.T) {
	prev := cfg
	t.Cleanup(func() { cfg = prev })

	cfg = &config.Config{Quiet: true}
	env := currentLoginArtEnv(&bytes.Buffer{})
	assert.True(t, env.Quiet)
	assert.False(t, env.JSON)

	cfg = &config.Config{JSON: true}
	env = currentLoginArtEnv(&bytes.Buffer{})
	assert.True(t, env.JSON)
	assert.False(t, env.Quiet)
}

// TestLoginSpinnerView_SuccessIsSilent verifies the spinner leaves nothing
// behind when the login worked, because the arrival block is the confirmation.
// Failure prevented: a second, redundant success line above the arrival block.
func TestLoginSpinnerView_SuccessIsSilent(t *testing.T) {
	assert.Empty(t, loginSpinnerModel{done: true}.View().Content)
	assert.Contains(t, ansi.Strip(loginSpinnerModel{done: true, canceled: true}.View().Content), "canceled", "the other end states still say something")
}

func TestWriteDeviceInstructions(t *testing.T) {
	tests := []struct {
		name string
		code auth.DeviceCodeResponse
		want string
	}{
		{
			name: "complete URL, code still printed",
			code: auth.DeviceCodeResponse{
				UserCode:                "WXYZ-1234",
				VerificationURI:         "https://sageox.ai/device",
				VerificationURIComplete: "https://sageox.ai/device?user_code=WXYZ-1234",
			},
			want: "\nVisit: https://sageox.ai/device?user_code=WXYZ-1234\n" +
				"Code:  WXYZ-1234  (match this in your browser)\n\n",
		},
		{
			name: "no complete URL: the person types the code",
			code: auth.DeviceCodeResponse{UserCode: "WXYZ-1234", VerificationURI: "https://sageox.ai/device"},
			want: "\nVisit: https://sageox.ai/device\nEnter code: WXYZ-1234\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writeDeviceInstructions(&out, &tt.code)
			assert.Equal(t, tt.want, ansi.Strip(out.String()))
		})
	}
}

// fakeSageox serves just enough of the device flow to sign person-a in: the
// code is approved on the first poll.
func fakeSageox(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case auth.DeviceCodeEndpoint:
			write(auth.DeviceCodeResponse{
				DeviceCode:              "device-code",
				UserCode:                "TEST-CODE",
				VerificationURI:         server.URL + "/device",
				VerificationURIComplete: server.URL + "/device?user_code=TEST-CODE",
				ExpiresIn:               900,
				Interval:                1,
			})
		case auth.DeviceTokenEndpoint:
			write(map[string]any{"access_token": "opaque", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/api/v1/cli/auth/token":
			write(map[string]any{"access_token": "jwt.access.token", "refresh_token": "refresh", "expires_in": 3600})
		case auth.UserInfoEndpoint:
			write(map[string]any{"user_id": "user-a", "email": "person.a@company.test", "name": "Person A"})
		case "/api/v1/cli/repos":
			write(api.ReposResponse{Token: "git-token", ServerURL: "https://git.example.test", Username: "oauth2"})
		default:
			w.WriteHeader(http.StatusNotFound) // includes /oauth2/token: a refresh of an expired login fails
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// isolateLoginEnv points every place a login reads or writes at a throwaway
// home, and keeps the flow from opening a browser or touching the OS keychain.
func isolateLoginEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SKIP_BROWSER", "1")
	t.Setenv("CI", "true") // CI is also how git credentials skip the OS keychain
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// TestRunLoginFlow_PrintsCodeAndThreeLineArrival drives `ox login` end to end
// against a fake SageOx and snapshots everything it prints when stdout is not
// a terminal (a pipe, CI): the code beside the URL, then who, where, what's
// next, then nothing.
// Failure prevented, one per assertion block:
//   - the code missing from the terminal, so the browser's "does this match?"
//     check has nothing to match (RFC 8628);
//   - "Welcome back" for a first login, and plain "Welcome" for a person who
//     has signed in here before;
//   - the random tip, the "designed initially for Claude Code" disclaimer, and
//     art all returning to a flow that login-flow.md says is "a step, not a
//     destination".
func TestRunLoginFlow_PrintsCodeAndThreeLineArrival(t *testing.T) {
	if testing.Short() {
		t.Skip("short: the device flow polls no faster than every 5 seconds")
	}

	server := fakeSageox(t)
	isolateLoginEnv(t)
	t.Chdir(t.TempDir()) // refreshing remotes looks for a repo in the working directory

	// login returns what runLoginFlow wrote to its own writer, and what it
	// printed past it: to the process's stdout and stderr, where the tip and
	// the disclaimer used to go.
	login := func(t *testing.T) (out, stdout, stderr string) {
		t.Helper()
		var buf bytes.Buffer
		stderr = captureStderr(t, func() {
			stdout = captureStdoutForPlanCLI(t, func() {
				cmd := &cobra.Command{}
				cmd.SetContext(context.Background())
				cmd.SetOut(&buf)
				require.NoError(t, runLoginFlow(cmd, server.URL))
			})
		})
		return buf.String(), stdout, stderr
	}
	wantFor := func(greeting string) string {
		return "Initiating device authentication flow...\n" +
			"\n" +
			"Visit: " + server.URL + "/device?user_code=TEST-CODE\n" +
			"Code:  TEST-CODE  (match this in your browser)\n" +
			"\n" +
			"Waiting for authorization...\n" +
			"Status: Successfully authenticated\n" +
			"\n" +
			"✓ " + greeting + ", Person (person.a@company.test)\n" +
			"  Endpoint: " + endpoint.NormalizeSlug(server.URL) + "\n" +
			"  Next:     ox init\n" +
			"\n"
	}

	t.Run("first login on this endpoint", func(t *testing.T) {
		out, stdout, stderr := login(t)
		assert.Equal(t, wantFor("Welcome"), ansi.Strip(out))
		assert.NotContains(t, out, "\x1b[38;2", "no truecolor art in a run that is not a terminal")
		assert.NotContains(t, out, "▀")
		assert.Empty(t, stdout, "nothing follows the block: no sync line, no disclaimer")
		assert.NotContains(t, stderr, "✦", "no random tip")
		assert.NotContains(t, stderr, "designed initially for Claude Code")
	})

	t.Run("same account again after its login expired", func(t *testing.T) {
		stored, err := auth.GetTokenForEndpoint(server.URL)
		require.NoError(t, err)
		require.NotNil(t, stored, "the first login stored a login to be compared with")
		stored.ExpiresAt = time.Now().Add(-time.Hour)
		require.NoError(t, auth.SaveTokenForEndpoint(server.URL, stored))

		out, stdout, _ := login(t)
		assert.Equal(t, wantFor("Welcome back"), ansi.Strip(out))
		assert.Empty(t, stdout)
	})
}

// TestRunLoginFlow_ArrivalNamesTheEndpointAfterAFallback signs in through an
// endpoint fallback and checks the arrival's Endpoint line names the endpoint
// the login was stored on.
// Failure prevented: the block naming the unreachable endpoint the person
// declined, while the new login sits on the one they switched to.
func TestRunLoginFlow_ArrivalNamesTheEndpointAfterAFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("short: the device flow polls no faster than every 5 seconds")
	}

	// the project is configured for the live server; login is asked to use an
	// IPv6 loopback address nothing listens on (its slug, "[::1]", differs from
	// the live server's "localhost", where a second 127.0.0.1 port would not)
	const unreachable = "http://[::1]:1"
	live := fakeSageox(t)
	isolateLoginEnv(t)
	repo := t.TempDir()
	initGitRepo(t, repo)
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{Endpoint: live.URL}))
	t.Chdir(repo)
	require.Equal(t, "[::1]", endpoint.NormalizeSlug(unreachable), "the two endpoints must have distinct slugs")
	require.NotEqual(t, endpoint.NormalizeSlug(unreachable), endpoint.NormalizeSlug(live.URL))

	var buf bytes.Buffer
	captureConfirmOutput(t, func() { // the switch prompt prints to stdout
		withStdin(t, "y\n", func() {
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetOut(&buf)
			require.NoError(t, runLoginFlow(cmd, unreachable))
		})
	})

	out := ansi.Strip(buf.String())
	require.Contains(t, out, "Authenticating to "+live.URL, "the person switched endpoints")
	assert.Contains(t, out, "  Endpoint: "+endpoint.NormalizeSlug(live.URL)+"\n")
	assert.NotContains(t, out, "Endpoint: "+endpoint.NormalizeSlug(unreachable))
}
