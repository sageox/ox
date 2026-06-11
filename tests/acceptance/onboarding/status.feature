Feature: Checking SageOx Status
  Devon runs `ox status` for an at-a-glance read on the current repo: whether
  he is signed in, whether the repo is initialized, whether the Ledger and Team
  Context are syncing, and whether the background daemon is healthy. Each
  not-OK line names the one command that fixes it.

  See also: business-actions/check-status.md
  See also: onboarding/init-repo.feature
  See also: onboarding/doctor.feature

  Rule: Status reports authentication, initialization, sync, and daemon health

    Scenario: Devon checks a healthy project
      Given Devon is signed in and the repository is initialized and syncing
      When he runs `ox status`
      Then ox shows him as authenticated
      And ox shows the repository as initialized
      And ox shows the Ledger and Team Context as in sync
      And ox shows the daemon as healthy

  Rule: Each unhealthy line names the command that fixes it

    Scenario: Status flags that Devon is not signed in
      Given Devon is not signed in
      When he runs `ox status`
      Then ox shows authentication as missing
      And ox points him to run `ox login`

    Scenario: Status flags an un-initialized repository
      Given Devon is in a repository that is not initialized for SageOx
      When he runs `ox status`
      Then ox shows the repository as not initialized
      And ox points him to run `ox init`

    Scenario: Status flags an offline daemon
      Given Devon's project is initialized but the daemon is not running
      When he runs `ox status`
      Then ox shows the daemon as offline
      And ox points him to start the daemon

  Rule: Status is readable for both humans and tooling

    Scenario: Devon asks for machine-readable status
      Given Devon is signed in and the repository is initialized
      When he runs `ox status` requesting JSON output
      Then ox emits the same state as structured JSON for scripting
