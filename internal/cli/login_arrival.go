package cli

import (
	"fmt"
	"strings"
)

// login_arrival.go — the text of the two moments `ox login` has a person's
// attention: the code to match, and the three-line "you're in" block.
// docs/design/patterns/login-flow.md is the design. The text lives here, not in
// cmd/ox, so the catalog entry (ox dev catalog --component=login-screen) draws
// the real thing and cannot drift from it.

// LoginNextCommand is the one thing to do after signing in. A fixed command,
// not a rotating tip: the person just arrived and needs a first step.
const LoginNextCommand = "ox init"

// loginArtGap separates Oxy's picture from the success lines beside it.
const loginArtGap = "  "

// DeviceCodeLines is where to go and the code to match there.
//
// The code prints every time, even when the URL already carries it. RFC 8628
// asks the person to compare the code on the approval page with the one the
// terminal showed, which is the only check that the request being approved is
// theirs; a terminal that hides it turns that check into a click.
//
// Without a URL that carries the code the person types it, so the line says
// "Enter code". The "Visit:" line is the same in both layouts, so anything that
// reads it keeps working.
func DeviceCodeLines(visitURL, userCode string, urlCarriesCode bool) string {
	code := StyleBold.Render(SanitizeTerminalText(userCode))
	if urlCarriesCode {
		return fmt.Sprintf("Visit: %s\nCode:  %s  (match this in your browser)\n", visitURL, code)
	}
	return fmt.Sprintf("Visit: %s\nEnter code: %s\n", visitURL, code)
}

// LoginArrival is what the success block says: who signed in, where, and what
// to do next.
type LoginArrival struct {
	Name      string // first name; empty when the account has none
	Email     string
	Endpoint  string // slug, e.g. sageox.ai
	Returning bool   // signed in on this endpoint before; see cmd/ox isReturningLogin
}

// NewLoginArrival builds the block from a signed-in account. Name and email
// come from the server, so they are scrubbed of terminal control sequences.
func NewLoginArrival(fullName, email, endpointSlug string, returning bool) LoginArrival {
	name := SanitizeTerminalText(fullName)
	if parts := strings.Fields(name); len(parts) > 0 {
		name = parts[0]
	}
	return LoginArrival{
		Name:      name,
		Email:     SanitizeTerminalText(email),
		Endpoint:  endpointSlug,
		Returning: returning,
	}
}

// Lines is the block itself: three lines, none wider than 80 columns.
func (a LoginArrival) Lines() []string {
	greeting := "Welcome"
	if a.Returning {
		greeting = "Welcome back"
	}
	who := a.Email
	if a.Name != "" {
		who = fmt.Sprintf("%s (%s)", a.Name, a.Email)
	}
	field := func(label, value string) string {
		return "  " + StyleDim.Render(fmt.Sprintf("%-10s", label+":")) + value
	}
	return []string{
		StyleSuccess.Render("✓") + " " + StyleBold.Render(greeting+", "+who),
		field("Endpoint", a.Endpoint),
		field("Next", StyleCommand.Render(LoginNextCommand)),
	}
}

// Render draws the block, with Oxy's picture beside it when art is given: one
// string per terminal row, all the same width. The text sits level with the
// middle of the figure.
func (a LoginArrival) Render(art []string) string {
	text := a.Lines()
	var b strings.Builder
	if len(art) == 0 {
		for _, line := range text {
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	top := max(0, (len(art)-len(text))/2)
	for i, artLine := range art {
		b.WriteString(artLine)
		if j := i - top; j >= 0 && j < len(text) {
			b.WriteString(loginArtGap + text[j])
		}
		b.WriteByte('\n')
	}
	return b.String()
}
