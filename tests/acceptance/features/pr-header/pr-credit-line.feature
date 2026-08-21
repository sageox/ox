Feature: A SageOx Credit Line on a Pull Request
  When Devon opens a pull request for work an AI coworker did, ox gives him a
  thin credit line to paste at the TOP of the PR description — the human-facing
  counterpart to the SageOx-Session: trailer at the bottom. The line names the
  team and links the session(s) and plan(s) that produced the change, so a
  reviewer sees the provenance at a glance. The promise under test is what Devon
  can paste and what a reviewer can reach, not the exact markup: the line always
  renders, links only what a reviewer can open, and quietly disappears when a
  team has turned it off.

  See also: config/settings-that-matter.feature
  See also: business-actions/create-a-pr.md

  Rule: The credit line links the work that produced the PR

    Scenario: Devon asks for the credit line on a session that made a plan
      Given Devon's AI coworker recorded a session and saved a plan
      When Devon asks ox for the PR header
      Then the line names Devon's team
      And the line links to the session
      And the line links to the plan

    Scenario: The line credits SageOx only when enrichment actually fired
      Given the session's plan surfaced prior team work
      When Devon asks ox for the PR header
      Then the line whispers that SageOx guided the work
      And the whisper reports only the signals that fired

  Rule: The line always renders, even with little to credit

    Scenario: A PR with a session but no plan and no enrichment
      Given Devon's AI coworker recorded a session but saved no plan
      When Devon asks ox for the PR header
      Then the line still renders with the team and the session
      And the line makes no claim that SageOx enrichment helped

    Scenario: A PR opened outside any recorded session
      Given Devon has no recorded session for this work
      When Devon asks ox for the PR header
      Then the line still renders with the team and the SageOx wordmark
      And the line links no session

  Rule: A reviewer never gets a link that cannot open

    Scenario: The session has not finished uploading yet
      Given Devon's session has not been confirmed on the server
      When Devon asks ox for the PR header
      Then the line does not link that session
      And ox explains the session will be linkable once it uploads

  Rule: The header and the trailer are both present, and the team can opt out

    Scenario: The credit line does not replace the machine trailer
      Given Devon pastes the PR header at the top of his PR body
      When Devon finishes the PR body
      Then the SageOx-Session trailer still sits on the last line

    Scenario: A team turns the credit line off
      Given Devon's team has set the PR header to off
      When Devon asks ox for the PR header
      Then ox emits no line
      And ox tells Devon how to turn it back on
