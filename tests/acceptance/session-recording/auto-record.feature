Feature: Recording a Session to the Ledger
  A session records automatically from the moment Avery primes. As the work
  proceeds, ox captures the session; when the session ends, ox finalizes it,
  summarizes it, and lands it in the team's Ledger so future coworkers can
  recall what happened and why. Capture keeps working offline and the recording
  syncs when the network returns.

  See also: business-actions/record-session.md
  See also: priming/prime-session.feature
  See also: session-recording/pause-resume.feature
  See also: session-recording/list-and-view.feature

  Rule: Recording starts at prime and finalizes at session end

    Scenario: Avery's session records from prime to finish
      Given Avery primed at the start of her session
      When she finishes her work and ends the session
      Then ox finalizes the recording
      And ox produces a summary of the session
      And the session lands in the "Acme Engineering" Ledger

  Rule: Finalizing produces a recallable summary

    Scenario: A finished session becomes recallable team knowledge
      Given Avery has finished and finalized a session about the auth refactor
      When Riley later asks what happened with the auth refactor
      Then Riley can recall Avery's session and its summary from the Ledger

  Rule: Capture survives an offline stretch and syncs later

    Scenario: Quinn records a session while offline
      Given Quinn primed and is working with no network connection
      When she continues her session offline
      Then ox keeps capturing the session locally
      And nothing interrupts her work to complain about the network

    Scenario: A recorded session syncs once the network returns
      Given Quinn finished a session while offline
      When her machine reconnects to the network
      Then ox syncs the recorded session to the Ledger on its own
      And the session data is safe even before the sync completes

  Rule: A session that did not finalize cleanly is recoverable

    Scenario: Avery recovers a session that was interrupted
      Given Avery's session was interrupted before it finalized
      When she runs the session recovery step
      Then ox recovers the recording
      And the session can still be finalized and land in the Ledger
