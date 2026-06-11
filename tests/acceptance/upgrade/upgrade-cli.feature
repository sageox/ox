Feature: Upgrading the ox CLI
  Devon upgrades ox with `ox upgrade`. ox detects how it was installed —
  Homebrew, `go install`, or a manual download — and upgrades using the right
  method for that install, so Devon doesn't have to remember how he set it up.
  After upgrading, `ox version` reflects the new build.

  See also: business-actions/upgrade-ox.md
  See also: upgrade/version-mismatch.feature
  See also: install/build-and-install.feature

  Rule: Upgrade uses the method that matches how ox was installed

    Scenario Outline: Devon upgrades regardless of how he installed ox
      Given Devon installed ox via <method>
      When he runs `ox upgrade`
      Then ox upgrades using the method appropriate for a <method> install
      And `ox version` afterward reflects the newer build

      Examples: Install methods
        | method            |
        | Homebrew          |
        | go install        |
        | a manual download |

  Rule: Upgrade reports the outcome clearly

    Scenario: Devon is already on the latest version
      Given Devon is already running the latest ox version
      When he runs `ox upgrade`
      Then ox tells him he is already up to date
      And ox does not reinstall

    Scenario: Devon asks for a machine-readable upgrade result
      Given Devon is scripting an upgrade
      When he runs `ox upgrade` requesting JSON output
      Then ox emits the upgrade outcome as structured JSON
