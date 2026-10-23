Feature: Credentials in a session never reach the shared Ledger
  Devon records or imports an AI coworker session without publishing credentials
  quoted in the conversation, passed to a tool, or printed by a tool. ox applies
  the same mandatory redaction to every supported recording and import path.
  Teammates receive useful session context and redaction markers instead of keys.

  This feature states user intent. Executable tests must prove it; this prose alone
  is not a passing integration test.

  See also: session-recording/auto-record.feature
  See also: session-recording/import-past-sessions.feature
  See also: session-recording/manual-publishing.feature

  Rule: Every supported recording protects all of its content
    A credential Devon's tool prints would otherwise be stored and shared with the
    team. Observable difference: no stored or published artifact contains it, and
    teammates see a redaction marker in its place.

    Scenario: Devon's tool prints a credential during a recorded session
      Given Devon's session contains a synthetic credential in a conversation turn
      And a tool call carries the credential inside a nested argument
      And the tool's output prints the credential
      When ox stores and publishes the session
      Then no stored or published session artifact contains that credential
      And teammates see redaction markers where it appeared
      And the benign conversation and tool context remain readable

    Scenario: Avery selects a session adapter that is not available
      Given the session adapter for Avery's tool is not installed
      When Avery tries to record or import a session from that tool
      Then ox tells Avery the adapter is unavailable
      And ox produces no captured or published session

  Rule: Imports and reconstructed history have the same protection as live recording
    An imported earlier session is the easiest place for a credential to slip past
    redaction. Observable difference: the stored import and any reconstructed
    recording are as clean as a live one.

    Scenario: Sam imports an earlier session containing a credential
      Given Sam has an earlier native session with a synthetic credential in conversation and tool data
      When Sam imports that session
      And ox reconstructs or merges the earlier history with a current recording
      Then neither the stored import nor the reconstructed recording contains the credential
      And the benign turns and their order remain intact
      And publishing the imported session applies the same checks as a live one

  Rule: Capture continuity never resets the protection for command output
    A tool that was running when recording restarted would otherwise return its
    credential unredacted. Observable difference: the later result is redacted and
    the next turn is still recorded.

    Scenario: Riley's credential-emitting tool returns after a recording restart
      Given Riley's recording captured a credential-emitting tool call in one batch
      And recording resumed after a restart before that tool returned
      When ox reads the tool's result in a later batch
      Then the credential output is redacted before it is stored
      And the following benign turn is still recorded

  Rule: A failed check holds publication instead of bypassing redaction
    A broken or bypassed check would otherwise publish unchecked bytes.
    Observable difference: nothing reaches the shared Ledger, and Devon keeps the
    local original and gets a way to recover.

    Scenario: Devon publishes when the redaction rules cannot be loaded
      Given Devon's session is ready to publish
      And the mandatory redaction rules are missing or invalid
      When Devon publishes the session
      Then ox refuses publication and explains how to recover
      And no unchecked session bytes reach the shared Ledger
      And the original source remains available locally

    Scenario: Quinn tries to override the check to publish a credential
      Given Quinn's session artifact contains a synthetic credential
      And Quinn asks ox to allow secrets
      When Quinn publishes the session
      Then ox does not publish the credential
      And ox explains that session credential protection cannot be bypassed

  Rule: Redaction follows upstream detection improvements
    A credential format added upstream after Devon installed ox would otherwise go
    unrecognized until the next ox release. Observable difference: a new or
    imported session is checked against the current upstream rules, and ox holds
    publication if it cannot confirm they are current.

    Scenario: Devon publishes after upstream adds a detection rule
      Given the upstream credential rule catalog has a detector added since Devon installed ox
      When Devon publishes a new or imported session
      Then the published session is checked against the current complete catalog
      And ox holds publication if it cannot confirm the catalog is current
