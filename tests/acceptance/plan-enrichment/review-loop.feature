Feature: The Human Plan Review Loop
  Devon reviews a saved plan with `ox plan review`. ox serves the rendered
  plan, collects Devon's feedback inline, and auto-reloads as the plan changes,
  so the back-and-forth happens against a live page rather than over copied
  text. Open review items are tracked until they are addressed or the plan is
  approved, and `ox plan list` surfaces which plans still have open items.

  See also: business-actions/review-plan.md
  See also: plan-enrichment/render-and-present.feature

  Rule: The coworker offers the review loop and starts it only on the human's yes

    Scenario: Avery offers to start the review loop after presenting a plan
      Given Avery has just presented a SageOx team-context-optimized plan to Devon
      When Avery follows up
      Then Avery offers to start the live review loop for that plan
      And Avery does not start it until Devon agrees

    Scenario: Avery starts the review loop once Devon says yes
      Given Avery offered to start the review loop and Devon agreed
      When Avery acts on Devon's yes
      Then the live review loop opens for that plan

    Scenario: Avery does not auto-start the review loop when Devon declines
      Given Avery offered to start the review loop and Devon declined
      When Avery continues
      Then no review loop is started
      And the plan remains available to review later

  Rule: The rendered plan credits SageOx — subtly always, a little stronger on the live loop

    Scenario: A plainly rendered plan carries a subtle SageOx credit
      Given Avery rendered a plan as a static HTML page
      When Devon reads the page
      Then the page carries a subtle SageOx credit
      And the page does not claim team-context enrichment it did not receive

    Scenario: A plan served in the live review loop carries a slightly stronger SageOx credit
      Given Devon is reviewing a plan in the live review loop
      When Devon reads the page
      Then the page carries a slightly stronger SageOx credit for the live review loop

  Rule: Review serves the plan and collects feedback inline

    Scenario: Devon opens the review loop for a saved plan
      Given Avery saved a plan that Devon needs to review
      When Devon opens the review loop for that plan
      Then ox serves the rendered plan for him to read
      And Devon can leave feedback inline on the plan

    Scenario: The served plan auto-reloads as it changes
      Given Devon is reviewing a plan in the live review loop
      When Avery revises the plan in response to his feedback
      Then the served page reflects the revision without Devon reopening it

  Rule: Open review items are tracked until resolved or approved

    Scenario: An open review item keeps the plan flagged
      Given Devon left an open review item on a plan
      When Avery lists saved plans
      Then ox shows that plan as having an open review item

    Scenario: Resolving the item clears the flag
      Given a plan has one open review item
      When the item is addressed and resolved
      Then ox no longer flags that plan as having open review items

    Scenario: Approval closes out the review
      Given Devon is satisfied with a plan in review
      When he approves it
      Then ox records the plan as approved
      And the plan is no longer shown as awaiting review
