Feature: Presenting a SageOx Team-Context-Optimized Plan
  When Avery presents a plan, ox offers a SageOx team-context-optimized HTML
  render — a self-contained page a busy human can grasp in under a minute, with
  the plan, its team-context badges, and diagrams where they reduce cognitive
  load. A plan-exit nudge fires on presentation, pointing Devon at the render
  and the review loop. The enriched render credits SageOx by construction; an
  un-enriched plan does not overclaim.

  See also: business-actions/enrich-plan.md
  See also: plan-enrichment/enrich-while-drafting.feature
  See also: plan-enrichment/review-loop.feature

  Rule: A presented plan can be rendered as a self-contained HTML page

    Scenario: Avery renders an enriched plan for Devon to review
      Given Avery has an enriched plan ready to present
      When she renders it and opens it
      Then the plan is rendered as a SageOx team-context-optimized HTML page
      And the page opens in Devon's browser
      And the page is self-contained, readable without a network connection

    Scenario: Rendering a plan in a headless shell prints the path instead of opening
      Given Avery is rendering a plan in a shell with no browser
      When she asks to open the rendered plan
      Then ox prints the path to the rendered HTML instead of trying to open a browser

  Rule: The plan-exit nudge fires on presentation

    Scenario: Devon is offered the render and the review loop when a substantial plan is presented
      Given Avery has just presented a substantial plan
      When the plan is presented to Devon
      Then ox nudges that a SageOx team-context-optimized HTML page is available to view
      And ox points to opening it for review and to the inline review loop

    Scenario: A trivial plan does not trigger the render nudge
      Given Avery presents a plan with no team-context signals and little substance
      When the plan is presented
      Then ox does not push a render-for-review nudge

  Rule: The render's SageOx attribution matches whether the plan was enriched

    Scenario: An enriched render credits SageOx
      Given Avery's plan carried SageOx team-context signals
      When it is rendered as HTML
      Then the page credits the SageOx enrichment it used

    Scenario: An un-enriched render does not overclaim
      Given Avery's plan carried no SageOx team-context signals
      When it is rendered as HTML
      Then the page does not claim SageOx enrichment it did not use

  Rule: A rendered plan is saved so it can be reopened

    Scenario: A rendered plan is captured to the Ledger
      Given Avery rendered a plan with plan capture enabled
      When the render completes
      Then ox saves the plan and its render to the "Acme Engineering" Ledger
      And the saved plan can be reopened later by its slug
