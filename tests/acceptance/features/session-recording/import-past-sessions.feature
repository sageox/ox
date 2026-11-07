Feature: Importing Past Sessions into the Ledger
  Coworkers often worked in a repo with Claude Code or Codex before SageOx was
  set up, or with recording off. Importing uploads those past sessions to the
  team's Ledger so they become recallable like any recorded session: each one
  lands once, with a summary written on the coworker's machine first, and
  nothing moves until the coworker confirms what they see in the preview.

  See also: session-recording/auto-record.feature
  See also: session-recording/list-and-view.feature

  Rule: Every chosen past session lands in the Ledger once, with a summary

    Scenario: Devon imports past Claude Code and Codex sessions
      Given Devon worked in this repo with Claude Code and Codex before SageOx
      When Devon runs the import and confirms the preview
      Then each chosen session appears in the "Acme Engineering" Ledger once
      And each one has a summary that was written before it was uploaded
      And each one has its own session link

  Rule: Sessions not worth sharing stay on this computer

    Scenario: Sam's quick question is not uploaded
      Given one of Sam's past sessions only asked a quick question
      When Sam runs the import
      Then that session is reported as not worth sharing
      And it is not uploaded
      And running the import again does not summarize it again

  Rule: Running the import again uploads nothing twice

    Scenario: Avery re-runs the import
      Given Avery already imported last month's sessions
      When Avery runs the import again
      Then the preview lists those sessions as already imported
      And nothing is summarized, uploaded or committed

    Scenario: Sam imports the same history from a second laptop
      Given Sam's past sessions were imported from Sam's first laptop
      And Sam's second laptop holds a copy of the same history
      When Sam runs the import on the second laptop
      Then the preview lists those sessions as already imported

    Scenario: Avery continued a session after importing it
      Given Avery imported a session and later continued it
      When Avery runs the import again
      Then the preview says the session continued after it was imported
      And it says whether ox recorded the continuation or it is not in the Ledger
      And nothing is uploaded again

  Rule: Sessions ox already recorded are never uploaded again

    Scenario: Riley's session recorded by an older version of ox
      Given Riley recorded a session with a version of ox that did not keep native session IDs
      When Riley runs the import
      Then the preview lists that session as recorded live by ox
      And it is not uploaded a second time

    Scenario: Riley resumed an old session before importing it
      Given ox recorded one of Riley's sessions only from when Riley resumed it
      When Riley runs the import
      Then the preview lists that session as recorded live by ox, not from its start
      And it says the part before the resume is not in the Ledger

  Rule: A resumed conversation is imported as its own session, without the one it continues

    Scenario: Devon imports a session resumed from one ox already recorded
      Given ox recorded Devon's Monday session live
      And on Wednesday Devon resumed it in Claude Code, and ox did not record Wednesday's work
      When Devon runs the import and confirms the preview
      Then Wednesday's session appears in the Ledger once, holding only Wednesday's turns
      And it is dated Wednesday
      And Monday's session is listed as recorded live by ox and is not uploaded again

    Scenario: Devon's transcript mixes in another session's turns after its own
      Given one of Devon's transcripts holds another session's turns after its own
      When Devon runs the import
      Then that transcript is counted as unreadable
      And nothing from it is uploaded

  Rule: A session whose recording the coworker aborted is not recorded or imported again unless they ask

    Scenario: Devon aborts a recording, then runs the import
      Given ox was recording one of Devon's sessions
      And Devon aborted that recording to discard it
      When Devon runs the import after the session has gone quiet
      Then the preview lists that session as aborted by Devon, with the date
      And it is not selected
      And it is not uploaded, even when Devon confirms or names it on the command line

    Scenario: Devon kills a session already in the Ledger, then runs the import
      Given one of Devon's recorded sessions is in the Ledger
      And Devon aborted it by its exact name
      When Devon runs the import
      Then the preview lists that session as aborted by Devon
      And it is not uploaded again

    Scenario: Devon keeps working after aborting
      Given Devon aborted a recording
      And Devon later recorded and stopped other sessions, and aborted another one
      When Devon runs the import
      Then both aborted sessions are listed as aborted
      And neither is uploaded

    Scenario: Devon resumes a conversation whose recording was aborted
      Given Devon aborted ox's recording of a conversation
      When Devon resumes that conversation
      Then ox does not start recording it again
      And Devon is told why, and how to record it on purpose
      And Devon's other conversations are still recorded

    Scenario: Devon aborted a recording before the Ledger was set up
      Given Devon's repo had no Ledger yet when Devon aborted a recording
      When Devon runs the import after the Ledger is set up
      Then the preview lists that session as aborted by Devon

    Scenario: Devon changes their mind about one aborted session
      Given Devon aborted the recordings of two sessions
      When Devon names one of them and asks to include aborted sessions
      Then the confirmation says that session's recording was aborted
      And only that session is uploaded once Devon confirms
      And the other aborted session stays aborted

    Scenario: Devon's record of aborted sessions is damaged
      Given the record of the sessions Devon aborted cannot be read
      When Devon runs the import
      Then the import stops and says to run ox doctor
      And ox doctor repairs the record without forgetting any session it names

  Rule: The preview comes first and nothing moves without confirmation

    Scenario: Quinn looks before importing
      When Quinn runs the import
      Then Quinn sees the destination Ledger, whether anyone can read it, and which summarizer will run
      And Quinn sees which sessions are ready, which are skipped and why
      And nothing is uploaded until Quinn confirms

    Scenario: Devon's AI coworker asks before importing
      Given Devon asks an AI coworker to import past sessions
      When the AI coworker runs the import
      Then the AI coworker shows Devon the preview and asks before uploading

    Scenario: Devon's AI coworker uploads exactly what Devon approved
      Given Devon previewed only last week's Codex sessions and approved them
      When the AI coworker runs the upload command the preview printed
      Then only the sessions Devon saw are uploaded

  Rule: Unfinished and empty sessions are left alone

    Scenario: Avery's session is still running
      Given Avery's Codex session is still in progress
      When Avery runs the import
      Then the preview asks Avery to finish that session and run the import again

    Scenario: A transcript with no conversation
      Given one of Sam's transcripts has a prompt but no reply
      When Sam runs the import
      Then that transcript is skipped as having no conversation and is never uploaded

    Scenario: Avery resumes a session while it is being imported
      Given Avery resumes a session while the import is summarizing it
      When the import finishes
      Then that session is left for a later run and nothing of it is uploaded

  Rule: A failure is reported with the exact retry, and leaves nothing half-done

    Scenario: A summary cannot be written for one of Devon's sessions
      Given the summarizer times out on one of Devon's sessions
      When the import finishes
      Then Devon's other sessions are uploaded
      And the failed session is reported with the command that retries it
      And nothing from the failed session reached the Ledger

    Scenario: The Ledger push fails partway through Devon's import
      Given one push to the Ledger fails during Devon's import
      When the import finishes
      Then every session is uploaded and reported once
      And when the last push fails, the next import pushes those sessions first

  Rule: Credential output is redacted before anything leaves the machine

    Scenario: Quinn's past session printed a token
      Given one of Quinn's past sessions ran a command that printed a GitHub token
      When Quinn imports that session
      Then the uploaded session shows that output as redacted

    Scenario: A summary that repeats a credential is held
      Given the summary written for one of Quinn's sessions contains an access key
      When Quinn imports that session
      Then that session is reported as held
      And nothing from it is uploaded
