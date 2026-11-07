package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestRedactSecrets_Shapes verifies every credential shape the sink must scrub.
// Failure prevented: a raw token/PAT/URL-credential reaching a log line.
func TestRedactSecrets_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		secret  string // must NOT appear in output
		mustCon string // must still appear (non-secret context preserved)
	}{
		{
			name:    "repos response git PAT (the HIGH finding)",
			in:      `{"token":"glpat-AABBCCDDEEFFGGHHIIJJ","server_url":"https://git.sageox.ai","username":"oauth2"}`,
			secret:  "glpat-AABBCCDDEEFFGGHHIIJJ",
			mustCon: "server_url",
		},
		{
			name:    "team info git_token",
			in:      `{"git_token":"oxp_1234567890abcdefghij","name":"team-x"}`,
			secret:  "oxp_1234567890abcdefghij",
			mustCon: "team-x",
		},
		{
			name:    "oauth access + refresh token",
			in:      `{"access_token":"eyJhbGciOi.secretpart.sig","refresh_token":"rt_supersecretvalue"}`,
			secret:  "rt_supersecretvalue",
			mustCon: "refresh_token",
		},
		{
			name:    "repo_salt auth material",
			in:      `{"repo_salt":"c2FsdHNhbHRzYWx0","repo":"acme"}`,
			secret:  "c2FsdHNhbHRzYWx0",
			mustCon: "acme",
		},
		{
			name:    "authorization bearer header",
			in:      "Authorization: Bearer eyJ0eXAiOiJKV1QifQ.payload.signature",
			secret:  "eyJ0eXAiOiJKV1QifQ.payload.signature",
			mustCon: "Authorization",
		},
		{
			name:    "credentials embedded in url",
			in:      "cloning https://oauth2:glpat-ZZZZZZZZZZ@git.sageox.ai/team/ledger.git",
			secret:  "glpat-ZZZZZZZZZZ",
			mustCon: "git.sageox.ai",
		},
		{
			name:    "bare sageox PAT prefix",
			in:      "probe used token oxp_deadbeefdeadbeefdead for ls-remote",
			secret:  "oxp_deadbeefdeadbeefdead",
			mustCon: "ls-remote",
		},
		{
			// A team token (oxt_) has a LARGER blast radius than a
			// personal PAT — it must be redacted just as aggressively.
			// This is the family the ox[a-z]_ generalization exists for.
			name:    "bare sageox team-token prefix",
			in:      "probe used token oxt_deadbeefdeadbeefdead for ls-remote",
			secret:  "oxt_deadbeefdeadbeefdead",
			mustCon: "ls-remote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret leaked through redaction:\n in:  %s\n out: %s", tc.in, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] marker in output, got: %s", got)
			}
			if tc.mustCon != "" && !strings.Contains(got, tc.mustCon) {
				t.Fatalf("non-secret context %q was destroyed: %s", tc.mustCon, got)
			}
		})
	}
}

// TestRedactSecrets_NoFalsePositives keeps the scrubber from mangling ordinary
// log lines — over-redaction hides real debugging signal.
func TestRedactSecrets_NoFalsePositives(t *testing.T) {
	clean := []string{
		`{"status":"ok","count":3,"endpoint":"sageox.ai"}`,
		"http response body (empty body)",
		"POST https://api.sageox.ai/api/v1/repo/init 200 in 42ms",
		// The ox[a-z]_ family pattern matches inside "proxy_" unless it is
		// left-anchored on a word boundary — and proxy_* identifiers are the
		// native vocabulary of network-failure log lines, exactly the ones that
		// must stay legible when someone is debugging a connection problem.
		"failed to dial via proxy_endpoint_override: connection refused",
		"HTTPS_PROXY=http://proxy_internal_corp:3128",
		"no_proxy_hosts_configured for this run",
		"epoxy_batch_identifier_12345",
	}
	for _, s := range clean {
		if got := redactSecrets(s); got != s {
			t.Errorf("clean string altered:\n in:  %s\n out: %s", s, got)
		}
	}
}

// TestRedactedTokenPrefix_SurvivesLogRedaction pins the margin that lets a
// rejected-credential diagnostic name WHICH credential was rejected.
//
// The auth package logs a short prefix of a malformed token ("oxp_test…[REDACTED]")
// so an operator can tell a personal token from a team one. That line only works if
// it survives this sink: patRe needs {8,} body characters after the prefix, and an
// 8-character show window leaves only 4. The margin is 3 characters and is enforced
// nowhere else — widen the show window to 12 and the diagnostic silently degrades to
// "[REDACTED]…[REDACTED]", telling the operator nothing, with no other test noticing.
//
// Asserted on the literal shape on purpose: internal/logger must import no other
// internal package (it sits underneath all of them), so it cannot import the auth
// helper that produces this string.
//
// Failure prevented: a token-family diagnostic that is scrubbed into uselessness by
// its own safety net — either by widening the show window upstream or by loosening
// patRe's length floor here.
func TestRedactedTokenPrefix_SurvivesLogRedaction(t *testing.T) {
	cases := []struct {
		name      string
		showChars int
		line      string
		survives  bool
	}{
		{
			name:      "current 8-char window stays legible",
			showChars: 8,
			line:      "prefix=oxp_test…[REDACTED] length=15",
			survives:  true,
		},
		{
			name:      "11 is the widest window that still survives",
			showChars: 11,
			line:      "prefix=oxp_test123…[REDACTED] length=15",
			survives:  true,
		},
		{
			// The cliff: 12 shown characters means 8 body characters, which is
			// exactly patRe's {8,} floor, so the prefix redacts itself away.
			name:      "12-char window destroys the diagnostic",
			showChars: 12,
			line:      "prefix=oxp_test1234…[REDACTED] length=15",
			survives:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.line)
			if survived := strings.Contains(got, "oxp_test"); survived != tc.survives {
				t.Fatalf("showChars=%d: token family prefix survived=%v, want %v\n in:  %s\n out: %s",
					tc.showChars, survived, tc.survives, tc.line, got)
			}
		})
	}
}

// TestRedactSecrets_ExportedWrapper covers the exported entry point used by call
// sites that hold a value the slog ReplaceAttr hook never sees — notably an error
// returned to a caller and printed straight to the terminal.
// Failure prevented: a credential surfacing in terminal output because the only
// scrubber was wired into the log sink.
func TestRedactSecrets_ExportedWrapper(t *testing.T) {
	got := RedactSecrets(`introspection failed: {"error_description":"bad token oxt_deadbeefdeadbeef"}`)
	if strings.Contains(got, "oxt_deadbeefdeadbeef") {
		t.Fatalf("exported scrubber leaked a team token: %s", got)
	}
	if !strings.Contains(got, "introspection failed") {
		t.Fatalf("exported scrubber destroyed the error context: %s", got)
	}
}

// TestReplaceAttr_Backstop proves the sink itself scrubs — the durable fix.
// Failure prevented: any slog call site logging a token-bearing string leaks it.
func TestReplaceAttr_Backstop(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: RedactAttr,
	})
	l := slog.New(h)

	// Simulate LogHTTPResponseBody on the GetRepos success path.
	l.Debug("http response body", "body", `{"token":"glpat-LIVEPATVALUE123456","username":"oauth2"}`)
	// an error value is not a string attr, but slog prints its message
	l.Debug("clone failed", "error", errors.New("unable to access 'https://oauth2:glpat-ERRVALUE1234567@git.sageox.ai/t/l.git/'"))

	out := buf.String()
	if strings.Contains(out, "glpat-LIVEPATVALUE123456") {
		t.Fatalf("PAT reached the log sink despite ReplaceAttr backstop:\n%s", out)
	}
	if strings.Contains(out, "glpat-ERRVALUE1234567") {
		t.Fatalf("PAT in an error value reached the log sink:\n%s", out)
	}

	// a typed-nil error, e.g. the target of an errors.As that did not match:
	// its Error method dereferences nil, and slog does not recover panics in
	// ReplaceAttr, so the hook must not call it bare
	var typedNil *nilDerefError
	l.Debug("command failed", "error", typedNil)
	if !strings.Contains(buf.String(), "error=<nil>") {
		t.Fatalf("typed-nil error not logged as <nil>:\n%s", buf.String())
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker in sink output:\n%s", out)
	}
}

type nilDerefError struct{ msg string }

func (e *nilDerefError) Error() string { return e.msg }
