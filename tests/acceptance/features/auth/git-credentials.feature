Feature: SageOx Git Credentials Only Go to SageOx's Git Server
  ox reaches the team's Ledger and Team Context with a Git credential it
  manages for Devon. That credential belongs to one git server: the one SageOx
  named when it issued it. ox never hands it to a repository hosted anywhere
  else, even when Devon or Avery asks ox to fetch a stub file that lives in
  someone else's repository.

  See also: auth/login.feature
  See also: auth/logout.feature

  Rule: Fetching a stub from a repository on another server never sends the SageOx credential

    Scenario: Avery fetches a stub inside a clone from another server
      Given Devon is signed in to SageOx
      And Devon has cloned a repository from a server that is not SageOx's
      When Avery runs `ox fetch` on a stub file in that clone
      Then ox refuses and names the server it will not send the credential to
      And that server receives no SageOx credential
      And ox mints no new credential for it

  Rule: The team's git server keeps working wherever SageOx hosts it

    Scenario: Riley fetches from the git server SageOx named for her credential
      Given Riley's SageOx credential was issued for her team's git server
      And her team's Ledger lives on that server, at an address unlike the SageOx endpoint's
      When Riley fetches a stub file from the Ledger
      Then ox uses her SageOx credential for that server
