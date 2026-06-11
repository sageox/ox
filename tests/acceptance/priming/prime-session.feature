Feature: Priming an AI Coworker Session
  At the start of a session, Avery runs `ox agent prime` to load the team's
  shared context and start recording. Priming pulls in conventions, decisions,
  and expert coworkers, issues an agent ID for the session, and begins the
  session recording — so Avery starts every session with the full picture
  instead of from zero. Priming again after a context compaction or a context
  clear reloads that picture without starting a duplicate session.

  See also: business-actions/prime-session.md
  See also: session-recording/auto-record.feature
  See also: team-context/team-ctx.feature
  See also: plan-enrichment/enrich-while-drafting.feature

  Rule: Priming loads team context and registers the coworker

    Scenario: Avery primes at the start of a session
      Given Avery is starting a session in a repository initialized for "Acme Engineering"
      When she runs `ox agent prime`
      Then ox loads the team's context — conventions, decisions, and expert coworkers
      And ox issues an agent ID for the session
      And ox confirms priming with the agent ID so Avery can identify herself later

  Rule: Priming starts session recording

    Scenario: Priming begins recording the session
      Given Avery is starting a session
      When she runs `ox agent prime`
      Then ox begins recording the session to the Ledger
      And ox surfaces a one-time transparency notice about session recording the first time

  Rule: Re-priming after compaction or clear reloads context

    Scenario: Avery re-primes after a context compaction
      Given Avery primed earlier and her context has since been compacted
      When she runs `ox agent prime` again
      Then ox reloads the team context into her session
      And ox continues the same session rather than starting a duplicate

    Scenario: Avery re-primes after clearing her context
      Given Avery primed earlier and then cleared her context
      When she runs `ox agent prime` again
      Then ox reloads the team context
      And ox reuses her existing agent identity for the session

  Rule: Priming guides the coworker when setup is incomplete

    Scenario: Priming in an un-initialized repository
      Given Avery is in a repository that has not been initialized for SageOx
      When she runs `ox agent prime`
      Then ox tells her there is no team context to load
      And ox points to running `ox init` first

  Rule: A stale context can be refreshed on demand

    Scenario: Avery forces a refresh of the loaded context
      Given Avery's loaded team context predates a recent change
      When she primes again asking for a refresh
      Then ox reloads the team context from source rather than a cache
