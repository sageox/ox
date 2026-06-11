Feature: Listing and Viewing Past Sessions
  Devon and Avery browse the team's recorded sessions and open one to read what
  happened. Heavy session content lives out-of-band as a stub until it is
  fetched; ox downloads it on demand and renders it in the reader's preferred
  format.

  See also: business-actions/recall-prior-work.md
  See also: session-recording/auto-record.feature
  See also: team-context/query.feature

  Rule: Listing shows recent sessions from the Ledger

    Scenario: Devon lists recent sessions
      Given the "Acme Engineering" Ledger has several recorded sessions
      When Devon lists recent sessions
      Then ox shows him the most recent sessions with their status
      And he can choose one to view

  Rule: A stub session is fetched before it can be read

    Scenario: Devon views a session whose content is still a stub
      Given a session Devon wants to view is shown as a stub
      When he opens that session
      Then ox fetches the session content so it becomes local
      And ox then renders the session for him to read

  Rule: A session renders in the reader's preferred format

    Scenario Outline: Devon reads a session in his chosen format
      Given Devon's preferred view format is "<format>"
      When he views a recorded session
      Then ox renders the session as "<format>"

      Examples: View formats
        | format |
        | html   |
        | text   |
        | json   |
