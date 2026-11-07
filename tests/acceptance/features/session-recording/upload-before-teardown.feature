Feature: Every Session Reaches the Ledger Before the Machine Goes Away
  Some AI coworkers run on a machine that goes away after each job, such as a
  CI-like pod. There, as on a laptop, the ox daemon summarizes and uploads each
  session. Before the machine goes away, the job's runner waits until the
  daemon has put every session recorded there in the Ledger, and learns how
  many made it. A session that cannot reach the Ledger fails the job instead
  of disappearing with the machine.

  See also: session-recording/auto-record.feature

  Rule: The runner waits for the daemon to upload each session, with its summary

    Scenario: Avery's session ends normally
      Given Avery's session on a pod ended and was handed to the daemon
      When the runner waits for the pod's sessions
      Then the runner waits until the daemon has uploaded it, with its summary, to the "Acme Engineering" Ledger
      And the report counts it

    Scenario: A session from an earlier job never reached the Ledger
      Given the pod's home holds a session from an earlier job that never reached the Ledger
      When the runner waits for the pod's sessions
      Then the daemon uploads it to the "Acme Engineering" Ledger
      And the report lists it as an older pending session

  Rule: A crashed AI coworker's session is the daemon's to finish

    Scenario: Avery crashes before the session ends
      Given Avery recorded a session on a pod and crashed before the session ended
      When the runner waits for the pod's sessions
      Then the runner waits while the daemon notices the crash and finishes Avery's session
      And a recording whose AI coworker is still running is reported, not finished

  Rule: The runner learns when a session did not reach the Ledger

    Scenario: The daemon does not finish in time
      Given a session Avery recorded during this job is waiting for the daemon
      And the daemon does not upload it before the runner's timeout
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

  Rule: Waiting uploads nothing itself

    Scenario: Devon waits on a machine without a daemon
      Given Devon's machine runs no ox daemon
      And a session on it never reached the Ledger
      When Devon waits for the machine's sessions
      Then ox reports the session with the command that publishes it
      And ox uploads nothing
