Feature: Loading Team Context for Planning
  Before making changes, Avery pulls the team's shared context — meeting
  outcomes, decisions, and conventions — with `ox agent team-ctx`. This gives
  her the team's accumulated judgment so her plan honors existing conventions
  and decisions instead of relighting settled questions. Expert coworkers
  defined in Team Context can be loaded to bring a specialist's perspective
  into the session.

  See also: business-actions/load-team-context.md
  See also: team-context/query.feature
  See also: priming/prime-session.feature
  See also: plan-enrichment/enrich-while-drafting.feature

  Rule: Team context is available for AI-coworker planning

    Scenario: Avery pulls team context before planning a change
      Given Avery is in a session for "Acme Engineering"
      When she requests the team context
      Then ox returns the team's distilled discussions, decisions, and conventions
      And Avery can plan against the team's existing judgment

    Scenario: Avery scopes team context to a specific area
      Given "Acme Engineering" has team context organized by area
      When Avery requests the team context for a specific slug
      Then ox returns the context for that area

  Rule: Expert coworkers can be brought into the session

    Scenario: Avery lists the team's expert coworkers
      Given "Acme Engineering" defines expert coworkers in its team context
      When Avery lists available coworkers
      Then ox shows her the experts she can load

    Scenario: Avery loads an expert coworker's perspective
      Given the team defines an expert coworker for the auth area
      When Avery loads that coworker
      Then ox loads the expert's guidance into Avery's session
      And Avery plans the auth change with that specialist perspective in context
