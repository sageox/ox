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
