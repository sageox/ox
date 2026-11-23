Feature: A Coworker's Config Choices Take Effect
  When a coworker changes an `ox config` setting, the change shows up in what
  they experience — the commit they get, whether a session records, whether work
  leaves their machine, whether ox reaches the network. These scenarios pin the
  settings whose wrong value a coworker would actually notice, against the flow
  each one governs. The promise under test is join, not round-trip: not "the
  value was stored" but "the behavior changed."

  See also: config/precedence.feature
  See also: session-recording/session-abort-kill.feature

  Rule: Attribution the coworker sets is exactly what lands in the commit

    Scenario: Devon turns off commit attribution
      Given Devon's repo uses the default attribution
      When Devon sets the commit attribution to empty
      And Devon makes a commit
      Then the commit message carries no Co-Authored-By trailer

    Scenario: Devon sets a custom commit attribution
      Given Devon has set a custom commit attribution line
      When Devon makes a commit
      Then the commit message carries exactly Devon's line and not the default

  Rule: Turning session recording off records nothing

    Scenario: Avery disables session recording, then starts an AI teammate
      Given Avery has set session recording to disabled
      When Avery primes an AI teammate and works for several turns
      Then no session is recorded to the Ledger
      And the session's /c/ link never resolves, because there is nothing to link

  Rule: The privacy default makes no network call on the prompt path

    Scenario: Riley works with the default privacy settings
      Given Riley has not enabled the cloud query
      When Riley submits a prompt to an AI teammate
      Then ox answers from the local Ledger only and makes no network call
      And no remote-tagged context is injected into the prompt

    Scenario: Riley opts in to the cloud query
      Given Riley has enabled the cloud query and is signed in
      When Riley submits a prompt to an AI teammate
      Then ox also asks the cloud, and the remote-tagged context appears

  Rule: Turning off plan HTML silences the render and its nudge

    Scenario: Quinn turns plan HTML off, then finishes a material plan
      Given Quinn has set plan HTML to off
      When Quinn finishes a plan ox would normally offer to render
      Then ox produces no HTML plan and offers no nudge to open one
