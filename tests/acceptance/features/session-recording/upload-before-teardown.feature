Feature: Every Session Reaches the Ledger Before the Machine Goes Away
  Some AI coworkers run on a machine that goes away after each job, such as a
  CI-like pod. Before it goes away, the job's runner asks ox to finish and upload
  every session recorded there and to say how many reached the Ledger. A session
  that cannot reach the Ledger fails the job instead of disappearing with the
  machine.

  See also: session-recording/auto-record.feature

  Rule: A recording whose AI coworker crashed is finished the way its session end would have been

    Scenario: Avery crashes before the session ends
      Given Avery recorded a session on a pod and crashed before the session ended
      When the runner waits for the pod's sessions
      Then ox finishes Avery's recording and uploads it to the "Acme Engineering" Ledger
      And a recording whose AI coworker is still running is reported, not finished

  Rule: Sessions still on the machine reach the Ledger and are counted

    Scenario: A session from an earlier job never reached the Ledger
      Given the pod's home holds a session from an earlier job that never reached the Ledger
      When the runner waits for the pod's sessions
      Then ox uploads it to the "Acme Engineering" Ledger
      And the report lists it as an older pending session

    Scenario: A session's upload was committed but its push failed
      Given a session was committed to the pod's copy of the Ledger but never pushed
      When the runner waits for the pod's sessions
      Then ox pushes it to the "Acme Engineering" Ledger
      And the report counts it

  Rule: The runner learns when a session cannot reach the Ledger

    Scenario: The content store refuses an upload
      Given a session Avery recorded during this job is waiting to upload
      And the content store refuses the upload
      When the runner waits for the pod's sessions
      Then ox reports the session as not in the Ledger, with the reason
      And the command exits with a failure
      And the session stays on the pod for the next try

    Scenario: A session is held on the machine
      Given Devon publishes sessions manually and a session is held on the pod
      When the runner waits for the pod's sessions
      Then ox reports the session as held, with the command that publishes it
      And ox never uploads it
      And the command exits with a failure

  Rule: Where the daemon uploads sessions, waiting never uploads beside it

    Scenario: Devon waits on a laptop whose daemon uploads the sessions
      Given Devon's laptop runs the ox daemon, which uploads Devon's sessions
      And a session is waiting for the daemon
      When Devon waits for the laptop's sessions
      Then ox reports the session as waiting for the daemon
      And ox uploads nothing itself
