Feature: Searching Code Before Planning
  Before drafting a change, Avery searches the repo's code and git history with
  `ox code search` so she plans against the live shape of the codebase — real
  symbols, real call sites, real history — instead of a stale mental model.
  Results come back compact by default to keep her session context lean, with a
  fuller form available when she needs it.

  See also: business-actions/explore-code.md
  See also: code-intelligence/insights.feature
  See also: plan-enrichment/enrich-while-drafting.feature

  Rule: Code search finds symbols and history in the repo

    Scenario: Avery searches for where a symbol is used
      Given Avery is planning a change to the auth middleware
      When she searches the code for the relevant symbol
      Then ox returns the matching symbols and their locations
      And Avery uses the results to ground her plan

    Scenario: Avery searches git history for how something evolved
      Given Avery wants to understand how a function changed over time
      When she searches the repo's history
      Then ox returns the relevant historical changes

  Rule: Results are compact by default to keep session context lean

    Scenario: Avery gets compact results by default
      Given Avery searches the code from within a session
      When ox returns the results
      Then the results are compact by default to conserve her context budget
      And she can ask for the fuller form when she needs more detail

  Rule: Search guides the coworker when the index is not ready

    Scenario: Avery searches before the code index exists
      Given the repo's code index has not been built yet
      When Avery searches the code
      Then ox tells her the index is not ready
      And ox points her to build it
