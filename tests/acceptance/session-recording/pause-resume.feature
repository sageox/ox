Feature: Pausing and Resuming a Session Recording
  Avery can suspend recording for a stretch that should not be captured — a
  side conversation, sensitive context, an unrelated detour — and resume when
  it's relevant again. The paused interval is excluded from the recording, and
  ox tells Avery how much was left out so the boundary is never a surprise.

  See also: business-actions/record-session.md
  See also: session-recording/auto-record.feature

  Rule: Pausing suspends capture without ending the session

    Scenario: Avery pauses recording for a side conversation
      Given Avery is recording a session
      When she pauses the recording
      Then ox suspends capture for the session
      And the session is not ended

  Rule: Resuming excludes the paused interval and reports what was left out

    Scenario: Avery resumes and sees what was excluded
      Given Avery paused her session recording for several minutes
      When she resumes the recording
      Then ox closes the paused interval and excludes it from the recording
      And ox tells her roughly how much was excluded

  Rule: A session can be ended while paused

    Scenario: Avery stops a session that is currently paused
      Given Avery's session recording is paused
      When she ends the session
      Then ox finalizes the session
      And the paused interval is excluded from what lands in the Ledger

  Rule: A session can be aborted without landing in the Ledger

    Scenario: Avery aborts a session she does not want recorded
      Given Avery is recording a session she has decided not to keep
      When she aborts the session
      Then ox discards the recording
      And nothing about that session lands in the "Acme Engineering" Ledger
