Feature: Searching Team Knowledge
  Avery and Riley search across the team's discussions, decisions, docs, and
  prior sessions with `ox query` to answer "have we talked about this before?"
  and "why did we decide it this way?". Querying surfaces the relevant prior
  context with enough provenance to follow up, and still works against the
  locally-cached Ledger when there's no network.

  See also: business-actions/recall-prior-work.md
  See also: team-context/team-ctx.feature
  See also: session-recording/list-and-view.feature

  Rule: Querying searches team discussions, decisions, and session history

    Scenario: Riley asks why a decision was made
      Given "Acme Engineering" has recorded discussions and decisions
      When Riley queries why the team chose its current auth approach
      Then ox surfaces the relevant discussions and decisions
      And Riley can trace each result back to its source

    Scenario: Avery checks whether the team has done this before
      Given the team has prior sessions touching a similar change
      When Avery queries for prior work on that change
      Then ox surfaces the prior sessions as prior art

  Rule: Query works offline against the cached Ledger

    Scenario: Quinn queries with no network connection
      Given Quinn has a locally-cached Ledger and no network
      When she queries the team's knowledge restricted to local data
      Then ox answers from the cached Ledger without a network call

  Rule: Query results are usable by both humans and tooling

    Scenario: Avery queries for machine-readable results in-session
      Given Avery is querying from within an AI-coworker session
      When she runs a query
      Then ox returns the results as structured data she can act on
