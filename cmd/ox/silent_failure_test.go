package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/errkind"
)

// TestCommands_ExplainedFailuresSayWhy fails on any command that returns a
// bare cli.ErrSilent.
//
// Failure prevented: a command that prints its own account of a failure
// reaching usage telemetry as error_kind=other, error_detail=cli.SilentError,
// so the dashboard cannot say why it failed. Return silentFailure(kind,
// detail, cause) instead: just as silent, and it says why.
func TestCommands_ExplainedFailuresSayWhy(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(packageDir, "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	fset := token.NewFileSet()
	var bare []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, r := range ret.Results {
				sel, ok := r.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ErrSilent" {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "cli" {
					pos := fset.Position(r.Pos())
					bare = append(bare, fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line))
				}
			}
			return true
		})
	}
	assert.Empty(t, bare, "these return a bare cli.ErrSilent; return silentFailure(kind, detail, cause) so usage telemetry learns why")
}

// TestInvite_FailuresSayWhy drives ox team invite into each way it fails after
// explaining itself, through the real command and a fake server.
//
// Failure prevented: every invite failure reaching usage telemetry as
// other / cli.SilentError. The detail is the code --json already prints.
func TestInvite_FailuresSayWhy(t *testing.T) {
	const (
		personalTeam = `{"error":{"code":"personal_team_immutable","message":"personal teams are single-member and cannot take invitations"}}`
		notAMember   = `{"error":{"code":"not_found","message":"no such team"}}`
		refused      = `{"error":{"message":"only admins can invite"}}`
	)
	created := inviteReply{status: http.StatusCreated, body: createdInviteBody}
	unauthorized := inviteReply{status: http.StatusUnauthorized, body: `{}`}
	serverError := inviteReply{status: http.StatusInternalServerError, body: `{}`}
	forbidden := inviteReply{status: http.StatusForbidden, body: refused}

	two := []string{"alice@acme.com", "bob@acme.com"}
	for _, tc := range []struct {
		name     string
		replies  []inviteReply
		emails   []string
		noLogin  bool // the auth store holds no login
		noTeam   bool // no --team, and the project names none
		jsonOnly bool // text mode prints this one as an ordinary error
		kind     errkind.Kind
		detail   string
	}{
		{name: "never logged in", noLogin: true, kind: errkind.NotLoggedIn, detail: "unauthenticated"},
		{name: "the server rejects the login", replies: []inviteReply{unauthorized}, kind: errkind.Auth, detail: "unauthenticated"},
		{name: "no team to invite to", noTeam: true, kind: errkind.Usage, detail: "no_team"},
		{name: "a personal team", replies: []inviteReply{{status: http.StatusConflict, body: personalTeam}}, kind: errkind.Usage, detail: "personal_team"},
		{name: "a server without CLI invitations", replies: []inviteReply{{status: http.StatusNotFound, body: "404 page not found\n"}}, kind: errkind.Other, detail: "unsupported"},
		{name: "a malformed address", emails: []string{"not-an-email"}, kind: errkind.Usage, detail: "invalid_email"},
		{name: "the server refuses an address", replies: []inviteReply{forbidden}, kind: errkind.Auth, detail: "not_permitted"},
		{name: "a team the inviter is not in", replies: []inviteReply{{status: http.StatusNotFound, body: notAMember}}, kind: errkind.Auth, detail: "not_a_member"},
		{name: "a server error on an address", replies: []inviteReply{serverError}, kind: errkind.Other, detail: "error"},
		{name: "a server error outranks a refusal", emails: two,
			replies: []inviteReply{forbidden, serverError}, kind: errkind.Other, detail: "error"},
		{name: "one sent, then the login is rejected", emails: two,
			replies: []inviteReply{created, unauthorized}, kind: errkind.Auth, detail: "unauthenticated"},
		{name: "one sent, then a personal team", emails: two,
			replies: []inviteReply{created, {status: http.StatusConflict, body: personalTeam}}, kind: errkind.Usage, detail: "personal_team"},
		{name: "one sent, then no CLI invitations", emails: two,
			replies: []inviteReply{created, {status: http.StatusNotFound, body: "404 page not found\n"}}, kind: errkind.Other, detail: "unsupported"},
		{name: "one sent, then this ox is too old", emails: two, jsonOnly: true,
			replies: []inviteReply{created, {status: http.StatusUpgradeRequired, body: `{}`}}, kind: errkind.VersionUnsupported, detail: "version_unsupported"},
	} {
		for _, jsonMode := range []bool{false, true} {
			if tc.jsonOnly && !jsonMode {
				continue
			}
			t.Run(fmt.Sprintf("%s/json=%v", tc.name, jsonMode), func(t *testing.T) {
				replies := tc.replies
				if replies == nil {
					replies = []inviteReply{created}
				}
				newInviteCommandHarness(t, replies)
				if !tc.noTeam {
					require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
				}
				if jsonMode {
					require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
				}
				if tc.noLogin {
					t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // away from the login the harness saved
				}
				emails := tc.emails
				if emails == nil {
					emails = []string{"alice@acme.com"}
				}

				err := runInvite(inviteCmd, emails)

				assertFailureKind(t, err, tc.kind, tc.detail)
				assert.True(t, cli.IsSilent(err), "the explanation is already printed; got %v", err)
			})
		}
	}

	for _, tc := range []struct {
		name   string
		err    error
		kind   errkind.Kind
		detail string
	}{
		{"the server rejects the login", api.ErrUnauthorized, errkind.Auth, "unauthenticated"},
		{"a team that is missing or not the inviter's", api.ErrInviteNotAMember, errkind.Auth, "no_access"},
		// Printed the same as a team out of reach, but the server has no route.
		{"a server without CLI invitations", api.ErrInviteUnsupported, errkind.Other, "no_access"},
		{"the server refuses", &api.ForbiddenError{Reason: "admins only"}, errkind.Auth, "forbidden"},
	} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("--list: %s/json=%v", tc.name, jsonMode), func(t *testing.T) {
				err := runInviteList(context.Background(), io.Discard, &fakeLister{listErr: tc.err}, testTarget, jsonMode)
				assertFailureKind(t, err, tc.kind, tc.detail)
				assert.True(t, cli.IsSilent(err), "the explanation is already printed; got %v", err)
			})
		}
	}
}

// TestSessionImport_EveryRefusalSaysWhy files each code ox session import
// refuses with under a kind, with the code as the detail.
//
// Failure prevented: a refusal reaching usage telemetry as other /
// cli.SilentError, or a new code added without deciding its kind.
func TestSessionImport_EveryRefusalSaysWhy(t *testing.T) {
	want := map[string]errkind.Kind{
		importErrNotInitialized:    errkind.NotInitialized,
		importErrNotLoggedIn:       errkind.NotLoggedIn,
		importErrNoLedger:          errkind.NotInitialized,
		importErrRecordingDisabled: errkind.Usage,
		importErrReadOnly:          errkind.Auth,
		importErrRedactionRules:    errkind.Usage,
		importErrUnverified:        errkind.Other,
		importErrInProgress:        errkind.Usage,
		importErrLedgerWedged:      errkind.Other,
		importErrLedgerUnreadable:  errkind.Other,
		importErrNativeUnreadable:  errkind.Other,
		importErrBadFlag:           errkind.Usage,
	}
	for _, code := range constStrings(t, "session_import.go", "importErr") {
		kind, filed := want[code]
		if !assert.True(t, filed, "import refusal %q has no kind in this table", code) {
			continue
		}
		for _, jsonMode := range []bool{false, true} {
			err := renderImportFailure(io.Discard, jsonMode, importFailure{Code: code, Message: "refused"})
			assertFailureKind(t, err, kind, code)
			assert.True(t, cli.IsSilent(err), "%s: the refusal is already printed", code)
		}
	}
}

// TestBulletin_EveryFailureSaysWhy files each code ox bulletin post fails with
// under a kind, with the code as the detail.
//
// Failure prevented: a failed post reaching usage telemetry as other /
// cli.SilentError, or a new code added without deciding its kind.
func TestBulletin_EveryFailureSaysWhy(t *testing.T) {
	want := map[string]errkind.Kind{
		bulletinCodeValidation:         errkind.Usage,
		bulletinCodeDuplicate:          errkind.Usage,
		bulletinCodeNotEnabled:         errkind.Other,
		bulletinCodeNotAMember:         errkind.Auth,
		bulletinCodeUnsupportedServer:  errkind.Other,
		bulletinCodeUnauthenticated:    errkind.Auth,
		bulletinCodeTeamToken:          errkind.Auth,
		bulletinCodeTooLarge:           errkind.Usage,
		bulletinCodeUnavailable:        errkind.Other,
		bulletinCodeVersionUnsupported: errkind.VersionUnsupported,
		bulletinCodeHashMismatch:       errkind.Other,
		bulletinCodeNoTeam:             errkind.Usage,
		bulletinCodeForbidden:          errkind.Auth,
		bulletinCodeError:              errkind.Other,
	}
	for _, code := range constStrings(t, "bulletin.go", "bulletinCode") {
		kind, filed := want[code]
		if !assert.True(t, filed, "bulletin failure %q has no kind in this table", code) {
			continue
		}
		for _, jsonMode := range []bool{false, true} {
			err := renderBulletinFailure(io.Discard, jsonMode, bulletinFailure{Code: code, Headline: "failed"})
			assertFailureKind(t, err, kind, code)
			assert.True(t, cli.IsSilent(err), "%s: the failure is already printed", code)
		}
	}

	t.Run("an unclassified failure keeps its cause's kind", func(t *testing.T) {
		offline := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		err := renderBulletinFailure(io.Discard, false, bulletinFailureFor(offline, "acme", 1))
		assertFailureKind(t, err, errkind.Network, bulletinCodeError)
	})
}

// TestKB_UnavailableSaysWhy covers the kb paths that print their own account
// of what is unavailable.
//
// Failure prevented: a deferred scope or a feature that is off reaching usage
// telemetry as other / cli.SilentError. The detail is the status --json prints.
func TestKB_UnavailableSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func() error
		kind   errkind.Kind
		detail string
	}{
		{"describe: the personal scope is not available yet", func() error {
			return handleKBDescribeError(io.Discard, errKBScopeDeferred, "notes", false)
		}, errkind.Usage, "deferred"},
		{"describe: no such bubble, or knowledge bubbles are off", func() error {
			return handleKBDescribeError(io.Discard, api.ErrKBAPIUnavailable, "notes", false)
		}, errkind.Other, "unavailable"},
		{"query: search is off", func() error {
			return handleKBSearchError(io.Discard, api.ErrKBAPIUnavailable, false)
		}, errkind.Other, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			assertFailureKind(t, err, tc.kind, tc.detail)
			assert.True(t, cli.IsSilent(err), "the explanation is already printed; got %v", err)
		})
	}
}

// constStrings returns the values of the string constants in file whose names
// start with prefix: every code a command can fail with.
func constStrings(t *testing.T, file, prefix string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(packageDir, file), nil, 0)
	require.NoError(t, err)
	var values []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					values = append(values, v)
				}
			}
		}
	}
	require.NotEmpty(t, values, "no %s constants in %s", prefix, file)
	return values
}
