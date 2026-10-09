Feature: Aborting a Session is a Total Kill
  When Avery aborts a session, ox removes it and all the summarized data around
  it — in whatever state the session has reached. A session that was committed
  to the Ledger after a few turns (a draft placeholder), and even a fully
  finalized session already in the Ledger, are both discarded completely. The
  one thing a kill must never do is destroy a teammate's finalized session that
  a partial name merely collided with.

  See also: session-recording/pause-resume.feature
  See also: business-actions/record-session.md

  Rule: Aborting removes a session committed after N turns, along with its summary data

    Scenario: Avery aborts a recording that was already committed to the Ledger
      Given Avery is recording a session that has been committed to the "Acme Engineering" Ledger after several turns
      And the recording has local summary artifacts on her machine
      When she aborts the session
      Then ox removes the committed session from the Ledger, not just from her machine
      And ox deletes the local recording and every summary artifact around it
      And ox clears the recording so a new session can start

  Rule: Aborting also deletes a finalized session and all of its summarized data

    Scenario: Avery aborts a session that was already finalized in the Ledger
      Given a finalized session of Avery's exists in the "Acme Engineering" Ledger with its summary, transcript, and context trace
      When she aborts it by its exact name
      Then ox removes the finalized session and all of its summarized data from the Ledger
      And ox removes the local and hydrated copies of that session
      And the Ledger history remains intact for everyone else

  Rule: Aborting a session committed after N turns and then finalized leaves nothing behind

    Scenario: Avery aborts a session that was published mid-recording and later finalized
      Given Avery's session was committed to the "Acme Engineering" Ledger after several turns as a draft placeholder
      And that same session was later finalized in the Ledger with its summary, transcript, and context trace
      When she aborts it by its exact name
      Then ox removes the session and every piece of its summarized data from the Ledger
      And ox removes the local and hydrated copies of that session
      And the Ledger history remains intact for everyone else

  Rule: A partial name never deletes a teammate's finalized session by collision

    Scenario: Avery's partial name collides with a teammate's finalized session
      Given a teammate's finalized session exists in the shared Ledger
      When Avery aborts using only a partial name that matches it
      Then ox refuses and asks for the exact session name
      And the teammate's finalized session is left untouched

  Rule: A name that is not a single session never deletes anything

    Scenario: Avery passes ".." as the session name
      Given a teammate's finalized session exists in the shared Ledger
      And Devon has sessions recorded on his machine
      When Avery aborts or deletes the session named ".." with --force
      Then ox refuses the name
      And every session in the Ledger and on Devon's machine is left untouched
