Feature: Holding Sessions on This Machine Until Publishing Them
  Devon publishes sessions manually. A session Devon stops is held on Devon's
  machine: nothing summarizes it, shares it with the team, or deletes it, no
  matter how the session ended. It reaches the "Acme Engineering" Ledger only
  when Devon publishes it with one explicit command, and ox shows every held
  session along with that command so none is forgotten.

  See also: business-actions/record-session.md
  See also: session-recording/auto-record.feature
  See also: session-recording/pause-resume.feature

  Rule: A held session stays on Devon's machine untouched

    Scenario: Devon stops a session while publishing manually
      Given Devon publishes sessions manually
      And Devon is recording a session
      When Devon stops the session
      Then ox tells Devon the session is held on this machine and how to publish it
      And the session is not summarized
      And nothing about the session reaches the "Acme Engineering" Ledger
      And nothing about the session reaches the team's memory

    Scenario: A held session survives background housekeeping
      Given Devon has a held session with a short, unremarkable conversation
      When ox runs its background housekeeping and Devon runs ox doctor
      Then the held session is still on Devon's machine, unchanged
      And it has not been shared with the team

  Rule: Closing the AI coworker never publishes a held session

    Scenario: Devon closes the AI coworker without stopping the session
      Given Devon publishes sessions manually
      And Devon is recording a session
      When Devon closes the AI coworker
      Then the session is held on Devon's machine
      And it is not shared with the team

    Scenario: Devon clears the conversation and starts a new one right away
      Given Devon publishes sessions manually
      And Devon has just stopped a session
      When Devon clears the conversation within the same minute
      Then the held session's transcript is unchanged

  Rule: A held session that was cut off is recovered locally, not published

    Scenario: Devon's AI coworker crashes mid-session
      Given Devon publishes sessions manually
      And Devon's AI coworker crashed while recording
      When ox recovers the interrupted session
      Then the recovered session is held on Devon's machine
      And it is not shared with the team

  Rule: Publishing a held session takes one explicit command

    Scenario: Devon publishes a held session
      Given Devon has a held session
      When Devon publishes it by name
      Then the session appears in the "Acme Engineering" Ledger
      And it is no longer held
      And ox generates its summary in the background

  Rule: ox shows held sessions without ever publishing them

    Scenario: Devon checks on unpublished sessions
      Given Devon has two held sessions
      When Devon runs ox doctor or ox status
      Then ox lists the held sessions with the command that publishes each one
      And neither session is shared with the team
