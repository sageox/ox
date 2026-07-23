package prime

// RequiredDirective is one load-bearing steering directive that a FULL
// prime call (the agent's first prime this window, or any clear/compact-
// forced re-prime) must contain. Marker is a stable substring — not exact
// prose — chosen so a wording polish doesn't spuriously fail the
// manifest; only the directive's outright disappearance should.
//
// This is the single source of truth for "what must never be silently
// dropped from a full prime." Two layers consume it:
//   - internal/prime/conformance_test.go validates the manifest itself
//     (well-formed, no duplicate markers) and cross-checks the subset of
//     entries reachable from pure prime-package functions (consult-first
//     routes, BuildGuidance command rows).
//   - cmd/ox/agent_prime_xml_test.go renders full prime XML and asserts
//     every marker is present — the actual enforcement, since rendering
//     only happens in cmd/ox. It also asserts a routine compact re-prime
//     is explicitly NOT required to carry these (the agent already has
//     them from its earlier prime call this window — see
//     ShouldCompactReprime), while a clear/compact-forced re-prime is
//     required to carry them again in full.
type RequiredDirective struct {
	Name   string // human-readable, for test failure messages
	Marker string // stable substring expected in full prime XML output
}

// RequiredFullPrimeDirectives returns the manifest of steering directives
// that MUST survive into every FULL prime call. See bd ox-32f6 — this
// manifest is the belt-and-suspenders proof that compacting prime's
// verbose static preamble never drops a directive that actively steers
// agent behavior.
func RequiredFullPrimeDirectives() []RequiredDirective {
	return []RequiredDirective{
		// consult-first: the reflex that routes a cue to the right corpus
		// BEFORE the agent reasons from first principles.
		{Name: "consult-first query routing", Marker: `ox query "<question>"`},

		// intent -> command table rows (output.Guidance.Commands).
		{Name: "command table: query", Marker: `ox query "<your question>"`},
		{Name: "command table: code search", Marker: `ox code search "<pattern>"`},
		{Name: "command table: plan render", Marker: "ox plan render --open"},
		{Name: "command table: decision enrich", Marker: "ox decision enrich"},
		{Name: "command table: coworker load", Marker: "ox coworker load <name>"},
		{Name: "command table: session score", Marker: "ox session score --score"},
		{Name: "command table: murmur", Marker: "ox murmur --topic=wip"},

		// richer behavioral directives, distinct from the one-line command
		// table rows above — these carry the WHY/WHEN, not just the WHAT.
		{Name: "plan-render directive", Marker: "ox plan render --open"},
		{Name: "session-scoring directive", Marker: "SageOx contribution score"},
		{Name: "murmur directive", Marker: "Murmuring is ENABLED"},
		{Name: "attribution rule", Marker: "When SageOx guidance influences your approach"},
	}
}
