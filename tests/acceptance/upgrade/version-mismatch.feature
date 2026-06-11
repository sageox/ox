Feature: Detecting and Healing a Version Mismatch
  When the installed ox falls behind what a repo or the team expects, ox
  notices and tells Devon, rather than failing in a confusing way later. After
  Devon upgrades, `ox doctor` confirms compatibility and repairs anything the
  new version changed, so the whole team converges on a consistent setup.

  See also: business-actions/upgrade-ox.md
  See also: upgrade/upgrade-cli.feature
  See also: onboarding/doctor.feature

  Rule: A behind version is detected and surfaced

    Scenario: Devon's ox is behind what the project expects
      Given Devon's installed ox is older than the version the project expects
      When he runs a command in that project
      Then ox tells him his version is behind
      And ox points him to upgrade

  Rule: Doctor verifies compatibility after an upgrade

    Scenario: Sam confirms his team is consistent after upgrading
      Given Sam has just upgraded ox
      When he runs `ox doctor`
      Then ox verifies the new version is compatible with the project
      And ox repairs anything the upgrade changed so the setup is consistent

  Rule: Generated wrappers stay in step with the installed version

    Scenario: Doctor flags an outdated agent wrapper after an upgrade
      Given an upgrade changed the shipped agent command wrappers
      When Devon runs `ox doctor`
      Then ox detects the outdated wrappers
      And ox refreshes them to match the installed version
