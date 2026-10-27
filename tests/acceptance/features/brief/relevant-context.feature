Feature: Seeing Which Team Context the AI Coworker Used
  Devon runs `ox brief` to see what SageOx gave his AI coworker this session.
  The brief separates the team context the AI coworker actually used, with
  the evidence for each use, from what was only put in front of it. Every
  used discussion, plan or session links to its page on sageox.ai, and the
  brief says whether SageOx would be credited as a co-author, and why.

  See also: session-recording/auto-record.feature
  See also: pr-header/pr-credit-line.feature

  # Executable proof for rules 1 and 2 (red-first, observed failing with the
  # use evidence removed): TestBrief_UsedItemsLinkedAvailableFolded in the ox
  # CLI's brief E2E tests. Rule 3 is a specification; its rendering is pinned
  # by unit tests only.

  Rule: The brief separates what the AI coworker used from what was only available

    Scenario: Devon sees the discussion his AI coworker cited, apart from the plan it never opened
      Given Devon's AI coworker was handed a team discussion and a plan this session
      And it cited the discussion in a commit
      And it never opened the plan
      When Devon runs `ox brief`
      Then the brief lists the discussion as used
      And the brief says it was cited in that commit
      And the brief counts the plan as available, not used

    Scenario: Devon sees that nothing was used when his AI coworker says so
      Given Devon's AI coworker reported that none of the team context it was handed changed what it did
      When Devon runs `ox brief`
      Then the brief says none of it was used
      And the brief lists everything it was handed as available

  Rule: Each used discussion, plan or session links to its page on sageox.ai; available items do not

    Scenario: Devon opens a used discussion from the brief
      Given Devon's AI coworker used a team discussion this session
      When Devon runs `ox brief`
      Then the used discussion is shown with its sageox.ai link on the next line
      And Devon can open that link to read the discussion

    Scenario: Devon sees no link for team context nobody used
      Given Devon's AI coworker was handed a plan it never opened
      When Devon runs `ox brief`
      Then the plan appears only as a count of available plans
      And the brief shows no link to it

  Rule: The brief says whether SageOx would be credited as a co-author and why, without changing commit trailers

    Scenario: Devon sees SageOx would be credited for a session that relied on team context
      Given Devon's AI coworker cited a team discussion in a commit
      When Devon runs `ox brief`
      Then the brief says SageOx would be credited as a co-author
      And the brief says that the credit comes from the cited discussion

    Scenario: Devon sees a session that falls below the credit threshold
      Given Devon's AI coworker reported that the team context it used changed little
      When Devon runs `ox brief`
      Then the brief says the session is below the threshold for co-author credit
      And the brief says the AI coworker reported that itself

    Scenario: Running the brief leaves Devon's commits as they were
      Given Devon's AI coworker made commits this session
      When Devon runs `ox brief`
      Then the commits carry exactly the co-author lines they had before

  Rule: The brief counts what the AI coworker used while the session is still recording

    Scenario: Avery sees the discussions her AI coworker built its answer from, mid-session
      Given Avery's AI coworker is recording a session
      And it listed the team's discussions and opened two of them
      And it wrote its reply from those two, naming each by title
      And it never reported using them
      When Avery runs `ox brief` before the session ends
      Then the brief lists both discussions as used
      And each used discussion is shown with its sageox.ai link
      And the status line counts the same two as used

    Scenario: Avery sees a discussion counted after her AI coworker re-opens it by its sageox.ai link
      Given Avery's AI coworker is recording a session in which it searched the team's discussions
      When it re-opens one of them by its sageox.ai link
      And Avery's brief refreshes
      Then the brief lists that discussion as re-opened

    Scenario: Avery sees nothing counted that only appeared in a tool's output or her own message
      Given Avery's AI coworker is recording a session in which it was handed a plan
      And the plan is named only in a command's output and in Avery's own message
      When Avery runs `ox brief`
      Then the brief counts the plan as available, not used

    Scenario: Running the brief mid-session leaves the session's record as it was
      Given Avery's AI coworker is recording a session
      When Avery runs `ox brief` many times
      Then the session's recorded context is unchanged until the session ends
      And when the session ends its record shows the same uses the brief did
