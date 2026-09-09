Feature: A SageOx Credit Line on a Pull Request
  When Devon opens a pull request for work an AI coworker did, ox gives him a
  thin one-line credit to paste at the TOP of the PR description — the
  human-facing counterpart to the SageOx-Session: trailer at the bottom. The line
  links the session(s), plan(s), and recorded discussion(s) that produced the
  change and names the team they belong to, so a reviewer sees the provenance at
  a glance and can go read it. The promise under test is what Devon can paste and
  what a reviewer can reach, not the exact markup: the line links only what a
  reviewer can open, says nothing when it has nothing to link, and quietly
  disappears when a team has turned it off.

  See also: config/settings-that-matter.feature

  Rule: The credit line links the work that produced the PR

    Scenario: Devon asks for the credit line on a session that made a plan
      Given Devon's AI coworker recorded a session and saved a plan
      When Devon asks ox for the PR header
      Then the line names Devon's team
      And the line links to the session
      And the line links to the plan

    Scenario: Devon credits a recorded discussion the PR came directly out of
      Given Devon's team recorded a discussion that this PR implements
      When Devon asks ox for the PR header naming that discussion
      Then the line links to the discussion
      And a reviewer can open it from the PR description

    Scenario: Avery reads the credit line on a teammate's PR
      Given Devon's PR carries the credit line
      When Avery opens the PR to review it
      Then Avery can reach the session, plan, and discussion behind the change
      And the line costs a single row above Devon's description

  Rule: A credit line that links nothing is not shown at all

    A header exists so a reviewer can go look. A wordmark with no session, plan,
    or discussion behind it is a logo stamp on someone's pull request, not
    provenance — so ox would rather say nothing.

    Scenario: A PR opened outside any recorded session
      Given Devon has no recorded session, plan, or discussion for this work
      When Devon asks ox for the PR header
      Then ox emits no line
      And ox explains that a team name alone is not a credit

    Scenario: The team is configured but nothing was recorded
      Given Devon's repository names his team
      And Devon has no recorded session, plan, or discussion for this work
      When Devon asks ox for the PR header
      Then ox emits no line

    Scenario: Only a plan survives to be credited
      Given Devon's session cannot be linked
      But Devon saved a plan during it
      When Devon asks ox for the PR header
      Then the line still renders and links the plan
      And the line does not link the session

  Rule: A reviewer never gets a link that cannot open

    Scenario: The session has not finished uploading yet
      Given Devon's session has not been confirmed on the server
      When Devon asks ox for the PR header
      Then the line does not link that session
      And ox explains the session will be linkable once it uploads

    Scenario: The unconfirmed session was the only thing to credit
      Given Devon's session has not been confirmed on the server
      And Devon has no plan or discussion to credit
      When Devon asks ox for the PR header
      Then ox emits no line
      And Devon is not left with a credit that leads nowhere

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
