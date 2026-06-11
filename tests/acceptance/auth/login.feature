Feature: Logging In to SageOx
  Devon authenticates ox with SageOx so it can sync the Ledger and Team Context
  and reach team knowledge. Login uses a device-code flow: ox shows a short
  code and a verification link, opens the browser when it can, and polls until
  Devon authorizes. When already signed in, ox says so and offers to
  re-authenticate rather than forcing it.

  See also: business-actions/sign-in.md
  See also: auth/headless-login.feature
  See also: auth/logout.feature
  See also: onboarding/init-repo.feature

  Rule: Login authenticates the coworker against the project endpoint

    Scenario: Devon logs in for the first time
      Given Devon has installed ox and is not signed in
      When he runs `ox login`
      Then ox shows a verification link and a short user code
      And ox opens the verification page in his browser
      And once Devon authorizes in the browser, ox confirms he is signed in by name and email

    Scenario: Login syncs git credentials for the team's repos
      Given Devon has just completed the login flow
      When ox finishes authenticating him
      Then ox syncs the git credentials for his team's repos
      And a later failure to sync credentials does not undo his login

  Rule: An already-authenticated coworker is told, and re-auth is opt-in

    Scenario: Devon runs login while already signed in
      Given Devon is already signed in on the project endpoint
      When he runs `ox login`
      Then ox tells him he is already authenticated and shows the account
      And ox asks whether he wants to re-authenticate
      And declining leaves his existing session untouched

  Rule: Login targets exactly one endpoint, selectable when several are known

    Scenario: Devon picks an endpoint when more than one is known
      Given Devon has signed in to more than one SageOx endpoint before
      When he runs `ox login`
      Then ox lets him pick which endpoint to authenticate
      And ox shows which endpoints are already valid and which have expired

    Scenario Outline: The chosen endpoint is shown as a clean slug
      Given Devon authenticates against "<entered>"
      When ox confirms the endpoint
      Then ox displays it as "<shown>"

      Examples: Endpoint normalization
        | entered                  | shown          |
        | api.sageox.ai            | sageox.ai      |
        | www.test.sageox.ai       | test.sageox.ai |
        | app.acme.sageox.ai       | acme.sageox.ai |

  Rule: Login can be canceled cleanly

    Scenario: Devon cancels the login while waiting for authorization
      Given Devon has started `ox login` and ox is waiting for authorization
      When he cancels before authorizing in the browser
      Then ox reports the authentication was canceled
      And Devon is left signed out, with no partial session stored
