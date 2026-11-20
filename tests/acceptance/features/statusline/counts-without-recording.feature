Feature: Status Line Counts Team Context Without a Recording
  Devon watches the ox status line in Claude Code to see how much Team Context
  shaped their session. The line counts the plans and discussions their AI coworker
  read and the ones it reported using whether or not the session is recording,
  so a session that is primed but not recorded no longer looks as if nothing was
  used. When ox cannot tell which session a use belongs to, it counts nothing
  rather than counting on the wrong session's line.

  See also: session-report/relevant-context.feature
  See also: session-recording/auto-record.feature
  See also: priming/prime-session.feature

  Rule: A session that is not recording counts the Team Context its AI coworker reads and reports using

    Scenario: Devon sees a plan counted as read and used in a session that is not recording
      Given Devon's session is primed but not recording
      And the status line shows "● ox" with no counts
      When Avery reads one of the team's plans with the ox CLI
      And Avery reports that the plan changed what she did
      Then the status line shows "1 plan · 1 used"
      And the status line does not show "● rec"

    Scenario: Devon sees the same counts in the Claude desktop app band
      Given Devon's session is primed but not recording
      And Avery read one of the team's plans and reported using it
      When Devon looks at the ox band above the prompt in the Claude desktop app
      Then the band shows "1 plan · 1 used"

  Rule: Reading a discussion through the SageOx MCP tools counts like reading it with the ox CLI

    Scenario: Devon sees a discussion counted after Avery reads it through the SageOx MCP tools
      Given Devon's session is primed
      When Avery reads one of the team's discussions through the SageOx MCP tools
      Then the status line counts one discussion
      And the count is the same as if Avery had read it with the ox CLI

  Rule: Uses ox finds on its own are counted only while the session is recording

    Scenario: Devon sees no use counted from Avery's reply in a session that is not recording
      Given Devon's session is primed but not recording
      And Avery read one of the team's discussions
      When Avery names that discussion in her reply without reporting that she used it
      Then the status line counts the discussion as read
      And the status line does not count it as used

    Scenario: Devon sees no use counted from a commit message in a session that is not recording
      Given Devon's session is primed but not recording
      When Avery cites one of the team's plans in a commit message
      Then the status line does not count the plan as used

  Rule: A session's first recording keeps the counts the session already had

    Scenario: Devon starts recording and keeps the counts from before
      Given Devon's session has never been recorded
      And the status line shows "1 plan · 1 used"
      When Devon starts recording the session
      Then the status line shows "1 plan · 1 used  ● rec"
      And later reads and uses add to those counts

  Rule: When two sessions share one AI coworker, a reported use is counted on neither line

    Scenario: Devon sees neither status line count a use ox cannot place
      Given Devon has two Claude Code sessions open that share one AI coworker
      And neither session is recording
      When Avery reports using one of the team's plans
      Then neither session's status line counts the use
      And each status line shows what it showed before
