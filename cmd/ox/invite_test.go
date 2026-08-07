package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/pflag"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// A. splitEmails — turning however a human pasted a list into addresses
// ============================================================================

// TestSplitEmails_AcceptsEverySeparatorFormAHumanWouldPaste covers the whole
// separator matrix in one table.
//
// Failure prevented: someone pastes "alice@x.com; bob@x.com" out of a calendar
// invite or a Slack message and ox silently treats it as ONE malformed address,
// so bob never gets invited and the user has no idea a name was dropped.
func TestSplitEmails_AcceptsEverySeparatorFormAHumanWouldPaste(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"single address", []string{"a@x.com"}, []string{"a@x.com"}},
		{"comma inside one arg", []string{"a@x.com,b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"semicolon inside one arg", []string{"a@x.com;b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"space inside one quoted arg", []string{"a@x.com b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"separate positional args", []string{"a@x.com", "b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"tab and newline (pasted from a doc)", []string{"a@x.com\tb@x.com\nc@x.com\r\nd@x.com"},
			[]string{"a@x.com", "b@x.com", "c@x.com", "d@x.com"}},
		{"mixed separators across args", []string{"a@x.com,;b@x.com", "  c@x.com; d@x.com  "},
			[]string{"a@x.com", "b@x.com", "c@x.com", "d@x.com"}},
		{"repeated separators collapse", []string{"a@x.com,,,b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"trailing separator yields nothing extra", []string{"a@x.com,"}, []string{"a@x.com"}},
		{"whitespace-only arg yields nothing", []string{"   ", "\t\n"}, nil},
		{"empty string arg yields nothing", []string{""}, nil},
		{"no args at all", nil, nil},
		{"separators only", []string{",;, ;"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, splitEmails(tt.args))
		})
	}
}

// TestSplitEmails_CollapsesDuplicatesCaseInsensitivelyKeepingFirstSpelling
// pins the dedupe contract.
//
// Failure prevented: `ox invite Alice@Acme.com alice@acme.com` sends two
// invitations for one person — the second of which the server answers 409, so
// the user sees a spurious "already invited" for someone they just invited,
// and (per the code's own note about the missing uniqueness constraint) may
// end up with two live invitation rows.
func TestSplitEmails_CollapsesDuplicatesCaseInsensitivelyKeepingFirstSpelling(t *testing.T) {
	got := splitEmails([]string{"Alice@Acme.com", "alice@acme.com", "ALICE@ACME.COM", "bob@acme.com", "Bob@acme.com"})

	require.Equal(t, []string{"Alice@Acme.com", "bob@acme.com"}, got,
		"duplicates must collapse case-insensitively, first-seen order and ORIGINAL spelling preserved")
}

// TestSplitEmails_PreservesCallerOrder proves order is first-seen input order,
// not sorted and not map-iteration order.
//
// Failure prevented: a future refactor that dedupes via a map and returns
// map-iteration order makes the invite list — and therefore the rendered
// report and the request sequence — nondeterministic between runs.
func TestSplitEmails_PreservesCallerOrder(t *testing.T) {
	in := []string{"zoe@x.com", "adam@x.com", "mia@x.com", "adam@x.com"}
	assert.Equal(t, []string{"zoe@x.com", "adam@x.com", "mia@x.com"}, splitEmails(in))
}

// ============================================================================
// B. isValidEmail — the local gate that stands between a typo and the network
// ============================================================================

// TestIsValidEmail_RejectsWhatWouldCertainlyWasteARoundTrip is the local-gate
// table.
//
// Failure prevented: `ox invite dave@acme` (a real, extremely common typo)
// burns a request and comes back with a raw server 400 instead of a legible
// "invalid address"; conversely, over-tightening the check silently refuses a
// legitimate address like a.b+tag@sub.example.co.uk and the person never gets
// invited.
func TestIsValidEmail_RejectsWhatWouldCertainlyWasteARoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  bool
	}{
		// --- must be accepted: real addresses people actually have ---
		{"plain", "alice@acme.com", true},
		{"dots plus-tag and multi-label domain", "a.b+tag@sub.example.co.uk", true},
		{"mixed case", "Alice@Acme.com", true},
		{"short but well-formed", "x@y.io", true},
		{"two-letter tld", "a@b.co", true},

		// --- must be rejected: the typos this gate exists for ---
		{"domain with no dot", "dave@acme", false},
		{"no at sign at all", "notanemail", false},
		{"single-letter domain no dot", "a@b", false},
		{"missing local part", "@x.com", false},
		{"missing domain", "a@", false},
		{"empty", "", false},
		{"embedded space", "a b@x.com", false},
		{"double at", "a@@b.com", false},
		{"two addresses in one token", "a@b.com,c@d.com", false},
		{"consecutive dots in domain", "alice@acme..com", false},
		{"domain starts with dot", "a@.com", false},
		{"domain ends with dot", "a@com.", false},
		{"trailing dot after tld", "a@b.com.", false},
		{"bare hostname domain", "a@localhost", false},

		// --- must be rejected: display-name forms net/mail would happily
		// accept but the server would not ---
		{"display name form", "Alice <alice@acme.com>", false},
		{"stray angle bracket", "alice@acme.com>", false},
		{"quoted local part with space", `"a b"@acme.com`, false},

		// --- must be rejected: control bytes (see the terminal-injection
		// test below for why this one matters beyond validity) ---
		{"trailing ANSI escape", "alice@acme.com\x1b[2J", false},
		{"leading ANSI escape", "\x1b[31malice@acme.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidEmail(tt.email), "isValidEmail(%q)", tt.email)
		})
	}
}

// TestIsValidEmail_RejectsAddressesTooLongForSMTP asserts the local gate also
// catches lengths no mail system will ever accept.
//
// Failure prevented: an address whose local part exceeds RFC 5321's 64-octet
// limit (or whose total exceeds 254) is a guaranteed server rejection, so
// sending it costs a round-trip AND surfaces as a raw `error` outcome with a
// server message instead of the legible "invalid address" the user needs.
// Because sendInvites is sequential by design, a pasted list full of garbage
// long tokens serializes N doomed requests.
func TestIsValidEmail_RejectsAddressesTooLongForSMTP(t *testing.T) {
	tests := []struct {
		name  string
		email string
	}{
		{"local part 500 octets", strings.Repeat("a", 500) + "@acme.com"},
		{"total length over 254 octets", strings.Repeat("a", 60) + "@" + strings.Repeat("d", 200) + ".com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, isValidEmail(tt.email),
				"an address of %d octets can never be delivered; it must not consume a network round-trip", len(tt.email))
		})
	}
}

// ============================================================================
// C. normalizeRole
// ============================================================================

// TestNormalizeRole_NormalizesAndRejects covers the whole role surface.
//
// Failure prevented: `--role Admin` (capitalized, as a human would type it)
// gets forwarded verbatim and the server 400s every single address in the
// batch; or a blank role is forwarded and the server 400s despite its OpenAPI
// doc claiming a "member" default.
func TestNormalizeRole_NormalizesAndRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"member", "member", api.RoleMember, false},
		{"admin", "admin", api.RoleAdmin, false},
		{"owner", "owner", api.RoleOwner, false},
		{"uppercase", "MEMBER", api.RoleMember, false},
		{"title case with surrounding space", "  Admin  ", api.RoleAdmin, false},
		{"mixed case owner", "OwNeR", api.RoleOwner, false},
		{"empty means member (server 400s on blank)", "", api.RoleMember, false},
		{"whitespace-only means member", "   ", api.RoleMember, false},
		{"plural typo", "admins", "", true},
		{"unknown role", "root", "", true},
		{"close-but-wrong", "maintainer", "", true},
		{"injection-ish", "member; admin", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRole(tt.in)
			if tt.wantErr {
				require.Error(t, err, "normalizeRole(%q) must reject", tt.in)
				// The message has to name the valid set — an agent (or human)
				// retrying needs to know what to retry WITH.
				msg := err.Error()
				assert.Contains(t, msg, "member")
				assert.Contains(t, msg, "admin")
				assert.Contains(t, msg, "owner")
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ============================================================================
// D. sendInvites fakes
// ============================================================================

// recordingSender is a fake inviteSender that records exactly which addresses
// reached the network layer, in the order they got there.
type recordingSender struct {
	mu       sync.Mutex
	requests []api.CreateInviteRequest
	teamRefs []string
	// reply is consulted per address; nil means "201 Created".
	reply func(email string) (*api.InviteResponse, error)
}

func (s *recordingSender) CreateTeamInvite(_ context.Context, teamRef string, req api.CreateInviteRequest) (*api.InviteResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.teamRefs = append(s.teamRefs, teamRef)
	s.mu.Unlock()

	if s.reply == nil {
		return &api.InviteResponse{ID: "inv_" + req.Email, Email: req.Email, ExpiresAt: "2026-08-14T00:00:00Z"}, nil
	}
	return s.reply(req.Email)
}

func (s *recordingSender) emails() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.requests))
	for _, r := range s.requests {
		out = append(out, r.Email)
	}
	return out
}

func outcomeEmails(res inviteResult) []string {
	out := make([]string, 0, len(res.Outcomes))
	for _, o := range res.Outcomes {
		out = append(out, o.Email)
	}
	return out
}

func outcomeStatuses(res inviteResult) []inviteStatus {
	out := make([]inviteStatus, 0, len(res.Outcomes))
	for _, o := range res.Outcomes {
		out = append(out, o.Status)
	}
	return out
}

var testTarget = inviteTarget{Ref: "team_abc", ID: "team_abc", Name: "Acme", Slug: "acme"}

// ============================================================================
// E. sendInvites — validation gates the network, per-recipient
// ============================================================================

// TestSendInvites_InvalidAddressesNeverReachTheNetwork is the core of the
// per-recipient design.
//
// Failure prevented: (1) a typo'd address gets POSTed and comes back as an
// opaque server error instead of "invalid address"; (2) worse — one bad
// address short-circuits the batch, so the four good addresses in the same
// command are silently never invited and the user believes they were.
func TestSendInvites_InvalidAddressesNeverReachTheNetwork(t *testing.T) {
	sender := &recordingSender{}
	emails := []string{
		"alice@acme.com",
		"dave@acme", // no dot in domain
		"notanemail",
		"bob@acme.com",
		"@x.com",
		"a@",
		"carol@acme.com",
	}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, emails)
	require.NoError(t, err, "malformed addresses are per-recipient outcomes, never a whole-command abort")

	assert.Equal(t, []string{"alice@acme.com", "bob@acme.com", "carol@acme.com"}, sender.emails(),
		"only well-formed addresses may consume a network round-trip")

	assert.Equal(t, emails, outcomeEmails(res), "every input address needs an outcome, in input order")
	assert.Equal(t, []inviteStatus{
		statusSent, statusInvalidEmail, statusInvalidEmail,
		statusSent, statusInvalidEmail, statusInvalidEmail, statusSent,
	}, outcomeStatuses(res))
}

// TestSendInvites_EveryAddressCarriesTheResolvedRoleAndTeamRef proves the
// request payload is what the caller asked for.
//
// Failure prevented: the role is dropped or defaulted somewhere in the fan-out
// so `--role admin` quietly invites everyone as a member — a privilege
// difference nobody notices until the invitee can't do their job.
func TestSendInvites_EveryAddressCarriesTheResolvedRoleAndTeamRef(t *testing.T) {
	sender := &recordingSender{}
	_, err := sendInvites(context.Background(), sender, testTarget, api.RoleAdmin,
		[]string{"a@x.com", "b@x.com", "c@x.com"})
	require.NoError(t, err)

	require.Len(t, sender.requests, 3)
	for i, req := range sender.requests {
		assert.Equal(t, api.RoleAdmin, req.Role, "request %d lost the role", i)
		assert.Equal(t, testTarget.Ref, sender.teamRefs[i], "request %d went to the wrong team", i)
	}
}

// TestSendInvites_SentOutcomeCarriesTheServersInviteMetadata proves the
// created-invite id/expiry survive into the outcome.
//
// Failure prevented: --json emits `"status":"sent"` with no invite_id, so an
// AI coworker or script has nothing to correlate against later.
func TestSendInvites_SentOutcomeCarriesTheServersInviteMetadata(t *testing.T) {
	sender := &recordingSender{reply: func(email string) (*api.InviteResponse, error) {
		return &api.InviteResponse{ID: "inv_123", Email: email, ExpiresAt: "2026-08-14T00:00:00Z"}, nil
	}}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, []string{"a@x.com"})
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, "inv_123", res.Outcomes[0].InviteID)
	assert.Equal(t, "2026-08-14T00:00:00Z", res.Outcomes[0].ExpiresAt)
}

// TestSendInvites_NilInviteBodyStillCountsAsSent guards the degenerate 2xx.
//
// Failure prevented: a 2xx with an empty/unparseable body makes the sender
// return (nil, nil) and the renderer dereferences it — panicking the command
// AFTER the invitation was actually created server-side.
func TestSendInvites_NilInviteBodyStillCountsAsSent(t *testing.T) {
	sender := &recordingSender{reply: func(string) (*api.InviteResponse, error) { return nil, nil }}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, []string{"a@x.com"})
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, statusSent, res.Outcomes[0].Status)
	assert.Empty(t, res.Outcomes[0].InviteID)
}

// ============================================================================
// F. sendInvites — sequential fan-out is a correctness requirement
// ============================================================================

// concurrencyProbe records the maximum number of CreateTeamInvite calls that
// were ever in flight at the same instant.
type concurrencyProbe struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	order       []string
	hold        time.Duration
}

func (p *concurrencyProbe) CreateTeamInvite(_ context.Context, _ string, req api.CreateInviteRequest) (*api.InviteResponse, error) {
	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	p.order = append(p.order, req.Email)
	p.mu.Unlock()

	// A real request takes time. Holding the slot open is what makes an
	// overlapping call observable at all.
	time.Sleep(p.hold)

	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return &api.InviteResponse{ID: "inv"}, nil
}

// TestSendInvites_IsStrictlySequentialAndInCallerOrder is a correctness test,
// not a style test.
//
// Failure prevented: someone "optimizes" the fan-out with a goroutine per
// address. The server has NO uniqueness constraint on (team_id, email) and no
// rate limit on this route — duplicate suppression is a read-then-write check
// in its service layer — so two overlapping requests for the same address can
// both create a row and the invitee gets two live invitations. Ordering is
// asserted alongside because a parallel fan-out also scrambles the report and
// the request sequence.
func TestSendInvites_IsStrictlySequentialAndInCallerOrder(t *testing.T) {
	probe := &concurrencyProbe{hold: 3 * time.Millisecond}
	emails := []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com"}

	res, err := sendInvites(context.Background(), probe, testTarget, api.RoleMember, emails)
	require.NoError(t, err)

	probe.mu.Lock()
	maxInFlight, order := probe.maxInFlight, probe.order
	probe.mu.Unlock()

	assert.Equal(t, 1, maxInFlight,
		"invites must go out ONE AT A TIME: overlapping requests for one address can create two live invitations")
	assert.Equal(t, emails, order, "addresses must reach the server in the caller's order")
	assert.Equal(t, emails, outcomeEmails(res), "outcomes must mirror the caller's order")
}

// ============================================================================
// G. sendInvites — status mapping is total, and survives wrapping
// ============================================================================

// TestSendInvites_MapsEveryServerSentinelToItsStatus pins the sentinel→status
// table, including through fmt.Errorf wrapping.
//
// Failure prevented: (1) a new error path maps to the wrong status, so an
// "already invited" is reported as a hard failure and the command exits 1 on a
// re-run that should be a no-op; (2) someone adds context with fmt.Errorf and
// a `==` comparison somewhere silently stops matching — errors.Is is what
// makes wrapping safe, and only a wrapped-input test proves it is in use.
func TestSendInvites_MapsEveryServerSentinelToItsStatus(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		want        inviteStatus
		wantFailure bool
	}{
		{"already invited", api.ErrInviteExists, statusAlreadyInvited, false},
		{"already invited, wrapped", fmt.Errorf("create invite: %w", api.ErrInviteExists), statusAlreadyInvited, false},
		{"forbidden role", api.ErrInviteForbidden, statusNotPermitted, true},
		{"forbidden role, wrapped", fmt.Errorf("x: %w", api.ErrInviteForbidden), statusNotPermitted, true},
		{"not a member", api.ErrInviteNotAMember, statusNotAMember, true},
		{"not a member, doubly wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", api.ErrInviteNotAMember)), statusNotAMember, true},
		{"unknown server error", errors.New("HTTP 500: database is on fire"), statusError, true},
		{"network error", fmt.Errorf("network error: %w", errors.New("connection refused")), statusError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &recordingSender{reply: func(string) (*api.InviteResponse, error) { return nil, tt.err }}

			res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, []string{"a@x.com"})
			require.NoError(t, err, "a per-recipient error must never abort the command")
			require.Len(t, res.Outcomes, 1)

			assert.Equal(t, tt.want, res.Outcomes[0].Status)
			assert.Equal(t, tt.wantFailure, res.Outcomes[0].Status.isFailure(),
				"exit-code classification for %s", tt.want)
			assert.NotEmpty(t, res.Outcomes[0].Message, "every non-sent outcome needs a human-readable reason")
		})
	}
}

// TestSendInvites_UnknownErrorPreservesTheServersMessage proves the fallback
// arm doesn't swallow diagnostic detail.
//
// Failure prevented: an unmapped 5xx renders as a bare "failed" with no
// message, leaving the user (and any AI coworker reading --json) with nothing
// to act on.
func TestSendInvites_UnknownErrorPreservesTheServersMessage(t *testing.T) {
	sender := &recordingSender{reply: func(string) (*api.InviteResponse, error) {
		return nil, errors.New("HTTP 503: invite service unavailable")
	}}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, []string{"a@x.com"})
	require.NoError(t, err)
	assert.Contains(t, res.Outcomes[0].Message, "invite service unavailable")
}

// TestSendInvites_PerRecipientFailureNeverStopsTheBatch is the complement of
// the abort test below.
//
// Failure prevented: one recipient's 403 aborts the loop, so the three people
// after them in the list are never invited even though nothing was wrong with
// their addresses — and the user has no way to tell from the output.
func TestSendInvites_PerRecipientFailureNeverStopsTheBatch(t *testing.T) {
	perRecipient := []error{
		api.ErrInviteExists, api.ErrInviteForbidden, api.ErrInviteNotAMember,
		errors.New("HTTP 500: boom"),
	}

	for _, failFor := range perRecipient {
		t.Run(failFor.Error(), func(t *testing.T) {
			sender := &recordingSender{reply: func(email string) (*api.InviteResponse, error) {
				if email == "bob@acme.com" {
					return nil, failFor
				}
				return &api.InviteResponse{ID: "inv_" + email}, nil
			}}

			emails := []string{"alice@acme.com", "bob@acme.com", "carol@acme.com", "dan@acme.com"}
			res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, emails)
			require.NoError(t, err)

			assert.Equal(t, emails, sender.emails(), "every address must still be attempted")
			assert.Equal(t, emails, outcomeEmails(res))
			assert.Equal(t, statusSent, res.Outcomes[0].Status)
			assert.NotEqual(t, statusSent, res.Outcomes[1].Status)
			assert.Equal(t, statusSent, res.Outcomes[2].Status, "carol must still be invited after bob failed")
			assert.Equal(t, statusSent, res.Outcomes[3].Status)
		})
	}
}

// ============================================================================
// H. sendInvites — whole-command aborts stop the loop
// ============================================================================

// TestSendInvites_AbortErrorStopsTheBatchAndKeepsEarlierOutcomes attacks the
// boundary: the abort lands on the SECOND of three addresses.
//
// Failure prevented: (1) an abort condition is treated as one recipient's
// outcome, so a server with no invite route emits N identical "failed" rows
// and N pointless requests; (2) the abort is detected but the loop keeps
// going, hammering the server once per address; (3) the abort discards the
// outcomes already accumulated, losing the record of invitations that DID get
// created before the batch died.
func TestSendInvites_AbortErrorStopsTheBatchAndKeepsEarlierOutcomes(t *testing.T) {
	aborts := []struct {
		name string
		err  error
	}{
		{"route absent (old server)", api.ErrInviteUnsupported},
		{"route absent, wrapped", fmt.Errorf("create invite: %w", api.ErrInviteUnsupported)},
		{"credentials rejected", api.ErrUnauthorized},
		{"credentials rejected, wrapped", fmt.Errorf("create invite: %w", api.ErrUnauthorized)},
		{"cli version blocked", api.ErrVersionUnsupported},
		{"cli version blocked, wrapped", fmt.Errorf("create invite: %w", api.ErrVersionUnsupported)},
		// A personal team is a property of the TEAM, not of any recipient: no
		// address and no role can ever succeed against it, so reporting it
		// per-address would print the same refusal N times and imply that a
		// different address might have worked.
		{"personal team", api.ErrPersonalTeam},
		{"personal team, wrapped", fmt.Errorf("create invite: %w", api.ErrPersonalTeam)},
	}

	for _, tt := range aborts {
		t.Run(tt.name, func(t *testing.T) {
			sender := &recordingSender{reply: func(email string) (*api.InviteResponse, error) {
				if email == "bob@acme.com" {
					return nil, tt.err
				}
				return &api.InviteResponse{ID: "inv_" + email}, nil
			}}

			emails := []string{"alice@acme.com", "bob@acme.com", "carol@acme.com"}
			res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, emails)

			require.Error(t, err, "a doomed-batch condition must abort, not become one recipient's outcome")
			assert.ErrorIs(t, err, tt.err)

			assert.Equal(t, []string{"alice@acme.com", "bob@acme.com"}, sender.emails(),
				"carol must never be attempted once the batch is known to be doomed")

			require.Len(t, res.Outcomes, 1, "alice's completed invitation must survive the abort")
			assert.Equal(t, "alice@acme.com", res.Outcomes[0].Email)
			assert.Equal(t, statusSent, res.Outcomes[0].Status)
		})
	}
}

// TestSendInvites_AbortOnFirstAddressAttemptsNothingElse is the degenerate
// case of the same rule.
//
// Failure prevented: an old server (no invite route) gets one request per
// address in a 50-person paste, all of them guaranteed 404s.
func TestSendInvites_AbortOnFirstAddressAttemptsNothingElse(t *testing.T) {
	sender := &recordingSender{reply: func(string) (*api.InviteResponse, error) {
		return nil, api.ErrInviteUnsupported
	}}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember,
		[]string{"a@x.com", "b@x.com", "c@x.com", "d@x.com"})

	require.ErrorIs(t, err, api.ErrInviteUnsupported)
	assert.Len(t, sender.emails(), 1, "exactly one request should be spent discovering the route is gone")
	assert.Empty(t, res.Outcomes)
}

// TestSendInvites_InvalidAddressesBeforeAnAbortStillProduceOutcomes mixes the
// two stop conditions.
//
// Failure prevented: the abort path forgets the locally-rejected addresses
// that preceded it, so a user fixing a typo'd list loses the very diagnostic
// that told them which entry was malformed.
func TestSendInvites_InvalidAddressesBeforeAnAbortStillProduceOutcomes(t *testing.T) {
	sender := &recordingSender{reply: func(string) (*api.InviteResponse, error) {
		return nil, api.ErrUnauthorized
	}}

	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember,
		[]string{"notanemail", "alice@acme.com", "bob@acme.com"})

	require.ErrorIs(t, err, api.ErrUnauthorized)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, statusInvalidEmail, res.Outcomes[0].Status)
	assert.Equal(t, []string{"alice@acme.com"}, sender.emails(), "bob is never attempted after the abort")
}

// TestSendInvites_EmptyEmailListIsAWellFormedNoOp guards the zero case.
//
// Failure prevented: an empty list panics or, worse, fires a request with a
// blank address.
func TestSendInvites_EmptyEmailListIsAWellFormedNoOp(t *testing.T) {
	sender := &recordingSender{}
	res, err := sendInvites(context.Background(), sender, testTarget, api.RoleMember, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Outcomes)
	assert.Empty(t, sender.emails())
	assert.False(t, res.hasFailure(), "nothing attempted means nothing failed")
}

// ============================================================================
// I. Exit-code semantics
// ============================================================================

// TestInviteResult_HasFailureMatchesTheExitContract pins which statuses make
// `ox invite` exit non-zero.
//
// Failure prevented: `already_invited` is classified as a failure, so re-running
// the exact same `ox invite` command exits 1 — which breaks every idempotent
// script and provisioning loop that runs it. The desired end state (this person
// has a pending invitation) already holds.
func TestInviteResult_HasFailureMatchesTheExitContract(t *testing.T) {
	tests := []struct {
		name        string
		statuses    []inviteStatus
		wantFailure bool
	}{
		{"all sent", []inviteStatus{statusSent, statusSent}, false},
		{"all already invited (a re-run)", []inviteStatus{statusAlreadyInvited, statusAlreadyInvited}, false},
		{"sent plus already invited", []inviteStatus{statusSent, statusAlreadyInvited}, false},
		{"nothing at all", nil, false},
		{"one invalid address", []inviteStatus{statusSent, statusInvalidEmail}, true},
		{"one forbidden", []inviteStatus{statusAlreadyInvited, statusNotPermitted}, true},
		{"one not-a-member", []inviteStatus{statusNotAMember}, true},
		{"one personal-team", []inviteStatus{statusPersonalTeam}, true},
		{"one unknown error", []inviteStatus{statusSent, statusError}, true},
		{"failure buried in the middle of a long batch",
			[]inviteStatus{statusSent, statusSent, statusError, statusSent, statusSent}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := inviteResult{Team: testTarget, Role: api.RoleMember}
			for i, s := range tt.statuses {
				res.Outcomes = append(res.Outcomes, inviteOutcome{Email: fmt.Sprintf("p%d@x.com", i), Status: s})
			}
			assert.Equal(t, tt.wantFailure, res.hasFailure())
		})
	}
}

// TestInviteResult_CountsAgreeWithHasFailure proves the summary arithmetic and
// the exit decision can never disagree.
//
// Failure prevented: the JSON summary reports `"failed": 0` while the command
// exits 1 (or vice versa) — an AI coworker branching on the summary reaches the
// opposite conclusion from a shell branching on `$?`.
func TestInviteResult_CountsAgreeWithHasFailure(t *testing.T) {
	all := []inviteStatus{
		statusSent, statusAlreadyInvited, statusNotPermitted, statusNotAMember,
		statusInvalidEmail, statusPersonalTeam, statusError,
	}

	for _, s := range all {
		t.Run(string(s), func(t *testing.T) {
			res := inviteResult{Outcomes: []inviteOutcome{{Email: "a@x.com", Status: s}}}
			sent, pending, failed := res.counts()
			assert.Equal(t, 1, sent+pending+failed, "every outcome must land in exactly one bucket")
			assert.Equal(t, failed > 0, res.hasFailure(), "summary.failed and the exit decision must agree")
		})
	}
}

// ============================================================================
// J. renderInviteResult — the --json contract AI coworkers branch on
// ============================================================================

// documentedInviteStatuses is the closed set published in invite.go's contract.
var documentedInviteStatuses = map[string]bool{
	"sent": true, "already_invited": true, "not_permitted": true,
	"not_a_member": true, "invalid_email": true, "personal_team": true, "error": true,
}

func mixedInviteResult() inviteResult {
	return inviteResult{
		Team: inviteTarget{Ref: "team_abc", ID: "team_abc", Name: "Acme Corp", Slug: "acme"},
		Role: api.RoleAdmin,
		Outcomes: []inviteOutcome{
			{Email: "alice@acme.com", Status: statusSent, InviteID: "inv_1", ExpiresAt: "2026-08-14T00:00:00Z"},
			{Email: "bob@acme.com", Status: statusAlreadyInvited, Message: "still pending"},
			{Email: "dave@acme", Status: statusInvalidEmail, Message: "not a valid email address; not sent"},
			{Email: "eve@acme.com", Status: statusNotPermitted, Message: "you can't invite above your role"},
			{Email: "carol@acme.com", Status: statusSent, InviteID: "inv_2"},
			{Email: "mallory@acme.com", Status: statusError, Message: "HTTP 500: boom"},
		},
	}
}

// TestRenderInviteResult_JSONCarriesTheDocumentedEnvelope pins every top-level
// key and the closed status set.
//
// Failure prevented: a field gets renamed or dropped and every AI coworker /
// script that parses `ox invite --json` breaks silently — the command still
// exits 0, so nothing surfaces the breakage until someone notices nobody got
// invited.
func TestRenderInviteResult_JSONCarriesTheDocumentedEnvelope(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, mixedInviteResult(), true))

	var generic map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &generic), "--json output must parse: %s", buf.String())

	for _, key := range []string{"team", "role", "results", "summary", "guidance"} {
		assert.Contains(t, generic, key, "top-level key %q is part of the published contract", key)
	}

	summary, ok := generic["summary"].(map[string]any)
	require.True(t, ok, "summary must be an object")
	for _, key := range []string{"sent", "already_invited", "failed"} {
		assert.Contains(t, summary, key, "summary key %q is part of the published contract", key)
	}

	results, ok := generic["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 6)
	for i, raw := range results {
		row, ok := raw.(map[string]any)
		require.True(t, ok, "result %d must be an object", i)
		assert.Contains(t, row, "email")
		status, ok := row["status"].(string)
		require.True(t, ok, "result %d needs a string status", i)
		assert.True(t, documentedInviteStatuses[status],
			"result %d status %q is outside the documented closed set — AI coworkers branch on these", i, status)
	}

	assert.Equal(t, api.RoleAdmin, generic["role"])
	assert.NotEmpty(t, generic["guidance"], "guidance is what tells an agent what to do next")
}

// TestRenderInviteResult_JSONSummaryArithmeticMatchesTheOutcomes attacks the
// counting with mixed batches.
//
// Failure prevented: `already_invited` is folded into `sent` (or into
// `failed`), so a re-run reports invitations it did not send — or reports a
// failure where the desired end state already held.
func TestRenderInviteResult_JSONSummaryArithmeticMatchesTheOutcomes(t *testing.T) {
	tests := []struct {
		name                        string
		statuses                    []inviteStatus
		sent, alreadyInvited, faild int
	}{
		{"empty", nil, 0, 0, 0},
		{"one sent", []inviteStatus{statusSent}, 1, 0, 0},
		{"all already invited", []inviteStatus{statusAlreadyInvited, statusAlreadyInvited, statusAlreadyInvited}, 0, 3, 0},
		{"every status exactly once", []inviteStatus{
			statusSent, statusAlreadyInvited, statusNotPermitted, statusNotAMember,
			statusInvalidEmail, statusPersonalTeam, statusError,
		}, 1, 1, 5},
		{"sent-heavy mix", []inviteStatus{statusSent, statusSent, statusSent, statusError, statusAlreadyInvited}, 3, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := inviteResult{Team: testTarget, Role: api.RoleMember}
			for i, s := range tt.statuses {
				res.Outcomes = append(res.Outcomes, inviteOutcome{Email: fmt.Sprintf("p%d@x.com", i), Status: s})
			}

			var buf bytes.Buffer
			require.NoError(t, renderInviteResult(&buf, res, true))

			var out struct {
				Results []inviteOutcome `json:"results"`
				Summary struct {
					Sent           int `json:"sent"`
					AlreadyInvited int `json:"already_invited"`
					Failed         int `json:"failed"`
				} `json:"summary"`
			}
			require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

			assert.Equal(t, tt.sent, out.Summary.Sent)
			assert.Equal(t, tt.alreadyInvited, out.Summary.AlreadyInvited)
			assert.Equal(t, tt.faild, out.Summary.Failed)
			assert.Equal(t, len(tt.statuses), out.Summary.Sent+out.Summary.AlreadyInvited+out.Summary.Failed,
				"the three buckets must partition the results exactly — no double-count, no drop")
			assert.Len(t, out.Results, len(tt.statuses))
		})
	}
}

// TestRenderInviteResult_JSONIsStillValidWithHostileFieldContent proves the
// encoder — not hand-built strings — produces the JSON.
//
// Failure prevented: a server error message containing a quote or a newline
// gets concatenated into the output and `ox invite --json | jq` fails, which
// for an AI coworker looks like the command produced nothing at all.
func TestRenderInviteResult_JSONIsStillValidWithHostileFieldContent(t *testing.T) {
	res := inviteResult{
		Team: inviteTarget{Ref: `team"with'quotes`, ID: "team_1", Name: "Acme \"Corp\"\n", Slug: "acme"},
		Role: api.RoleMember,
		Outcomes: []inviteOutcome{
			{Email: "a@x.com", Status: statusError, Message: "HTTP 500: {\"nested\":\"json\"}\nline two\ttabbed"},
			{Email: "\x1b[31mred@x.com", Status: statusInvalidEmail, Message: "not a valid email address; not sent"},
			{Email: "b@x.com", Status: statusError, Message: `he said "boom" \ then quit`},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, res, true))

	var generic map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &generic), "output must stay parseable: %s", buf.String())
	assert.NotContains(t, buf.String(), "\x1b", "raw ESC must be escaped by the JSON encoder, never emitted literally")
}

// TestInviteTarget_DisplayNameFallbackChain covers every combination of the
// identifiers we might know about a team.
//
// Failure prevented: a team synced without a name renders as an empty label —
// "Team    " with nothing after it — so the confirmation preview asks the user
// to approve sending invitations to a team they cannot identify.
func TestInviteTarget_DisplayNameFallbackChain(t *testing.T) {
	tests := []struct {
		name   string
		target inviteTarget
		want   string
	}{
		{"name and slug", inviteTarget{Ref: "team_1", Name: "Acme", Slug: "acme"}, "Acme (acme)"},
		{"name only", inviteTarget{Ref: "team_1", Name: "Acme"}, "Acme"},
		{"slug only", inviteTarget{Ref: "team_1", Slug: "acme"}, "acme"},
		{"ref only", inviteTarget{Ref: "team_1"}, "team_1"},
		{"nothing known at all", inviteTarget{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.target.displayName())
		})
	}
}

// TestRenderInviteResult_JSONSurvivesAnEmptyTeamIdentity guards the degenerate
// target.
//
// Failure prevented: a team resolved only by --team passthrough (no id, no
// name locally) makes the renderer panic or emit malformed JSON.
func TestRenderInviteResult_JSONSurvivesAnEmptyTeamIdentity(t *testing.T) {
	res := inviteResult{
		Team:     inviteTarget{},
		Role:     api.RoleMember,
		Outcomes: []inviteOutcome{{Email: "a@x.com", Status: statusSent}},
	}
	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, res, true))

	var generic map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &generic))
	assert.Contains(t, generic, "team")
}

// ============================================================================
// K. renderInviteResult — text rendering under hostile data
// ============================================================================

// TestRenderInviteResult_TextSurvivesHostileShapes runs the renderer over the
// degenerate and oversized shapes.
//
// Failure prevented: a zero-outcome or 200-outcome batch panics on an index or
// a negative pad width, killing the command AFTER the invitations were already
// created server-side — the user gets a crash instead of the record of what
// just happened.
func TestRenderInviteResult_TextSurvivesHostileShapes(t *testing.T) {
	long := strings.Repeat("a", 240) + "@" + strings.Repeat("d", 240) + ".com"

	tests := []struct {
		name     string
		outcomes []inviteOutcome
	}{
		{"zero outcomes", nil},
		{"one outcome", []inviteOutcome{{Email: "a@x.com", Status: statusSent}}},
		{"one very long address", []inviteOutcome{{Email: long, Status: statusInvalidEmail, Message: "not a valid email address; not sent"}}},
		{"long address next to a short one", []inviteOutcome{
			{Email: "a@x.com", Status: statusSent},
			{Email: long, Status: statusInvalidEmail, Message: "not a valid email address; not sent"},
		}},
		{"empty email string", []inviteOutcome{{Email: "", Status: statusInvalidEmail, Message: "not a valid email address; not sent"}}},
		{"unknown status value", []inviteOutcome{{Email: "a@x.com", Status: inviteStatus("something_new"), Message: "?"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NotPanics(t, func() {
				require.NoError(t, renderInviteResult(&buf, inviteResult{Team: testTarget, Role: api.RoleMember, Outcomes: tt.outcomes}, false))
			})
			assert.Contains(t, stripANSI(buf.String()), "Invitations", "the header must always be present")
			assert.Contains(t, stripANSI(buf.String()), "sent", "the summary line must always be present")
		})
	}
}

// TestRenderInviteResult_TextEmitsOneLinePerOutcome guards the table shape at
// scale.
//
// Failure prevented: a 200-address paste renders fewer rows than addresses (a
// silently dropped recipient) — the exact failure the per-recipient report
// exists to make impossible.
func TestRenderInviteResult_TextEmitsOneLinePerOutcome(t *testing.T) {
	const n = 200
	res := inviteResult{Team: testTarget, Role: api.RoleMember}
	for i := 0; i < n; i++ {
		res.Outcomes = append(res.Outcomes, inviteOutcome{Email: fmt.Sprintf("person%03d@acme.com", i), Status: statusSent})
	}

	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, res, false))

	rendered := stripANSI(buf.String())
	for i := 0; i < n; i++ {
		assert.Contains(t, rendered, fmt.Sprintf("person%03d@acme.com", i), "recipient %d vanished from the report", i)
	}
	assert.Contains(t, rendered, "200 sent")
}

// TestRenderInviteResult_TextNeverEmitsRawControlBytes is a terminal-injection
// guard.
//
// Failure prevented: `ox invite` echoes attacker-influenced bytes straight to
// the terminal. Two live vectors: (1) a malformed address from an untrusted
// list — it is rejected locally as invalid_email and then printed VERBATIM in
// the report, escape sequences and all; (2) an `error` outcome's Message,
// which is the server's own response body wrapped by parseInviteError. Either
// can clear the screen, repaint earlier output, or forge a "✓ sent" line.
func TestRenderInviteResult_TextNeverEmitsRawControlBytes(t *testing.T) {
	tests := []struct {
		name     string
		outcome  inviteOutcome
		injected string
	}{
		{
			name:     "escape sequence inside an untrusted address",
			outcome:  inviteOutcome{Email: "\x1b[2J\x1b[H✓ eve@acme.com", Status: statusInvalidEmail, Message: "not a valid email address; not sent"},
			injected: "\x1b[2J",
		},
		{
			name:     "escape sequence inside a server-supplied message",
			outcome:  inviteOutcome{Email: "mallory@acme.com", Status: statusError, Message: "HTTP 500: \x1b[1;32m✓ all invitations sent\x1b[0m"},
			injected: "\x1b[1;32m",
		},
		{
			name:     "carriage return can repaint the current row",
			outcome:  inviteOutcome{Email: "trudy@acme.com", Status: statusError, Message: "HTTP 500: boom\r  ✓  trudy@acme.com  sent"},
			injected: "\r",
		},
		{
			name:     "backspaces can erase the failure glyph",
			outcome:  inviteOutcome{Email: "peggy@acme.com", Status: statusError, Message: "HTTP 500: boom\x08\x08\x08ok"},
			injected: "\x08",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := inviteResult{Team: testTarget, Role: api.RoleMember, Outcomes: []inviteOutcome{tt.outcome}}
			var buf bytes.Buffer
			require.NoError(t, renderInviteResult(&buf, res, false))

			// The renderer's own lipgloss styling emits SGR sequences, so the
			// assertion targets the specific bytes that came from the payload.
			assert.NotContains(t, buf.String(), tt.injected,
				"attacker-influenced text reached the terminal verbatim; rendered:\n%q", buf.String())
		})
	}
}

func TestRenderInviteList_TextNeverEmitsRawControlBytes(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	invites := []api.PendingInvite{
		{
			ID:        "inv_1\n  ✓ forged@example.com sent",
			Email:     "\x1b[2J\x1b[Hmallory@example.com",
			Role:      "member\r  ✓ forged@example.com sent",
			ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderInviteList(&buf, testTarget, invites, now, false))

	for _, injected := range []string{"\x1b[2J", "\x1b[H", "\r", "\n  ✓ forged@example.com sent"} {
		assert.NotContains(t, buf.String(), injected,
			"attacker-influenced pending invite text reached the terminal verbatim; rendered:\n%q", buf.String())
	}
}

// TestRenderInviteResult_TextRowCountIsExactlyOnePerRecipient proves a newline
// smuggled into a server message cannot forge a table row.
//
// Failure prevented: an `error` outcome's Message is the server's own response
// body. A body containing "\n  ✓  attacker@evil.com  sent" renders as an extra,
// perfectly convincing row — so the report claims an invitation that was never
// created. The report is the ONLY record the user gets of what just happened.
func TestRenderInviteResult_TextRowCountIsExactlyOnePerRecipient(t *testing.T) {
	res := inviteResult{
		Team: testTarget,
		Role: api.RoleMember,
		Outcomes: []inviteOutcome{
			{Email: "a@x.com", Status: statusError, Message: "boom\n  ✓  attacker@evil.com  sent"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, res, false))

	// header + rule + one row (possibly plus one indented continuation line for
	// the reason) + blank + summary. Nothing sent, so no delivery footer.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.LessOrEqual(t, len(lines), 6,
		"a newline inside a server message forged an extra line; rendered:\n%q", buf.String())

	// Whatever the message says, it must stay inside the row that owns it. A
	// forged row would START its own line with a status glyph.
	rows := 0
	for _, line := range lines {
		plain := stripANSI(line)
		for _, glyph := range []string{"  ✓  ", "  ✗  ", "  ⚠  "} {
			if strings.HasPrefix(plain, glyph) {
				rows++
				break
			}
		}
	}
	assert.Equal(t, 1, rows,
		"exactly one recipient row must be rendered for one outcome; got %d in:\n%q", rows, buf.String())
}

// TestRenderInviteResult_ColumnsAlignForNonASCIIAddresses checks the table
// stays a table when an address is not pure ASCII.
//
// Failure prevented: column padding computed on BYTE length instead of display
// width. A perfectly ordinary address like josé@acme.com is 13 columns wide but
// 14 bytes, so every other row in the batch is padded one column too far and
// the status column zig-zags — the same class of bug that mangles any table fed
// accented names, and the reason internal styling goes through lipgloss.Width.
func TestRenderInviteResult_ColumnsAlignForNonASCIIAddresses(t *testing.T) {
	res := inviteResult{
		Team: testTarget,
		Role: api.RoleMember,
		Outcomes: []inviteOutcome{
			{Email: "josé@acme.com", Status: statusSent},
			{Email: "bob@acme.com", Status: statusSent},
			{Email: "zoë@acme.com", Status: statusSent},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, res, false))

	var statusColumns []int
	for _, line := range strings.Split(stripANSI(buf.String()), "\n") {
		idx := strings.Index(line, "sent")
		if idx < 0 || !strings.Contains(line, "@acme.com") {
			continue
		}
		statusColumns = append(statusColumns, lipgloss.Width(line[:idx]))
	}

	require.Len(t, statusColumns, 3, "expected one row per recipient")
	for i, col := range statusColumns {
		assert.Equal(t, statusColumns[0], col,
			"row %d's status column starts at a different visual offset — padding is using byte length, not display width", i)
	}
}

// TestRenderInviteResult_FitsIn80Columns enforces house design rule 12.
//
// Failure prevented: a perfectly ordinary batch — one corporate-length address
// plus one typo — wraps at 80 columns, and the wrapped remainder of a row reads
// as a separate, statusless recipient. Terminal width is not a style question
// when the output is a table.
func TestRenderInviteResult_FitsIn80Columns(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	t.Setenv("NO_COLOR", "1")

	cases := map[string]inviteResult{
		"corporate addresses next to a typo": {
			Team: inviteTarget{Ref: "team_abc", ID: "team_abc", Name: "Acme Corporation", Slug: "acme-corp"},
			Role: api.RoleMember,
			Outcomes: []inviteOutcome{
				{Email: "alice.jones@enterprise-corp.com", Status: statusSent},
				{Email: "dave@acme", Status: statusInvalidEmail, Message: "not a valid email address; not sent"},
				{Email: "eve@enterprise-corp.com", Status: statusNotPermitted, Message: "you can't invite above your role"},
			},
		},
		// Server messages are unbounded: parseInviteError wraps the response
		// body verbatim, so a chatty 500 can be arbitrarily long.
		"unbounded server message": {
			Team: testTarget,
			Role: api.RoleMember,
			Outcomes: []inviteOutcome{
				{Email: "a@x.com", Status: statusError, Message: "HTTP 500: " + strings.Repeat("stack frame; ", 40)},
			},
		},
		// Addresses are unbounded too: nothing validates or truncates their
		// length, and this one is a perfectly legal address a real enterprise
		// directory would produce.
		"long but entirely legitimate address": {
			Team: testTarget,
			Role: api.RoleMember,
			Outcomes: []inviteOutcome{
				{
					Email:   "firstname.lastname+project-onboarding@engineering.subsidiary.example-corp.com",
					Status:  statusNotPermitted,
					Message: "you can't invite at that role",
				},
			},
		},
	}

	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, renderInviteResult(&buf, res, false))

			for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
				assert.LessOrEqual(t, lipgloss.Width(stripANSI(line)), 80,
					"line %d is %d columns wide and will wrap:\n%s", i, lipgloss.Width(stripANSI(line)), line)
			}
		})
	}
}

// TestRenderInviteResult_StatusLabelsAreDistinctAndNonEmpty guards the human
// column.
//
// Failure prevented: two different statuses render the same label, so a user
// cannot tell "you can't invite at that role" from "that team isn't yours" —
// two problems with completely different fixes.
func TestRenderInviteResult_StatusLabelsAreDistinctAndNonEmpty(t *testing.T) {
	all := []inviteStatus{
		statusSent, statusAlreadyInvited, statusNotPermitted, statusNotAMember,
		statusInvalidEmail, statusPersonalTeam, statusError,
	}

	seen := map[string]inviteStatus{}
	for _, s := range all {
		label := inviteStatusLabel(s)
		require.NotEmpty(t, label, "status %q has no human label", s)
		if prev, dup := seen[label]; dup {
			t.Fatalf("statuses %q and %q both render as %q — indistinguishable to the user", prev, s, label)
		}
		seen[label] = s
	}
}

// TestRenderInviteResult_TextIsReadableWithNoColor covers the NO_COLOR
// contract (house design rule 7).
//
// Failure prevented: the only signal distinguishing a sent row from a failed
// one is color, so with NO_COLOR=1 (or piped into a file, or read by an agent)
// success and failure look identical.
func TestRenderInviteResult_TextIsReadableWithNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	require.NoError(t, renderInviteResult(&buf, mixedInviteResult(), false))

	plain := stripANSI(buf.String())
	for _, want := range []string{
		"alice@acme.com", "bob@acme.com", "dave@acme", "eve@acme.com",
		"sent", "already invited", "invalid address", "not permitted",
		"2 sent", "1 already pending", "3 failed",
	} {
		assert.Contains(t, plain, want, "colorless output lost %q, so status is only conveyed by color", want)
	}
}

// TestInviteGuidance_TellsAnAgentWhatToDoNext covers the agent-facing field.
//
// Failure prevented: --json guidance omits the actionable next step for a
// recoverable outcome, so an AI coworker retries the identical failing command
// (or gives up) instead of correcting the address or lowering the role.
func TestInviteGuidance_TellsAnAgentWhatToDoNext(t *testing.T) {
	tests := []struct {
		name     string
		outcome  inviteOutcome
		contains []string
	}{
		{"invalid address names the address and the retry", inviteOutcome{Email: "dave@acme", Status: statusInvalidEmail},
			[]string{"dave@acme", "ox invite"}},
		{"refused role names WHO was refused", inviteOutcome{Email: "eve@acme.com", Status: statusNotPermitted, Message: "you can't invite at that role"},
			[]string{"eve@acme.com"}},
		{"personal team points at ox teams", inviteOutcome{Email: "a@x.com", Status: statusPersonalTeam},
			[]string{"ox teams"}},
		{"not a member points at ox teams", inviteOutcome{Email: "a@x.com", Status: statusNotAMember},
			[]string{"ox teams"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := inviteResult{Team: testTarget, Role: api.RoleMember, Outcomes: []inviteOutcome{tt.outcome}}
			sent, pending, failed := res.counts()
			got := inviteGuidance(res, sent, pending, failed)
			for _, want := range tt.contains {
				assert.Contains(t, got, want)
			}
			assert.Contains(t, got, "Exit code is 1", "guidance must explain the non-zero exit it is about to produce")
		})
	}
}

// TestInviteGuidance_ReRunOfAnAlreadyInvitedBatchDoesNotClaimFailure pins the
// idempotent-re-run wording.
//
// Failure prevented: guidance says "Exit code is 1" on a run that exits 0,
// teaching an AI coworker to treat a successful no-op as a failure and retry
// forever.
func TestInviteGuidance_ReRunOfAnAlreadyInvitedBatchDoesNotClaimFailure(t *testing.T) {
	res := inviteResult{
		Team: testTarget, Role: api.RoleMember,
		Outcomes: []inviteOutcome{
			{Email: "a@x.com", Status: statusAlreadyInvited},
			{Email: "b@x.com", Status: statusAlreadyInvited},
		},
	}
	sent, pending, failed := res.counts()
	got := inviteGuidance(res, sent, pending, failed)

	assert.NotContains(t, got, "Exit code is 1")
	assert.Contains(t, got, "no action needed")
	assert.False(t, res.hasFailure())
}

// ============================================================================
// L. Full-command runs against a real HTTP server
// ============================================================================

// inviteServerLog records what actually crossed the wire.
type inviteServerLog struct {
	mu       sync.Mutex
	paths    []string
	requests []api.CreateInviteRequest
}

func (l *inviteServerLog) record(path string, req api.CreateInviteRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
	l.requests = append(l.requests, req)
}

func (l *inviteServerLog) emails() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.requests))
	for _, r := range l.requests {
		out = append(out, r.Email)
	}
	return out
}

// inviteReply is one scripted server response.
type inviteReply struct {
	status int
	body   string
}

// newInviteCommandHarness wires runInvite to a throwaway HTTP server, an
// isolated auth store, and a captured writer.
//
// respond is consulted once per create-invite request, in order; the last entry
// repeats if there are more requests than replies.
func newInviteCommandHarness(t *testing.T, replies []inviteReply) (*bytes.Buffer, *inviteServerLog) {
	t.Helper()

	// nothing may try to open a bubbletea prompt: a blocked read here would
	// hang the whole package's test run.
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })
	t.Setenv("CI", "true")

	log := &inviteServerLog{}
	var calls int
	var callsMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/invites") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req api.CreateInviteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		log.record(r.URL.Path, req)

		callsMu.Lock()
		idx := calls
		calls++
		callsMu.Unlock()
		if idx >= len(replies) {
			idx = len(replies) - 1
		}

		reply := replies[idx]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(strings.ReplaceAll(reply.body, "{{email}}", req.Email)))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   "invite_test_repo",
		Endpoint: srv.URL,
	})
	t.Setenv(config.EnvProjectRoot, projectRoot)

	require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	buf := &bytes.Buffer{}
	resetInviteCmdState(t, buf)
	return buf, log
}

// resetInviteCmdState scrubs the package-level cobra singletons so one test's
// flag values cannot leak into the next.
func resetInviteCmdState(t *testing.T, out *bytes.Buffer) {
	t.Helper()
	reset := func() {
		// Every flag inviteCmd registers, not just the ones this test sets:
		// cobra commands are package-level singletons, so a value left behind
		// by one test silently changes the next one's behavior.
		inviteCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		_ = rootCmd.PersistentFlags().Set("json", "false")
		inviteCmd.SetOut(nil)
	}
	reset()
	inviteCmd.SetOut(out)
	// cobra only populates Context() inside ExecuteC; calling RunE directly
	// leaves it nil, which every net/http call downstream would reject.
	inviteCmd.SetContext(context.Background())
	t.Cleanup(reset)
}

const createdInviteBody = `{"id":"inv_1","email":"{{email}}","team_id":"team_abc","role":"member","expires_at":"2026-08-14T00:00:00Z"}`

// TestRunInvite_AllSentExitsZero is the happy path end to end.
//
// Failure prevented: a fully successful invite run returns a non-nil error and
// the CLI exits 1, which fails any CI job or provisioning script that runs it.
func TestRunInvite_AllSentExitsZero(t *testing.T) {
	buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

	err := runInvite(inviteCmd, []string{"alice@acme.com,bob@acme.com"})
	require.NoError(t, err, "all-sent must exit 0")

	assert.Equal(t, []string{"alice@acme.com", "bob@acme.com"}, log.emails())
	assert.Contains(t, stripANSI(buf.String()), "2 sent")
}

// TestRunInvite_AllAlreadyInvitedExitsZero is the re-runnability contract.
//
// Failure prevented: running the SAME `ox invite` command twice exits 1 the
// second time. Nothing is wrong — every listed person already has a pending
// invitation, which is precisely the state the command was asked to produce —
// but any script, Makefile target, or agent loop that re-runs it now believes
// it failed.
func TestRunInvite_AllAlreadyInvitedExitsZero(t *testing.T) {
	buf, _ := newInviteCommandHarness(t, []inviteReply{
		{status: http.StatusConflict, body: `{"success":false,"error":"an active invite already exists for this email"}`},
	})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

	err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com"})
	require.NoError(t, err, "already_invited is the desired end state, not a failure")
	assert.Contains(t, stripANSI(buf.String()), "already pending")
}

// TestRunInvite_AnyHardFailureExitsSilentlyNonZero covers each per-recipient
// failure mode end to end.
//
// Failure prevented: a genuine failure (bad role, wrong team, malformed
// address) exits 0, so an automated caller happily proceeds as though the
// person was invited.
func TestRunInvite_AnyHardFailureExitsSilentlyNonZero(t *testing.T) {
	tests := []struct {
		name      string
		reply     inviteReply
		args      []string
		wantOnCLI string
		wantCalls int
	}{
		{
			name:      "forbidden role",
			reply:     inviteReply{status: http.StatusForbidden, body: `{"success":false,"error":"you can't invite at that role"}`},
			args:      []string{"alice@acme.com"},
			wantOnCLI: "not permitted",
			wantCalls: 1,
		},
		{
			name:      "not a member (404 WITH a json body)",
			reply:     inviteReply{status: http.StatusNotFound, body: `{"error":{"code":"not_found","message":"no such team"}}`},
			args:      []string{"alice@acme.com"},
			wantOnCLI: "no access",
			wantCalls: 1,
		},
		{
			name:      "unknown server error",
			reply:     inviteReply{status: http.StatusInternalServerError, body: `{"success":false,"error":"database is on fire"}`},
			args:      []string{"alice@acme.com"},
			wantOnCLI: "failed",
			wantCalls: 1,
		},
		{
			name:      "malformed address never reaches the server",
			reply:     inviteReply{status: http.StatusCreated, body: createdInviteBody},
			args:      []string{"dave@acme"},
			wantOnCLI: "invalid address",
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, log := newInviteCommandHarness(t, []inviteReply{tt.reply})
			require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

			err := runInvite(inviteCmd, tt.args)

			require.Error(t, err, "a hard failure must produce a non-zero exit")
			assert.True(t, cli.IsSilent(err),
				"the report is already rendered; the error must be silent so main does not print a second error line, got %v", err)
			assert.Contains(t, stripANSI(buf.String()), tt.wantOnCLI)
			assert.Len(t, log.emails(), tt.wantCalls)
		})
	}
}

// TestRunInvite_OneBadAddressStillInvitesTheGoodOnes is the whole point of the
// per-recipient design, proven end to end.
//
// Failure prevented: a single typo in a pasted list of five aborts the run, so
// four people who were fine are silently never invited — and the user, seeing
// an error, has no idea which of the five did go out.
func TestRunInvite_OneBadAddressStillInvitesTheGoodOnes(t *testing.T) {
	buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

	err := runInvite(inviteCmd, []string{"alice@acme.com,dave@acme,bob@acme.com", "notanemail", "carol@acme.com"})

	require.Error(t, err)
	assert.True(t, cli.IsSilent(err))

	assert.Equal(t, []string{"alice@acme.com", "bob@acme.com", "carol@acme.com"}, log.emails(),
		"the three well-formed addresses must be invited; the two malformed ones must never hit the network")

	rendered := stripANSI(buf.String())
	assert.Contains(t, rendered, "3 sent")
	assert.Contains(t, rendered, "2 failed")
}

// TestRunInvite_InviteTokenNeverReachesTheOutput is the credential-leak guard.
//
// Failure prevented: the server returns the plaintext invite token in the 201
// body so its own web composer can offer a copy-link. An invite token is a live
// credential — anyone holding it can join the team. If someone ever adds a
// Token field to api.InviteResponse (or the renderer starts echoing the raw
// body), that credential lands in terminal scrollback, CI logs, and every agent
// transcript that captured the command.
func TestRunInvite_InviteTokenNeverReachesTheOutput(t *testing.T) {
	const secret = "invtok_SUPERSECRET_c0ffee"
	body := `{"id":"inv_1","email":"{{email}}","team_id":"team_abc","role":"member",` +
		`"expires_at":"2026-08-14T00:00:00Z","token":"` + secret + `","invite_url":"https://sageox.ai/join/` + secret + `"}`

	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonMode), func(t *testing.T) {
			buf, _ := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: body}})
			require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
			if jsonMode {
				require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
			}

			require.NoError(t, runInvite(inviteCmd, []string{"alice@acme.com"}))

			out := buf.String()
			assert.NotContains(t, out, secret, "the invite token must never reach any output surface")
			assert.NotContains(t, strings.ToLower(out), "invtok_")
			assert.NotContains(t, strings.ToLower(out), `"token"`)
		})
	}
}

// TestRunInvite_InvalidRoleFailsBeforeAnythingIsSent proves --role is
// validated locally.
//
// Failure prevented: `--role Owner!` is forwarded to the server, which 400s
// every address one at a time — N pointless requests, a partially-confusing
// report, and no clear statement of what was actually wrong.
func TestRunInvite_InvalidRoleFailsBeforeAnythingIsSent(t *testing.T) {
	buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
	require.NoError(t, inviteCmd.Flags().Set("role", "superuser"))

	err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "superuser")
	assert.Empty(t, log.emails(), "an invalid role must be caught before a single invitation goes out")
	assert.Empty(t, buf.String(), "nothing should be rendered when the command never ran")
}

// TestRunInvite_NoAddressesInNonInteractiveModeFailsFast guards the agent path.
//
// Failure prevented: with no addresses and no TTY the command falls into the
// interactive prompt and blocks forever — an AI coworker or CI job hangs
// instead of getting a usage error.
func TestRunInvite_NoAddressesInNonInteractiveModeFailsFast(t *testing.T) {
	_, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

	err := runInvite(inviteCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email address")
	assert.Empty(t, log.emails())
}

// TestRunInvite_NoTeamResolvableExitsSilentlyWithGuidance covers the
// unlinked-directory path.
//
// Failure prevented: run outside a linked repo with no --team, the command
// either picks an arbitrary team (inviting a stranger into the wrong org) or
// exits 0 having done nothing.
func TestRunInvite_NoTeamResolvableExitsSilentlyWithGuidance(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonMode), func(t *testing.T) {
			buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
			if jsonMode {
				require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
			}

			err := runInvite(inviteCmd, []string{"alice@acme.com"})

			require.Error(t, err)
			assert.True(t, cli.IsSilent(err), "guidance is already printed; got %v", err)
			assert.Empty(t, log.emails())

			if jsonMode {
				var generic map[string]any
				require.NoError(t, json.Unmarshal(buf.Bytes(), &generic), "output: %s", buf.String())
				assert.Equal(t, "no_team", generic["error"])
				assert.NotEmpty(t, generic["guidance"])
			} else {
				assert.Contains(t, stripANSI(buf.String()), "--team")
			}
		})
	}
}

// TestRunInvite_JSONOutputParsesEndToEnd proves the machine contract holds
// through the real command, not just the renderer.
//
// Failure prevented: something upstream of renderInviteResult writes a banner,
// a warning, or a spinner frame into the same writer and `ox invite --json |
// jq` breaks.
func TestRunInvite_JSONOutputParsesEndToEnd(t *testing.T) {
	buf, _ := newInviteCommandHarness(t, []inviteReply{
		{status: http.StatusCreated, body: createdInviteBody},
		{status: http.StatusConflict, body: `{"success":false,"error":"an active invite already exists for this email"}`},
	})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
	require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))

	require.NoError(t, runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com"}),
		"one sent plus one already-invited is a clean run")

	var out struct {
		Team struct {
			Slug string `json:"slug"`
		} `json:"team"`
		Role    string          `json:"role"`
		Results []inviteOutcome `json:"results"`
		Summary struct {
			Sent           int `json:"sent"`
			AlreadyInvited int `json:"already_invited"`
			Failed         int `json:"failed"`
		} `json:"summary"`
		Guidance string `json:"guidance"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "raw output: %s", buf.String())

	assert.Equal(t, "acme", out.Team.Slug, "--team passthrough must be reflected back")
	assert.Equal(t, api.RoleMember, out.Role)
	require.Len(t, out.Results, 2)
	assert.Equal(t, statusSent, out.Results[0].Status)
	assert.Equal(t, statusAlreadyInvited, out.Results[1].Status)
	assert.Equal(t, 1, out.Summary.Sent)
	assert.Equal(t, 1, out.Summary.AlreadyInvited)
	assert.Equal(t, 0, out.Summary.Failed)
	assert.NotEmpty(t, out.Guidance)
}

// TestRunInvite_UnsupportedServerExitsNonZero covers an old server (bare 404,
// no JSON body) in BOTH output modes.
//
// Failure prevented: `ox invite alice@acme.com --json` against a SageOx server
// that predates CLI invitations invites nobody and exits 0 — so a provisioning
// script or AI coworker records the onboarding step as done. The text path
// returns cli.ErrSilent correctly; --json must behave identically, since the
// exit code is the ONLY signal a caller shares between the two modes.
func TestRunInvite_UnsupportedServerExitsNonZero(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonMode), func(t *testing.T) {
			// a bare 404 with a non-JSON body is chi's unrouted answer, and is
			// what distinguishes "no such route" from "not a member".
			buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusNotFound, body: "404 page not found\n"}})
			require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
			if jsonMode {
				require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
			}

			err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com"})

			assert.Len(t, log.emails(), 1, "one request is enough to learn the route is absent")
			require.Error(t, err, "nobody was invited — the command must not report success")
			assert.True(t, cli.IsSilent(err), "the explanation is already rendered; got %v", err)
			assert.NotEmpty(t, buf.String(), "the user must be told why")
		})
	}
}

// TestRunInvite_PersonalTeamAbortsTheWholeBatchAfterOneRequest covers the
// team-level refusal end to end.
//
// Failure prevented: a private per-user team is single-member by design, so
// NO address can ever be invited into it. Treating that as a per-recipient
// outcome prints the identical refusal once per address in a ten-person paste
// — and implies to the reader (human or agent) that some other address might
// have worked, so they retry with a different list instead of a different
// team. It also spends one doomed request per address.
func TestRunInvite_PersonalTeamAbortsTheWholeBatchAfterOneRequest(t *testing.T) {
	const body = `{"error":{"code":"personal_team_immutable","message":"personal teams are single-member and cannot take invitations"}}`

	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonMode), func(t *testing.T) {
			buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusConflict, body: body}})
			require.NoError(t, inviteCmd.Flags().Set("team", "my-personal-team"))
			if jsonMode {
				require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
			}

			err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com", "carol@acme.com"})

			require.Error(t, err, "nobody was invited — this must not report success")
			assert.True(t, cli.IsSilent(err), "the explanation is already rendered; got %v", err)
			assert.Len(t, log.emails(), 1, "one request is enough to learn the team can never accept invitations")

			if jsonMode {
				var generic map[string]any
				require.NoError(t, json.Unmarshal(buf.Bytes(), &generic), "output: %s", buf.String())
				assert.NotEmpty(t, generic["guidance"], "an agent needs to be told to pick a different team")
			} else {
				assert.NotEmpty(t, buf.String(), "the user must be told why")
			}
		})
	}
}

// TestRunInvite_MidBatchAuthLossStillReportsWhatWasAlreadySent covers the
// partially-applied batch.
//
// Failure prevented: the first invitation IS created, then the session is
// revoked (or the token ages out) and the second address comes back 401. The
// command reports only "Not authenticated" and discards the record of the
// invitation it already created — so the user re-runs, sees a confusing
// "already invited" for someone they were told was never invited, and cannot
// tell a partially-applied batch from a completely failed one.
func TestRunInvite_MidBatchAuthLossStillReportsWhatWasAlreadySent(t *testing.T) {
	buf, log := newInviteCommandHarness(t, []inviteReply{
		{status: http.StatusCreated, body: createdInviteBody},
		{status: http.StatusUnauthorized, body: `{"success":false,"error":"token revoked"}`},
	})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))

	err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com", "carol@acme.com"})

	require.Error(t, err)
	assert.True(t, cli.IsSilent(err))
	assert.Equal(t, []string{"alice@acme.com", "bob@acme.com"}, log.emails(),
		"carol must not be attempted once credentials are known bad")

	assert.Contains(t, stripANSI(buf.String()), "alice@acme.com",
		"alice's invitation was actually created; dropping that fact makes a partially-applied batch indistinguishable from a failed one")
}

func TestRunInvite_PartialAbortJSONIsSingleEnvelope(t *testing.T) {
	buf, log := newInviteCommandHarness(t, []inviteReply{
		{status: http.StatusCreated, body: createdInviteBody},
		{status: http.StatusUnauthorized, body: `{"success":false,"error":"token revoked"}`},
	})
	require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
	require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))

	err := runInvite(inviteCmd, []string{"alice@acme.com", "bob@acme.com"})

	require.Error(t, err, "the abort still has to exit non-zero")
	assert.True(t, cli.IsSilent(err), "the explanation is already rendered; got %v", err)
	assert.Equal(t, []string{"alice@acme.com", "bob@acme.com"}, log.emails())

	dec := json.NewDecoder(strings.NewReader(buf.String()))
	var out struct {
		Error   string          `json:"error"`
		Message string          `json:"message"`
		Results []inviteOutcome `json:"results"`
		Summary struct {
			Sent           int `json:"sent"`
			AlreadyInvited int `json:"already_invited"`
			Failed         int `json:"failed"`
		} `json:"summary"`
	}
	require.NoError(t, dec.Decode(&out), "first JSON document must parse: %s", buf.String())
	assert.Equal(t, "unauthenticated", out.Error)
	assert.NotEmpty(t, out.Message)
	require.Len(t, out.Results, 1, "only the invitation created before the abort should be reported as a result")
	assert.Equal(t, "alice@acme.com", out.Results[0].Email)
	assert.Equal(t, statusSent, out.Results[0].Status)
	assert.Equal(t, 1, out.Summary.Sent)

	var extra map[string]any
	require.ErrorIs(t, dec.Decode(&extra), io.EOF,
		"partial abort JSON must be one top-level document, not a result document followed by an error document; output:\n%s", buf.String())
}

// TestRunInvite_ModeFlagsNeverSilentlySwallowAddresses guards the mode split.
//
// Failure prevented: `ox invite alice@acme.com --list` (a plausible slip, or an
// AI coworker composing flags) resolves as a listing, prints a table of
// outstanding invitations, exits 0 — and alice is never invited. The user has
// every reason to believe she was. The same applies to --cancel, and to the two
// mode flags used together.
func TestRunInvite_ModeFlagsNeverSilentlySwallowAddresses(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		args  []string
	}{
		{"--list with addresses", map[string]string{"list": "true"}, []string{"alice@acme.com"}},
		{"--cancel with addresses", map[string]string{"cancel": "inv_1"}, []string{"alice@acme.com"}},
		{"--list and --cancel together", map[string]string{"list": "true", "cancel": "inv_1"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
			require.NoError(t, inviteCmd.Flags().Set("team", "acme"))
			for name, value := range tt.flags {
				require.NoError(t, inviteCmd.Flags().Set(name, value))
			}

			err := runInvite(inviteCmd, tt.args)

			require.Error(t, err, "an ambiguous invocation must be refused, never silently reinterpreted")
			assert.Empty(t, log.emails(), "no invitation may go out on an invocation the user has to re-issue")
			assert.Empty(t, buf.String(), "nothing should be rendered for a refused invocation")
		})
	}
}

// TestRunInvite_TeamRefIsWhatLandsInTheRequestURL guards the routing.
//
// Failure prevented: --team is dropped or double-encoded, so invitations go to
// whatever team the repo happens to be linked to — inviting an outsider into
// the wrong organization, which is not something a re-run can undo.
func TestRunInvite_TeamRefIsWhatLandsInTheRequestURL(t *testing.T) {
	_, log := newInviteCommandHarness(t, []inviteReply{{status: http.StatusCreated, body: createdInviteBody}})
	require.NoError(t, inviteCmd.Flags().Set("team", "some-other-team"))

	require.NoError(t, runInvite(inviteCmd, []string{"alice@acme.com"}))

	log.mu.Lock()
	defer log.mu.Unlock()
	require.Len(t, log.paths, 1)
	assert.Equal(t, "/api/v1/teams/some-other-team/invites", log.paths[0])
}

// ---------------------------------------------------------------------------
// --list / --cancel
// ---------------------------------------------------------------------------

// fakeLister records what --list/--cancel asked the server for.
type fakeLister struct {
	invites  []api.PendingInvite
	listErr  error
	revErr   error
	revoked  []string
	listedAs []string
}

func (f *fakeLister) ListTeamInvites(_ context.Context, teamRef string) ([]api.PendingInvite, error) {
	f.listedAs = append(f.listedAs, teamRef)
	return f.invites, f.listErr
}

func (f *fakeLister) RevokeTeamInvite(_ context.Context, _, inviteID string) error {
	f.revoked = append(f.revoked, inviteID)
	return f.revErr
}

// fixedNow is the clock every --list assertion is measured against, so the
// relative expiry column is deterministic instead of depending on wall time.
var fixedNow = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func pendingFixture() []api.PendingInvite {
	return []api.PendingInvite{
		{ID: "0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70", Email: "alice@acme.com", Role: "member",
			ExpiresAt: fixedNow.Add(5 * 24 * time.Hour).Format(time.RFC3339)},
		{ID: "0198f3c2-8e3c-7e40-9d2e-4b0f6a7c8d92", Email: "carol@acme.com", Role: "admin",
			ExpiresAt: fixedNow.Add(-2 * 24 * time.Hour).Format(time.RFC3339)},
	}
}

// TestRenderInviteList_ShowsWhoIsOutstandingAndWhenItLapses is the whole point
// of --list: a person and a deadline.
//
// Failure prevented: the expiry column silently rendering an AGE ("in 5d ago")
// instead of a remaining duration — the exact bug real output caught — or the
// invite id being truncated, which would make it useless for --cancel.
func TestRenderInviteList_ShowsWhoIsOutstandingAndWhenItLapses(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderInviteList(&buf, testTarget, pendingFixture(), fixedNow, false))
	out := buf.String()

	assert.Contains(t, out, "alice@acme.com")
	assert.Contains(t, out, "in 5d", "a live invite must show time REMAINING")
	assert.Contains(t, out, "expired 2d ago", "a lapsed invite must say so")
	assert.NotContains(t, out, "in 5d ago", "remaining time must not be formatted as an age")
	assert.NotContains(t, out, "ago ago")

	// The id is --cancel's operand, so it must appear complete and copyable.
	assert.Contains(t, out, "0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70")
	assert.NotContains(t, out, "…0198", "a truncated id cannot be pasted into --cancel")
}

// Failure prevented: an empty list rendering as a bare header, leaving the user
// unsure whether the command worked or the team simply has no invitations.
func TestRenderInviteList_EmptyStateSaysSoPlainly(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderInviteList(&buf, testTarget, nil, fixedNow, false))
	assert.Contains(t, strings.ToLower(buf.String()), "no outstanding invitations")
}

// Failure prevented: a long address wrapping the row. --list had no width cap
// at all, and the id-on-its-own-line fallback cannot help — it only sheds the id.
func TestRenderInviteList_FitsIn80Columns(t *testing.T) {
	long := []api.PendingInvite{{
		ID:        "0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70",
		Email:     "firstname.lastname+project-onboarding@engineering.subsidiary.example-corp.com",
		Role:      "member",
		ExpiresAt: fixedNow.Add(72 * time.Hour).Format(time.RFC3339),
	}}
	var buf bytes.Buffer
	require.NoError(t, renderInviteList(&buf, testTarget, long, fixedNow, false))

	for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), 80,
			"line %d is %d columns and will wrap: %q", i+1, lipgloss.Width(line), line)
	}
}

// Failure prevented: --json drifting from the documented shape that AI
// coworkers parse, or the expiry being reported without the machine-readable
// timestamp alongside the human one.
func TestRenderInviteList_JSONCarriesIDsAndExpiry(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderInviteList(&buf, testTarget, pendingFixture(), fixedNow, true))

	var got struct {
		Total   int `json:"total"`
		Invites []struct {
			InviteID   string `json:"invite_id"`
			Email      string `json:"email"`
			ExpiresAt  string `json:"expires_at"`
			HasExpired bool   `json:"has_expired"`
		} `json:"invites"`
		Guidance string `json:"guidance"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	require.Len(t, got.Invites, 2)
	assert.Equal(t, 2, got.Total)
	assert.Equal(t, "0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70", got.Invites[0].InviteID)
	assert.False(t, got.Invites[0].HasExpired)
	assert.True(t, got.Invites[1].HasExpired, "a lapsed invite must be flagged for an agent too")
	assert.NotEmpty(t, got.Invites[0].ExpiresAt)
	assert.Contains(t, got.Guidance, "--cancel", "guidance must name the way to act on this list")
}

// Failure prevented: --cancel silently accepting an email address, sending it
// as an id, and surfacing the server's opaque 400 instead of naming the real
// mistake — which is the likeliest way to get this flag wrong.
func TestRunInviteCancel_RejectsAnEmailBeforeCallingTheServer(t *testing.T) {
	f := &fakeLister{}
	var buf bytes.Buffer

	err := runInviteCancel(context.Background(), &buf, f, testTarget, "alice@acme.com", false, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invite id")
	assert.Empty(t, f.revoked, "an address must never be sent as an invite id")
}

// Failure prevented: --cancel with no value reaching the network and deleting
// something unintended, or failing with an unhelpful server error.
func TestRunInviteCancel_RequiresAnID(t *testing.T) {
	f := &fakeLister{}
	var buf bytes.Buffer

	err := runInviteCancel(context.Background(), &buf, f, testTarget, "   ", false, true)

	require.Error(t, err)
	assert.Empty(t, f.revoked)
}

// Failure prevented: a cancel that reports success without the revoke actually
// happening, or that fails to tell the user the link is now dead.
func TestRunInviteCancel_RevokesAndConfirms(t *testing.T) {
	f := &fakeLister{}
	var buf bytes.Buffer

	const id = "0198f3c2-6c1a-7e40-9a0b-2f8c4d5e6f70"
	require.NoError(t, runInviteCancel(context.Background(), &buf, f, testTarget, id, false, true))

	assert.Equal(t, []string{id}, f.revoked)
	assert.Contains(t, strings.ToLower(buf.String()), "canceled")
}

// Failure prevented: a refusal to list being reported as "no invitations",
// which would tell the user the team is empty when it may be full.
func TestRunInviteList_ForbiddenIsNotRenderedAsAnEmptyList(t *testing.T) {
	f := &fakeLister{listErr: &api.ForbiddenError{Reason: "access denied"}}
	var buf bytes.Buffer

	err := runInviteList(context.Background(), &buf, f, testTarget, false)

	require.Error(t, err)
	out := strings.ToLower(buf.String())
	assert.NotContains(t, out, "no outstanding invitations")
	assert.Contains(t, out, "access denied", "the server's own reason must be shown")
}
