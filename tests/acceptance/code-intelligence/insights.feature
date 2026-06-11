Feature: Reading Code Insights Before Planning
  Avery runs `ox code insights` to see the planning-relevant shape of the repo:
  hotspots, recent activity, open PRs, and where contention is likely. This
  tells her where her change might collide with work already in flight, so the
  plan she drafts — and the team-context signals that enrich it — route around
  contention from the start.

  See also: business-actions/explore-code.md
  See also: code-intelligence/code-search.feature
  See also: plan-enrichment/enrich-while-drafting.feature

  Rule: Insights surface hotspots, recent activity, and open PRs

    Scenario: Avery reviews insights before planning
      Given Avery is about to plan a change
      When she reads the code insights
      Then ox shows her the repo's hotspots and recent activity
      And ox shows her the open PRs that touch nearby code

  Rule: Insights flag contention with in-flight work

    Scenario: Avery sees that her area is contended
      Given another coworker has recent activity and an open PR in the area Avery plans to change
      When she reads the code insights
      Then ox flags the area as contended
      And Avery factors the contention into her plan before presenting it

  Rule: Insights are usable by both humans and tooling

    Scenario: Avery reads insights as machine-readable data in-session
      Given Avery is reading insights from within a session
      When she requests structured output
      Then ox returns the insights as data she can reason over
