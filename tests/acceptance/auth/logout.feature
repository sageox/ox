Feature: Logging Out of SageOx
  Devon signs out of SageOx, removing the local credentials so the machine no
  longer holds an active session. After logging out, commands that need the
  cloud tell Devon to sign in again rather than acting on a stale token.

  See also: business-actions/sign-in.md
  See also: auth/login.feature

  Rule: Logout removes the local credential

    Scenario: Devon logs out
      Given Devon is signed in on the project endpoint
      When he runs `ox logout`
      Then ox removes his local authentication for that endpoint
      And ox confirms he is signed out

  Rule: After logout, cloud commands ask the coworker to sign in

    Scenario: Devon checks status after logging out
      Given Devon has logged out
      When he runs `ox status`
      Then ox shows his authentication as missing
      And ox points him to run `ox login` to sign back in
