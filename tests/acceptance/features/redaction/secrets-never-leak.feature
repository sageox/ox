Feature: A coworker's detected credentials never reach the shared Ledger
  A coworker can record or import a session without publishing credentials quoted
  in the conversation, passed to a tool, or printed by a tool. ox applies the same
  mandatory redaction boundary to every supported recording and import mechanism.
  Teammates receive useful session context and redaction markers instead of keys.

  This feature states user intent. Executable native-adapter and persistence tests
  must prove it; this prose alone is not a passing integration test.

  Rule: Every recording adapter protects all of its supported content surfaces

    Scenario: A tool prints a credential during a recorded session
      Given a supported adapter reads native user and assistant turns containing a synthetic credential
      And its native tool arguments contain the credential inside a nested object
      And its native tool output contains the credential
      When ox stores and publishes the session
      Then no persisted or published session artifact contains that credential
      And teammates see redaction markers where it appeared
      And the benign conversation and supported tool context remain readable

    Scenario: A new bundled adapter is registered without a secret regression
      Given a new bundled session adapter appears in the adapter registry
      And it has no executable native-format redaction fixture
      When the adapter redaction contract tests run
      Then the tests fail and name the missing adapter proof

    Scenario: An unavailable external adapter is selected
      Given the registry lists an external session adapter without an available implementation
      When a coworker attempts to record or import its native session
      Then ox reports that the adapter is unavailable
      And ox produces no captured or published session artifact

  Rule: Imports and reconstructed history have the same protection as live recording

    Scenario: A coworker imports an earlier session containing a credential
      Given a supported native session contains a synthetic credential in conversation and tool data
      When the coworker imports that session
      And ox reconstructs or merges its earlier history with a current recording
      Then neither the stored import nor reconstructed recording contains the credential
      And the benign turns and their order remain intact
      And publishing the imported session applies the same mandatory checks

  Rule: Capture continuity never resets the protection for command output

    Scenario: A credential-emitting tool returns after a capture restart
      Given ox records a credential-emitting tool call in one capture batch
      And recording resumes after a restart before that tool returns
      When ox reads the correlated tool result in a later batch
      Then the credential output is redacted before persistence
      And the following benign turn is still recorded

  Rule: Failure holds publication without silently bypassing redaction

    Scenario: Required redaction rules cannot be loaded
      Given a session is ready to publish
      And the mandatory redaction rules are missing or invalid
      When the coworker publishes the session
      Then ox refuses publication with a recovery explanation
      And no unchecked session bytes reach the shared Ledger
      And the original source remains available locally for recovery

    Scenario: An explicit override attempts to publish a detected credential
      Given a session artifact contains a synthetic credential
      And the coworker requests an allow-secrets override
      When the coworker publishes the session
      Then ox does not publish the credential
      And ox explains that session credential protection cannot be bypassed

  Rule: Rule updates follow upstream detection improvements

    Scenario: Upstream Gitleaks adds a detection rule after ox was released
      Given the official upstream rule catalog contains a newer detector
      When a coworker publishes a new or imported session
      Then ox automatically obtains and validates the current complete catalog
      And the published session is checked using that catalog
      And inability to establish required freshness holds publication
      And the offline fallback has a complete-catalog refresh tool with regression verification
