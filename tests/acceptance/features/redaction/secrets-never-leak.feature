Feature: A Coworker's Secrets Never Leak
  ox watches every point where a recorded session could carry a credential to
  the shared Ledger. If a key slips into a recording, ox scrubs it before the
  push leaves the machine — the teammate who pulls the Ledger sees a redaction
  marker, never the key. When ox cannot rewrite a file safely, it holds that one
  file back and lets everything else sync, so a single leak never wedges the
  team. The scrub covers what ox records into a session; content a coworker
  authors by hand elsewhere is their own to guard.

  See also: session-recording/session-abort-kill.feature
  See also: business-actions/record-session.md

  Rule: A credential captured in a session is scrubbed before it reaches the Ledger

    Scenario: Sam pushes a session whose transcript captured an access key
      Given a session of Sam's recorded a live access key into its transcript
      When Sam's Ledger is pushed to the team remote
      Then the access key appears in no version of the Ledger the team can pull
      And teammates see a redaction marker in the transcript where the key was
      And the session records that a redaction happened, so the scrub is auditable

  Rule: Redaction covers what a command was given and what it printed, not only the conversation

    Scenario: Avery runs a command that prints a team token and a term Sam's team redacts
      Given Sam has added an internal project name to the repo's redaction rules
      And during Devon's session, Avery runs a command that mentions the project name and prints a SageOx team token
      When the session is recorded
      Then neither the project name nor the token appears in the recorded session
      And teammates see redaction markers in their place

  Rule: A credential ox cannot rewrite is held back while everyone else's work still syncs

    Scenario: Sam pushes a session whose summary contains a key alongside a clean session
      Given one of Sam's session summaries contains an access key
      And a second, unrelated session of Sam's is clean
      When Sam's Ledger is pushed to the team remote
      Then the access key appears in no version of the Ledger the team can pull
      And the summary carrying the key is held back from the push
      And ox preserves the held-back file on Sam's machine and tells Sam how to recover it
      And the unrelated clean session still reaches the team remote

  Rule: A coworker can override the gate and ship a credential on purpose

    Scenario: Sam overrides the secret gate to publish raw
      Given Sam has set the explicit "allow secrets" override
      And a session of Sam's recorded an access key into its transcript
      When Sam's Ledger is pushed to the team remote
      Then ox warns loudly that credentials may be published
      And the access key reaches the team remote unchanged, as Sam chose
