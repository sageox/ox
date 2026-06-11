Feature: Diagnosing and Repairing with ox doctor
  When something about a project's SageOx setup is off, Devon runs `ox doctor`.
  Doctor is the last line of defense: it detects every known failure mode —
  treating a missing value as just as broken as a wrong one — and auto-fixes
  the safe ones. For anything it will not touch automatically, it gives Devon a
  clear remediation step.

  See also: business-actions/run-doctor.md
  See also: onboarding/init-repo.feature
  See also: onboarding/status.feature
  See also: upgrade/version-mismatch.feature

  Rule: Doctor reports a healthy project as healthy

    Scenario: Devon runs doctor on a healthy project
      Given Devon's project is signed in, initialized, and syncing
      When he runs `ox doctor`
      Then ox reports the checks as passing
      And ox does not change anything

  Rule: Doctor auto-fixes the safe failure modes

    Scenario: Doctor restores a missing prime marker
      Given the prime marker is missing from the repo's agent instructions
      When Devon runs `ox doctor`
      Then ox detects the missing marker
      And ox restores it so AI coworkers prime at session start

    Scenario: Doctor repairs a misconfigured endpoint
      Given the repo's stored endpoint carries a subdomain prefix that should be normalized
      When Devon runs `ox doctor`
      Then ox detects the malformed endpoint
      And ox repairs it to the normalized form

    Scenario Outline: Doctor treats a missing value as broken, not absent
      Given the project is missing its <thing>
      When Devon runs `ox doctor`
      Then ox flags the missing <thing> as a problem
      And ox repairs it or tells Devon exactly how to

      Examples: Things doctor will not let be silently absent
        | thing                |
        | prime marker         |
        | agent hooks          |
        | ledger configuration |
        | git credentials      |

  Rule: Doctor surfaces what it cannot safely auto-fix

    Scenario: Doctor finds a problem that needs Devon's decision
      Given Devon's project has a problem doctor will not auto-fix on its own
      When he runs `ox doctor`
      Then ox describes the problem in plain language
      And ox gives Devon the exact command or step to resolve it

  Rule: Doctor reassures that deferred sync is safe

    Scenario: Doctor explains a not-yet-synced session is not lost
      Given a recorded session has not finished syncing to the Ledger
      When Devon runs `ox doctor`
      Then ox reports the session data is safe
      And ox explains that sync will complete on its own or on the next run
