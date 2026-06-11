Feature: Enriching a Plan While Drafting It
  While Avery is drafting an implementation plan in plan mode, ox folds the
  team's context into the plan *before* a human ever sees it. A just-in-time
  hint reminds Avery to run `ox plan enrich` while still drafting; the
  enrichment returns deterministic team-context signals — collisions with
  in-flight work, prior art, expert routing — at zero LLM and zero network
  cost. Avery reasons over those signals and routes the plan around contention
  before presenting it. The hint fires once per plan-mode entry, not on every
  prompt.

  See also: business-actions/enrich-plan.md
  See also: plan-enrichment/render-and-present.feature
  See also: plan-enrichment/review-loop.feature
  See also: code-intelligence/insights.feature

  Rule: Enrichment returns team-context signals for an in-progress plan

    Scenario: Avery enriches her draft plan while still in plan mode
      Given Avery is drafting an implementation plan in plan mode
      When she runs `ox plan enrich` on her draft
      Then ox returns the team-context signals it found — collisions, prior art, and expert routes
      And ox returns them as structured data Avery can reason over

    Scenario: Enrichment costs no LLM call and no network round-trip
      Given Avery has a draft plan to enrich
      When she enriches it
      Then ox computes the signals locally
      And the enrichment makes no LLM or network call

  Rule: Enriched signals reshape the plan before it reaches the human

    Scenario Outline: A team-context signal changes the plan before presentation
      Given Avery's draft plan touches an area with <signal>
      When she enriches the plan and reasons over the result
      Then she adjusts the plan to account for the <signal> before presenting it

      Examples: Signals that should reshape a plan
        | signal                                          |
        | a collision with another coworker's in-flight work |
        | prior art the team already produced             |
        | an expert coworker who owns the area            |

    Scenario: A plan with no signals is presented as drafted
      Given Avery's draft plan touches an area with no team-context signals
      When she enriches it
      Then ox reports that no team-context signals fired
      And Avery presents the plan as drafted

  Rule: The plan-mode hint fires once per plan-mode entry

    Scenario: Avery is reminded to enrich on entering plan mode
      Given Avery is starting to draft in plan mode for the first time this entry
      When she submits her first plan-mode prompt
      Then ox reminds her to fold in `ox plan enrich` team context before presenting

    Scenario: The hint does not repeat on every prompt within one plan-mode entry
      Given Avery has already been reminded to enrich this plan-mode entry
      When she submits another prompt while still in the same plan-mode entry
      Then ox does not repeat the enrichment reminder

    Scenario: Re-entering plan mode re-arms the hint
      Given Avery left plan mode after being reminded earlier
      When she enters plan mode again and submits a plan-mode prompt
      Then ox reminds her to enrich once more for the new entry

  Rule: Enrichment works with no plan or no signals, gracefully

    Scenario: Avery enriches when no plan can be found
      Given there is no draft plan to enrich
      When Avery runs `ox plan enrich`
      Then ox tells her no plan was found
      And ox tells her how to supply one
